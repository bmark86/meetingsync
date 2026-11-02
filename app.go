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
	"slices"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"meetingsync/internal/domain"
	"meetingsync/internal/transcript"
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

	// interruptedNow: Hashes, die in diesem Durchlauf per Neustart-Regel nach
	// _fehler gingen. Nur je Durchlauf, damit eine von Mark zurückgelegte
	// Datei auch ohne Neustart des Prozesses wieder normal läuft (ADR 0004).
	interruptedNow map[string]bool

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
	kind    domain.Kind
}

type entry struct {
	Path    string
	Name    string
	ModTime time.Time
	Size    int64
	Stable  bool
	Kind    domain.Kind
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
		a.notion = NewNotion(cfg.Notion, token)
	case dry:
		logger.Printf("Hinweis: %v – Dry-Run läuft ohne Notion (keine Projektliste).", err)
	default:
		return nil, err
	}
	return a, nil
}

// transcriptOpts reicht die konfigurierten Endungen an internal/transcript.
func (a *App) transcriptOpts() transcript.Options {
	return transcript.Options{TranscriptExtensions: a.cfg.TranscriptExtensions, SummaryExtensions: a.cfg.SummaryExtensions}
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
	if slices.Contains(models, a.cfg.LLM.Model) {
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
	a.interruptedNow = map[string]bool{}
	var transcripts, summaries, unstable []*entry
	for _, e := range entries {
		switch {
		case !e.Stable:
			unstable = append(unstable, e)
		case e.Kind == domain.KindTranscript:
			transcripts = append(transcripts, e)
		case e.Kind == domain.KindSummary:
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
		// vor HasNear: auch ein fehlgeschlagenes Archivieren wartet (R-M1-07)
		if st := a.state[s.Path]; st != nil && now.Before(st.nextTry) {
			continue
		}
		if a.ledger.HasNear(s.ModTime, window) {
			a.log.Printf("📎 %s gehört zu einem bereits verarbeiteten Transkript → Archiv", s.Name)
			a.archiveAt(now, s)
			continue
		}
		if now.Sub(s.ModTime) < orphanWait {
			continue
		}
		switch a.cfg.OrphanSummaryAction {
		case "archive":
			a.log.Printf("📎 %s: kein Transkript gefunden → Archiv (orphan_summary_action=archive)", s.Name)
			a.archiveAt(now, s)
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
		a.archiveJob(j, now)
		return
	}
	if a.ledger.Started(hash) || a.interruptedNow[hash] {
		a.interrupted(j, hash, now)
		return
	}
	a.log.Printf("→ %s (%s, %d KB)", p.Name, map[bool]string{true: "nur Zusammenfassung", false: "Transkript"}[j.summaryOnly], p.Size/1024)
	url, g, err := a.process(ctx, j, hash)
	if err == nil {
		// Auch nach einem Abbruch in createTasks: die Seite existiert, also fertig
		// (mit den Lücken), sonst schickt der Neustart sie nach _fehler (ADR 0004).
		if err := a.ledger.Add(LedgerEntry{Hash: hash, File: p.Name, ModTime: p.ModTime, URL: url, At: time.Now(), MissingTasks: g.tasks, BlockErrors: g.blocks}); err != nil {
			// Die Notiz existiert. Im Speicher gilt der Hash als fertig (keine
			// zweite Seite in diesem Prozess), die Dateien gehen ins Archiv.
			a.log.Printf("   ⚠️  Protokoll nicht geschrieben (Notiz existiert: %s): %v", url, err)
		}
		// vor archiveJob: ein Archiv-Fehler setzt eine neue Wartezeit
		delete(a.state, p.Path)
		a.archiveJob(j, now)
		return
	}

	a.handleError(ctx, j, err, now)
}

// errUnterbrochen steht in .fehler.txt, wenn ein früherer Lauf nach
// "begonnen" abgebrochen ist (ADR 0004).
var errUnterbrochen = errors.New("Notion-Seite existiert evtl. schon – bitte in Notion prüfen. " +
	"Die Verarbeitung wurde beim Anlegen der Seite unterbrochen (Absturz oder Abbruch). " +
	"Fehlt die Seite in Notion, die Datei(en) zurück in den Exportordner legen; sie werden dann neu verarbeitet.")

// interrupted: Der letzte Eintrag zu hash ist "begonnen", der Lauf davor
// endete also zwischen "begonnen" und "fertig". Die Seite kann existieren:
// nicht neu anlegen, sondern nach _fehler zur Prüfung durch Mark.
func (a *App) interrupted(j job, hash string, now time.Time) {
	p := j.primary
	a.log.Printf("✗ %s: letzter Lauf wurde beim Anlegen in Notion unterbrochen → %s (bitte in Notion prüfen)", p.Name, a.cfg.FailedDir)
	// Partner zuerst, das Transkript zuletzt, Stopp beim ersten Fehler: bleibt
	// etwas liegen, dann immer das Transkript mit "begonnen". So legt nie eine
	// allein liegende Zusammenfassung eine zweite Seite an.
	files := j.files()
	for i := len(files) - 1; i >= 0; i-- {
		if !a.fail(errUnterbrochen, files[i]) {
			// bleibt "begonnen": neuer Versuch in 2 min statt bei jedem Durchlauf
			// (Logzeile und Verschieben nicht alle poll_interval_seconds).
			// Zählt nicht gegen max_attempts.
			st := a.state[p.Path]
			if st == nil {
				st = &fileState{}
				a.state[p.Path] = st
			}
			st.nextTry = now.Add(2 * time.Minute)
			return
		}
	}
	delete(a.state, p.Path)
	// Kopien mit demselben Hash in diesem Durchlauf gehen ebenfalls nach _fehler.
	a.interruptedNow[hash] = true
	if !a.ledger.Started(hash) {
		return // Kopie: verworfen steht schon im Protokoll
	}
	// verworfen: legt Mark die Datei zurück, läuft sie normal.
	if err := a.ledger.Add(LedgerEntry{Hash: hash, Status: StatusVerworfen, File: p.Name, ModTime: p.ModTime, At: time.Now()}); err != nil {
		a.log.Printf("   ⚠️  Protokoll nicht geschrieben: %v", err)
	}
}

// handleError zählt vorübergehende und andere Fehler gemeinsam gegen max_attempts.
// Nur die Wartezeit unterscheidet sich; ein Abbruch per Kontext zählt nicht.
func (a *App) handleError(ctx context.Context, j job, err error, now time.Time) {
	p := j.primary
	trans := domain.IsTransient(err)
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

// gaps zählt, was nach dem Anlegen der Notiz in Notion fehlt (OD-04: nur
// protokollieren, nicht nachholen).
type gaps struct {
	tasks  int // nicht angelegte To-dos (Fehler oder Abbruch)
	blocks int // Fehler beim Anhängen der restlichen Blöcke
}

// process: Text aufbereiten → lokales LLM → Notion-Seite + Tasks.
// hash != "": vor CreatePage "begonnen" ins Protokoll (ADR 0004); leer bei -file.
// Ist die Seite angelegt, ist der Fehler nil, auch wenn Teile fehlen (gaps).
func (a *App) process(ctx context.Context, j job, hash string) (string, gaps, error) {
	p := j.primary
	var text string
	var dur time.Duration
	if j.summaryOnly {
		text = strings.TrimSpace(transcript.NormalizeNewlines(string(p.Content)))
	} else {
		text, dur = transcript.Text(p.Path, p.Content)
	}
	if text == "" {
		return "", gaps{}, errors.New("Datei ist leer")
	}
	timing := transcript.MeetingTiming(p.Name, p.ModTime, dur, a.cfg.loc)

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
		return "", gaps{}, err
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
	meta := domain.NoteMeta{Timing: timing, ReviewReasons: reasons}
	for _, f := range j.files() {
		meta.SourceFiles = append(meta.SourceFiles, f.Name)
	}
	if a.cfg.IncludeTranscript {
		meta.Transcript = text
	}
	blocks := buildNoteBlocks(note, meta)

	if a.dry {
		a.printDryRun(title, project, note, reasons, len(blocks))
		return "dry-run", gaps{}, nil
	}

	le := LedgerEntry{Hash: hash, Status: StatusBegonnen, File: p.Name, ModTime: p.ModTime, At: time.Now()}
	if hash != "" {
		if err := a.ledger.Add(le); err != nil {
			// Ohne "begonnen" keine Notion-Anfrage. verworfen setzt den Speicher
			// zurück, damit der nächste Versuch normal läuft.
			le.Status = StatusVerworfen
			_ = a.ledger.Add(le) //nolint:errcheck // gemeldet wird der erste Fehler; bleibt auf Platte "begonnen", greift beim Neustart die Prüfregel (sicher)
			return "", gaps{}, fmt.Errorf("Protokoll nicht schreibbar, Notion-Seite nicht angelegt: %w", err)
		}
	}
	var g gaps
	pageID, url, err := a.notion.CreatePage(ctx, a.notesDS, a.noteProps(title, project, len(reasons) > 0), blocks)
	if errors.Is(err, errUnvollstaendig) {
		// Seite existiert: kein Fehler, sonst entsteht beim nächsten Versuch eine Dublette.
		a.log.Printf("   ⚠️  %v", err)
		g.blocks, err = 1, nil
	}
	if err != nil {
		// Antwort mit Fehler: neuer Versuch wie bisher (OD-05), also verworfen.
		// Bei Abbruch per Kontext bleibt "begonnen": die Anfrage kann Notion
		// erreicht haben, der Neustart schickt die Datei zur Prüfung.
		if hash != "" && ctx.Err() == nil {
			le.Status, le.At = StatusVerworfen, time.Now()
			if lerr := a.ledger.Add(le); lerr != nil {
				a.log.Printf("   ⚠️  Protokoll nicht geschrieben: %v", lerr)
			}
		}
		return "", gaps{}, err
	}
	mine, missing := a.createTasks(ctx, note, pageID, project, title)
	g.tasks = missing
	a.log.Printf("   ✓ Notion: %s (%d To-dos für dich%s) %s", title, mine, map[bool]string{true: ", ⚠️ prüfen", false: ""}[len(reasons) > 0], url)
	if g.tasks > 0 || g.blocks > 0 {
		a.log.Printf("   ⚠️  %s: Notiz unvollständig – %d To-do(s) nicht angelegt, %d Fehler beim Anhängen von Blöcken (nicht nachgeholt)", p.Name, g.tasks, g.blocks)
	}

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
	return url, g, nil
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
// Liefert die Zahl der angelegten und der fehlenden To-dos (Fehler oder Abbruch).
func (a *App) createTasks(ctx context.Context, n *domain.MeetingNote, noteID string, project *pageRef, title string) (created, missing int) {
	mode := a.cfg.Notion.CreateTasksFor
	if a.tasksDS == "" || mode == "none" {
		return 0, 0
	}
	tp, types := a.cfg.Notion.Tasks, a.tasksTypes
	for _, act := range n.NextActions {
		if mode == "me" && !act.FuerMich {
			continue
		}
		if ctx.Err() != nil {
			missing++ // Abbruch: keine weiteren Anfragen, nur zählen
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
			missing++
			continue
		}
		created++
	}
	return created, missing
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
	e.Kind = transcript.Classify(path, content, a.transcriptOpts())
	a.log.Printf("%s erkannt als: %s", e.Name, e.Kind)
	if e.Kind == domain.KindUnsupported {
		return fmt.Errorf("Dateityp %s wird nicht unterstützt", filepath.Ext(path))
	}
	url, _, err := a.process(ctx, job{primary: e, summaryOnly: e.Kind == domain.KindSummary}, "")
	if err != nil {
		return err
	}
	if !a.dry {
		fmt.Println(url)
	}
	return nil
}

func (a *App) printDryRun(title string, project *pageRef, n *domain.MeetingNote, reasons []string, blocks int) {
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
		if !d.Type().IsRegular() {
			a.warnOnce(path, "%s: keine reguläre Datei (z. B. Symlink) → übersprungen", name)
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		if !transcript.IsSupportedExt(ext, a.transcriptOpts()) {
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
				e.Kind = transcript.Classify(path, e.Content, a.transcriptOpts())
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

// archive verschiebt die Dateien ins Archiv und liefert die, die liegen
// blieben. Erfolg löscht den Retry-Zustand der Datei.
func (a *App) archive(files ...*entry) (failed []*entry) {
	if a.dry {
		return nil
	}
	for _, f := range files {
		dir := filepath.Join(a.cfg.ArchiveDir, f.ModTime.In(a.cfg.loc).Format("2006-01"))
		if dst, err := a.move(f, dir); err != nil {
			a.log.Printf("   ⚠️  %s konnte nicht archiviert werden: %v", f.Name, err)
			failed = append(failed, f)
		} else {
			delete(a.cache, f.Path)
			delete(a.state, f.Path)
			a.log.Printf("   📦 %s → %s", f.Name, dst)
		}
	}
	return failed
}

// archiveAt archiviert; bleibt eine Datei liegen, neuer Versuch erst in 2 min
// statt bei jedem Durchlauf (Logzeile und Kopie nicht alle
// poll_interval_seconds). Zählt nicht gegen max_attempts (R-M1-07).
func (a *App) archiveAt(now time.Time, files ...*entry) {
	for _, f := range a.archive(files...) {
		st := a.state[f.Path]
		if st == nil {
			st = &fileState{}
			a.state[f.Path] = st
		}
		st.nextTry = now.Add(2 * time.Minute)
	}
}

// move ruft moveFile; scheitert nur der Sync des Zielordners, ist die Datei
// verschoben: Warnung, kein Fehler (Mark, 2026-11-02).
func (a *App) move(f *entry, dir string) (string, error) {
	dst, err := moveFile(f.Path, dir)
	var se *dirSyncError
	if errors.As(err, &se) {
		a.log.Printf("   ⚠️  %s: %v", f.Name, err)
		return dst, nil
	}
	return dst, err
}

// archiveJob archiviert die Dateien des Jobs. TOCTOU (S-15): nur die primäre
// Datei wird neu geprüft, denn der Ledger-Schlüssel ist ihr Hash. Geändert →
// bleibt liegen, der nächste Durchlauf sieht den neuen Hash. Partner gehen wie
// bisher ins Archiv (liegen gelassen würden sie zur Waise, ggf. zweite Notiz).
func (a *App) archiveJob(j job, now time.Time) {
	files := j.files()
	if !a.dry && changedSinceRead(j.primary) {
		a.log.Printf("   ⚠️  %s wurde seit dem Einlesen geändert → nicht archiviert, bleibt im Exportordner (nächster Durchlauf prüft sie erneut)", j.primary.Name)
		files = files[1:]
	}
	a.archiveAt(now, files...)
}

// changedSinceRead meldet, ob Größe oder mtime von f nicht mehr zu den Werten
// aus listEntries passen (Lstat wie d.Info). Fehlt die Datei, entscheidet
// moveFile wie bisher.
func changedSinceRead(f *entry) bool {
	info, err := os.Lstat(f.Path)
	if err != nil {
		return false
	}
	return info.Size() != f.Size || !info.ModTime().Equal(f.ModTime)
}

// fail verschiebt die Dateien nach _fehler; ok=false, wenn eine liegen blieb.
func (a *App) fail(cause error, files ...*entry) (ok bool) {
	ok = true
	if a.dry {
		return ok
	}
	for _, f := range files {
		dst, err := a.move(f, a.cfg.FailedDir)
		if err != nil {
			a.log.Printf("   ⚠️  %s konnte nicht verschoben werden: %v", f.Name, err)
			ok = false
			continue
		}
		delete(a.cache, f.Path)
		if err := writeFehlerTxt(dst, cause); err != nil {
			a.log.Printf("   ⚠️  Fehlerhinweis zu %s nicht geschrieben: %v", f.Name, err)
		}
	}
	return ok
}

func (a *App) warnOnce(key, format string, args ...any) {
	if a.warned[key] {
		return
	}
	a.warned[key] = true
	a.log.Printf(format, args...)
}

// writeFehlerTxt legt dst.fehler.txt an, ohne einen vorhandenen Hinweis zu
// überschreiben; ist der Name belegt, "dst (n).fehler.txt".
func writeFehlerTxt(dst string, cause error) error {
	data := []byte(fmt.Sprintf("%s\n%v\n", time.Now().Format(time.RFC3339), cause))
	name := dst + ".fehler.txt"
	for i := 2; ; i++ {
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) //nolint:gosec // G304: Pfad aus moveFile unter failed_dir
		if errors.Is(err, os.ErrExist) {
			name = fmt.Sprintf("%s (%d).fehler.txt", dst, i)
			continue
		}
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		return err
	}
}

// renameFn benennt src exklusiv in dst um (renamex_np mit RENAME_EXCL):
// atomar, ein vorhandenes dst bleibt unangetastet (EEXIST). Nur Tests tauschen
// sie aus.
var renameFn = func(src, dst string) error {
	if err := unix.RenamexNp(src, dst, unix.RENAME_EXCL); err != nil {
		return &os.LinkError{Op: "renamex_np", Old: src, New: dst, Err: err}
	}
	return nil
}

// copyFn kopiert den Inhalt; nur Tests tauschen sie aus.
var copyFn = io.Copy

// syncDirFn synchronisiert den Zielordner in moveFile; nur Tests tauschen sie aus.
var syncDirFn = syncDir

// dirSyncError: Kopie liegt in dst, das Original ist gelöscht, nur der Sync
// des Zielordners schlug fehl. moveFile liefert dst mit diesem Fehler.
type dirSyncError struct {
	dst string
	err error
}

func (e *dirSyncError) Error() string {
	return fmt.Sprintf("Zielordner nicht synchronisiert, trotzdem verschoben nach %s: %v", e.dst, e.err)
}

func (e *dirSyncError) Unwrap() error { return e.err }

// moveFile verschiebt src nach dir, ohne vorhandene Dateien zu überschreiben.
// Gleiches Volume: atomares Umbenennen mit RENAME_EXCL (ein Symlink wird als
// Symlink verschoben). Anderes Volume oder Flag nicht unterstützt: Kopie mit
// O_EXCL, Zielordner sync, dann src löschen, aber nur, wenn src noch die
// kopierte Datei ist. Ist der Name belegt, "stem (n).ext". Gelöscht wird
// außer src nur die eigene, eben angelegte Kopie (von Mark erlaubt,
// 2026-11-02), und nur nach Identitätsprüfung. Scheitert nur der Sync des
// Zielordners, geht es weiter wie sonst; dann kommen dst und ein
// *dirSyncError zurück (Mark, 2026-11-02).
func moveFile(src, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	base := filepath.Base(src)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	name := func(i int) string {
		if i == 1 {
			return filepath.Join(dir, base)
		}
		return filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
	}
	i := 1
	for ; ; i++ {
		err := renameFn(src, name(i))
		if err == nil {
			return name(i), nil
		}
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if !errors.Is(err, unix.EXDEV) && !errors.Is(err, unix.ENOTSUP) {
			return "", err
		}
		break // anderes Volume oder RENAME_EXCL nicht unterstützt: kopieren
	}
	for ; ; i++ {
		dst := name(i)
		srcInfo, dstInfo, err := copyExcl(src, dst)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		// Ordnereintrag der Kopie soll einen Absturz überstehen, bevor src weg ist.
		// Scheitert das, trotzdem weiter (Kopie selbst ist synchronisiert).
		syncErr := syncDirFn(dir)
		cur, err := os.Lstat(src)
		if err != nil || !os.SameFile(cur, srcInfo) {
			msg := fmt.Sprintf("Quelle %s während des Kopierens ersetzt oder entfernt, Quelle und Kopie %s bleiben liegen", src, dst)
			if err != nil {
				msg += fmt.Sprintf(" (%v)", err)
			}
			return "", errors.New(msg)
		}
		if err := os.Remove(src); err != nil {
			// Keine Kopie je Versuch ansammeln: nur die eigene, frische Kopie löschen,
			// einmal und ohne Wiederholung.
			if rerr := removeOwn(dst, dstInfo); rerr != nil {
				return "", fmt.Errorf("Quelle nicht löschbar, Original bleibt liegen: %w; eigene Kopie %s nicht entfernt: %v", err, dst, rerr)
			}
			return "", fmt.Errorf("Quelle nicht löschbar, Original bleibt liegen, eigene Kopie entfernt: %w", err)
		}
		if syncErr != nil {
			return dst, &dirSyncError{dst: dst, err: syncErr}
		}
		return dst, nil
	}
}

// copyExcl kopiert src nach dst; dst darf noch nicht existieren (O_EXCL).
// Sync vor Close, damit die Kopie vor dem Löschen der Quelle auf Platte ist.
// Liefert die Identität von Quelle und Kopie. Schlägt das Kopieren fehl,
// löscht copyExcl die eigene Teilkopie (von Mark erlaubt, 2026-11-02).
func copyExcl(src, dst string) (srcInfo, dstInfo os.FileInfo, err error) {
	in, err := os.Open(src) //nolint:gosec // G304: Pfad aus dem Exportordner
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = in.Close() }() //nolint:errcheck // nur gelesen, Close-Fehler ohne Folgen
	if srcInfo, err = in.Stat(); err != nil {
		return nil, nil, err
	}
	// Nur eine reguläre Datei, die noch unter src liegt (kein Symlink): sonst
	// würde die Kopie bei jedem Versuch neu angelegt und bliebe liegen.
	li, err := os.Lstat(src)
	if err != nil {
		return nil, nil, err
	}
	if !li.Mode().IsRegular() || !os.SameFile(li, srcInfo) {
		return nil, nil, fmt.Errorf("Quelle %s ist keine reguläre Datei oder wurde ersetzt, nicht kopiert", src)
	}

	// Rechte wie bisher os.Create
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666) //nolint:gosec // G302/G304: Pfad aus moveFile
	if err != nil {
		return nil, nil, err
	}
	dstInfo, err = out.Stat()
	if err == nil {
		_, err = copyFn(out, in)
	}
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		if rerr := removeOwn(dst, dstInfo); rerr != nil {
			return nil, nil, fmt.Errorf("Kopie nach %s fehlgeschlagen: %w; Teilkopie nicht entfernt: %v", dst, err, rerr)
		}
		return nil, nil, fmt.Errorf("Kopie nach %s fehlgeschlagen, Teilkopie entfernt: %w", dst, err)
	}
	return srcInfo, dstInfo, nil
}

// removeOwn löscht path nur, wenn es noch die selbst angelegte Datei own ist.
func removeOwn(path string, own os.FileInfo) error {
	if own == nil {
		return errors.New("Identität der eigenen Kopie unbekannt")
	}
	cur, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !os.SameFile(cur, own) {
		return fmt.Errorf("%s ist nicht mehr die eigene Kopie", path)
	}
	return os.Remove(path)
}

// --- Protokoll gegen Doppelverarbeitung ---------------------------------------

// Status eines Ledger-Eintrags (ADR 0004). Pro Hash gilt die letzte Zeile.
// Leerer Status (Zeilen von v1.0.0, Add ohne Status) bedeutet fertig.
const (
	StatusBegonnen  = "begonnen"  // Verarbeitung gestartet, Notion-Seite evtl. angelegt
	StatusFertig    = "fertig"    // Notiz angelegt
	StatusVerworfen = "verworfen" // nach _fehler verschoben, darf neu laufen
)

type LedgerEntry struct {
	Hash    string
	Status  string
	File    string
	ModTime time.Time
	URL     string
	At      time.Time
	// Nur bei fertig, 0 im Normalfall (R-M1-03, OD-04: nur protokolliert);
	// nur für das Schreiben, LoadLedger liest sie nicht zurück.
	MissingTasks int // nicht angelegte To-dos
	BlockErrors  int // Fehler beim Anhängen der restlichen Blöcke
}

// ledgerLine ist das Zeilenformat auf Platte. Fertige Zeilen tragen den Hash
// in "hash" und keinen Status, byte-gleich zu v1.0.0. Alle anderen Zeilen
// tragen ihn in "sha256": v1.0.0 wertet jede Zeile mit "hash" als verarbeitet
// und würde sonst eine begonnene Datei ohne Notiz archivieren.
type ledgerLine struct {
	Hash    string    `json:"hash,omitempty"`
	SHA256  string    `json:"sha256,omitempty"`
	Status  string    `json:"status,omitempty"`
	File    string    `json:"file"`
	ModTime time.Time `json:"mtime"`
	URL     string    `json:"url"`
	At      time.Time `json:"at"`
	// omitempty: ohne Lücken bleibt die fertig-Zeile byte-gleich zu v1.0.0;
	// v1.0.0 ignoriert die Zusatzfelder beim Lesen.
	TodosFehlen   int `json:"todos_fehlen,omitempty"`
	BloeckeFehler int `json:"bloecke_fehler,omitempty"`
}

type Ledger struct {
	path    string
	persist bool
	byHash  map[string]LedgerEntry
}

// put übernimmt e als letzten Stand für e.Hash.
func (l *Ledger) put(e LedgerEntry) LedgerEntry {
	if e.Status == "" {
		e.Status = StatusFertig
	}
	l.byHash[e.Hash] = e
	return e
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
	defer func() { _ = f.Close() }() //nolint:errcheck // nur gelesen, Close-Fehler ohne Folgen
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var ln ledgerLine
		if json.Unmarshal(sc.Bytes(), &ln) != nil {
			continue
		}
		hash := ln.Hash
		if hash == "" {
			hash = ln.SHA256
		}
		if hash == "" {
			continue
		}
		l.put(LedgerEntry{Hash: hash, Status: ln.Status, File: ln.File, ModTime: ln.ModTime, URL: ln.URL, At: ln.At})
	}
	return l, sc.Err()
}

// Has meldet, ob der letzte Eintrag zu hash fertig ist.
func (l *Ledger) Has(hash string) bool { return l.byHash[hash].Status == StatusFertig }

// Started meldet, ob der letzte Eintrag zu hash "begonnen" ist.
func (l *Ledger) Started(hash string) bool { return l.byHash[hash].Status == StatusBegonnen }

// HasNear meldet, ob ein fertiger Eintrag eine mtime im Fenster um t hat.
func (l *Ledger) HasNear(t time.Time, window time.Duration) bool {
	for _, e := range l.byHash {
		if e.Status == StatusFertig && absDur(e.ModTime.Sub(t)) <= window {
			return true
		}
	}
	return false
}

// Add übernimmt e sofort in den Speicher und hängt danach eine Zeile an die
// Datei an (mit fsync, beim ersten Anlegen auch des Ordners). Fehler von
// Write, Sync und Close kommen zurück; der Eintrag im Speicher bleibt dann
// trotzdem stehen.
func (l *Ledger) Add(e LedgerEntry) error {
	e = l.put(e)
	if !l.persist {
		return nil
	}
	ln := ledgerLine{File: e.File, ModTime: e.ModTime, URL: e.URL, At: e.At, TodosFehlen: e.MissingTasks, BloeckeFehler: e.BlockErrors}
	if e.Status == StatusFertig {
		ln.Hash = e.Hash
	} else {
		ln.SHA256, ln.Status = e.Hash, e.Status
	}
	b, err := json.Marshal(ln)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	_, statErr := os.Lstat(l.path)
	created := errors.Is(statErr, os.ErrNotExist)
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	line := append(b, '\n')
	// Abgerissene letzte Zeile (Absturz beim Schreiben): erst '\n', sonst
	// verschmilzt die neue Zeile mit dem Rest und geht beim Laden verloren.
	info, err := f.Stat()
	if err == nil && info.Size() > 0 {
		last := []byte{0}
		if _, err = f.ReadAt(last, info.Size()-1); err == nil && last[0] != '\n' {
			line = append([]byte{'\n'}, line...)
		}
	}
	if err == nil {
		_, err = f.Write(line)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && created {
		err = syncDir(filepath.Dir(l.path)) // neuer Ordnereintrag soll den Absturz überstehen
	}
	return err
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}

// --- Sonstiges -----------------------------------------------------------------------

func notify(title, msg string) {
	if runtime.GOOS != "darwin" {
		return
	}
	q := func(s string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"` }
	_ = exec.Command("osascript", "-e", "display notification "+q(msg)+" with title "+q(title)).Run() //nolint:errcheck // Benachrichtigung ist Best-Effort, Fehler egal
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
