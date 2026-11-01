package main

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
)

func retryApp(t *testing.T, max int) (*App, job, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := defaultConfig()
	cfg.MaxAttempts = max
	cfg.FailedDir = filepath.Join(dir, "_fehler")
	path := filepath.Join(dir, "meeting.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: cfg, log: log.New(io.Discard, "", 0), state: map[string]*fileState{}, cache: map[string]cachedKind{}}
	return a, job{primary: &entry{Name: "meeting.txt", Path: path}}, cfg.FailedDir
}

func failedFiles(dir string) int {
	e, _ := os.ReadDir(dir)
	return len(e)
}

var (
	errTrans = transient(errors.New("LM Studio nicht erreichbar"))
	errOther = errors.New("kaputt")
)

func TestDefaultMaxAttempts(t *testing.T) {
	if got := defaultConfig().MaxAttempts; got != 10 {
		t.Fatalf("MaxAttempts = %d, want 10", got)
	}
}

func TestTransientErrorsCountAgainstMaxAttempts(t *testing.T) {
	a, j, failed := retryApp(t, 3)
	now := time.Now()
	for i := 1; i <= 2; i++ {
		a.handleError(context.Background(), j, errTrans, now)
		if failedFiles(failed) != 0 {
			t.Fatalf("Versuch %d: Datei zu früh in _fehler", i)
		}
		if a.state[j.primary.Path] == nil {
			t.Fatalf("Versuch %d: state fehlt", i)
		}
	}
	a.handleError(context.Background(), j, errTrans, now)
	if failedFiles(failed) != 2 { // Datei + .fehler.txt
		t.Fatalf("erwartet Datei und .fehler.txt in _fehler, got %d", failedFiles(failed))
	}
	if _, err := os.Stat(j.primary.Path); !os.IsNotExist(err) {
		t.Fatal("Originaldatei sollte verschoben sein")
	}
	if a.state[j.primary.Path] != nil {
		t.Fatal("state sollte gelöscht sein")
	}
}

func TestMixedErrorsCountTogether(t *testing.T) {
	a, j, failed := retryApp(t, 3)
	now := time.Now()
	a.handleError(context.Background(), j, errOther, now)
	a.handleError(context.Background(), j, errTrans, now)
	if failedFiles(failed) != 0 {
		t.Fatal("nach 2 von 3 Versuchen noch nicht in _fehler")
	}
	a.handleError(context.Background(), j, errOther, now)
	if failedFiles(failed) != 2 {
		t.Fatal("nach 3 gemischten Fehlern erwartet in _fehler")
	}
}

func TestTransientBackoffUnchanged(t *testing.T) {
	a, j, _ := retryApp(t, 100)
	now := time.Now()
	want := []time.Duration{1, 2, 4, 8, 16, 16, 16}
	for i, w := range want {
		a.handleError(context.Background(), j, errTrans, now)
		if got := a.state[j.primary.Path].nextTry.Sub(now); got != w*time.Minute {
			t.Fatalf("Versuch %d: Wartezeit %s, want %s", i+1, got, w*time.Minute)
		}
	}
	a.handleError(context.Background(), j, errOther, now)
	if got := a.state[j.primary.Path].nextTry.Sub(now); got != 2*time.Minute {
		t.Fatalf("andere Fehler: Wartezeit %s, want 2m", got)
	}
}

func TestCanceledContextDoesNotCount(t *testing.T) {
	a, j, failed := retryApp(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.handleError(ctx, j, context.Canceled, time.Now())
	if failedFiles(failed) != 0 {
		t.Fatal("Abbruch darf nicht nach _fehler führen")
	}
	if st := a.state[j.primary.Path]; st != nil && st.attempts != 0 {
		t.Fatalf("attempts = %d, want 0", st.attempts)
	}
}

func TestBackoffStartsAtOneMinuteAfterOtherError(t *testing.T) {
	a, j, _ := retryApp(t, 100)
	now := time.Now()
	a.handleError(context.Background(), j, errOther, now)
	if got := a.state[j.primary.Path].nextTry.Sub(now); got != 2*time.Minute {
		t.Fatalf("anderer Fehler: Wartezeit %s, want 2m", got)
	}
	a.handleError(context.Background(), j, errTrans, now)
	if got := a.state[j.primary.Path].nextTry.Sub(now); got != time.Minute {
		t.Fatalf("erster vorübergehender Fehler nach anderem: Wartezeit %s, want 1m", got)
	}
	a.handleError(context.Background(), j, errOther, now)
	a.handleError(context.Background(), j, errTrans, now)
	if got := a.state[j.primary.Path].nextTry.Sub(now); got != 2*time.Minute {
		t.Fatalf("zweiter vorübergehender Fehler: Wartezeit %s, want 2m", got)
	}
}

func TestPairMovesTogetherAfterLimit(t *testing.T) {
	a, j, failed := retryApp(t, 2)
	sumPath := filepath.Join(filepath.Dir(j.primary.Path), "meeting-summary.txt")
	if err := os.WriteFile(sumPath, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	j.summary = &entry{Name: "meeting-summary.txt", Path: sumPath}
	now := time.Now()
	a.handleError(context.Background(), j, errTrans, now)
	if failedFiles(failed) != 0 {
		t.Fatal("Paar zu früh in _fehler")
	}
	a.handleError(context.Background(), j, errTrans, now)
	entries, _ := os.ReadDir(failed)
	var data, notes int
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".fehler.txt") {
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
	a, j, failed := retryApp(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.handleError(ctx, j, errOther, time.Now())
	if failedFiles(failed) != 2 {
		t.Fatalf("erwartet Datei + .fehler.txt in _fehler, got %d", failedFiles(failed))
	}
}

func TestLogShowsAttemptCounter(t *testing.T) {
	a, j, _ := retryApp(t, 5)
	var buf bytes.Buffer
	a.log = log.New(&buf, "", 0)
	a.handleError(context.Background(), j, errTrans, time.Now())
	a.handleError(context.Background(), j, errOther, time.Now())
	out := buf.String()
	for _, want := range []string{"(Versuch 1/5)", "(Versuch 2/5)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("Log enthält %q nicht:\n%s", want, out)
		}
	}
}
