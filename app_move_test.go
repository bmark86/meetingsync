package main

import (
	"bytes"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"meetingsync/internal/config"
	"meetingsync/internal/ledger"
	"meetingsync/internal/watch"
)

// moveEnv: Quelle src mit Inhalt "neu" und leerer Zielordner dir.
func moveEnv(t *testing.T) (src, dir string) {
	t.Helper()
	root := t.TempDir()
	src = filepath.Join(root, "a.txt")
	if err := os.WriteFile(src, []byte("neu"), 0o644); err != nil {
		t.Fatal(err)
	}
	return src, filepath.Join(root, "ziel")
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

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

func failApp(t *testing.T) (*App, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	cfg := &config.Config{FailedDir: filepath.Join(t.TempDir(), "_fehler")}
	return &App{cfg: cfg, log: log.New(&buf, "", 0), cache: watch.Cache{}, notifier: &fakeNotifier{}}, &buf
}

// Fängt: vorhandene .fehler.txt wird überschrieben (os.WriteFile).
func TestFailFehlerTxtNoOverwrite(t *testing.T) {
	a, _ := failApp(t)
	src, _ := moveEnv(t)
	old := filepath.Join(a.cfg.FailedDir, "a.txt.fehler.txt")
	writeFile(t, old, "alt")
	if !a.fail(errors.New("Ursache neu"), &watch.Entry{Path: src, Name: "a.txt"}) {
		t.Fatal("fail = false")
	}
	if got := mustRead(t, old); got != "alt" {
		t.Fatalf("vorhandener Hinweis überschrieben: %q", got)
	}
	if got := mustRead(t, filepath.Join(a.cfg.FailedDir, "a.txt (2).fehler.txt")); !strings.Contains(got, "Ursache neu") {
		t.Fatalf("neuer Hinweis = %q", got)
	}
}

// Name so lang, dass "<name>.fehler.txt" über NAME_MAX (255) liegt.
// Fängt: Schreibfehler der .fehler.txt wird ignoriert.
func TestFailFehlerTxtWriteErrorLogged(t *testing.T) {
	a, buf := failApp(t)
	name := strings.Repeat("x", 250) + ".txt"
	src := filepath.Join(t.TempDir(), name)
	writeFile(t, src, "neu")
	if !a.fail(errors.New("Ursache"), &watch.Entry{Path: src, Name: name}) {
		t.Fatal("fail = false")
	}
	if !exists(filepath.Join(a.cfg.FailedDir, name)) {
		t.Fatal("Datei nicht verschoben")
	}
	if !strings.Contains(buf.String(), "Fehlerhinweis zu "+name+" nicht geschrieben") {
		t.Fatalf("Schreibfehler nicht geloggt:\n%s", buf.String())
	}
}
