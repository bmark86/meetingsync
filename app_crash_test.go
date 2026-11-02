package main

// Crash-Tests (R-M1-02, ADR 0004): Abbruch nach jedem Schritt von handle.
// Ein Absturz ist ein panic in der Goroutine der App, ausgelöst im
// Notion-Transport (vor/nach POST der Notiz) oder im Logger (an einer
// bekannten Logzeile). Der Stand auf Platte ist dann echt, nicht nachgebaut.
// Neustart = neue App auf denselben Ordnern und demselben Notion-Fake.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"meetingsync/internal/cli"
	"meetingsync/internal/config"
	"meetingsync/internal/ledger"
	"meetingsync/internal/pipeline"
)

type crash struct{}

// hookRT: before != nil und liefert Antwort/Fehler → der Server wird nicht gefragt.
type hookRT struct {
	before func(*http.Request) (*http.Response, error)
	after  func(*http.Request)
}

func (h *hookRT) RoundTrip(r *http.Request) (*http.Response, error) {
	if h.before != nil {
		if resp, err := h.before(r); resp != nil || err != nil {
			return resp, err
		}
	}
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err == nil && h.after != nil {
		h.after(r)
	}
	return resp, err
}

// isNotePost: POST /v1/pages in die Notes-Datenbank (nicht Tasks).
func isNotePost(r *http.Request) bool {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/pages" || r.GetBody == nil {
		return false
	}
	body, err := r.GetBody()
	if err != nil {
		return false
	}
	b, _ := io.ReadAll(body)
	return strings.Contains(string(b), `"data_source_id":"notes-ds"`)
}

// hookLog schreibt ins Log und ruft on für jede Zeile (in der App-Goroutine).
type hookLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
	on  func(line string)
}

func (h *hookLog) Write(p []byte) (int, error) {
	h.mu.Lock()
	h.buf.Write(p)
	h.mu.Unlock()
	if h.on != nil {
		h.on(string(p))
	}
	return len(p), nil
}

func (h *hookLog) String() string { h.mu.Lock(); defer h.mu.Unlock(); return h.buf.String() }

type crashEnv struct {
	f     *fakeServer
	app   *testApp
	dir   string
	log   *hookLog
	rt    *hookRT
	month string
	tr    string // Transkript
	sum   string // Zusammenfassung (Partner)
}

// newCrashEnv: opts passen Env vor Init an (nach dem Hook-Logger).
func newCrashEnv(t *testing.T, opts ...func(e *crashEnv, env *cli.Env)) *crashEnv {
	t.Helper()
	f := &fakeServer{t: t, appends: map[string]int{}}
	e := &crashEnv{f: f, log: &hookLog{}, rt: &hookRT{}}
	app, dir := setupWith(t, f, e.rt, func(env *cli.Env) {
		env.Log = log.New(e.log, "", 0)
		for _, o := range opts {
			o(e, env)
		}
	})
	e.app, e.dir = app, dir
	e.tr = filepath.Join(dir, "Meeting 2026-10-28 14-00.txt")
	e.sum = filepath.Join(dir, "Meeting 2026-10-28 14-00 (1).txt")
	writeOld(t, e.tr, srtLikeText(), 5*time.Minute)
	writeOld(t, e.sum, "Zusammenfassung\n\nEs ging um die Pipeline. Cache kommt.\n", 5*time.Minute)
	st, err := os.Stat(e.tr)
	if err != nil {
		t.Fatal(err)
	}
	e.month = st.ModTime().In(app.loc).Format("2006-01")
	return e
}

func (e *crashEnv) hash() string { return sha256Hex([]byte(srtLikeText())) }

// restart baut eine neue App auf denselben Ordnern (Ledger von Platte).
func (e *crashEnv) restart(t *testing.T) {
	t.Helper()
	e.log = &hookLog{}
	e.rt = &hookRT{}
	env, err := newEnv(e.app.cfg, e.app.loc, log.New(e.log, "", 0), false, e.rt)
	if err != nil {
		t.Fatal(err)
	}
	env.Notifier = &fakeNotifier{}
	app, err := startTestApp(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	e.app = app
}

// pass läuft einen Durchlauf; crashed=true, wenn er per crash{} abbrach.
func (e *crashEnv) pass(now time.Time) (crashed bool) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(crash); !ok {
				panic(r)
			}
			crashed = true
		}
	}()
	e.app.Pass(context.Background(), now)
	return false
}

// pages zählt angelegte Seiten je Datenbank.
func (e *crashEnv) pages() (notes, tasks int) {
	e.f.mu.Lock()
	defer e.f.mu.Unlock()
	for _, p := range e.f.pages {
		switch p["parent"].(map[string]any)["data_source_id"] {
		case "notes-ds":
			notes++
		case "tasks-ds":
			tasks++
		}
	}
	return notes, tasks
}

// ledgerPath ist der Pfad der Ledger-Datei im Archivordner von cfg.
func ledgerPath(cfg *config.Config) string {
	return filepath.Join(cfg.ArchiveDir, ".meetingsync-ledger.jsonl")
}

// diskStatus liest den letzten Status zu hash roh aus der Ledger-Datei, wie
// ledger.Load: Hash steht in "hash" oder "sha256", ein leerer Status ist
// fertig (ADR 0004); "" wenn es keine Zeile gibt.
func diskStatus(t *testing.T, path, hash string) string {
	t.Helper()
	status := ""
	for _, raw := range strings.Split(mustRead(t, path), "\n") {
		var ln struct{ Hash, SHA256, Status string }
		if json.Unmarshal([]byte(raw), &ln) != nil {
			continue
		}
		h := ln.Hash
		if h == "" {
			h = ln.SHA256
		}
		if h != hash {
			continue
		}
		status = ln.Status
		if status == "" {
			status = ledger.StatusFertig
		}
	}
	return status
}

// lastStatus liest den letzten Status zu hash von Platte.
func (e *crashEnv) lastStatus(t *testing.T, hash string) string {
	t.Helper()
	return diskStatus(t, ledgerPath(e.app.cfg), hash)
}

func (e *crashEnv) inFailed(t *testing.T) (data, notes []string) {
	t.Helper()
	entries, _ := os.ReadDir(e.app.cfg.FailedDir)
	for _, d := range entries {
		if strings.HasSuffix(d.Name(), ".fehler.txt") {
			b, err := os.ReadFile(filepath.Join(e.app.cfg.FailedDir, d.Name())) //nolint:gosec // G304: Pfad unter t.TempDir()
			if err != nil {
				t.Fatal(err)
			}
			notes = append(notes, string(b))
		} else {
			data = append(data, d.Name())
		}
	}
	return data, notes
}

func onNotePost(fn func()) func(*http.Request) {
	return func(r *http.Request) {
		if isNotePost(r) {
			fn()
		}
	}
}

// Crash-Matrix: Abbruch nach jedem Schritt → nach dem Neustart nie eine zweite Notiz.
// Fängt: begonnen nach statt vor CreatePage (Spalten 1-3 legen eine zweite
// Seite an), Neustart-Regel aus, Partner bleibt liegen, fertig fehlt.
func TestCrashAfterEachStepNoSecondPage(t *testing.T) {
	cases := []struct {
		name      string
		arm       func(t *testing.T, e *crashEnv)
		disarm    func(t *testing.T, e *crashEnv)
		wantDisk  string // Status auf Platte direkt nach dem Absturz
		wantNotes int    // Notizen nach Absturz (= nach Neustart)
		wantTasks int
		toFailed  bool // Neustart-Regel: beide Dateien → _fehler
	}{
		{
			name: "nach begonnen",
			arm: func(_ *testing.T, e *crashEnv) {
				e.rt.before = func(r *http.Request) (*http.Response, error) {
					if isNotePost(r) {
						panic(crash{})
					}
					return nil, nil
				}
			},
			wantDisk: ledger.StatusBegonnen, wantNotes: 0, wantTasks: 0, toFailed: true,
		},
		{
			name:     "nach CreatePage",
			arm:      func(_ *testing.T, e *crashEnv) { e.rt.after = onNotePost(func() { panic(crash{}) }) },
			wantDisk: ledger.StatusBegonnen, wantNotes: 1, wantTasks: 0, toFailed: true,
		},
		{
			name: "nach Tasks",
			arm: func(_ *testing.T, e *crashEnv) {
				e.log.on = func(l string) {
					if strings.Contains(l, "✓ Notion:") {
						panic(crash{})
					}
				}
			},
			wantDisk: ledger.StatusBegonnen, wantNotes: 1, wantTasks: 1, toFailed: true,
		},
		{
			// Archiv blockiert (Datei statt Monatsordner), Absturz bei der Warnung:
			// fertig steht auf Platte, die Dateien liegen noch im Exportordner.
			name: "nach fertig",
			arm: func(t *testing.T, e *crashEnv) {
				if err := os.MkdirAll(e.app.cfg.ArchiveDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(e.app.cfg.ArchiveDir, e.month), nil, 0o644); err != nil {
					t.Fatal(err)
				}
				e.log.on = func(l string) {
					if strings.Contains(l, "konnte nicht archiviert werden") {
						panic(crash{})
					}
				}
			},
			disarm: func(t *testing.T, e *crashEnv) {
				if err := os.Remove(filepath.Join(e.app.cfg.ArchiveDir, e.month)); err != nil {
					t.Fatal(err)
				}
			},
			wantDisk: ledger.StatusFertig, wantNotes: 1, wantTasks: 1,
		},
		{
			// Transkript archiviert, Partner liegt noch: Neustart archiviert ihn per HasNear.
			name: "nach Archiv",
			arm: func(_ *testing.T, e *crashEnv) {
				e.log.on = func(l string) {
					if strings.Contains(l, "📦") {
						panic(crash{})
					}
				}
			},
			wantDisk: ledger.StatusFertig, wantNotes: 1, wantTasks: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newCrashEnv(t)
			tc.arm(t, e)
			if !e.pass(time.Now()) {
				t.Fatalf("kein Absturz ausgelöst; Log:\n%s", e.log.String())
			}
			if got := e.lastStatus(t, e.hash()); got != tc.wantDisk {
				t.Fatalf("Status auf Platte nach Absturz = %q, want %q", got, tc.wantDisk)
			}
			if n, k := e.pages(); n != tc.wantNotes || k != tc.wantTasks {
				t.Fatalf("nach Absturz: %d Notizen, %d Tasks; want %d, %d", n, k, tc.wantNotes, tc.wantTasks)
			}
			if tc.disarm != nil {
				tc.disarm(t, e)
			}

			e.restart(t)
			for i := 0; i < 2; i++ {
				if e.pass(time.Now()) {
					t.Fatal("Absturz nach dem Neustart")
				}
			}
			if n, k := e.pages(); n != tc.wantNotes || k != tc.wantTasks {
				t.Fatalf("nach Neustart: %d Notizen, %d Tasks; want %d, %d (keine zweite Seite)\nLog:\n%s", n, k, tc.wantNotes, tc.wantTasks, e.log.String())
			}
			if left := remaining(t, e.dir); len(left) != 0 {
				t.Fatalf("Dateien im Exportordner: %v", left)
			}
			data, notes := e.inFailed(t)
			if !tc.toFailed {
				if len(data) != 0 {
					t.Fatalf("unerwartet in _fehler: %v", data)
				}
				if got := e.lastStatus(t, e.hash()); got != ledger.StatusFertig {
					t.Fatalf("Status = %q, want fertig", got)
				}
				return
			}
			if len(data) != 2 || len(notes) != 2 {
				t.Fatalf("_fehler: %v + %d Hinweise, want beide Dateien mit Hinweis", data, len(notes))
			}
			for _, n := range notes {
				if !strings.Contains(n, pipeline.ErrUnterbrochen.Error()) {
					t.Fatalf(".fehler.txt ohne Hinweis:\n%s", n)
				}
			}
			if got := e.lastStatus(t, e.hash()); got != ledger.StatusVerworfen {
				t.Fatalf("Status nach Neustart-Regel = %q, want verworfen", got)
			}
			if !strings.Contains(e.log.String(), "letzter Lauf wurde beim Anlegen in Notion unterbrochen") {
				t.Fatalf("Log ohne Hinweis:\n%s", e.log.String())
			}
		})
	}
}

// Fängt: verworfen fehlt nach der Neustart-Regel (zurückgelegte Datei landet
// erneut in _fehler statt neu zu laufen).
func TestRestartRuleFileMovedBackRunsAgain(t *testing.T) {
	e := newCrashEnv(t)
	e.rt.before = func(r *http.Request) (*http.Response, error) {
		if isNotePost(r) {
			panic(crash{})
		}
		return nil, nil
	}
	if !e.pass(time.Now()) {
		t.Fatal("kein Absturz")
	}
	e.restart(t)
	e.pass(time.Now())
	data, _ := e.inFailed(t)
	if len(data) != 2 {
		t.Fatalf("_fehler: %v", data)
	}
	// Mark prüft Notion, findet keine Seite und legt die Dateien zurück.
	for _, name := range data {
		dst := filepath.Join(e.dir, name)
		if err := os.Rename(filepath.Join(e.app.cfg.FailedDir, name), dst); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-5 * time.Minute)
		if err := os.Chtimes(dst, old, old); err != nil {
			t.Fatal(err)
		}
	}
	e.restart(t)
	e.pass(time.Now())
	if n, _ := e.pages(); n != 1 {
		t.Fatalf("Notizen = %d, want 1\nLog:\n%s", n, e.log.String())
	}
	if left := remaining(t, e.dir); len(left) != 0 {
		t.Fatalf("nicht archiviert: %v", left)
	}
	if got := e.lastStatus(t, e.hash()); got != ledger.StatusFertig {
		t.Fatalf("Status = %q, want fertig", got)
	}
}

// Fängt: Verschieben nach _fehler scheitert, trotzdem verworfen (nächster
// Durchlauf würde die Datei neu anlegen).
func TestRestartRuleMoveFailsKeepsBegonnen(t *testing.T) {
	e := newCrashEnv(t)
	l := e.app.ledger
	if err := l.Add(ledger.Entry{Hash: e.hash(), Status: ledger.StatusBegonnen, File: "x", ModTime: flowM}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.app.cfg.FailedDir, nil, 0o644); err != nil { // Datei statt Ordner
		t.Fatal(err)
	}
	e.pass(time.Now())
	if !exists(e.tr) || !exists(e.sum) {
		t.Fatal("Dateien müssen liegen bleiben")
	}
	if !l.Started(e.hash()) || e.lastStatus(t, e.hash()) != ledger.StatusBegonnen {
		t.Fatal("ohne Verschieben bleibt begonnen (Speicher und Platte)")
	}
	e.f.mu.Lock()
	calls := e.f.llmCalls
	e.f.mu.Unlock()
	if calls != 0 {
		t.Fatalf("LLM-Aufrufe = %d, want 0", calls)
	}
}

// crashAfterCreatePage: Absturz direkt nach POST der Notiz (1 Seite, begonnen
// auf Platte), dann Neustart.
func crashAfterCreatePage(t *testing.T, e *crashEnv) {
	t.Helper()
	e.rt.after = onNotePost(func() { panic(crash{}) })
	if !e.pass(time.Now()) {
		t.Fatal("kein Absturz")
	}
	e.restart(t)
}

// Fängt: Neustart-Regel verschiebt das Transkript vor dem Partner bzw. macht
// nach einem Fehler weiter. Dann liegt die Zusammenfassung allein im
// Exportordner und legt als Waise eine zweite Seite an.
// Die Zusammenfassung ist unveränderlich (chflags uchg): Rename und Löschen
// scheitern, das Verschieben schlägt fehl.
func TestRestartRulePartialMoveNoSecondPage(t *testing.T) {
	e := newCrashEnv(t)
	crashAfterCreatePage(t, e)
	chflags := func(flag string) error {
		return exec.Command("chflags", flag, e.sum).Run() //nolint:gosec // G204: feste Flags, Pfad unter t.TempDir()
	}
	if err := chflags("uchg"); err != nil {
		t.Fatalf("chflags: %v", err)
	}
	unlock := func() {
		if err := chflags("nouchg"); err != nil {
			t.Fatalf("chflags: %v", err)
		}
	}
	t.Cleanup(func() { _ = chflags("nouchg") })

	now := time.Now()
	e.pass(now)
	e.pass(now.Add(2 * time.Hour)) // weit nach orphan_summary_minutes
	if n, _ := e.pages(); n != 1 {
		t.Fatalf("Notizen = %d, want 1 (keine zweite Seite)\nLog:\n%s", n, e.log.String())
	}
	if !exists(e.tr) || e.lastStatus(t, e.hash()) != ledger.StatusBegonnen {
		t.Fatal("Transkript muss mit begonnen liegen bleiben")
	}

	unlock()
	e.pass(now.Add(3 * time.Hour))
	if n, _ := e.pages(); n != 1 {
		t.Fatalf("Notizen = %d, want 1\nLog:\n%s", n, e.log.String())
	}
	if left := remaining(t, e.dir); len(left) != 0 {
		t.Fatalf("Dateien im Exportordner: %v", left)
	}
	if got := e.lastStatus(t, e.hash()); got != ledger.StatusVerworfen {
		t.Fatalf("Status = %q, want verworfen", got)
	}
}

// Fängt: eine identische Kopie (gleicher Hash) läuft nach der Neustart-Regel
// normal und legt eine zweite Seite an.
func TestRestartRuleIdenticalCopyToFailed(t *testing.T) {
	e := newCrashEnv(t)
	crashAfterCreatePage(t, e)
	writeOld(t, filepath.Join(e.dir, "Kopie.txt"), srtLikeText(), 5*time.Minute)
	e.pass(time.Now())
	e.pass(time.Now())
	if n, _ := e.pages(); n != 1 {
		t.Fatalf("Notizen = %d, want 1\nLog:\n%s", n, e.log.String())
	}
	if left := remaining(t, e.dir); len(left) != 0 {
		t.Fatalf("Dateien im Exportordner: %v", left)
	}
	data, notes := e.inFailed(t)
	if len(data) != 3 || len(notes) != 3 {
		t.Fatalf("_fehler: %v + %d Hinweise, want 3 Dateien mit Hinweis", data, len(notes))
	}
	for _, n := range notes {
		if !strings.Contains(n, pipeline.ErrUnterbrochen.Error()) {
			t.Fatalf(".fehler.txt ohne Hinweis:\n%s", n)
		}
	}
}

// Fängt: die Kopien-Sperre gilt über den Durchlauf hinaus. Eine von Mark
// zurückgelegte Datei muss auch ohne Neustart des Prozesses normal laufen.
func TestRestartRuleMovedBackSameProcessRunsAgain(t *testing.T) {
	e := newCrashEnv(t)
	e.rt.before = func(r *http.Request) (*http.Response, error) {
		if isNotePost(r) {
			panic(crash{})
		}
		return nil, nil
	}
	if !e.pass(time.Now()) {
		t.Fatal("kein Absturz")
	}
	e.restart(t)
	e.pass(time.Now())
	data, _ := e.inFailed(t)
	if len(data) != 2 {
		t.Fatalf("_fehler: %v", data)
	}
	for _, name := range data {
		writeOld(t, filepath.Join(e.dir, name), mustRead(t, filepath.Join(e.app.cfg.FailedDir, name)), 5*time.Minute)
	}
	e.pass(time.Now())
	if n, _ := e.pages(); n != 1 {
		t.Fatalf("Notizen = %d, want 1\nLog:\n%s", n, e.log.String())
	}
	if got := e.lastStatus(t, e.hash()); got != ledger.StatusFertig {
		t.Fatalf("Status = %q, want fertig", got)
	}
}

// Fängt: die Neustart-Regel löscht den Fehlerzähler der Datei nicht. Eine von
// Mark zurückgelegte Datei hätte sonst schon Versuche verbraucht.
func TestRestartRuleClearsRetryState(t *testing.T) {
	e := newCrashEnv(t)
	if err := e.app.ledger.Add(ledger.Entry{Hash: e.hash(), Status: ledger.StatusBegonnen, File: "x", ModTime: flowM}); err != nil {
		t.Fatal(err)
	}
	e.app.states()[e.tr] = &pipeline.FileState{Attempts: e.app.cfg.MaxAttempts - 1}
	e.pass(time.Now())
	if exists(e.tr) {
		t.Fatal("Transkript muss nach _fehler verschoben sein")
	}
	if st := e.app.states()[e.tr]; st != nil {
		t.Fatalf("Fehlerzähler nicht gelöscht: %+v", st)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // G304: Pfad unter t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Fängt: Add(begonnen) scheitert und CreatePage läuft trotzdem / der Fehler
// zählt nicht als Versuch / der Retry landet als Abbruch in _fehler.
func TestBeginLedgerErrorNoNotionRequest(t *testing.T) {
	e := newCrashEnv(t)
	// Ledger-Pfad durch einen Ordner ersetzen: Add scheitert (EISDIR).
	path := ledgerPath(e.app.cfg)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	var posts int
	e.rt.before = func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/pages" {
			posts++
		}
		return nil, nil
	}
	now := time.Now()
	e.pass(now)
	if posts != 0 {
		t.Fatalf("POST /v1/pages = %d, want 0", posts)
	}
	if st := e.app.states()[e.tr]; st == nil || st.Attempts != 1 {
		t.Fatalf("Fehler muss als Versuch zählen: %+v", st)
	}
	if !exists(e.tr) || !strings.Contains(e.log.String(), "Protokoll nicht schreibbar, Notion-Seite nicht angelegt") {
		t.Fatalf("Datei muss liegen bleiben, Log:\n%s", e.log.String())
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	e.pass(now.Add(2 * time.Minute))
	if n, _ := e.pages(); n != 1 || posts != 2 {
		t.Fatalf("Retry: %d Notizen, %d POSTs; want 1 Notiz + 1 Task\nLog:\n%s", n, posts, e.log.String())
	}
	if left := remaining(t, e.dir); len(left) != 0 {
		t.Fatalf("nicht archiviert: %v", left)
	}
}

// Fängt: Add(fertig)-Fehler wird verschluckt / Hash gilt im Speicher nicht als
// fertig (zweite Seite im selben Prozess) / Dateien bleiben liegen.
func TestFinishLedgerErrorLoggedNoSecondPage(t *testing.T) {
	e := newCrashEnv(t)
	path := ledgerPath(e.app.cfg)
	e.rt.after = onNotePost(func() {
		// Ledger-Datei durch einen Ordner ersetzen: Add(fertig) scheitert (EISDIR).
		if err := os.Remove(path); err != nil {
			panic(err)
		}
		if err := os.Mkdir(path, 0o755); err != nil {
			panic(err)
		}
	})
	e.pass(time.Now())
	if !strings.Contains(e.log.String(), "Protokoll nicht geschrieben (Notiz existiert: https://notion.so/page-1)") {
		t.Fatalf("Fehler beim fertig-Eintrag nicht geloggt:\n%s", e.log.String())
	}
	if !e.app.ledger.Has(e.hash()) {
		t.Fatal("Hash muss im Speicher fertig sein")
	}
	if left := remaining(t, e.dir); len(left) != 0 {
		t.Fatalf("nicht archiviert: %v", left)
	}
	writeOld(t, filepath.Join(e.dir, "Kopie.txt"), srtLikeText(), 5*time.Minute)
	e.pass(time.Now())
	if n, _ := e.pages(); n != 1 {
		t.Fatalf("Notizen = %d, want 1", n)
	}
}

// Fängt: nach einer Fehlerantwort von CreatePage bleibt begonnen stehen
// (Neustart schickt die Datei grundlos nach _fehler) / Retry-Verhalten ändert sich.
func TestCreatePageErrorSetsVerworfen(t *testing.T) {
	for _, tc := range []struct {
		status  int
		wantLog string
	}{
		{500, "⏸"},           // vorübergehend, wie bisher (OD-05)
		{400, "(Versuch 1/"}, // anderer Fehler
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			e := newCrashEnv(t)
			e.rt.before = func(r *http.Request) (*http.Response, error) {
				if !isNotePost(r) {
					return nil, nil
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(`{"code":"x","message":"y"}`)), Header: http.Header{}, Request: r}, nil
			}
			e.pass(time.Now())
			if !exists(e.tr) || !strings.Contains(e.log.String(), tc.wantLog) {
				t.Fatalf("Datei muss liegen bleiben, Log mit %q:\n%s", tc.wantLog, e.log.String())
			}
			if got := e.lastStatus(t, e.hash()); got != ledger.StatusVerworfen {
				t.Fatalf("Status = %q, want verworfen", got)
			}
			e.restart(t)
			e.pass(time.Now())
			if n, _ := e.pages(); n != 1 {
				t.Fatalf("Notizen nach Neustart = %d, want 1\nLog:\n%s", n, e.log.String())
			}
			if data, _ := e.inFailed(t); len(data) != 0 {
				t.Fatalf("unerwartet in _fehler: %v", data)
			}
		})
	}
}

// Fängt: Abbruch per Kontext während POST setzt verworfen (die Anfrage kann
// Notion erreicht haben; ein Neustart würde eine zweite Seite anlegen).
func TestCreatePageCanceledKeepsBegonnen(t *testing.T) {
	e := newCrashEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.rt.before = func(r *http.Request) (*http.Response, error) {
		if !isNotePost(r) {
			return nil, nil
		}
		cancel()
		return nil, context.Canceled
	}
	e.app.Pass(ctx, time.Now())
	if got := e.lastStatus(t, e.hash()); got != ledger.StatusBegonnen {
		t.Fatalf("Status = %q, want begonnen", got)
	}
	if st := e.app.states()[e.tr]; st != nil && st.Attempts != 0 {
		t.Fatalf("Abbruch darf nicht zählen: %+v", st)
	}
	e.restart(t)
	e.pass(time.Now())
	if data, _ := e.inFailed(t); len(data) != 2 {
		t.Fatalf("Neustart-Regel: _fehler = %v, want 2 Dateien", data)
	}
	if n, _ := e.pages(); n != 0 {
		t.Fatalf("Notizen = %d, want 0", n)
	}
}

// Fängt: -file (ProcessSingleFile) schreibt begonnen ins Protokoll (gehört zu R-M3-14).
func TestProcessSingleFileWritesNoLedger(t *testing.T) {
	e := newCrashEnv(t)
	if err := e.app.ProcessSingleFile(context.Background(), e.tr); err != nil {
		t.Fatal(err)
	}
	if exists(ledgerPath(e.app.cfg)) {
		t.Fatal("-file darf kein Protokoll schreiben")
	}
}
