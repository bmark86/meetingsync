package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

type App struct {
	cfg    *Config
	log    *log.Logger
	dry    bool
	llm    *LLM
	notion *Notion
	ledger *Ledger

	notesDS, tasksDS, projectsDS          string
	notesTypes, tasksTypes, projectsTypes map[string]string

	state  map[string]*fileState
	warned map[string]bool
	cache  map[string]cachedKind

	projects   []pageRef
	projectsAt time.Time
}

type fileState struct {
	attempts  int
	transient int
	nextTry   time.Time
}

type cachedKind struct {
	modTime time.Time
	size    int64
	kind    Kind
}

type entry struct {
	Path    string
	Name    string
	ModTime time.Time
	Size    int64
	Stable  bool
	Kind    Kind
	Content []byte
}

type job struct {
	primary     *entry
	summary     *entry // optional, gehört zum Transkript
	summaryOnly bool
}

func (j job) files() []*entry {
	if j.summary != nil {
		return []*entry{j.primary, j.summary}
	}
	return []*entry{j.primary}
}

func NewApp(cfg *Config, logger *log.Logger, dry bool) (*App, error) {
	a := &App{
		cfg:    cfg,
		log:    logger,
		dry:    dry,
		llm:    NewLLM(cfg.LLM, logger),
		state:  map[string]*fileState{},
		warned: map[string]bool{},
		cache:  map[string]cachedKind{},
	}
	token, err := cfg.NotionToken()
	switch {
	case err == nil:
		a.notion = NewNotion(cfg.Notion, token, logger)
	case dry:
		logger.Printf("Hinweis: %v – Dry-Run läuft ohne Notion (keine Projektliste).", err)
	default:
		return nil, err
	}
	return a, nil
}

// Init prüft die Notion-Datenbanken und lädt das Verarbeitungsprotokoll.
func (a *App) Init(ctx context.Context) error {
	ledger, err := LoadLedger(filepath.Join(a.cfg.ArchiveDir, ".meetingsync-ledger.jsonl"), !a.dry)
	if err != nil {
		return err
	}
	a.ledger = ledger
	if a.notion == nil {
		return nil
	}

	nc := a.cfg.Notion
	if a.notesDS, a.notesTypes, err = a.notion.ResolveDataSource(ctx, nc.NotesDataSourceID); err != nil {
		return fmt.Errorf("Notes-Datenbank nicht lesbar: %w", err)
	}
	if a.notesTypes[nc.Notes.Title] != "title" {
		return fmt.Errorf("Notes-Datenbank: Titel-Property %q nicht gefunden", nc.Notes.Title)
	}
	if t := a.notesTypes[nc.Notes.Status]; t != "status" && t != "select" {
		return fmt.Errorf("Notes-Datenbank: Status-Property %q nicht gefunden", nc.Notes.Status)
	}
	if a.notesTypes[nc.Notes.Review] != "checkbox" {
		a.log.Printf("Hinweis: Notes hat keine Checkbox %q – Prüf-Hinweise stehen nur als Hinweisbox oben auf der Seite.", nc.Notes.Review)
	}

	if nc.CreateTasksFor != "none" && nc.TasksDataSourceID != "" {
		if a.tasksDS, a.tasksTypes, err = a.notion.ResolveDataSource(ctx, nc.TasksDataSourceID); err != nil {
			return fmt.Errorf("Tasks-Datenbank nicht lesbar: %w", err)
		}
		if a.tasksTypes[nc.Tasks.Title] != "title" {
			return fmt.Errorf("Tasks-Datenbank: Titel-Property %q nicht gefunden", nc.Tasks.Title)
		}
	}
	if nc.ProjectsDataSourceID != "" {
		if a.projectsDS, a.projectsTypes, err = a.notion.ResolveDataSource(ctx, nc.ProjectsDataSourceID); err != nil {
			a.log.Printf("Hinweis: Projects-Datenbank nicht lesbar (%v) – Projektvorschläge sind aus.", err)
			a.projectsDS = ""
		}
	}
	return nil
}

// Check prüft Notion und LM Studio und gibt einen kurzen Bericht aus.
func (a *App) Check(ctx context.Context) error {
	fmt.Printf("Ordner:   %s\nArchiv:   %s\nFehler:   %s\n", a.cfg.WatchDir, a.cfg.ArchiveDir, a.cfg.FailedDir)
	if _, err := os.Stat(a.cfg.WatchDir); err != nil {
		fmt.Printf("✗ Ordner nicht gefunden: %v\n", err)
	}
	if a.notion == nil {
		return errors.New("kein Notion-Token gefunden")
	}
	if err := a.Init(ctx); err != nil {
		fmt.Printf("✗ Notion: %v\n", err)
		return err
	}
	fmt.Printf("✓ Notion: Notes erreichbar (%s)\n", a.notesDS)
	if a.tasksDS != "" {
		fmt.Printf("✓ Notion: Tasks erreichbar (%s)\n", a.tasksDS)
	}
	if a.projectsDS != "" {
		p := a.activeProjects(ctx)
		fmt.Printf("✓ Notion: %d aktive Projekte\n", len(p))
	}
	models, err := a.llm.Models(ctx)
	if err != nil {
		fmt.Printf("✗ LM Studio nicht erreichbar unter %s: %v\n", a.cfg.LLM.BaseURL, err)
		return nil
	}
	if contains(models, a.cfg.LLM.Model) {
		fmt.Printf("✓ LM Studio: Modell %q verfügbar\n", a.cfg.LLM.Model)
	} else {
		fmt.Printf("✗ LM Studio: Modell %q nicht gefunden. Verfügbar:\n", a.cfg.LLM.Model)
		for _, m := range models {
			fmt.Printf("    %s\n", m)
		}
	}
	return nil
}

func (a *App) Run(ctx context.Context) {
	a.log.Printf("Überwache %s (alle %ds). Beenden mit Ctrl+C.", a.cfg.WatchDir, a.cfg.PollIntervalSeconds)
	ticker := time.NewTicker(time.Duration(a.cfg.PollIntervalSeconds) * time.Second)
	defer ticker.Stop()
	a.Pass(ctx, time.Now())
	for {
		select {
		case <-ctx.Done():
			a.log.Printf("Beendet.")
			return
		case <-ticker.C:
			a.Pass(ctx, time.Now())
		}
	}
}

// Pass ist ein Durchlauf über den Exportordner.
func (a *App) Pass(ctx context.Context, now time.Time) {
	entries, err := a.listEntries(now)
	if err != nil {
		a.log.Printf("Ordner nicht lesbar: %v", err)
		return
	}
	var transcripts, summaries, unstable []*entry
	for _, e := range entries {
		switch {
		case !e.Stable:
			unstable = append(unstable, e)
		case e.Kind == KindTranscript:
			transcripts = append(transcripts, e)
		case e.Kind == KindSummary:
			summaries = append(summaries, e)
		}
	}
	sort.Slice(transcripts, func(i, j int) bool { return transcripts[i].ModTime.Before(transcripts[j].ModTime) })
	window := time.Duration(a.cfg.PairWindowSeconds) * time.Second

	// Erst alle Paare bilden, damit eine Zusammenfassung nie als Waise gilt,
	// nur weil ihr Transkript gerade in der Warteschleife steckt.
	claimed := map[string]bool{}
	pairs := make(map[string]*entry)
	for _, t := range transcripts {
		if s := pairSummary(t, summaries, claimed, window); s != nil {
			claimed[s.Path] = true
			pairs[t.Path] = s
		}
	}

	for _, t := range transcripts {
		if ctx.Err() != nil {
			return
		}
		if st := a.state[t.Path]; st != nil && now.Before(st.nextTry) {
			continue
		}
		if near(unstable, t.ModTime, window) {
			continue // die passende Zusammenfassung wird evtl. noch geschrieben
		}
		a.handle(ctx, job{primary: t, summary: pairs[t.Path]}, now)
	}

	orphanWait := time.Duration(a.cfg.OrphanSummaryMinutes) * time.Minute
	for _, s := range summaries {
		if ctx.Err() != nil || claimed[s.Path] {
			continue
		}
		if near(unstable, s.ModTime, window) {
			continue // vielleicht wird das passende Transkript gerade geschrieben
		}
		if a.ledger.HasNear(s.ModTime, window) {
			a.log.Printf("📎 %s gehört zu einem bereits verarbeiteten Transkript → Archiv", s.Name)
			a.archive(s)
			continue
		}
		if now.Sub(s.ModTime) < orphanWait {
			continue
		}
		if st := a.state[s.Path]; st != nil && now.Before(st.nextTry) {
			continue
		}
		switch a.cfg.OrphanSummaryAction {
		case "archive":
			a.log.Printf("📎 %s: kein Transkript gefunden → Archiv (orphan_summary_action=archive)", s.Name)
			a.archive(s)
		case "keep":
			a.warnOnce(s.Path, "%s: kein Transkript gefunden, bleibt liegen (orphan_summary_action=keep)", s.Name)
		default:
			a.handle(ctx, job{primary: s, summaryOnly: true}, now)
		}
	}
}

func (a *App) handle(ctx context.Context, j job, now time.Time) {
	p := j.primary
	hash := sha256Hex(p.Content)
	if a.ledger.Has(hash) {
		a.log.Printf("↺ %s wurde schon verarbeitet → Archiv", p.Name)
		a.archive(j.files()...)
		return
	}
	a.log.Printf("→ %s (%s, %d KB)", p.Name, map[bool]string{true: "nur Zusammenfassung", false: "Transkript"}[j.summaryOnly], p.Size/1024)
	url, err := a.process(ctx, j)
	if err == nil {
		_ = a.ledger.Add(LedgerEntry{Hash: hash, File: p.Name, ModTime: p.ModTime, URL: url, At: time.Now()})
		a.archive(j.files()...)
		delete(a.state, p.Path)
		return
	}

	a.handleError(ctx, j, err, now)
}

// handleError zählt vorübergehende und andere Fehler gemeinsam gegen max_attempts.
// Nur die Wartezeit unterscheidet sich; ein Abbruch per Kontext zählt nicht.
func (a *App) handleError(ctx context.Context, j job, err error, now time.Time) {
	p := j.primary
	trans := isTransient(err)
	if trans && ctx.Err() != nil {
		return
	}
	st := a.state[p.Path]
	if st == nil {
		st = &fileState{}
		a.state[p.Path] = st
	}
	st.attempts++
	if st.attempts >= a.cfg.MaxAttempts {
		a.log.Printf("   ✗ %v – gebe auf, Dateien → %s", err, a.cfg.FailedDir)
		a.fail(err, j.files()...)
		delete(a.state, p.Path)
		return
	}
	if trans {
		wait := time.Minute << min(st.transient, 4) // 1, 2, 4, 8, 16 min
		st.transient++
		st.nextTry = now.Add(wait)
		a.log.Printf("   ⏸  %v – neuer Versuch in %s (Versuch %d/%d)", err, wait, st.attempts, a.cfg.MaxAttempts)
		return
	}
	st.nextTry = now.Add(2 * time.Minute)
	a.log.Printf("   ✗ %v (Versuch %d/%d)", err, st.attempts, a.cfg.MaxAttempts)
}

// process: Text aufbereiten → lokales LLM → Notion-Seite + Tasks.
func (a *App) process(ctx context.Context, j job) (string, error) {
	p := j.primary
	var text string
	var dur time.Duration
	if j.summaryOnly {
		text = strings.TrimSpace(normalizeNewlines(string(p.Content)))
	} else {
		text, dur = transcriptText(p.Path, p.Content)
	}
	if text == "" {
		return "", errors.New("Datei ist leer")
	}
	timing := meetingTiming(p.Name, p.ModTime, dur, a.cfg.loc)

	in := NoteInput{
		Text:        text,
		SummaryOnly: j.summaryOnly,
		Timing:      timing,
		MyName:      a.cfg.MyName,
		MyLabel:     a.cfg.MySpeakerLabel,
	}
	if j.summary != nil && a.cfg.UseSummaryAsContext {
		in.SummaryText = string(j.summary.Content)
	}
	projects := a.activeProjects(ctx)
	for _, pr := range projects {
		in.Projects = append(in.Projects, pr.Name)
	}

	start := time.Now()
	a.log.Printf("   LLM läuft (%d Zeichen, Modell %s) …", len(text), a.cfg.LLM.Model)
	note, err := a.llm.Summarize(ctx, in)
	if err != nil {
		return "", err
	}
	normalizeNote(note)
	a.log.Printf("   LLM fertig nach %s: %q", time.Since(start).Round(time.Second), note.Titel)

	reasons := append([]string{}, note.PruefGruende...)
	if j.summaryOnly {
		reasons = append(reasons, "Kein Transkript mit Zeitmarken gefunden – Notiz basiert auf einer Datei ohne Sprecher/Zeitstempel")
	}
	var project *pageRef
	for i := range projects {
		if strings.EqualFold(projects[i].Name, note.Projekt) {
			project = &projects[i]
			break
		}
	}
	title := formatTitle(a.cfg.TitleFormat, note, timing)
	meta := NoteMeta{Timing: timing, ReviewReasons: reasons}
	for _, f := range j.files() {
		meta.SourceFiles = append(meta.SourceFiles, f.Name)
	}
	if a.cfg.IncludeTranscript {
		meta.Transcript = text
	}
	blocks := buildNoteBlocks(note, meta)

	if a.dry {
		a.printDryRun(title, project, note, reasons, len(blocks))
		return "dry-run", nil
	}

	pageID, url, err := a.notion.CreatePage(ctx, a.notesDS, a.noteProps(title, project, len(reasons) > 0), blocks)
	if err != nil {
		return "", err
	}
	mine := a.createTasks(ctx, note, pageID, project, title)
	a.log.Printf("   ✓ Notion: %s (%d To-dos für dich%s) %s", title, mine, map[bool]string{true: ", ⚠️ prüfen", false: ""}[len(reasons) > 0], url)

	if a.cfg.Notify && (len(reasons) > 0 || mine > 0) {
		msg := note.Titel
		if mine > 0 {
			msg += fmt.Sprintf(" · %d To-do(s) für dich", mine)
		}
		if len(reasons) > 0 {
			msg += " · bitte prüfen"
		}
		notify("Neue Meeting-Notiz", msg)
	}
	return url, nil
}

func (a *App) noteProps(title string, project *pageRef, review bool) map[string]any {
	np := a.cfg.Notion.Notes
	props := map[string]any{np.Title: map[string]any{"title": rt(title)}}
	switch a.notesTypes[np.Status] {
	case "status":
		props[np.Status] = map[string]any{"status": map[string]any{"name": np.StatusValue}}
	case "select":
		props[np.Status] = map[string]any{"select": map[string]any{"name": np.StatusValue}}
	}
	if project != nil && a.notesTypes[np.Projects] == "relation" {
		props[np.Projects] = map[string]any{"relation": []any{map[string]any{"id": project.ID}}}
	}
	if a.notesTypes[np.Review] == "checkbox" {
		props[np.Review] = map[string]any{"checkbox": review}
	}
	return props
}

// createTasks legt To-dos in der Tasks-Datenbank an und verknüpft sie mit der Notiz.
func (a *App) createTasks(ctx context.Context, n *MeetingNote, noteID string, project *pageRef, title string) int {
	mode := a.cfg.Notion.CreateTasksFor
	if a.tasksDS == "" || mode == "none" {
		return 0
	}
	tp, types := a.cfg.Notion.Tasks, a.tasksTypes
	created := 0
	for _, act := range n.NextActions {
		if mode == "me" && !act.FuerMich {
			continue
		}
		props := map[string]any{tp.Title: map[string]any{"title": rt(act.Aufgabe)}}
		if types[tp.Description] == "rich_text" {
			desc := "Aus Meeting: " + title
			if act.Owner != "" {
				desc += " · Owner: " + act.Owner
			}
			props[tp.Description] = map[string]any{"rich_text": rt(desc)}
		}
		if act.Deadline != "" && types[tp.DueDate] == "date" {
			props[tp.DueDate] = map[string]any{"date": map[string]any{"start": act.Deadline}}
		}
		if types[tp.Notes] == "relation" {
			props[tp.Notes] = map[string]any{"relation": []any{map[string]any{"id": noteID}}}
		}
		if project != nil && types[tp.Projects] == "relation" {
			props[tp.Projects] = map[string]any{"relation": []any{map[string]any{"id": project.ID}}}
		}
		if types[tp.Completed] == "checkbox" {
			props[tp.Completed] = map[string]any{"checkbox": false}
		}
		if _, _, err := a.notion.CreatePage(ctx, a.tasksDS, props, nil); err != nil {
			a.log.Printf("   ⚠️  To-do %q nicht angelegt: %v", act.Aufgabe, err)
			continue
		}
		created++
	}
	return created
}

func (a *App) activeProjects(ctx context.Context) []pageRef {
	if a.notion == nil || a.projectsDS == "" {
		return nil
	}
	if time.Since(a.projectsAt) < 15*time.Minute {
		return a.projects
	}
	p, err := a.notion.ActiveProjects(ctx, a.projectsDS, a.cfg.Notion.Projects, a.projectsTypes)
	if err != nil {
		a.log.Printf("Hinweis: Projektliste nicht abrufbar: %v", err)
		return a.projects
	}
	a.projects, a.projectsAt = p, time.Now()
	return p
}

// ProcessSingleFile verarbeitet eine Datei direkt (zum Testen). Die Datei
// bleibt liegen und wird nicht ins Protokoll eingetragen.
func (a *App) ProcessSingleFile(ctx context.Context, path string) error {
	path = expandHome(path)
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	e := &entry{Path: path, Name: filepath.Base(path), ModTime: info.ModTime(), Size: info.Size(), Stable: true, Content: content}
	e.Kind = classify(path, content, a.cfg)
	a.log.Printf("%s erkannt als: %s", e.Name, e.Kind)
	if e.Kind == KindUnsupported {
		return fmt.Errorf("Dateityp %s wird nicht unterstützt", filepath.Ext(path))
	}
	url, err := a.process(ctx, job{primary: e, summaryOnly: e.Kind == KindSummary})
	if err != nil {
		return err
	}
	if !a.dry {
		fmt.Println(url)
	}
	return nil
}

func (a *App) printDryRun(title string, project *pageRef, n *MeetingNote, reasons []string, blocks int) {
	fmt.Println("────────── DRY-RUN ──────────")
	fmt.Println("Titel:   ", title)
	if project != nil {
		fmt.Println("Projekt: ", project.Name)
	}
	fmt.Println("Prüfen:  ", len(reasons) > 0, strings.Join(reasons, " · "))
	fmt.Println("Blöcke:  ", blocks)
	fmt.Println(mustJSON(n))
	fmt.Println("─────────────────────────────")
}

// --- Dateien ---------------------------------------------------------------------

func (a *App) listEntries(now time.Time) ([]*entry, error) {
	dirEntries, err := os.ReadDir(a.cfg.WatchDir)
	if err != nil {
		return nil, err
	}
	stable := time.Duration(a.cfg.StableSeconds) * time.Second
	var out []*entry
	for _, d := range dirEntries {
		name := d.Name()
		if d.IsDir() || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "~$") || isTempName(name) {
			continue
		}
		path := filepath.Join(a.cfg.WatchDir, name)
		ext := strings.ToLower(filepath.Ext(name))
		if !isSupportedExt(ext, a.cfg) {
			a.warnOnce(path, "Ignoriere %s (Dateityp %s wird nicht verarbeitet)", name, ext)
			continue
		}
		info, err := d.Info()
		if err != nil {
			continue
		}
		e := &entry{Path: path, Name: name, ModTime: info.ModTime(), Size: info.Size()}
		e.Stable = now.Sub(e.ModTime) >= stable && e.Size > 0
		if e.Stable {
			if e.Size > 20<<20 {
				a.warnOnce(path, "Ignoriere %s (größer als 20 MB)", name)
				continue
			}
			if e.Content, err = os.ReadFile(path); err != nil {
				continue
			}
			if c, ok := a.cache[path]; ok && c.modTime.Equal(e.ModTime) && c.size == e.Size {
				e.Kind = c.kind
			} else {
				e.Kind = classify(path, e.Content, a.cfg)
				a.cache[path] = cachedKind{e.ModTime, e.Size, e.Kind}
				a.log.Printf("Neu: %s → %s", name, e.Kind)
			}
		}
		out = append(out, e)
	}
	return out, nil
}

func isTempName(name string) bool {
	l := strings.ToLower(name)
	for _, s := range []string{".tmp", ".part", ".crdownload", ".download", ".swp"} {
		if strings.HasSuffix(l, s) {
			return true
		}
	}
	return false
}

// pairSummary sucht die Zusammenfassung zum Transkript: gleicher Dateiname
// (ohne Endung) oder zeitlich am nächsten innerhalb des Fensters.
func pairSummary(t *entry, summaries []*entry, claimed map[string]bool, window time.Duration) *entry {
	stem := func(e *entry) string { return strings.ToLower(strings.TrimSuffix(e.Name, filepath.Ext(e.Name))) }
	for _, s := range summaries {
		if !claimed[s.Path] && stem(s) == stem(t) {
			return s
		}
	}
	var best *entry
	var bestD time.Duration
	for _, s := range summaries {
		if claimed[s.Path] {
			continue
		}
		d := absDur(s.ModTime.Sub(t.ModTime))
		if d <= window && (best == nil || d < bestD) {
			best, bestD = s, d
		}
	}
	return best
}

func near(list []*entry, t time.Time, window time.Duration) bool {
	for _, e := range list {
		if absDur(e.ModTime.Sub(t)) <= window {
			return true
		}
	}
	return false
}

func (a *App) archive(files ...*entry) {
	if a.dry {
		return
	}
	for _, f := range files {
		dir := filepath.Join(a.cfg.ArchiveDir, f.ModTime.In(a.cfg.loc).Format("2006-01"))
		if dst, err := moveFile(f.Path, dir); err != nil {
			a.log.Printf("   ⚠️  %s konnte nicht archiviert werden: %v", f.Name, err)
		} else {
			delete(a.cache, f.Path)
			a.log.Printf("   📦 %s → %s", f.Name, dst)
		}
	}
}

func (a *App) fail(cause error, files ...*entry) {
	if a.dry {
		return
	}
	for _, f := range files {
		dst, err := moveFile(f.Path, a.cfg.FailedDir)
		if err != nil {
			a.log.Printf("   ⚠️  %s konnte nicht verschoben werden: %v", f.Name, err)
			continue
		}
		delete(a.cache, f.Path)
		_ = os.WriteFile(dst+".fehler.txt", []byte(fmt.Sprintf("%s\n%v\n", time.Now().Format(time.RFC3339), cause)), 0o644)
	}
}

func (a *App) warnOnce(key, format string, args ...any) {
	if a.warned[key] {
		return
	}
	a.warned[key] = true
	a.log.Printf(format, args...)
}

// moveFile verschiebt src nach dir, ohne vorhandene Dateien zu überschreiben.
func moveFile(src, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	base := filepath.Base(src)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	dst := filepath.Join(dir, base)
	for i := 2; ; i++ {
		if _, err := os.Stat(dst); errors.Is(err, os.ErrNotExist) {
			break
		}
		dst = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
	}
	if err := os.Rename(src, dst); err == nil {
		return dst, nil
	}
	// anderes Volume: kopieren + löschen
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	return dst, os.Remove(src)
}

// --- Protokoll gegen Doppelverarbeitung ---------------------------------------

type LedgerEntry struct {
	Hash    string    `json:"hash"`
	File    string    `json:"file"`
	ModTime time.Time `json:"mtime"`
	URL     string    `json:"url"`
	At      time.Time `json:"at"`
}

type Ledger struct {
	path    string
	persist bool
	byHash  map[string]LedgerEntry
	mtimes  []time.Time
}

func LoadLedger(path string, persist bool) (*Ledger, error) {
	l := &Ledger{path: path, persist: persist, byHash: map[string]LedgerEntry{}}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var e LedgerEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Hash != "" {
			l.byHash[e.Hash] = e
			l.mtimes = append(l.mtimes, e.ModTime)
		}
	}
	return l, sc.Err()
}

func (l *Ledger) Has(hash string) bool { _, ok := l.byHash[hash]; return ok }

func (l *Ledger) HasNear(t time.Time, window time.Duration) bool {
	for _, m := range l.mtimes {
		if absDur(m.Sub(t)) <= window {
			return true
		}
	}
	return false
}

func (l *Ledger) Add(e LedgerEntry) error {
	l.byHash[e.Hash] = e
	l.mtimes = append(l.mtimes, e.ModTime)
	if !l.persist {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, _ := json.Marshal(e)
	_, err = f.Write(append(b, '\n'))
	return err
}

// --- Sonstiges -----------------------------------------------------------------------

func notify(title, msg string) {
	if runtime.GOOS != "darwin" {
		return
	}
	q := func(s string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"` }
	_ = exec.Command("osascript", "-e", "display notification "+q(msg)+" with title "+q(title)).Run()
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
