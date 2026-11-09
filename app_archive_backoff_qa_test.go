package main

// QA R-M1-07: Sync-Warnung auf dem _fehler-Pfad (fail) und FIFO im Exportordner.

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

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
