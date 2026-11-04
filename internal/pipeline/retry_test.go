package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"meetingsync/internal/domain"
	"meetingsync/internal/ledger"
	"meetingsync/internal/watch"
)

// retryEnv: Pipeline über New mit max_attempts=max und eine Datei meeting.txt.
func retryEnv(t *testing.T, max int) (*testEnv, job, string) {
	t.Helper()
	e := newEnv(t, func(o *Options, _ *Deps) { o.MaxAttempts = max })
	path := filepath.Join(t.TempDir(), "meeting.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return e, job{primary: &watch.Entry{Name: "meeting.txt", Path: path}}, e.o.FailedDir
}

func failedFiles(dir string) int {
	e, _ := os.ReadDir(dir)
	return len(e)
}

var (
	errTrans = domain.Transient(errors.New("LM Studio nicht erreichbar"))
	errOther = errors.New("kaputt")
)

func TestTransientErrorsCountAgainstMaxAttempts(t *testing.T) {
	e, j, failed := retryEnv(t, 3)
	now := testM
	for i := 1; i <= 2; i++ {
		e.p.handleError(context.Background(), j, errTrans, now)
		if failedFiles(failed) != 0 {
			t.Fatalf("Versuch %d: Datei zu früh in _fehler", i)
		}
		if e.p.states[j.primary.Path] == nil {
			t.Fatalf("Versuch %d: state fehlt", i)
		}
	}
	e.p.handleError(context.Background(), j, errTrans, now)
	if failedFiles(failed) != 2 { // Datei + .fehler.txt
		t.Fatalf("erwartet Datei und .fehler.txt in _fehler, got %d", failedFiles(failed))
	}
	if _, err := os.Stat(j.primary.Path); !os.IsNotExist(err) {
		t.Fatal("Originaldatei sollte verschoben sein")
	}
	if e.p.states[j.primary.Path] != nil {
		t.Fatal("state sollte gelöscht sein")
	}
}

func TestMixedErrorsCountTogether(t *testing.T) {
	e, j, failed := retryEnv(t, 3)
	now := testM
	e.p.handleError(context.Background(), j, errOther, now)
	e.p.handleError(context.Background(), j, errTrans, now)
	if failedFiles(failed) != 0 {
		t.Fatal("nach 2 von 3 Versuchen noch nicht in _fehler")
	}
	e.p.handleError(context.Background(), j, errOther, now)
	if failedFiles(failed) != 2 {
		t.Fatal("nach 3 gemischten Fehlern erwartet in _fehler")
	}
}

func TestTransientBackoffUnchanged(t *testing.T) {
	e, j, _ := retryEnv(t, 100)
	now := testM
	want := []time.Duration{1, 2, 4, 8, 16, 16, 16}
	for i, w := range want {
		e.p.handleError(context.Background(), j, errTrans, now)
		if got := e.p.states[j.primary.Path].NextTry.Sub(now); got != w*time.Minute {
			t.Fatalf("Versuch %d: Wartezeit %s, want %s", i+1, got, w*time.Minute)
		}
	}
	e.p.handleError(context.Background(), j, errOther, now)
	if got := e.p.states[j.primary.Path].NextTry.Sub(now); got != 2*time.Minute {
		t.Fatalf("andere Fehler: Wartezeit %s, want 2m", got)
	}
}

func TestCanceledContextDoesNotCount(t *testing.T) {
	e, j, failed := retryEnv(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e.p.handleError(ctx, j, context.Canceled, testM)
	if failedFiles(failed) != 0 {
		t.Fatal("Abbruch darf nicht nach _fehler führen")
	}
	if st := e.p.states[j.primary.Path]; st != nil && st.Attempts != 0 {
		t.Fatalf("attempts = %d, want 0", st.Attempts)
	}
}

func TestBackoffStartsAtOneMinuteAfterOtherError(t *testing.T) {
	e, j, _ := retryEnv(t, 100)
	now := testM
	e.p.handleError(context.Background(), j, errOther, now)
	if got := e.p.states[j.primary.Path].NextTry.Sub(now); got != 2*time.Minute {
		t.Fatalf("anderer Fehler: Wartezeit %s, want 2m", got)
	}
	e.p.handleError(context.Background(), j, errTrans, now)
	if got := e.p.states[j.primary.Path].NextTry.Sub(now); got != time.Minute {
		t.Fatalf("erster vorübergehender Fehler nach anderem: Wartezeit %s, want 1m", got)
	}
	e.p.handleError(context.Background(), j, errOther, now)
	e.p.handleError(context.Background(), j, errTrans, now)
	if got := e.p.states[j.primary.Path].NextTry.Sub(now); got != 2*time.Minute {
		t.Fatalf("zweiter vorübergehender Fehler: Wartezeit %s, want 2m", got)
	}
}

func TestPairMovesTogetherAfterLimit(t *testing.T) {
	e, j, failed := retryEnv(t, 2)
	sumPath := filepath.Join(filepath.Dir(j.primary.Path), "meeting-summary.txt")
	if err := os.WriteFile(sumPath, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	j.summary = &watch.Entry{Name: "meeting-summary.txt", Path: sumPath}
	now := testM
	e.p.handleError(context.Background(), j, errTrans, now)
	if failedFiles(failed) != 0 {
		t.Fatal("Paar zu früh in _fehler")
	}
	e.p.handleError(context.Background(), j, errTrans, now)
	entries, _ := os.ReadDir(failed)
	var data, notes int
	for _, d := range entries {
		if strings.HasSuffix(d.Name(), ".fehler.txt") {
			notes++
		} else {
			data++
		}
	}
	if data != 2 || notes != 2 {
		t.Fatalf("erwartet 2 Dateien + 2 .fehler.txt, got %d + %d", data, notes)
	}
	for _, p := range []string{j.primary.Path, sumPath} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s sollte verschoben sein", p)
		}
	}
}

func TestCanceledContextNonTransientStillCounts(t *testing.T) {
	e, j, failed := retryEnv(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e.p.handleError(ctx, j, errOther, testM)
	if failedFiles(failed) != 2 {
		t.Fatalf("erwartet Datei + .fehler.txt in _fehler, got %d", failedFiles(failed))
	}
}

func TestLogShowsAttemptCounter(t *testing.T) {
	e, j, _ := retryEnv(t, 5)
	e.p.handleError(context.Background(), j, errTrans, testM)
	e.p.handleError(context.Background(), j, errOther, testM)
	out := e.log.String()
	for _, want := range []string{"(Versuch 1/5)", "(Versuch 2/5)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("Log enthält %q nicht:\n%s", want, out)
		}
	}
}

// Fängt: Wartezeiten oder Zählung der Regel selbst verändert (ohne Pipeline).
func TestRetryPolicyRecord(t *testing.T) {
	r := newRetryPolicy(3)
	var st FileState
	if wait, giveUp := r.record(&st, true, testM); giveUp || wait != time.Minute || !st.NextTry.Equal(testM.Add(time.Minute)) || st.Transient != 1 {
		t.Fatalf("1. vorübergehend: wait=%s giveUp=%v st=%+v", wait, giveUp, st)
	}
	if wait, giveUp := r.record(&st, false, testM); giveUp || wait != 2*time.Minute || st.Transient != 1 {
		t.Fatalf("anderer Fehler: wait=%s giveUp=%v st=%+v", wait, giveUp, st)
	}
	if _, giveUp := r.record(&st, true, testM); !giveUp || st.Attempts != 3 {
		t.Fatalf("3. Versuch: giveUp=%v st=%+v", giveUp, st)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r.counts(ctx, errTrans) || !r.counts(ctx, errOther) || !r.counts(context.Background(), errTrans) {
		t.Fatal("counts: nur vorübergehend + Abbruch zählt nicht")
	}
}

// flakyMover: Rename scheitert für Pfade, für die fails true liefert, sonst
// os.Rename. tried zeichnet die Quellpfade in Reihenfolge auf.
type flakyMover struct {
	fails func(src string) bool
	tried []string
}

func (f *flakyMover) mover() watch.Mover {
	return watch.Mover{Rename: func(src, dst string) error {
		f.tried = append(f.tried, src)
		if f.fails(src) {
			return os.ErrPermission
		}
		return os.Rename(src, dst)
	}}
}

// Fängt: gescheitertes Verschieben nach max_attempts löscht den Zustand
// (Zählung beginnt von vorn), wartet nicht 2 min oder startet erneut das LLM.
func TestPassGiveUpMoveFailsKeepsAttempts(t *testing.T) {
	fm := &flakyMover{fails: func(string) bool { return true }}
	e := newEnv(t, func(o *Options, d *Deps) { o.MaxAttempts = 2; d.Mover = fm.mover() })
	e.llm.err = errOther
	tr := e.write(t, "Sitzung.srt", testSRT, testM)
	now := e.clock

	e.p.Pass(context.Background(), now) // Versuch 1
	// Versuch 2: gebe auf, Verschieben scheitert; danach Wartezeit, erneutes
	// Scheitern, Wartezeit
	for _, step := range []struct{ at, next time.Duration }{{2, 4}, {3, 4}, {4, 6}, {5, 6}} {
		e.p.Pass(context.Background(), now.Add(step.at*time.Minute))
		st := e.p.States()[tr]
		if want := now.Add(step.next * time.Minute); st == nil || st.Attempts != 2 || !st.NextTry.Equal(want) || !exists(tr) {
			t.Fatalf("+%d min: state %+v, want Attempts 2, NextTry %v, Datei bleibt", step.at, st, want)
		}
	}
	if len(e.llm.got) != 2 {
		t.Fatalf("LLM-Aufrufe = %d, want 2 (kein neuer Lauf nach max_attempts)", len(e.llm.got))
	}
	if n := strings.Count(e.log.String(), "max_attempts erreicht, neuer Versuch"); n != 1 {
		t.Fatalf("neue Verschiebe-Versuche = %d, want 1, Log:\n%s", n, e.log)
	}

	fm.fails = func(string) bool { return false }
	e.p.Pass(context.Background(), now.Add(6*time.Minute))
	if exists(tr) || len(e.p.States()) != 0 || len(e.llm.got) != 2 {
		t.Fatalf("Datei bleibt %v, state %v, LLM %d", exists(tr), e.p.States(), len(e.llm.got))
	}
	if got := mustRead(t, filepath.Join(e.o.FailedDir, "Sitzung.srt.fehler.txt")); !strings.Contains(got, "kaputt") {
		t.Fatalf(".fehler.txt = %q", got)
	}
}

// Fängt: Partner nicht zuerst verschoben, kein Stopp beim ersten Fehler oder
// die liegen gebliebene Zusammenfassung läuft als Waise (zweite Notiz).
func TestPassGiveUpPairSummaryMoveFails(t *testing.T) {
	fm := &flakyMover{fails: func(src string) bool { return strings.HasSuffix(src, ".md") }}
	e := newEnv(t, func(o *Options, d *Deps) { o.MaxAttempts = 1; d.Mover = fm.mover() })
	e.llm.err = errOther
	tr := e.write(t, "Sitzung.srt", testSRT, testM)
	sum := e.write(t, "Sitzung.md", testSummary, testM.Add(time.Minute))
	now := e.clock

	e.p.Pass(context.Background(), now)
	if len(fm.tried) != 1 || fm.tried[0] != sum || !exists(tr) || !exists(sum) {
		t.Fatalf("Verschiebe-Reihenfolge %v, want nur die Zusammenfassung zuerst; beide bleiben", fm.tried)
	}
	if st := e.p.States()[tr]; st == nil || st.Attempts != 1 || !st.NextTry.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("state %+v", st)
	}
	e.p.Pass(context.Background(), now.Add(time.Minute))
	e.p.Pass(context.Background(), now.Add(2*time.Minute))
	if len(e.llm.got) != 1 || len(e.notion.pages) != 0 || e.p.States()[sum] != nil {
		t.Fatalf("LLM %d, Seiten %d, Waise %v; Log:\n%s", len(e.llm.got), len(e.notion.pages), e.p.States()[sum], e.log)
	}

	fm.fails = func(string) bool { return false }
	e.p.Pass(context.Background(), now.Add(4*time.Minute))
	for _, n := range []string{"Sitzung.srt", "Sitzung.md"} {
		if !exists(filepath.Join(e.o.FailedDir, n)) || !exists(filepath.Join(e.o.FailedDir, n+".fehler.txt")) {
			t.Fatalf("%s nicht in _fehler", n)
		}
	}
	if len(e.p.States()) != 0 || len(e.llm.got) != 1 {
		t.Fatalf("state %v, LLM %d", e.p.States(), len(e.llm.got))
	}
}

// Fängt: nach verschobenem Partner läuft das liegen gebliebene Transkript neu
// durch das LLM statt direkt nach _fehler.
func TestPassGiveUpTranscriptMoveFails(t *testing.T) {
	fm := &flakyMover{fails: func(src string) bool { return strings.HasSuffix(src, ".srt") }}
	e := newEnv(t, func(o *Options, d *Deps) { o.MaxAttempts = 1; d.Mover = fm.mover() })
	e.llm.err = errOther
	tr := e.write(t, "Sitzung.srt", testSRT, testM)
	e.write(t, "Sitzung.md", testSummary, testM.Add(time.Minute))
	now := e.clock

	e.p.Pass(context.Background(), now)
	if !exists(tr) || !exists(filepath.Join(e.o.FailedDir, "Sitzung.md.fehler.txt")) {
		t.Fatal("want Zusammenfassung in _fehler, Transkript bleibt")
	}
	fm.fails = func(string) bool { return false }
	e.p.Pass(context.Background(), now.Add(2*time.Minute))
	if exists(tr) || !exists(filepath.Join(e.o.FailedDir, "Sitzung.srt.fehler.txt")) || len(e.llm.got) != 1 || len(e.p.States()) != 0 {
		t.Fatalf("Transkript bleibt %v, LLM %d, state %v", exists(tr), len(e.llm.got), e.p.States())
	}
}

// Fängt: giveUp vor der Ledger-Prüfung, eine schon fertige Datei landet in
// _fehler statt im Archiv.
func TestPassGiveUpLedgerDoneArchives(t *testing.T) {
	fm := &flakyMover{fails: func(string) bool { return true }}
	e := newEnv(t, func(o *Options, d *Deps) { o.MaxAttempts = 1; d.Mover = fm.mover() })
	e.llm.err = errOther
	tr := e.write(t, "Sitzung.srt", testSRT, testM)

	e.p.Pass(context.Background(), e.clock)
	if st := e.p.States()[tr]; st == nil || st.giveUp == nil {
		t.Fatalf("state %+v, want giveUp", st)
	}
	if err := e.ledger.Add(ledger.Entry{Hash: sha256Hex([]byte(testSRT)), File: "Sitzung.srt", ModTime: testM}); err != nil {
		t.Fatal(err)
	}
	fm.fails = func(string) bool { return false }
	e.p.Pass(context.Background(), e.clock.Add(2*time.Minute))
	if !exists(e.archived("Sitzung.srt")) || exists(filepath.Join(e.o.FailedDir, "Sitzung.srt")) || len(e.p.States()) != 0 {
		t.Fatalf("Archiv %v, _fehler %v, state %v", exists(e.archived("Sitzung.srt")), exists(filepath.Join(e.o.FailedDir, "Sitzung.srt")), e.p.States())
	}
}

// Fängt: veralteter giveUp-Zustand schickt eine neue, geänderte Datei am
// selben Pfad ohne Verarbeitung nach _fehler.
func TestPassGiveUpChangedContentProcesses(t *testing.T) {
	fm := &flakyMover{fails: func(string) bool { return true }}
	e := newEnv(t, func(o *Options, d *Deps) { o.MaxAttempts = 1; d.Mover = fm.mover() })
	e.llm.err = errOther
	tr := e.write(t, "Sitzung.srt", testSRT, testM)

	e.p.Pass(context.Background(), e.clock)
	if st := e.p.States()[tr]; st == nil || st.giveUp == nil || st.Attempts != 1 {
		t.Fatalf("state %+v, want giveUp", st)
	}
	changed := testSRT + "\n"
	e.write(t, "Sitzung.srt", changed, testM.Add(time.Second))
	fm.fails = func(string) bool { return false }
	e.llm.err = nil
	e.p.Pass(context.Background(), e.clock.Add(2*time.Minute))
	if len(e.llm.got) != 2 || len(e.notion.pages) == 0 || !e.ledger.Has(sha256Hex([]byte(changed))) {
		t.Fatalf("LLM %d, Seiten %d; Log:\n%s", len(e.llm.got), len(e.notion.pages), e.log)
	}
	if exists(filepath.Join(e.o.FailedDir, "Sitzung.srt")) || !exists(e.archived("Sitzung.srt")) || len(e.p.States()) != 0 {
		t.Fatalf("_fehler %v, Archiv %v, state %v", exists(filepath.Join(e.o.FailedDir, "Sitzung.srt")), exists(e.archived("Sitzung.srt")), e.p.States())
	}
}
