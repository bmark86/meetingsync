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
