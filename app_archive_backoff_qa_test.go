package main

// QA R-M1-07: Sync-Warnung auf dem _fehler-Pfad (fail) und FIFO im Exportordner.

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"meetingsync/internal/ledger"
)

// Kopierpfad + Sync-Fehler beim Verschieben nach _fehler: Warnung, Datei liegt
// in _fehler (genau eine, mit Hinweis), kein Verschieben-Fehler, kein Backoff.
// Auslöser (a) Neustart-Regel, (b) handleError bei max_attempts.
func TestFailPathDirSyncWarnsAndMoves(t *testing.T) {
	const syncWarn = "Zielordner nicht synchronisiert"
	for _, tc := range []struct {
		name string
		arm  func(t *testing.T, e *crashEnv)
		want string // Ursache im .fehler.txt
	}{
		{"Neustart-Regel", func(t *testing.T, e *crashEnv) {
			if err := e.app.ledger.Add(ledger.Entry{Hash: e.hash(), Status: ledger.StatusBegonnen, File: "x", ModTime: flowM}); err != nil {
				t.Fatal(err)
			}
		}, errUnterbrochen.Error()},
		{"max_attempts", func(_ *testing.T, e *crashEnv) {
			e.app.cfg.MaxAttempts = 1
			e.app.llm.http.Transport = e.rt
			e.rt.before = func(r *http.Request) (*http.Response, error) {
				if strings.Contains(r.URL.Path, "/chat/") {
					return &http.Response{StatusCode: http.StatusBadRequest, Status: "400 Bad Request", Proto: "HTTP/1.1",
						Header: http.Header{}, Body: io.NopCloser(bytes.NewBufferString(`{"error":"kaputt"}`)), Request: r}, nil
				}
				return nil, nil
			}
		}, "gebe auf"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newCrashEnv(t)
			if err := os.Remove(e.sum); err != nil { // eine Datei → genau eine Sync-Warnung
				t.Fatal(err)
			}
			tc.arm(t, e)
			setRename(t, noRename)
			calls := 0
			setSyncDir(t, func(string) error { calls++; return errors.New("EIO") })

			e.threePasses(time.Now())

			data, notes := e.inFailed(t)
			if len(data) != 1 || len(notes) != 1 {
				t.Fatalf("_fehler: %v + %d Hinweise, want 1 Datei mit Hinweis\nLog:\n%s", data, len(notes), e.log.String())
			}
			if tc.name == "Neustart-Regel" && !strings.Contains(notes[0], tc.want) {
				t.Fatalf(".fehler.txt ohne Ursache:\n%s", notes[0])
			}
			if a, _, x := e.counts(t); a != 0 || x != 0 {
				t.Fatalf("Archiv %d, Export %d; want 0, 0", a, x)
			}
			if calls != 1 {
				t.Fatalf("syncDirFn %d-mal, want 1", calls)
			}
			if n := strings.Count(e.log.String(), syncWarn); n != 1 {
				t.Fatalf("Sync-Warnung %d-mal, want 1\nLog:\n%s", n, e.log.String())
			}
			if strings.Contains(e.log.String(), "konnte nicht verschoben werden") {
				t.Fatalf("Sync-Fehler gilt als Verschiebe-Fehler:\n%s", e.log.String())
			}
			if st := e.app.state[e.tr]; st != nil {
				t.Fatalf("state = %+v, want nil", st)
			}
			if tc.name == "max_attempts" && strings.Count(e.log.String(), "gebe auf") != 1 {
				t.Fatalf("gebe auf nicht genau einmal:\n%s", e.log.String())
			}
			if tc.name == "Neustart-Regel" && strings.Count(e.log.String(), "unterbrochen") != 1 {
				t.Fatalf("Unterbrochen-Zeile wiederholt:\n%s", e.log.String())
			}
			if notes, _ := e.pages(); notes != 0 {
				t.Fatalf("Notizen = %d, want 0", notes)
			}
		})
	}
}

// FIFO mit unterstützter Endung: übersprungen (einmal gemeldet), Pass hängt nicht.
// Fängt: os.ReadFile auf einem FIFO blockiert ohne Schreiber.
func TestListEntriesSkipsFIFO(t *testing.T) {
	e := newCrashEnv(t)
	for _, p := range []string{e.tr, e.sum} {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	fifo := filepath.Join(e.dir, "Pipe 2026-10-28 14-00.srt")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.threePasses(time.Now())
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Pass hängt an der FIFO")
	}
	if n := strings.Count(e.log.String(), "Pipe 2026-10-28 14-00.srt: keine reguläre Datei"); n != 1 {
		t.Fatalf("Hinweis %d-mal, want 1\nLog:\n%s", n, e.log.String())
	}
	if a, f, x := e.counts(t); a != 0 || f != 0 || x != 1 {
		t.Fatalf("Archiv %d, _fehler %d, Export %d; want 0, 0, 1", a, f, x)
	}
	if st, err := os.Lstat(fifo); err != nil || st.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("FIFO verändert: %v", err)
	}
	if notes, _ := e.pages(); notes != 0 {
		t.Fatalf("Notizen = %d, want 0", notes)
	}
}
