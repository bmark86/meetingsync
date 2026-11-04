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
	"io/fs"
	"log"
	"slices"
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
	FindByText(ctx context.Context, dsID, prop, text string) ([]notion.PageRef, error)
	AppendRest(ctx context.Context, pageID string, children []any) error
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
	MyRole, ExampleName string
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

	// lockBusy: die Sperre hält gerade ein anderer Lauf (-file, zweiter Dienst), die Meldung
	// steht schon im Log (einmal je Sperrzeit).
	lockBusy bool

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

// Pass ist ein Durchlauf über den Exportordner. Außer im Dry-Run unter der
// Sperre auf den Exportordner (R-M3-14); hält sie ein anderer Lauf (-file,
// zweiter Dienst), fällt der Durchlauf aus. Danach liest er neue Protokollzeilen anderer Prozesse nach.
func (p *Pipeline) Pass(ctx context.Context, now time.Time) {
	if !p.o.Dry {
		lk, err := ledger.TryLock(p.o.WatchDir)
		switch {
		case errors.Is(err, ledger.ErrLocked):
			if !p.lockBusy {
				p.log.Printf("Anderer Lauf aktiv (-file oder zweiter Dienst) – Durchlauf übersprungen")
				p.lockBusy = true
			}
			return
		case err == nil:
			p.lockBusy = false
			defer func() { _ = lk.Unlock() }() //nolint:errcheck // Schließen gibt den flock frei, Fehler ohne Folgen
		case !errors.Is(err, fs.ErrNotExist):
			p.warnOnce("watch-lock", "⚠️  Sperre auf dem Exportordner nicht möglich (%v) – Schutz gegen gleichzeitige Läufe aus", err)
		}
		// Ordner fehlt: listEntries meldet es wie bisher
	}
	if err := p.ledger.Refresh(); err != nil {
		p.warnOnce("ledger-refresh", "Protokoll nicht nachlesbar: %v", err)
	}
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
	if st := p.states[pr.Path]; st != nil && st.giveUp != nil {
		if st.giveUpHash == hash {
			p.log.Printf("✗ %s: max_attempts erreicht, neuer Versuch, die Dateien nach %s zu verschieben", pr.Name, p.o.FailedDir)
			p.giveUp(j, st, st.giveUp, now)
			return
		}
		// anderer Inhalt am selben Pfad: neue Datei, Zählung von vorn
		delete(p.states, pr.Path)
	}
	p.log.Printf("→ %s (%s, %d KB)", pr.Name, map[bool]string{true: "nur Zusammenfassung", false: "Transkript"}[j.summaryOnly], pr.Size/1024)
	url, g, res, err := p.process(ctx, j, hash, p.ambiguous(pr.Path, hash))
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
	if st := p.states[pr.Path]; st != nil && st.ambiguous != nil && st.ambiguous.hash == hash && st.ambiguous.res == nil && res != nil {
		st.ambiguous.res = res // nächster Versuch schreibt dieselbe Notiz, ohne LLM-Lauf (auch nach Neustart)
	}
}

// ambiguous liefert den unklaren Ausgang des letzten Anlegens für den Inhalt
// hash am Pfad path, sonst nil. Anderer Inhalt am selben Pfad verwirft ihn.
// Ohne Zustand (Neustart, umbenannt, zurückgelegt) gilt mit Herkunftsspalte
// ein "verworfen" im Protokoll als unklar, seit dessen Zeit, mit dessen Datei
// und mtime als Herkunft, ohne LLM-Ergebnis.
func (p *Pipeline) ambiguous(path, hash string) *ambiguousCreate {
	st := p.states[path]
	if st != nil {
		if st.ambiguous != nil && st.ambiguous.hash != hash {
			st.ambiguous = nil
		}
		return st.ambiguous
	}
	if !p.o.NotesTypes.HasSource(p.o.Notes) {
		return nil
	}
	e, ok := p.ledger.Discarded(hash)
	if !ok {
		return nil
	}
	amb := &ambiguousCreate{hash: hash, marker: notion.SourceMarker(e.File, e.ModTime, p.o.Loc), at: e.At}
	p.state(path).ambiguous = amb
	return amb
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
	// bleibt etwas liegen, dann das Transkript mit "begonnen" (failJob)
	if !p.failJob(j, ErrUnterbrochen) {
		p.retryLater(pr.Path, now) // bleibt "begonnen"
		return
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
	if errors.Is(err, notion.ErrZuVieleBloecke) {
		// Ohne neuen Versuch nach _fehler (OD-11); max_attempts zählt nicht.
		if p.o.Dry {
			// nichts verschieben; Wartezeit, sonst LLM-Lauf bei jedem Durchlauf
			p.log.Printf("   ✗ %v – Notion-Seite nicht angelegt (Dry-Run, Dateien bleiben liegen)", err)
			p.retryLater(pr.Path, now)
			return
		}
		p.log.Printf("   ✗ %v – Notion-Seite nicht angelegt, Dateien → %s", err, p.o.FailedDir)
		if !p.failJob(j, err) {
			p.retryLater(pr.Path, now)
			return
		}
		delete(p.states, pr.Path)
		if p.o.Notify {
			p.notifier.Notify("Meeting-Notiz nicht angelegt", pr.Name+": "+err.Error())
		}
		return
	}
	if !p.retry.counts(ctx, err) {
		return
	}
	trans := domain.IsTransient(err)
	st := p.state(pr.Path)
	if errors.Is(err, notion.ErrUnklar) {
		// Seite evtl. angelegt: vor dem nächsten Versuch suchen (LLM-Ergebnis setzt handle).
		// Herkunft aus dem "verworfen" direkt nach dem Anlegen, sonst aus der Datei.
		hash := sha256Hex(pr.Content)
		e, ok := p.ledger.Discarded(hash)
		if !ok {
			e = ledger.Entry{File: pr.Name, ModTime: pr.ModTime}
		}
		st.ambiguous = &ambiguousCreate{hash: hash, marker: notion.SourceMarker(e.File, e.ModTime, p.o.Loc), at: p.now()}
	}
	wait, giveUp := p.retry.record(st, trans, now)
	if giveUp {
		p.log.Printf("   ✗ %v – gebe auf, Dateien → %s", err, p.o.FailedDir)
		p.giveUp(j, st, err, now)
		return
	}
	if trans {
		p.log.Printf("   ⏸  %v – neuer Versuch in %s (Versuch %d/%d)", err, wait, st.Attempts, p.retry.maxAttempts)
		return
	}
	p.log.Printf("   ✗ %v (Versuch %d/%d)", err, st.Attempts, p.retry.maxAttempts)
}

// giveUp verschiebt die Dateien nach max_attempts nach _fehler. Scheitert das,
// bleibt der Zustand mit Versuchszahl und Ursache: neuer Versuch in 2 min,
// dann ohne LLM-Lauf direkt nach _fehler (handle, R-M3-18).
// Ist nach dem letzten Fehler unklar, ob die Notiz angelegt ist, steht das in
// .fehler.txt (nur dort, R-M3-17).
func (p *Pipeline) giveUp(j job, st *FileState, cause error, now time.Time) {
	txt := cause
	if (errors.Is(cause, notion.ErrUnklar) || errors.Is(cause, errNochNichtAuffindbar)) && !strings.Contains(cause.Error(), "Seite evtl. angelegt") {
		txt = fmt.Errorf("%w – Seite evtl. angelegt – bitte in Notion prüfen", cause)
	}
	if !p.failJob(j, txt) {
		st.giveUp, st.giveUpHash = cause, sha256Hex(j.primary.Content)
		st.ambiguous = nil
		p.retryLater(j.primary.Path, now)
		return
	}
	delete(p.states, j.primary.Path)
}

// gaps zählt, was nach dem Anlegen der Notiz in Notion fehlt (OD-04: nur
// protokollieren, nicht nachholen).
type gaps struct {
	tasks  int // nicht angelegte To-dos (Fehler oder Abbruch)
	blocks int // Fehler beim Anhängen der restlichen Blöcke
}

// ProcessFile verarbeitet e direkt (-file), ohne Verschieben. Außer im
// Dry-Run schreibt es das Protokoll wie ein Durchlauf (begonnen, fertig;
// R-M3-14), auch wenn der Hash dort schon steht. Liefert die URL der Notiz
// ("dry-run" im Dry-Run).
func (p *Pipeline) ProcessFile(ctx context.Context, e *watch.Entry) (string, error) {
	hash := sha256Hex(e.Content)
	if !p.o.Dry {
		// Zeilen nachlesen, die ein Durchlauf während des Wartens auf die Sperre schrieb
		if err := p.ledger.Refresh(); err != nil {
			p.warnOnce("ledger-refresh", "Protokoll nicht nachlesbar: %v", err)
		}
	}
	url, g, _, err := p.process(ctx, job{primary: e, summaryOnly: e.Kind == domain.KindSummary}, hash, nil)
	if err != nil || p.o.Dry {
		return url, err
	}
	if err := p.ledger.Add(ledger.Entry{Hash: hash, File: e.Name, ModTime: e.ModTime, URL: url, At: p.now(), MissingTasks: g.tasks, BlockErrors: g.blocks}); err != nil {
		p.log.Printf("   ⚠️  Protokoll nicht geschrieben (Notiz existiert: %s): %v", url, err)
	}
	return url, nil
}

// noteResult ist das aufbereitete LLM-Ergebnis einer Notiz. Nach einem
// unklaren Fehler beim Anlegen bleibt es im FileState, damit der nächste
// Versuch dieselbe Notiz ohne neuen LLM-Lauf schreibt (R-M3-17).
type noteResult struct {
	note    *domain.MeetingNote
	title   string
	project *notion.Page // Kopie, kein Zeiger in die Projektliste
	reasons []string
	blocks  []any
}

// ambiguousLag: so lange nach einem unklaren Fehler beim Anlegen findet die
// Suche in Notion eine angelegte Seite evtl. noch nicht (Verzögerung der Suche).
const ambiguousLag = 5 * time.Minute

// errNochNichtAuffindbar: die Suche fand nach einem unklaren Fehler beim
// Anlegen nichts, ambiguousLag ist aber noch nicht vorbei.
var errNochNichtAuffindbar = errors.New("Notiz in Notion noch nicht auffindbar")

// process: Text aufbereiten → lokales LLM → Notion-Seite + Tasks.
// Vor CreatePage "begonnen" ins Protokoll (ADR 0004).
// Ist die Seite angelegt, ist der Fehler nil, auch wenn Teile fehlen (gaps).
// amb (optional): der letzte Versuch scheiterte unklar beim Anlegen. Sein
// LLM-Ergebnis wird verwendet, wenn vorhanden; mit Herkunftsspalte wird erst
// nach der Seite gesucht und eine gefundene verwendet (R-M3-17). res ist das
// verwendete LLM-Ergebnis (nil, wenn es nicht so weit kam).
func (p *Pipeline) process(ctx context.Context, j job, hash string, amb *ambiguousCreate) (url string, g gaps, res *noteResult, err error) {
	pr := j.primary
	if amb != nil {
		res = amb.res
	}
	if res == nil {
		if res, err = p.prepare(ctx, j); err != nil {
			return "", gaps{}, nil, err
		}
		if p.o.Dry {
			p.dryRun(res.title, res.project, res.note, res.reasons, len(res.blocks))
			return "dry-run", gaps{}, res, nil
		}
	}
	note, title, project, reasons := res.note, res.title, res.project, res.reasons

	le := ledger.Entry{Hash: hash, Status: ledger.StatusBegonnen, File: pr.Name, ModTime: pr.ModTime, At: p.now()}
	if err := p.ledger.Add(le); err != nil {
		// Ohne "begonnen" keine Notion-Anfrage. verworfen setzt den Speicher
		// zurück, damit der nächste Versuch normal läuft.
		le.Status = ledger.StatusVerworfen
		_ = p.ledger.Add(le) //nolint:errcheck // gemeldet wird der erste Fehler; bleibt auf Platte "begonnen", greift beim Neustart die Prüfregel (sicher)
		return "", gaps{}, res, fmt.Errorf("Protokoll nicht schreibbar, Notion-Seite nicht angelegt: %w", err)
	}
	marker := notion.SourceMarker(pr.Name, pr.ModTime, p.o.Loc)
	var pageID string
	found := false
	if amb != nil && p.o.NotesTypes.HasSource(p.o.Notes) {
		// erst die Herkunft des unklaren Anlegens (Datei evtl. umbenannt), dann die aktuelle
		markers := []string{amb.marker}
		if marker != amb.marker {
			markers = append(markers, marker)
		}
		for _, m := range markers {
			if pageID, url, found, err = p.findNote(ctx, m, res.blocks); found || err != nil {
				break
			}
		}
		if err == nil && !found && p.now().Sub(amb.at) < ambiguousLag {
			err = domain.Transient(errNochNichtAuffindbar) // Suche evtl. noch nicht aktuell: nicht anlegen
		}
	}
	if !found && err == nil {
		pageID, url, err = p.notion.CreatePage(ctx, p.o.NotesDS, notion.NoteProperties(p.o.NotesTypes, p.o.Notes, title, project, len(reasons) > 0, marker), res.blocks)
	}
	if errors.Is(err, notion.ErrUnvollstaendig) {
		// Seite existiert: kein Fehler, sonst entsteht beim nächsten Versuch eine Dublette.
		p.log.Printf("   ⚠️  %v", err)
		g.blocks, err = 1, nil
	}
	if err != nil {
		// Antwort mit Fehler: neuer Versuch wie bisher (OD-05), also verworfen.
		// Bei Abbruch per Kontext bleibt "begonnen": die Anfrage kann Notion
		// erreicht haben, der Neustart schickt die Datei zur Prüfung.
		if ctx.Err() == nil {
			le.Status, le.At = ledger.StatusVerworfen, p.now()
			if lerr := p.ledger.Add(le); lerr != nil {
				p.log.Printf("   ⚠️  Protokoll nicht geschrieben: %v", lerr)
			}
		}
		return "", gaps{}, res, err
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
	return url, g, res, nil
}

// prepare: Text aufbereiten → lokales LLM → Titel, Projekt, Prüfgründe, Blöcke.
func (p *Pipeline) prepare(ctx context.Context, j job) (*noteResult, error) {
	pr := j.primary
	var text string
	var dur time.Duration
	if j.summaryOnly {
		text = strings.TrimSpace(transcript.NormalizeNewlines(string(pr.Content)))
	} else {
		text, dur = transcript.Text(pr.Path, pr.Content)
	}
	if text == "" {
		return nil, errors.New("Datei ist leer")
	}
	timing := transcript.MeetingTiming(pr.Name, pr.ModTime, dur, p.o.Loc)

	in := llm.NoteInput{
		Text:        text,
		SummaryOnly: j.summaryOnly,
		Timing:      timing,
		MyName:      p.o.MyName,
		MyRole:      p.o.MyRole,
		ExampleName: p.o.ExampleName,
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
		return nil, err
	}
	llm.Normalize(note)
	p.log.Printf("   LLM fertig nach %s: %q", p.now().Sub(start).Round(time.Second), note.Titel)

	reasons := append([]string{}, note.PruefGruende...)
	if j.summaryOnly {
		reasons = append(reasons, "Kein Transkript mit Zeitmarken gefunden – Notiz basiert auf einer Datei ohne Sprecher/Zeitstempel")
	}
	if hatToDoFuerMich(note) && !slices.Contains(reasons, reasonToDoFuerMich) {
		reasons = append(reasons, reasonToDoFuerMich)
	}
	var project *notion.Page
	for i := range projects {
		if strings.EqualFold(projects[i].Name, note.Projekt) {
			pj := projects[i]
			project = &pj
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
	// vor "begonnen" und CreatePage: über der Grenze kein Protokolleintrag (OD-11)
	if err := notion.CheckBlockCount(note, meta); err != nil {
		return nil, err
	}
	return &noteResult{note: note, title: title, project: project, reasons: reasons, blocks: blocks}, nil
}

// findNote sucht die Notiz mit der Herkunft marker (exakt). Gefunden: ID und
// URL der ältesten, die Blöcke nach den ersten 100 werden angehängt (ein
// Fehler dabei umhüllt notion.ErrUnvollstaendig). Nie wird etwas gelöscht.
func (p *Pipeline) findNote(ctx context.Context, marker string, blocks []any) (id, url string, found bool, err error) {
	pages, err := p.notion.FindByText(ctx, p.o.NotesDS, p.o.Notes.Source, marker)
	if err != nil || len(pages) == 0 {
		return "", "", false, err
	}
	if len(pages) > 1 {
		p.log.Printf("   ⚠️  %d Notizen mit dieser Herkunft in Notion (%s) – nehme die älteste, bitte prüfen", len(pages), p.o.Notes.Source)
	}
	p.log.Printf("   ↺ Notiz existiert schon in Notion (%s) – wird verwendet", p.o.Notes.Source)
	return pages[0].ID, pages[0].URL, true, p.notion.AppendRest(ctx, pages[0].ID, blocks)
}

// createTasks legt To-dos in der Tasks-Datenbank an und verknüpft sie mit der Notiz.
// Liefert die Zahl der angelegten To-dos für den Nutzer (fuer_mich=true) und der
// fehlenden To-dos (Fehler oder Abbruch).
func (p *Pipeline) createTasks(ctx context.Context, n *domain.MeetingNote, noteID string, project *notion.Page, title string) (mine, missing int) {
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
		if act.FuerMich {
			mine++
		}
	}
	return mine, missing
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

// reasonToDoFuerMich ist der Prüfgrund für Notizen mit To-dos für den Nutzer (D5).
const reasonToDoFuerMich = "Enthält To-dos für dich – bitte Zuordnung und Fälligkeit prüfen"

// hatToDoFuerMich meldet, ob mindestens eine nächste Aktion fuer_mich=true hat.
func hatToDoFuerMich(note *domain.MeetingNote) bool {
	for _, a := range note.NextActions {
		if a.FuerMich {
			return true
		}
	}
	return false
}
