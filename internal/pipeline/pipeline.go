// Package pipeline ist der Ablauf je Durchlauf: Exportordner lesen, Paare
// bilden, LLM, Notion-Seite und Tasks, Protokoll, Archiv und _fehler,
// Wiederholung mit Backoff (ADR 0004).
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"meetingsync/internal/domain"
	"meetingsync/internal/ledger"
	"meetingsync/internal/llm"
	"meetingsync/internal/notion"
	"meetingsync/internal/transcript"
	"meetingsync/internal/watch"
)

// Notion ist der Teil des Notion-Clients, den die Pipeline braucht.
type Notion interface {
	notion.PageWriter
	ActiveProjects(ctx context.Context, dsID string, p notion.ProjectsProps, types notion.Schema) ([]notion.Page, error)
}

// Notifier zeigt eine Mitteilung (wie notify.Notifier). Eigene Deklaration,
// damit die Pipeline internal/notify und damit os/exec nicht importiert
// (Regel in arch_test.go).
type Notifier interface {
	Notify(title, msg string)
}

// Options sind die Werte aus der Konfiguration und aus Init (Datenbanken).
type Options struct {
	WatchDir, ArchiveDir, FailedDir string
	Stable                          time.Duration // stable_seconds
	PairWindow                      time.Duration // pair_window_seconds
	OrphanWait                      time.Duration // orphan_summary_minutes
	OrphanAction                    string        // orphan_summary_action
	MaxAttempts                     int
	Transcript                      transcript.Options
	Loc                             *time.Location // Zeitzone für Archivmonat und Zeiten
	Dry                             bool

	MyName, MyLabel     string
	UseSummaryAsContext bool
	IncludeTranscript   bool
	Notify              bool
	Model               string // nur für das Log
	TitleFormat         string

	CreateTasksFor                        string
	Notes                                 notion.NotesProps
	Tasks                                 notion.TasksProps
	Projects                              notion.ProjectsProps
	NotesDS, TasksDS, ProjectsDS          string
	NotesTypes, TasksTypes, ProjectsTypes notion.Schema
}

// Deps sind die Abhängigkeiten der Pipeline.
type Deps struct {
	Now      func() time.Time
	FS       watch.FS
	Mover    watch.Mover
	LLM      llm.Summarizer
	Notion   Notion // nil: ohne Notion (Dry-Run ohne Token, keine Projektliste)
	Notifier Notifier
	Ledger   *ledger.Ledger
	Log      *log.Logger
	// DryRun gibt das Ergebnis im Dry-Run aus (nötig bei Options.Dry).
	DryRun func(title string, project *notion.Page, n *domain.MeetingNote, reasons []string, blocks int)
}

// Pipeline verarbeitet den Exportordner. Nicht nebenläufig benutzen.
type Pipeline struct {
	o        Options
	now      func() time.Time
	fs       watch.FS
	mover    watch.Mover
	llm      llm.Summarizer
	notion   Notion
	notifier Notifier
	ledger   *ledger.Ledger
	log      *log.Logger
	dryRun   func(title string, project *notion.Page, n *domain.MeetingNote, reasons []string, blocks int)
	retry    retryPolicy

	states map[string]*FileState
	warned map[string]bool
	cache  watch.Cache

	// interruptedNow: Hashes, die in diesem Durchlauf per Neustart-Regel nach
	// _fehler gingen. Nur je Durchlauf, damit eine von Mark zurückgelegte
	// Datei auch ohne Neustart des Prozesses wieder normal läuft (ADR 0004).
	interruptedNow map[string]bool

	projects   []notion.Page
	projectsAt time.Time
}

// noopNotifier ist der Ersatz, wenn keine Mitteilung verdrahtet ist.
type noopNotifier struct{}

func (noopNotifier) Notify(string, string) {}

// New baut die Pipeline. Fehlt Notifier oder DryRun, tun sie nichts.
func New(o Options, d Deps) *Pipeline {
	if d.Notifier == nil {
		d.Notifier = noopNotifier{}
	}
	if d.DryRun == nil {
		d.DryRun = func(string, *notion.Page, *domain.MeetingNote, []string, int) {}
	}
	return &Pipeline{
		o: o, now: d.Now, fs: d.FS, mover: d.Mover, llm: d.LLM, notion: d.Notion,
		notifier: d.Notifier, ledger: d.Ledger, log: d.Log, dryRun: d.DryRun,
		retry:  newRetryPolicy(o.MaxAttempts),
		states: map[string]*FileState{},
		warned: map[string]bool{},
		cache:  watch.Cache{},
	}
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

// Pass ist ein Durchlauf über den Exportordner.
func (p *Pipeline) Pass(ctx context.Context, now time.Time) {
	entries, err := p.listEntries(now)
	if err != nil {
		p.log.Printf("Ordner nicht lesbar: %v", err)
		return
	}
	p.interruptedNow = map[string]bool{}
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
	window := p.o.PairWindow

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
		if st := p.states[t.Path]; st != nil && now.Before(st.NextTry) {
			continue
		}
		if watch.Near(unstable, t.ModTime, window) {
			continue // die passende Zusammenfassung wird evtl. noch geschrieben
		}
		p.handle(ctx, job{primary: t, summary: pairs[t.Path]}, now)
	}

	for _, s := range summaries {
		if ctx.Err() != nil || claimed[s.Path] {
			continue
		}
		if watch.Near(unstable, s.ModTime, window) {
			continue // vielleicht wird das passende Transkript gerade geschrieben
		}
		// vor HasNear: auch ein fehlgeschlagenes Archivieren wartet (R-M1-07)
		if st := p.states[s.Path]; st != nil && now.Before(st.NextTry) {
			continue
		}
		if p.ledger.HasNear(s.ModTime, window) {
			p.log.Printf("📎 %s gehört zu einem bereits verarbeiteten Transkript → Archiv", s.Name)
			p.archiveAt(now, s)
			continue
		}
		if now.Sub(s.ModTime) < p.o.OrphanWait {
			continue
		}
		switch p.o.OrphanAction {
		case "archive":
			p.log.Printf("📎 %s: kein Transkript gefunden → Archiv (orphan_summary_action=archive)", s.Name)
			p.archiveAt(now, s)
		case "keep":
			p.warnOnce(s.Path, "%s: kein Transkript gefunden, bleibt liegen (orphan_summary_action=keep)", s.Name)
		default:
			p.handle(ctx, job{primary: s, summaryOnly: true}, now)
		}
	}
}

func (p *Pipeline) handle(ctx context.Context, j job, now time.Time) {
	pr := j.primary
	hash := sha256Hex(pr.Content)
	if p.ledger.Has(hash) {
		p.log.Printf("↺ %s wurde schon verarbeitet → Archiv", pr.Name)
		p.archiveJob(j, now)
		return
	}
	if p.ledger.Started(hash) || p.interruptedNow[hash] {
		p.interrupted(j, hash, now)
		return
	}
	p.log.Printf("→ %s (%s, %d KB)", pr.Name, map[bool]string{true: "nur Zusammenfassung", false: "Transkript"}[j.summaryOnly], pr.Size/1024)
	url, g, err := p.process(ctx, j, hash)
	if err == nil {
		// Auch nach einem Abbruch in createTasks: die Seite existiert, also fertig
		// (mit den Lücken), sonst schickt der Neustart sie nach _fehler (ADR 0004).
		if err := p.ledger.Add(ledger.Entry{Hash: hash, File: pr.Name, ModTime: pr.ModTime, URL: url, At: p.now(), MissingTasks: g.tasks, BlockErrors: g.blocks}); err != nil {
			// Die Notiz existiert. Im Speicher gilt der Hash als fertig (keine
			// zweite Seite in diesem Prozess), die Dateien gehen ins Archiv.
			p.log.Printf("   ⚠️  Protokoll nicht geschrieben (Notiz existiert: %s): %v", url, err)
		}
		// vor archiveJob: ein Archiv-Fehler setzt eine neue Wartezeit
		delete(p.states, pr.Path)
		p.archiveJob(j, now)
		return
	}

	p.handleError(ctx, j, err, now)
}

// ErrUnterbrochen steht in .fehler.txt, wenn ein früherer Lauf nach
// "begonnen" abgebrochen ist (ADR 0004).
var ErrUnterbrochen = errors.New("Notion-Seite existiert evtl. schon – bitte in Notion prüfen. " +
	"Die Verarbeitung wurde beim Anlegen der Seite unterbrochen (Absturz oder Abbruch). " +
	"Fehlt die Seite in Notion, die Datei(en) zurück in den Exportordner legen; sie werden dann neu verarbeitet.")

// interrupted: Der letzte Eintrag zu hash ist "begonnen", der Lauf davor
// endete also zwischen "begonnen" und "fertig". Die Seite kann existieren:
// nicht neu anlegen, sondern nach _fehler zur Prüfung durch Mark.
func (p *Pipeline) interrupted(j job, hash string, now time.Time) {
	pr := j.primary
	p.log.Printf("✗ %s: letzter Lauf wurde beim Anlegen in Notion unterbrochen → %s (bitte in Notion prüfen)", pr.Name, p.o.FailedDir)
	// Partner zuerst, das Transkript zuletzt, Stopp beim ersten Fehler: bleibt
	// etwas liegen, dann immer das Transkript mit "begonnen". So legt nie eine
	// allein liegende Zusammenfassung eine zweite Seite an.
	files := j.files()
	for i := len(files) - 1; i >= 0; i-- {
		if !p.fail(ErrUnterbrochen, files[i]) {
			p.retryLater(pr.Path, now) // bleibt "begonnen"
			return
		}
	}
	delete(p.states, pr.Path)
	// Kopien mit demselben Hash in diesem Durchlauf gehen ebenfalls nach _fehler.
	p.interruptedNow[hash] = true
	if !p.ledger.Started(hash) {
		return // Kopie: verworfen steht schon im Protokoll
	}
	// verworfen: legt Mark die Datei zurück, läuft sie normal.
	if err := p.ledger.Add(ledger.Entry{Hash: hash, Status: ledger.StatusVerworfen, File: pr.Name, ModTime: pr.ModTime, At: p.now()}); err != nil {
		p.log.Printf("   ⚠️  Protokoll nicht geschrieben: %v", err)
	}
}

// handleError zählt vorübergehende und andere Fehler gemeinsam gegen max_attempts.
// Nur die Wartezeit unterscheidet sich; ein Abbruch per Kontext zählt nicht.
func (p *Pipeline) handleError(ctx context.Context, j job, err error, now time.Time) {
	pr := j.primary
	if !p.retry.counts(ctx, err) {
		return
	}
	trans := domain.IsTransient(err)
	st := p.state(pr.Path)
	wait, giveUp := p.retry.record(st, trans, now)
	if giveUp {
		p.log.Printf("   ✗ %v – gebe auf, Dateien → %s", err, p.o.FailedDir)
		p.fail(err, j.files()...)
		delete(p.states, pr.Path)
		return
	}
	if trans {
		p.log.Printf("   ⏸  %v – neuer Versuch in %s (Versuch %d/%d)", err, wait, st.Attempts, p.retry.maxAttempts)
		return
	}
	p.log.Printf("   ✗ %v (Versuch %d/%d)", err, st.Attempts, p.retry.maxAttempts)
}

// gaps zählt, was nach dem Anlegen der Notiz in Notion fehlt (OD-04: nur
// protokollieren, nicht nachholen).
type gaps struct {
	tasks  int // nicht angelegte To-dos (Fehler oder Abbruch)
	blocks int // Fehler beim Anhängen der restlichen Blöcke
}

// ProcessFile verarbeitet e direkt (-file): ohne Protokoll, ohne Verschieben.
// Liefert die URL der Notiz ("dry-run" im Dry-Run).
func (p *Pipeline) ProcessFile(ctx context.Context, e *watch.Entry) (string, error) {
	url, _, err := p.process(ctx, job{primary: e, summaryOnly: e.Kind == domain.KindSummary}, "")
	return url, err
}

// process: Text aufbereiten → lokales LLM → Notion-Seite + Tasks.
// hash != "": vor CreatePage "begonnen" ins Protokoll (ADR 0004); leer bei -file.
// Ist die Seite angelegt, ist der Fehler nil, auch wenn Teile fehlen (gaps).
func (p *Pipeline) process(ctx context.Context, j job, hash string) (string, gaps, error) {
	pr := j.primary
	var text string
	var dur time.Duration
	if j.summaryOnly {
		text = strings.TrimSpace(transcript.NormalizeNewlines(string(pr.Content)))
	} else {
		text, dur = transcript.Text(pr.Path, pr.Content)
	}
	if text == "" {
		return "", gaps{}, errors.New("Datei ist leer")
	}
	timing := transcript.MeetingTiming(pr.Name, pr.ModTime, dur, p.o.Loc)

	in := llm.NoteInput{
		Text:        text,
		SummaryOnly: j.summaryOnly,
		Timing:      timing,
		MyName:      p.o.MyName,
		MyLabel:     p.o.MyLabel,
	}
	if j.summary != nil && p.o.UseSummaryAsContext {
		in.SummaryText = string(j.summary.Content)
	}
	projects := p.ActiveProjects(ctx)
	for _, pj := range projects {
		in.Projects = append(in.Projects, pj.Name)
	}

	start := p.now()
	p.log.Printf("   LLM läuft (%d Zeichen, Modell %s) …", len(text), p.o.Model)
	note, err := p.llm.Summarize(ctx, in)
	if err != nil {
		return "", gaps{}, err
	}
	llm.Normalize(note)
	p.log.Printf("   LLM fertig nach %s: %q", p.now().Sub(start).Round(time.Second), note.Titel)

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
	title := notion.FormatTitle(p.o.TitleFormat, note, timing)
	meta := domain.NoteMeta{Timing: timing, ReviewReasons: reasons}
	for _, f := range j.files() {
		meta.SourceFiles = append(meta.SourceFiles, f.Name)
	}
	if p.o.IncludeTranscript {
		meta.Transcript = text
	}
	blocks := notion.NoteBlocks(note, meta)

	if p.o.Dry {
		p.dryRun(title, project, note, reasons, len(blocks))
		return "dry-run", gaps{}, nil
	}

	le := ledger.Entry{Hash: hash, Status: ledger.StatusBegonnen, File: pr.Name, ModTime: pr.ModTime, At: p.now()}
	if hash != "" {
		if err := p.ledger.Add(le); err != nil {
			// Ohne "begonnen" keine Notion-Anfrage. verworfen setzt den Speicher
			// zurück, damit der nächste Versuch normal läuft.
			le.Status = ledger.StatusVerworfen
			_ = p.ledger.Add(le) //nolint:errcheck // gemeldet wird der erste Fehler; bleibt auf Platte "begonnen", greift beim Neustart die Prüfregel (sicher)
			return "", gaps{}, fmt.Errorf("Protokoll nicht schreibbar, Notion-Seite nicht angelegt: %w", err)
		}
	}
	var g gaps
	pageID, url, err := p.notion.CreatePage(ctx, p.o.NotesDS, notion.NoteProperties(p.o.NotesTypes, p.o.Notes, title, project, len(reasons) > 0), blocks)
	if errors.Is(err, notion.ErrUnvollstaendig) {
		// Seite existiert: kein Fehler, sonst entsteht beim nächsten Versuch eine Dublette.
		p.log.Printf("   ⚠️  %v", err)
		g.blocks, err = 1, nil
	}
	if err != nil {
		// Antwort mit Fehler: neuer Versuch wie bisher (OD-05), also verworfen.
		// Bei Abbruch per Kontext bleibt "begonnen": die Anfrage kann Notion
		// erreicht haben, der Neustart schickt die Datei zur Prüfung.
		if hash != "" && ctx.Err() == nil {
			le.Status, le.At = ledger.StatusVerworfen, p.now()
			if lerr := p.ledger.Add(le); lerr != nil {
				p.log.Printf("   ⚠️  Protokoll nicht geschrieben: %v", lerr)
			}
		}
		return "", gaps{}, err
	}
	mine, missing := p.createTasks(ctx, note, pageID, project, title)
	g.tasks = missing
	p.log.Printf("   ✓ Notion: %s (%d To-dos für dich%s) %s", title, mine, map[bool]string{true: ", ⚠️ prüfen", false: ""}[len(reasons) > 0], url)
	if g.tasks > 0 || g.blocks > 0 {
		p.log.Printf("   ⚠️  %s: Notiz unvollständig – %d To-do(s) nicht angelegt, %d Fehler beim Anhängen von Blöcken (nicht nachgeholt)", pr.Name, g.tasks, g.blocks)
	}

	if p.o.Notify && (len(reasons) > 0 || mine > 0) {
		msg := note.Titel
		if mine > 0 {
			msg += fmt.Sprintf(" · %d To-do(s) für dich", mine)
		}
		if len(reasons) > 0 {
			msg += " · bitte prüfen"
		}
		p.notifier.Notify("Neue Meeting-Notiz", msg)
	}
	return url, g, nil
}

// createTasks legt To-dos in der Tasks-Datenbank an und verknüpft sie mit der Notiz.
// Liefert die Zahl der angelegten und der fehlenden To-dos (Fehler oder Abbruch).
func (p *Pipeline) createTasks(ctx context.Context, n *domain.MeetingNote, noteID string, project *notion.Page, title string) (created, missing int) {
	mode := p.o.CreateTasksFor
	if p.o.TasksDS == "" || mode == "none" {
		return 0, 0
	}
	for _, act := range n.NextActions {
		if mode == "me" && !act.FuerMich {
			continue
		}
		if ctx.Err() != nil {
			missing++ // Abbruch: keine weiteren Anfragen, nur zählen
			continue
		}
		props := notion.TaskProperties(p.o.TasksTypes, p.o.Tasks, act, title, noteID, project)
		if _, _, err := p.notion.CreatePage(ctx, p.o.TasksDS, props, nil); err != nil {
			p.log.Printf("   ⚠️  To-do %q nicht angelegt: %v", act.Aufgabe, err)
			missing++
			continue
		}
		created++
	}
	return created, missing
}

// projectsTTL: so lange gilt die Projektliste, dann wird sie neu geladen.
const projectsTTL = 15 * time.Minute

// ActiveProjects liefert die aktiven Projekte (zwischengespeichert für
// projectsTTL). Ohne Notion oder Projects-Datenbank nil; ist die Liste nicht
// abrufbar, die zuletzt geladene.
func (p *Pipeline) ActiveProjects(ctx context.Context) []notion.Page {
	if p.notion == nil || p.o.ProjectsDS == "" {
		return nil
	}
	if p.now().Sub(p.projectsAt) < projectsTTL {
		return p.projects
	}
	pj, err := p.notion.ActiveProjects(ctx, p.o.ProjectsDS, p.o.Projects, p.o.ProjectsTypes)
	if err != nil {
		p.log.Printf("Hinweis: Projektliste nicht abrufbar: %v", err)
		return p.projects
	}
	p.projects, p.projectsAt = pj, p.now()
	return pj
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
