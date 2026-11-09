package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"meetingsync/internal/domain"
	"meetingsync/internal/ledger"
	"meetingsync/internal/llm"
	"meetingsync/internal/notion"
	"meetingsync/internal/watch"
)

// testM ist die mtime der Eingaben (UTC, Archivmonat 2026-09).
var testM = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

const testSRT = "1\n00:00:01,000 --> 00:00:05,000\nSpeaker 1: Hallo zusammen.\n\n2\n00:00:06,000 --> 00:00:09,000\nSpeaker 2: Danke.\n"

// fakeLLM liefert note() oder err und zeichnet die Eingaben auf.
type fakeLLM struct {
	note func() *domain.MeetingNote
	err  error
	got  []llm.NoteInput
}

func (f *fakeLLM) Summarize(_ context.Context, in llm.NoteInput) (*domain.MeetingNote, error) {
	f.got = append(f.got, in)
	if f.err != nil {
		return nil, f.err
	}
	return f.note(), nil
}

func testNote() *domain.MeetingNote {
	return &domain.MeetingNote{
		Titel: "Sync", Typ: "Meeting", Projekt: "Sirius",
		NextActions: []domain.Action{
			{Aufgabe: "Mein To-do", FuerMich: true},
			{Aufgabe: "Fremdes To-do"},
		},
	}
}

type fakePage struct {
	ds     string
	props  map[string]any
	blocks []any
}

// fakeNotion zeichnet angelegte Seiten auf. onCreate (optional) entscheidet je
// Anfrage über einen Fehler; wickelt er notion.ErrUnvollstaendig ein, gilt die
// Seite als angelegt.
type fakeNotion struct {
	pages     []fakePage
	onCreate  func(ds string) error
	projects  []notion.Page
	projErr   error
	projCalls int
	// keepOnErr: die Seite entsteht auch bei einem Fehler von onCreate
	// (unklarer Ausgang: Notion legt an, die Antwort geht verloren)
	keepOnErr bool
	finds     int
	findErr   error
	appends   []string // "<pageID>:<Anzahl Blöcke>" je AppendRest
	appended  [][]any  // Blöcke je AppendRest
	appendErr error
}

func (f *fakeNotion) CreatePage(_ context.Context, ds string, props map[string]any, blocks []any) (string, string, error) {
	var err error
	if f.onCreate != nil {
		err = f.onCreate(ds)
	}
	if err != nil && !errors.Is(err, notion.ErrUnvollstaendig) {
		if f.keepOnErr {
			f.pages = append(f.pages, fakePage{ds: ds, props: props, blocks: blocks})
		}
		return "", "", err
	}
	f.pages = append(f.pages, fakePage{ds: ds, props: props, blocks: blocks})
	n := len(f.pages)
	return fmt.Sprintf("page-%d", n), fmt.Sprintf("https://notion.so/page-%d", n), err
}

func (f *fakeNotion) ActiveProjects(context.Context, string, notion.ProjectsProps, notion.Schema) ([]notion.Page, error) {
	f.projCalls++
	return f.projects, f.projErr
}

// FindByText liefert alle angelegten Seiten aus ds, deren Textspalte prop genau
// text ist (älteste zuerst), oder findErr.
func (f *fakeNotion) FindByText(_ context.Context, ds, prop, text string) ([]notion.PageRef, error) {
	f.finds++
	if f.findErr != nil {
		return nil, f.findErr
	}
	var res []notion.PageRef
	for i, pg := range f.pages {
		if pg.ds == ds && richText(pg.props[prop]) == text {
			n := i + 1
			res = append(res, notion.PageRef{ID: fmt.Sprintf("page-%d", n), URL: fmt.Sprintf("https://notion.so/page-%d", n)})
		}
	}
	return res, nil
}

func (f *fakeNotion) AppendRest(_ context.Context, pageID string, children []any) error {
	f.appends = append(f.appends, fmt.Sprintf("%s:%d", pageID, len(children)))
	f.appended = append(f.appended, children)
	return f.appendErr
}

// richText liest den Text eines rich_text-Werts wie aus notion.NoteProperties.
func richText(v any) string {
	b, _ := json.Marshal(v) //nolint:errcheck // Werte aus NoteProperties
	var p struct {
		RichText []struct {
			Text struct {
				Content string `json:"content"`
			} `json:"text"`
		} `json:"rich_text"`
	}
	_ = json.Unmarshal(b, &p) //nolint:errcheck // anderer Typ: leerer Text
	var sb strings.Builder
	for _, r := range p.RichText {
		sb.WriteString(r.Text.Content)
	}
	return sb.String()
}

func (f *fakeNotion) count(ds string) int {
	n := 0
	for _, p := range f.pages {
		if p.ds == ds {
			n++
		}
	}
	return n
}

type fakeNotifier struct {
	mu    sync.Mutex
	calls [][2]string
}

func (f *fakeNotifier) Notify(title, msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, [2]string{title, msg})
}

// testEnv ist eine Pipeline mit Fakes auf echten Temp-Ordnern.
type testEnv struct {
	p        *Pipeline
	o        Options
	llm      *fakeLLM
	notion   *fakeNotion
	notifier *fakeNotifier
	ledger   *ledger.Ledger
	log      *bytes.Buffer
	clock    time.Time
}

// newEnv baut die Pipeline; mod passt Options und Deps vor New an.
func newEnv(t *testing.T, mod func(*Options, *Deps)) *testEnv {
	t.Helper()
	root := t.TempDir()
	o := Options{
		WatchDir: filepath.Join(root, "watch"), ArchiveDir: filepath.Join(root, "archiv"), FailedDir: filepath.Join(root, "fehler"),
		Stable: 20 * time.Second, PairWindow: 180 * time.Second, OrphanWait: 10 * time.Minute, OrphanAction: "note",
		MaxAttempts: 10, Loc: time.UTC, MyName: "Mark", Model: "m", TitleFormat: "{date} – {title}",
		CreateTasksFor: "me", NotesDS: "notes-ds", TasksDS: "tasks-ds", ProjectsDS: "projects-ds",
	}
	if err := os.MkdirAll(o.WatchDir, 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := ledger.Load(filepath.Join(o.ArchiveDir, ".meetingsync-ledger.jsonl"), true)
	if err != nil {
		t.Fatal(err)
	}
	e := &testEnv{llm: &fakeLLM{note: testNote}, notion: &fakeNotion{}, notifier: &fakeNotifier{}, ledger: l, log: &bytes.Buffer{}, clock: testM.Add(time.Hour)}
	d := Deps{
		Now: func() time.Time { return e.clock }, FS: watch.OS{}, LLM: e.llm, Notion: e.notion,
		Notifier: e.notifier, Ledger: l, Log: log.New(e.log, "", 0),
	}
	if mod != nil {
		mod(&o, &d)
	}
	e.o = o
	e.p = New(o, d)
	return e
}

// write legt name im Exportordner mit mtime an.
func (e *testEnv) write(t *testing.T, name, content string, mtime time.Time) string {
	t.Helper()
	p := filepath.Join(e.o.WatchDir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return p
}

func (e *testEnv) archived(name string) string { return filepath.Join(e.o.ArchiveDir, "2026-09", name) }

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // G304: Pfad unter t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
