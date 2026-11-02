package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"meetingsync/internal/ledger"
)

// noRename simuliert ein anderes Volume: Umbenennen nicht möglich.
func noRename(src, dst string) error {
	return &os.LinkError{Op: "renamex_np", Old: src, New: dst, Err: syscall.EXDEV}
}

// lockFile setzt chflags uchg: Löschen und Umbenennen scheitern, Lesen geht.
func lockFile(t *testing.T, path string) {
	t.Helper()
	chflags := func(flag string) error {
		return exec.Command("chflags", flag, path).Run() //nolint:gosec // G204: feste Flags, Pfad unter t.TempDir()
	}
	if err := chflags("uchg"); err != nil {
		t.Fatalf("chflags: %v", err)
	}
	t.Cleanup(func() { _ = chflags("nouchg") })
}

// Neustart-Regel, Transkript lässt sich nicht löschen.
// Fängt: kein Backoff → Logzeile und Verschieben bei jedem Durchlauf.
func TestRestartRuleRemoveFailsBackoff(t *testing.T) {
	e := newCrashEnv(t)
	if err := e.app.ledger.Add(ledger.Entry{Hash: e.hash(), Status: ledger.StatusBegonnen, File: "x", ModTime: flowM}); err != nil {
		t.Fatal(err)
	}
	lockFile(t, e.tr)
	const line = "letzter Lauf wurde beim Anlegen in Notion unterbrochen"
	now := time.Now()
	for i := range 3 {
		e.pass(now.Add(time.Duration(i) * 10 * time.Second))
	}
	if n := strings.Count(e.log.String(), line); n != 1 {
		t.Fatalf("Logzeile %d-mal, want 1\nLog:\n%s", n, e.log.String())
	}
	if !exists(e.tr) || e.lastStatus(t, e.hash()) != ledger.StatusBegonnen {
		t.Fatal("Transkript muss mit begonnen liegen bleiben")
	}
	data, _ := e.inFailed(t)
	for _, d := range data {
		if d != filepath.Base(e.sum) {
			t.Fatalf("Kopie in _fehler: %v", data)
		}
	}
	e.pass(now.Add(3 * time.Minute)) // nach dem Backoff neuer Versuch
	if n := strings.Count(e.log.String(), line); n != 2 {
		t.Fatalf("nach Backoff Logzeile %d-mal, want 2", n)
	}
}
