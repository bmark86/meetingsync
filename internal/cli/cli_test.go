package cli

// Unit-Tests der Einstiegspunkte mit Fakes für Notion und LM Studio. Der
// Ablauf über die echte Verdrahtung (main, HTTP-Fakes, Golden) liegt in den
// Root-Tests.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"meetingsync/internal/config"
	"meetingsync/internal/domain"
	"meetingsync/internal/ledger"
	"meetingsync/internal/llm"
	"meetingsync/internal/notion"
)

// fakeNotion liefert je Data-Source-ID ein Schema oder einen Fehler.
// CreatePage legt Seiten nur mit pages=true an.
type fakeNotion struct {
	schemas  map[string]notion.Schema
	errs     map[string]error
	resolved []string
	pages    bool
}

func (f *fakeNotion) ResolveDataSource(_ context.Context, id string) (string, notion.Schema, error) {
	f.resolved = append(f.resolved, id)
	if err := f.errs[id]; err != nil {
		return "", nil, err
	}
	return id + "-ds", f.schemas[id], nil
}

func (f *fakeNotion) CreatePage(context.Context, string, map[string]any, []any) (string, string, error) {
	if !f.pages {
		return "", "", errors.New("CreatePage im Test nicht erwartet")
	}
	return "page-1", "https://notion.so/page-1", nil
}

func (f *fakeNotion) ActiveProjects(context.Context, string, notion.ProjectsProps, notion.Schema) ([]notion.Page, error) {
	return []notion.Page{{ID: "p1", Name: "AI-First"}}, nil
}

func (f *fakeNotion) FindByText(context.Context, string, string, string) ([]notion.PageRef, error) {
	return nil, errors.New("FindByText im Test nicht erwartet")
}

func (f *fakeNotion) AppendRest(context.Context, string, []any) error {
	return errors.New("AppendRest im Test nicht erwartet")
}

// fakeLLM meldet models oder err; Summarize liefert note.
type fakeLLM struct {
	models []string
	err    error
	note   *domain.MeetingNote
}

func (f fakeLLM) Summarize(context.Context, llm.NoteInput) (*domain.MeetingNote, error) {
	if f.note == nil {
		return nil, errors.New("Summarize im Test nicht erwartet")
	}
	n := *f.note
	return &n, nil
}

func (f fakeLLM) Models(context.Context) ([]string, error) { return f.models, f.err }

func goodNotion() *fakeNotion {
	return &fakeNotion{schemas: map[string]notion.Schema{
		"notes":    {"Name": notion.KindTitle, "Status": notion.KindStatus, "Prüfen": notion.KindCheckbox},
		"tasks":    {"Name": notion.KindTitle},
		"projects": {"Name": notion.KindTitle},
	}}
}

// testEnv baut ein Env mit Ordnern unter t.TempDir() und Log in buf.
func testEnv(t *testing.T, n *fakeNotion) (Env, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	cfg := config.Default()
	cfg.WatchDir = filepath.Join(root, "watch")
	cfg.ArchiveDir = filepath.Join(root, "archiv")
	cfg.FailedDir = filepath.Join(root, "fehler")
	cfg.Notion.NotesDataSourceID = "notes"
	cfg.Notion.TasksDataSourceID = "tasks"
	cfg.Notion.ProjectsDataSourceID = "projects"
	cfg.LLM.Model = "gemma"
	if _, err := cfg.Finalize(); err != nil {
		t.Fatal(err)
	}
	buf := &bytes.Buffer{}
	e := Env{Cfg: cfg, Loc: time.UTC, Dry: true, Log: log.New(buf, "", 0), LLM: fakeLLM{models: []string{"gemma"}}}
	if n != nil {
		e.Notion = n
	}
	return e, buf
}

// Fängt: Init fragt Notion ohne Token / lädt das Protokoll nicht.
func TestInitWithoutNotionLoadsLedger(t *testing.T) {
	e, _ := testEnv(t, nil)
	if err := os.MkdirAll(e.Cfg.ArchiveDir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"hash":"abc","status":"fertig","file":"x.srt"}` + "\n"
	if err := os.WriteFile(filepath.Join(e.Cfg.ArchiveDir, ".meetingsync-ledger.jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Init(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if s.Ledger == nil || !s.Ledger.Has("abc") {
		t.Fatal("Protokoll nicht geladen")
	}
	if s.NotesDS != "" || s.TasksDS != "" || s.ProjectsDS != "" {
		t.Fatalf("Datenbanken ohne Notion: %+v", s)
	}
}

// Fängt: falsche Data Source je Datenbank / Hinweis trotz Checkbox.
func TestInitResolvesDataSources(t *testing.T) {
	n := goodNotion()
	e, buf := testEnv(t, n)
	s, err := Init(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if s.NotesDS != "notes-ds" || s.TasksDS != "tasks-ds" || s.ProjectsDS != "projects-ds" {
		t.Fatalf("Datenbanken = %q %q %q", s.NotesDS, s.TasksDS, s.ProjectsDS)
	}
	if !s.NotesTypes.Is("Prüfen", notion.KindCheckbox) || !s.TasksTypes.Is("Name", notion.KindTitle) {
		t.Fatalf("Schemas fehlen: %+v", s)
	}
	if buf.Len() != 0 {
		t.Fatalf("unerwartetes Log:\n%s", buf)
	}
}

// Fängt: Tasks-Datenbank wird bei create_tasks_for=none trotzdem gelesen.
func TestInitSkipsTasksWhenNone(t *testing.T) {
	n := goodNotion()
	e, _ := testEnv(t, n)
	e.Cfg.Notion.CreateTasksFor = "none"
	s, err := Init(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if s.TasksDS != "" || strings.Join(n.resolved, ",") != "notes,projects" {
		t.Fatalf("TasksDS=%q, gelesen %v", s.TasksDS, n.resolved)
	}
}

// Fängt: Fehlertexte und Abbruch von Init je Datenbank; Projects ist nur ein Hinweis.
func TestInitErrors(t *testing.T) {
	boom := errors.New("404")
	cases := []struct {
		name    string
		mod     func(n *fakeNotion)
		wantErr string // "" = kein Fehler
		wantLog string
	}{
		{"Notes nicht lesbar", func(n *fakeNotion) { n.errs = map[string]error{"notes": boom} }, "Notes-Datenbank nicht lesbar: 404", ""},
		{"Notes ohne Titel", func(n *fakeNotion) { delete(n.schemas["notes"], "Name") }, `Notes-Datenbank: Titel-Property "Name" nicht gefunden`, ""},
		{"Notes ohne Checkbox", func(n *fakeNotion) { delete(n.schemas["notes"], "Prüfen") }, "", `Hinweis: Notes hat keine Checkbox "Prüfen"`},
		{"Tasks nicht lesbar", func(n *fakeNotion) { n.errs = map[string]error{"tasks": boom} }, "Tasks-Datenbank nicht lesbar: 404", ""},
		{"Tasks ohne Titel", func(n *fakeNotion) { n.schemas["tasks"] = notion.Schema{} }, `Tasks-Datenbank: Titel-Property "Name" nicht gefunden`, ""},
		{"Projects nicht lesbar", func(n *fakeNotion) { n.errs = map[string]error{"projects": boom} }, "", "Hinweis: Projects-Datenbank nicht lesbar (404) – Projektvorschläge sind aus."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := goodNotion()
			c.mod(n)
			e, buf := testEnv(t, n)
			s, err := Init(context.Background(), e)
			if c.wantErr != "" {
				if err == nil || err.Error() != c.wantErr {
					t.Fatalf("Fehler = %v, want %q", err, c.wantErr)
				}
				if strings.Contains(c.wantErr, "nicht lesbar") && !errors.Is(err, boom) {
					t.Fatalf("Ursache nicht gewrappt: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(buf.String(), c.wantLog) {
				t.Fatalf("Log ohne %q:\n%s", c.wantLog, buf)
			}
			if c.name == "Projects nicht lesbar" && s.ProjectsDS != "" {
				t.Fatalf("ProjectsDS = %q, want leer", s.ProjectsDS)
			}
		})
	}
}

// Fängt: Hinweis fehlt bei konfigurierter, aber fehlender oder falsch typisierter
// Herkunftsspalte, oder kommt ohne Konfiguration / bei passender Spalte (R-M3-17).
func TestInitSourceHint(t *testing.T) {
	const hint = `Hinweis: Notes hat keine Textspalte "Quelle" (notes_properties.source) – kein Schutz vor einer zweiten Notiz nach unklarem Fehler beim Anlegen.`
	cases := []struct {
		name   string
		source string
		kind   notion.PropertyKind // "" = Spalte fehlt
		want   bool
	}{
		{"nicht konfiguriert", "", "", false},
		{"Spalte fehlt", "Quelle", "", true},
		{"falscher Typ", "Quelle", notion.KindSelect, true},
		{"Textspalte", "Quelle", notion.KindRichText, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := goodNotion()
			if c.kind != "" {
				n.schemas["notes"]["Quelle"] = c.kind
			}
			e, buf := testEnv(t, n)
			e.Cfg.Notion.Notes.Source = c.source
			if _, err := Init(context.Background(), e); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(buf.String(), hint) != c.want {
				t.Fatalf("Log (Hinweis erwartet: %v):\n%s", c.want, buf)
			}
		})
	}
}

// captureStdout fängt die Ausgabe von fn auf os.Stdout ab.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	defer func() { os.Stdout = old }()
	fn()
	_ = w.Close()
	return <-done
}

// Fängt: Check ohne Token meldet keinen Fehler (Exit-Code 1 fehlt).
func TestCheckWithoutNotion(t *testing.T) {
	e, _ := testEnv(t, nil)
	var err error
	out := captureStdout(t, func() { err = Check(context.Background(), e) })
	if err == nil || err.Error() != "kein Notion-Token gefunden" {
		t.Fatalf("Fehler = %v", err)
	}
	if !strings.Contains(out, "Ordner:   "+e.Cfg.WatchDir+"\n") || !strings.Contains(out, "✗ Ordner nicht gefunden") {
		t.Fatalf("Ausgabe:\n%s", out)
	}
}

// Fängt: Check-Bericht unvollständig; LM-Studio-Fehler wird zum Exit-Code.
func TestCheckReport(t *testing.T) {
	cases := []struct {
		name    string
		llm     fakeLLM
		want    string
		wantErr bool
	}{
		{"Modell da", fakeLLM{models: []string{"x", "gemma"}}, "✓ LM Studio: Modell \"gemma\" verfügbar\n", false},
		{"Modell fehlt", fakeLLM{models: []string{"x"}}, "✗ LM Studio: Modell \"gemma\" nicht gefunden. Verfügbar:\n    x\n", true},
		{"nicht erreichbar", fakeLLM{err: errors.New("refused")}, "✗ LM Studio nicht erreichbar unter ", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, _ := testEnv(t, goodNotion())
			e.LLM = c.llm
			if err := os.MkdirAll(e.Cfg.WatchDir, 0o755); err != nil {
				t.Fatal(err)
			}
			var err error
			out := captureStdout(t, func() { err = Check(context.Background(), e) })
			if (err != nil) != c.wantErr {
				t.Fatalf("Fehler = %v, wantErr %v", err, c.wantErr)
			}
			for _, w := range []string{"✓ Notion: Notes erreichbar (notes-ds)\n", "✓ Notion: Tasks erreichbar (tasks-ds)\n", "✓ Notion: 1 aktive Projekte\n", c.want} {
				if !strings.Contains(out, w) {
					t.Fatalf("Ausgabe ohne %q:\n%s", w, out)
				}
			}
		})
	}
}

// Fängt: Pfad oder Query der LM-Studio-URL in der -check-Ausgabe (S-03).
func TestCheckUnreachableShowsOnlySchemeHostPort(t *testing.T) {
	e, _ := testEnv(t, goodNotion())
	e.Cfg.LLM.BaseURL = "http://127.0.0.1:9/v1?key=geheim"
	e.LLM = fakeLLM{err: errors.New("refused")}
	out := captureStdout(t, func() { _ = Check(context.Background(), e) })
	if !strings.Contains(out, "✗ LM Studio nicht erreichbar unter http://127.0.0.1:9: refused\n") || strings.Contains(out, "geheim") {
		t.Fatalf("Ausgabe:\n%s", out)
	}
}

// Fängt: Init-Fehler in Check ohne ✗-Zeile oder ohne Fehler.
func TestCheckInitError(t *testing.T) {
	n := goodNotion()
	n.errs = map[string]error{"notes": errors.New("404")}
	e, _ := testEnv(t, n)
	var err error
	out := captureStdout(t, func() { err = Check(context.Background(), e) })
	if err == nil || !strings.Contains(out, "✗ Notion: Notes-Datenbank nicht lesbar: 404\n") {
		t.Fatalf("err=%v Ausgabe:\n%s", err, out)
	}
}

// Fängt: -file mit fehlender oder nicht unterstützter Datei läuft weiter.
func TestProcessSingleFileErrors(t *testing.T) {
	e, buf := testEnv(t, nil)
	s, err := Init(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	p := NewPipeline(e, s)
	if err := ProcessSingleFile(context.Background(), e, p, filepath.Join(t.TempDir(), "fehlt.srt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fehlende Datei: %v", err)
	}
	bad := filepath.Join(t.TempDir(), "Bild.png")
	if err := os.WriteFile(bad, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ProcessSingleFile(context.Background(), e, p, bad); err == nil || err.Error() != "Dateityp .png wird nicht unterstützt" {
		t.Fatalf("Fehler = %v", err)
	}
	if !strings.Contains(buf.String(), "Bild.png erkannt als: ") {
		t.Fatalf("Log:\n%s", buf)
	}
}

const testSRT = "1\n00:00:01,000 --> 00:00:04,000\nSpeaker 1: Hallo zusammen.\n\n2\n00:00:05,000 --> 00:00:09,000\nSpeaker 2: Build dauert zwölf Minuten.\n"

// Fängt: -file gibt im Dry-Run eine URL aus / druckt keine Dry-Run-Ausgabe /
// gibt außerhalb des Dry-Runs die URL der Notiz nicht aus / schreibt das
// Protokoll im Dry-Run bzw. außerhalb nicht (R-M3-14).
func TestProcessSingleFileOutput(t *testing.T) {
	note := &domain.MeetingNote{Titel: "Sirius Sync", Teilnehmer: []string{"Mark"}}
	for _, dry := range []bool{true, false} {
		n := goodNotion()
		n.pages = true
		e, _ := testEnv(t, n)
		e.Dry = dry
		e.Cfg.Notion.CreateTasksFor = "none"
		e.LLM = fakeLLM{note: note}
		s, err := Init(context.Background(), e)
		if err != nil {
			t.Fatal(err)
		}
		p := NewPipeline(e, s)
		f := filepath.Join(t.TempDir(), "Sirius Sync.srt")
		if err := os.WriteFile(f, []byte(testSRT), 0o600); err != nil {
			t.Fatal(err)
		}
		out := captureStdout(t, func() { err = ProcessSingleFile(context.Background(), e, p, f) })
		if err != nil {
			t.Fatalf("dry=%v: %v", dry, err)
		}
		hasDry := strings.Contains(out, "────────── DRY-RUN ──────────\nTitel:   ") && strings.Contains(out, `"titel": "Sirius Sync"`)
		hasURL := strings.Contains(out, "https://notion.so/page-1\n")
		if hasDry != dry || hasURL == dry {
			t.Fatalf("dry=%v: Ausgabe:\n%s", dry, out)
		}
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("dry=%v: Quelldatei verschwunden: %v", dry, err)
		}
		if _, err := os.Stat(filepath.Join(e.Cfg.ArchiveDir, ".meetingsync-ledger.jsonl")); errors.Is(err, os.ErrNotExist) != dry {
			t.Fatalf("dry=%v: Ledger-Datei: %v", dry, err)
		}
	}
}

// chanWriter gibt jede Logzeile an einen Kanal (race-frei lesbar).
type chanWriter chan string

func (c chanWriter) Write(p []byte) (int, error) { c <- string(p); return len(p), nil }

// Fängt: -file wartet nicht auf einen laufenden Durchlauf (Sperre) / meldet
// das Warten nicht / läuft nach der Freigabe nicht weiter.
func TestProcessSingleFileWaitsForLock(t *testing.T) {
	old := lockPoll
	lockPoll = 5 * time.Millisecond
	defer func() { lockPoll = old }()
	n := goodNotion()
	n.pages = true
	e, _ := testEnv(t, n)
	e.Dry = false
	e.Cfg.Notion.CreateTasksFor = "none"
	e.LLM = fakeLLM{note: &domain.MeetingNote{Titel: "Sirius Sync"}}
	lines := make(chanWriter, 100)
	e.Log = log.New(lines, "", 0)
	if err := os.MkdirAll(e.Cfg.WatchDir, 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := Init(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	p := NewPipeline(e, s)
	f := filepath.Join(e.Cfg.WatchDir, "Sirius Sync.srt")
	if err := os.WriteFile(f, []byte(testSRT), 0o600); err != nil {
		t.Fatal(err)
	}
	held, err := ledger.TryLock(e.Cfg.WatchDir) // laufender Durchlauf des Dienstes
	if err != nil {
		t.Fatal(err)
	}
	ledgerFile := filepath.Join(e.Cfg.ArchiveDir, ".meetingsync-ledger.jsonl")
	captureStdout(t, func() {
		done := make(chan error, 1)
		go func() { done <- ProcessSingleFile(context.Background(), e, p, f) }()
		for waiting := false; !waiting; {
			select {
			case l := <-lines:
				waiting = strings.Contains(l, "Dienst verarbeitet gerade den Exportordner – warte, bis der Durchlauf fertig ist …")
			case err := <-done:
				t.Fatalf("-file lief trotz Sperre durch: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("keine Wartemeldung")
			}
		}
		time.Sleep(30 * time.Millisecond)
		if _, err := os.Stat(ledgerFile); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Protokoll schon während der Sperre geschrieben: %v", err)
		}
		if err := held.Unlock(); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
	if _, err := os.Stat(ledgerFile); err != nil {
		t.Fatalf("Protokoll fehlt: %v", err)
	}
}

// Fängt: -file liest die Datei vor der Sperre und legt nach dem Warten eine
// zweite Seite aus der alten Kopie an, obwohl der Dienst sie inzwischen
// verarbeitet und archiviert hat.
func TestProcessSingleFileReadsAfterLock(t *testing.T) {
	old := lockPoll
	lockPoll = 5 * time.Millisecond
	defer func() { lockPoll = old }()
	e, _ := testEnv(t, goodNotion()) // pages=false: jedes CreatePage wäre ein Fehler
	e.Dry = false
	e.Cfg.Notion.CreateTasksFor = "none"
	e.LLM = fakeLLM{note: &domain.MeetingNote{Titel: "Sirius Sync"}}
	lines := make(chanWriter, 100)
	e.Log = log.New(lines, "", 0)
	if err := os.MkdirAll(e.Cfg.WatchDir, 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := Init(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	p := NewPipeline(e, s)
	f := filepath.Join(e.Cfg.WatchDir, "Sirius Sync.srt")
	if err := os.WriteFile(f, []byte(testSRT), 0o600); err != nil {
		t.Fatal(err)
	}
	held, err := ledger.TryLock(e.Cfg.WatchDir) // laufender Durchlauf des Dienstes
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- ProcessSingleFile(context.Background(), e, p, f) }()
	for waiting := false; !waiting; {
		select {
		case l := <-lines:
			waiting = strings.Contains(l, "warte, bis der Durchlauf fertig ist")
		case err := <-done:
			t.Fatalf("-file lief trotz Sperre durch: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("keine Wartemeldung")
		}
	}
	// der Dienst archiviert die Datei, dann gibt er die Sperre frei
	if err := os.Rename(f, filepath.Join(t.TempDir(), "Sirius Sync.srt")); err != nil {
		t.Fatal(err)
	}
	if err := held.Unlock(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("erwartet: Datei fehlt, bekommen: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.Cfg.ArchiveDir, ".meetingsync-ledger.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Protokoll geschrieben: %v", err)
	}
}

// Fängt: Run ignoriert ctx.Done() / Start- oder Endmeldung fehlt.
func TestRunCanceled(t *testing.T) {
	e, buf := testEnv(t, nil)
	s, err := Init(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	Run(ctx, e, NewPipeline(e, s))
	if !strings.Contains(buf.String(), "Überwache "+e.Cfg.WatchDir+" (alle 10s). Beenden mit Ctrl+C.") || !strings.Contains(buf.String(), "Beendet.") {
		t.Fatalf("Log:\n%s", buf)
	}
}

// Fängt: ✗ in der Ausgabe ohne Fehler (Exit-Code 0) oder Fehler bei lauter ✓.
func TestCheckExitDecision(t *testing.T) {
	e, _ := testEnv(t, goodNotion())
	e.LLM = fakeLLM{models: []string{"gemma"}}
	if err := os.MkdirAll(e.Cfg.WatchDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var err error
	captureStdout(t, func() { err = Check(context.Background(), e) })
	if err != nil {
		t.Fatalf("alles ✓: Fehler = %v, want nil", err)
	}
	e.Cfg.WatchDir = filepath.Join(t.TempDir(), "fehlt")
	out := captureStdout(t, func() { err = Check(context.Background(), e) })
	if !errors.Is(err, errCheckFailed) || !strings.Contains(out, "✗ Ordner nicht gefunden") {
		t.Fatalf("Ordner ✗: err=%v Ausgabe:\n%s", err, out)
	}
}
