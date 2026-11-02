package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"meetingsync/internal/domain"
	"meetingsync/internal/ledger"
	"meetingsync/internal/notion"
)

// AC-A3: sechs Durchläufe mit vorübergehendem Fehler, Wartezeiten 1/2/4/8/16/16 min,
// kein Versuch vor NextTry, danach Erfolg und Zustand gelöscht.
func TestQATransientSequenceOverPasses(t *testing.T) {
	e := newEnv(t, nil)
	e.llm.err = domain.Transient(os.ErrDeadlineExceeded)
	tr := e.write(t, "A.srt", testSRT, testM)
	now := e.clock
	for i, want := range []time.Duration{1, 2, 4, 8, 16, 16} {
		before := len(e.llm.got)
		e.p.Pass(context.Background(), now)
		if len(e.llm.got) != before+1 {
			t.Fatalf("Durchlauf %d: LLM nicht genau einmal aufgerufen", i+1)
		}
		st := e.p.States()[tr]
		if st == nil || st.NextTry.Sub(now) != want*time.Minute || st.Attempts != i+1 {
			t.Fatalf("Durchlauf %d: state %+v, want delta %s", i+1, st, want*time.Minute)
		}
		// vor Ablauf: kein neuer Versuch
		e.p.Pass(context.Background(), st.NextTry.Add(-time.Second))
		if len(e.llm.got) != before+1 {
			t.Fatalf("Durchlauf %d: Versuch vor NextTry", i+1)
		}
		now = st.NextTry
	}
	if len(e.notion.pages) != 0 {
		t.Fatal("Seite trotz Fehler angelegt")
	}
	e.llm.err = nil
	e.p.Pass(context.Background(), now)
	if e.p.States()[tr] != nil || !exists(e.archived("A.srt")) || len(e.notion.pages) == 0 {
		t.Fatal("Erfolg nach Fehlerserie: Zustand nicht gelöscht oder nicht archiviert")
	}
}

// AC-A3: max_attempts über Pass -> _fehler, Transkript samt Hinweis.
func TestQAMaxAttemptsMovesToFailedViaPass(t *testing.T) {
	e := newEnv(t, func(o *Options, _ *Deps) { o.MaxAttempts = 3 })
	e.llm.err = domain.Transient(os.ErrDeadlineExceeded)
	tr := e.write(t, "A.srt", testSRT, testM)
	now := e.clock
	for i := 0; i < 3; i++ {
		e.p.Pass(context.Background(), now)
		now = now.Add(20 * time.Minute)
	}
	if exists(tr) || !exists(filepath.Join(e.o.FailedDir, "A.srt")) {
		t.Fatal("Datei nicht nach _fehler")
	}
	if e.p.States()[tr] != nil {
		t.Fatal("Zustand nicht gelöscht")
	}
}

// AC-A3 über den Konstruktor: Tabelle der Wartezeiten, Limit und Kontext.
func TestQARetryPolicyTable(t *testing.T) {
	r := newRetryPolicy(7)
	st := &FileState{}
	now := testM
	for i, w := range []time.Duration{1, 2, 4, 8, 16, 16} {
		wait, give := r.record(st, true, now)
		if give || wait != w*time.Minute || !st.NextTry.Equal(now.Add(w*time.Minute)) {
			t.Fatalf("transient %d: wait %s give %v", i, wait, give)
		}
	}
	if _, give := r.record(st, false, now); !give {
		t.Fatal("7. Fehler muss aufgeben")
	}
	o := newRetryPolicy(10)
	if w, _ := o.record(&FileState{}, false, now); w != 2*time.Minute {
		t.Fatalf("anderer Fehler wait %s", w)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if o.counts(ctx, domain.Transient(os.ErrClosed)) || !o.counts(ctx, os.ErrClosed) || !o.counts(context.Background(), domain.Transient(os.ErrClosed)) {
		t.Fatal("counts-Regel verletzt")
	}
}

// Neustart-Regel: begonnen -> _fehler ohne LLM/Notion, verworfen im Protokoll;
// zurückgelegt läuft die Datei im nächsten Durchlauf normal.
func TestQARestartRuleBegonnenThenPutBack(t *testing.T) {
	e := newEnv(t, nil)
	hash := sha256Hex([]byte(testSRT))
	if err := e.ledger.Add(ledger.Entry{Hash: hash, Status: ledger.StatusBegonnen, File: "x", ModTime: testM}); err != nil {
		t.Fatal(err)
	}
	tr := e.write(t, "A.srt", testSRT, testM)
	e.p.Pass(context.Background(), e.clock)
	if exists(tr) || !exists(filepath.Join(e.o.FailedDir, "A.srt")) || len(e.llm.got) != 0 || len(e.notion.pages) != 0 {
		t.Fatal("Neustart-Regel: Datei nicht nach _fehler oder LLM/Notion benutzt")
	}
	if !strings.Contains(e.log.String(), "unterbrochen") || e.ledger.Started(hash) {
		t.Fatal("verworfen fehlt")
	}
	e.write(t, "A.srt", testSRT, testM)
	e.p.Pass(context.Background(), e.clock.Add(time.Minute))
	if len(e.notion.pages) == 0 || !e.ledger.Has(hash) || !exists(e.archived("A.srt")) {
		t.Fatal("zurückgelegte Datei lief nicht normal")
	}
}

// Dry-Run schreibt nichts: weder Notion, Archiv, _fehler noch Protokolldatei; Datei bleibt.
func TestQADryRunWritesNothing(t *testing.T) {
	e := newEnv(t, func(o *Options, d *Deps) {
		o.Dry = true
		d.DryRun = func(string, *notion.Page, *domain.MeetingNote, []string, int) {}
		d.Ledger = dryLedger(t, o)
	})
	tr := e.write(t, "A.srt", testSRT, testM)
	e.write(t, "A-summary.txt", "Zusammenfassung", testM)
	e.p.Pass(context.Background(), e.clock)
	for _, d := range []string{e.o.ArchiveDir, e.o.FailedDir} {
		if ents, err := os.ReadDir(d); err == nil && len(ents) > 0 {
			t.Fatalf("%s nicht leer: %v", d, ents)
		}
	}
	if len(e.notion.pages) != 0 || !exists(tr) {
		t.Fatal("Dry-Run verändert Notion oder Datei")
	}
	// Dry-Run mit Dauerfehler bis max_attempts: ebenfalls nichts verschieben.
	e2 := newEnv(t, func(o *Options, d *Deps) { o.Dry, o.MaxAttempts = true, 1; d.Ledger = dryLedger(t, o) })
	e2.llm.err = os.ErrPermission
	tr2 := e2.write(t, "B.srt", testSRT, testM)
	e2.p.Pass(context.Background(), e2.clock)
	if !exists(tr2) || exists(filepath.Join(e2.o.FailedDir, "B.srt")) {
		t.Fatal("Dry-Run verschiebt nach _fehler")
	}
}

// AC-A2: kein time.Now() und kein os.* in Nicht-Test-Code der Pipeline.
func TestQANoTimeNowNoOSInProductionCode(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	re := regexp.MustCompile(`time\.Now\(|\bos\.[A-Z]|"os"`)
	n := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		n++
		b, err := os.ReadFile(f) //nolint:gosec // G304: Quelldatei im Paket
		if err != nil {
			t.Fatal(err)
		}
		for i, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(l), "//") {
				continue
			}
			if re.MatchString(l) {
				t.Errorf("%s:%d: %s", f, i+1, strings.TrimSpace(l))
			}
		}
	}
	if n == 0 {
		t.Fatal("keine Quelldateien gefunden")
	}
}

// dryLedger ist das Protokoll wie internal/cli.Init bei -dry-run (persist=false).
func dryLedger(t *testing.T, o *Options) *ledger.Ledger {
	t.Helper()
	l, err := ledger.Load(filepath.Join(o.ArchiveDir, ".meetingsync-ledger.jsonl"), false)
	if err != nil {
		t.Fatal(err)
	}
	return l
}
