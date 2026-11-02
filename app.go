package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"meetingsync/internal/config"
	"meetingsync/internal/domain"
	"meetingsync/internal/ledger"
	"meetingsync/internal/llm"
	"meetingsync/internal/notify"
	"meetingsync/internal/notion"
	"meetingsync/internal/secret"
	"meetingsync/internal/transcript"
	"meetingsync/internal/watch"
)

type App struct {
	cfg      *config.Config
	loc      *time.Location // Zeitzone aus cfg.Timezone, von main aufgelöst
	log      *log.Logger
	dry      bool
	llm      *llm.Client
	notion   *notion.Client
	ledger   *ledger.Ledger
	notifier notify.Notifier

	notesDS, tasksDS, projectsDS          string
	notesTypes, tasksTypes, projectsTypes notion.Schema

	state  map[string]*fileState
	warned map[string]bool
	cache  watch.Cache
	mover  watch.Mover // Nullwert: echtes Verschieben

	// interruptedNow: Hashes, die in diesem Durchlauf per Neustart-Regel nach
	// _fehler gingen. Nur je Durchlauf, damit eine von Mark zurückgelegte
	// Datei auch ohne Neustart des Prozesses wieder normal läuft (ADR 0004).
	interruptedNow map[string]bool

	projects   []notion.Page
	projectsAt time.Time
}

type fileState struct {
	attempts  int
	transient int
	nextTry   time.Time
}

type job struct {
	primary     *watch.Entry
	summary     *watch.Entry // optional, gehört zum Transkript
	summaryOnly bool
}

func (j job) files() []*watch.Entry {
	if j.summary != nil {
		return []*watch.Entry{j.primary, j.summary}
	}
	return []*watch.Entry{j.primary}
}

// newLLMClient baut den LM-Studio-Client aus der Konfiguration. transport nil
// heißt http.DefaultTransport.
func newLLMClient(cfg *config.Config, logger *log.Logger, transport http.RoundTripper) *llm.Client {
	return llm.NewClient(llm.Options{
		BaseURL:          cfg.LLM.BaseURL,
		Model:            cfg.LLM.Model,
		APIKey:           cfg.LLM.APIKey,
		Temperature:      cfg.LLM.Temperature,
		Timeout:          time.Duration(cfg.LLM.TimeoutMinutes) * time.Minute,
		MaxChunkChars:    cfg.LLM.MaxChunkChars,
		StructuredOutput: cfg.LLM.StructuredOutput,
	}, logger, transport)
}

// NewApp baut die App. loc ist die aufgelöste Zeitzone der Config, tokens
// liefert den Notion-Token.
func NewApp(cfg *config.Config, loc *time.Location, tokens secret.TokenSource, logger *log.Logger, dry bool) (*App, error) {
	a := &App{
		cfg:      cfg,
		loc:      loc,
		log:      logger,
		dry:      dry,
		llm:      newLLMClient(cfg, logger, nil),
		state:    map[string]*fileState{},
		warned:   map[string]bool{},
		cache:    watch.Cache{},
		notifier: notify.New(),
	}
	token, err := tokens.Token()
	switch {
	case err == nil:
		a.notion = notion.NewClient(cfg.Notion.BaseURL, cfg.Notion.APIVersion, token, nil)
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
	l, err := ledger.Load(filepath.Join(a.cfg.ArchiveDir, ".meetingsync-ledger.jsonl"), !a.dry)
	if err != nil {
		return err
	}
	a.ledger = l
	if a.notion == nil {
		return nil
	}

	nc := a.cfg.Notion
	if a.notesDS, a.notesTypes, err = a.notion.ResolveDataSource(ctx, nc.NotesDataSourceID); err != nil {
		return fmt.Errorf("Notes-Datenbank nicht lesbar: %w", err)
	}
	if err := a.notesTypes.CheckNotes(notion.NotesProps(nc.Notes)); err != nil {
		return err
	}
	if !a.notesTypes.Is(nc.Notes.Review, notion.KindCheckbox) {
		a.log.Printf("Hinweis: Notes hat keine Checkbox %q – Prüf-Hinweise stehen nur als Hinweisbox oben auf der Seite.", nc.Notes.Review)
	}

	if nc.CreateTasksFor != "none" && nc.TasksDataSourceID != "" {
		if a.tasksDS, a.tasksTypes, err = a.notion.ResolveDataSource(ctx, nc.TasksDataSourceID); err != nil {
			return fmt.Errorf("Tasks-Datenbank nicht lesbar: %w", err)
		}
		if err := a.tasksTypes.CheckTasks(notion.TasksProps(nc.Tasks)); err != nil {
			return err
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
	var transcripts, summaries, unstable []*watch.Entry
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
	pairs := make(map[string]*watch.Entry)
	for _, t := range transcripts {
		if s := watch.PairSummary(t, summaries, claimed, window); s != nil {
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
		if watch.Near(unstable, t.ModTime, window) {
			continue // die passende Zusammenfassung wird evtl. noch geschrieben
		}
		a.handle(ctx, job{primary: t, summary: pairs[t.Path]}, now)
	}

	orphanWait := time.Duration(a.cfg.OrphanSummaryMinutes) * time.Minute
	for _, s := range summaries {
		if ctx.Err() != nil || claimed[s.Path] {
			continue
		}
		if watch.Near(unstable, s.ModTime, window) {
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
		if err := a.ledger.Add(ledger.Entry{Hash: hash, File: p.Name, ModTime: p.ModTime, URL: url, At: time.Now(), MissingTasks: g.tasks, BlockErrors: g.blocks}); err != nil {
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
	if err := a.ledger.Add(ledger.Entry{Hash: hash, Status: ledger.StatusVerworfen, File: p.Name, ModTime: p.ModTime, At: time.Now()}); err != nil {
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
	timing := transcript.MeetingTiming(p.Name, p.ModTime, dur, a.loc)

	in := llm.NoteInput{
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
	llm.Normalize(note)
	a.log.Printf("   LLM fertig nach %s: %q", time.Since(start).Round(time.Second), note.Titel)

	reasons := append([]string{}, note.PruefGruende...)
	if j.summaryOnly {
		reasons = append(reasons, "Kein Transkript mit Zeitmarken gefunden – Notiz basiert auf einer Datei ohne Sprecher/Zeitstempel")
	}
	var project *notion.Page
	for i := range projects {
		if strings.EqualFold(projects[i].Name, note.Projekt) {
			project = &projects[i]
			break
		}
	}
	title := notion.FormatTitle(a.cfg.TitleFormat, note, timing)
	meta := domain.NoteMeta{Timing: timing, ReviewReasons: reasons}
	for _, f := range j.files() {
		meta.SourceFiles = append(meta.SourceFiles, f.Name)
	}
	if a.cfg.IncludeTranscript {
		meta.Transcript = text
	}
	blocks := notion.NoteBlocks(note, meta)

	if a.dry {
		a.printDryRun(title, project, note, reasons, len(blocks))
		return "dry-run", gaps{}, nil
	}

	le := ledger.Entry{Hash: hash, Status: ledger.StatusBegonnen, File: p.Name, ModTime: p.ModTime, At: time.Now()}
	if hash != "" {
		if err := a.ledger.Add(le); err != nil {
			// Ohne "begonnen" keine Notion-Anfrage. verworfen setzt den Speicher
			// zurück, damit der nächste Versuch normal läuft.
			le.Status = ledger.StatusVerworfen
			_ = a.ledger.Add(le) //nolint:errcheck // gemeldet wird der erste Fehler; bleibt auf Platte "begonnen", greift beim Neustart die Prüfregel (sicher)
			return "", gaps{}, fmt.Errorf("Protokoll nicht schreibbar, Notion-Seite nicht angelegt: %w", err)
		}
	}
	var g gaps
	pageID, url, err := a.notion.CreatePage(ctx, a.notesDS, notion.NoteProperties(a.notesTypes, notion.NotesProps(a.cfg.Notion.Notes), title, project, len(reasons) > 0), blocks)
	if errors.Is(err, notion.ErrUnvollstaendig) {
		// Seite existiert: kein Fehler, sonst entsteht beim nächsten Versuch eine Dublette.
		a.log.Printf("   ⚠️  %v", err)
		g.blocks, err = 1, nil
	}
	if err != nil {
		// Antwort mit Fehler: neuer Versuch wie bisher (OD-05), also verworfen.
		// Bei Abbruch per Kontext bleibt "begonnen": die Anfrage kann Notion
		// erreicht haben, der Neustart schickt die Datei zur Prüfung.
		if hash != "" && ctx.Err() == nil {
			le.Status, le.At = ledger.StatusVerworfen, time.Now()
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
		a.notifier.Notify("Neue Meeting-Notiz", msg)
	}
	return url, g, nil
}

// createTasks legt To-dos in der Tasks-Datenbank an und verknüpft sie mit der Notiz.
// Liefert die Zahl der angelegten und der fehlenden To-dos (Fehler oder Abbruch).
func (a *App) createTasks(ctx context.Context, n *domain.MeetingNote, noteID string, project *notion.Page, title string) (created, missing int) {
	mode := a.cfg.Notion.CreateTasksFor
	if a.tasksDS == "" || mode == "none" {
		return 0, 0
	}
	tp := notion.TasksProps(a.cfg.Notion.Tasks)
	for _, act := range n.NextActions {
		if mode == "me" && !act.FuerMich {
			continue
		}
		if ctx.Err() != nil {
			missing++ // Abbruch: keine weiteren Anfragen, nur zählen
			continue
		}
		props := notion.TaskProperties(a.tasksTypes, tp, act, title, noteID, project)
		if _, _, err := a.notion.CreatePage(ctx, a.tasksDS, props, nil); err != nil {
			a.log.Printf("   ⚠️  To-do %q nicht angelegt: %v", act.Aufgabe, err)
			missing++
			continue
		}
		created++
	}
	return created, missing
}

func (a *App) activeProjects(ctx context.Context) []notion.Page {
	if a.notion == nil || a.projectsDS == "" {
		return nil
	}
	if time.Since(a.projectsAt) < 15*time.Minute {
		return a.projects
	}
	p, err := a.notion.ActiveProjects(ctx, a.projectsDS, notion.ProjectsProps(a.cfg.Notion.Projects), a.projectsTypes)
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
	path = config.ExpandHome(path)
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	e := &watch.Entry{Path: path, Name: filepath.Base(path), ModTime: info.ModTime(), Size: info.Size(), Stable: true, Content: content}
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

func (a *App) printDryRun(title string, project *notion.Page, n *domain.MeetingNote, reasons []string, blocks int) {
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

func mustJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ") //nolint:errcheck // Marshal von Notiz/Properties (nur Strings, Zahlen, Listen) scheitert nicht
	return string(b)
}

// --- Dateien ---------------------------------------------------------------------

// listEntries liest den Exportordner über internal/watch (echtes Dateisystem).
func (a *App) listEntries(now time.Time) ([]*watch.Entry, error) {
	return watch.Lister{
		FS:       watch.OS{},
		Dir:      a.cfg.WatchDir,
		Stable:   time.Duration(a.cfg.StableSeconds) * time.Second,
		Opts:     a.transcriptOpts(),
		Cache:    a.cache,
		Log:      a.log,
		WarnOnce: a.warnOnce,
	}.List(now)
}

// archive verschiebt die Dateien ins Archiv und liefert die, die liegen
// blieben. Erfolg löscht den Retry-Zustand der Datei.
func (a *App) archive(files ...*watch.Entry) (failed []*watch.Entry) {
	if a.dry {
		return nil
	}
	for _, f := range files {
		dir := filepath.Join(a.cfg.ArchiveDir, f.ModTime.In(a.loc).Format("2006-01"))
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
func (a *App) archiveAt(now time.Time, files ...*watch.Entry) {
	for _, f := range a.archive(files...) {
		st := a.state[f.Path]
		if st == nil {
			st = &fileState{}
			a.state[f.Path] = st
		}
		st.nextTry = now.Add(2 * time.Minute)
	}
}

// move ruft Mover.Move; scheitert nur der Sync des Zielordners, ist die Datei
// verschoben: Warnung, kein Fehler (Mark, 2026-11-02).
func (a *App) move(f *watch.Entry, dir string) (string, error) {
	dst, err := a.mover.Move(f.Path, dir)
	var se *watch.DirSyncError
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
// Mover.Move wie bisher.
func changedSinceRead(f *watch.Entry) bool {
	info, err := os.Lstat(f.Path)
	if err != nil {
		return false
	}
	return info.Size() != f.Size || !info.ModTime().Equal(f.ModTime)
}

// fail verschiebt die Dateien nach _fehler; ok=false, wenn eine liegen blieb.
func (a *App) fail(cause error, files ...*watch.Entry) (ok bool) {
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
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) //nolint:gosec // G304: Pfad aus Mover.Move unter failed_dir; G302: 0o644 für .fehler.txt, Dateirechte geplant in S-12 / R-M3-04 (Entfernung geplant)
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

// --- Sonstiges -----------------------------------------------------------------------

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
