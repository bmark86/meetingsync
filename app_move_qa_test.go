package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"meetingsync/internal/cli"
	"meetingsync/internal/ledger"
)

// QA R-M1-04: Integrationstests über Pass (A4); Parallelität und fremde
// Dateien (A1, A5) in internal/watch; A3 (fail) in internal/pipeline.

// qaWalkCount zählt alle regulären Dateien unter root mit Namensanfang prefix.
func qaWalkCount(t *testing.T, root, prefix string) int {
	t.Helper()
	n := 0
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasPrefix(d.Name(), prefix) {
			n++
		}
		return nil
	})
	return n
}

// A4 end to end (Neustart-Regel): Kopier-Fallback erzwungen, Transkript nicht
// löschbar. Über 3 Durchläufe darf keine Kopie in _fehler entstehen, die
// Logzeile erscheint einmal; nach dem Backoff genau ein weiterer Versuch.
// Fängt: Kopie je Versuch angesammelt; Logzeile/Verschieben bei jedem Durchlauf.
func TestQAPassRestartRuleFallbackRemoveFails(t *testing.T) {
	e := newCrashEnv(t, func(_ *crashEnv, env *cli.Env) { env.Mover.Rename = noRename })
	if err := e.app.ledger.Add(ledger.Entry{Hash: e.hash(), Status: ledger.StatusBegonnen, File: "x", ModTime: flowM}); err != nil {
		t.Fatal(err)
	}
	lockFile(t, e.tr)
	const line = "letzter Lauf wurde beim Anlegen in Notion unterbrochen"
	now := time.Now()
	for i := range 3 {
		e.pass(now.Add(time.Duration(i) * 10 * time.Second))
	}
	log := e.log.String()
	if n := strings.Count(log, line); n != 1 {
		t.Fatalf("Logzeile %d-mal, want 1\n%s", n, log)
	}
	if n := strings.Count(log, "konnte nicht verschoben werden"); n != 1 {
		t.Fatalf("Warnung %d-mal, want 1\n%s", n, log)
	}
	if !strings.Contains(log, "eigene Kopie entfernt") {
		t.Fatalf("Entfernen der eigenen Kopie nicht geloggt\n%s", log)
	}
	data, _ := e.inFailed(t)
	if len(data) != 1 || data[0] != filepath.Base(e.sum) {
		t.Fatalf("_fehler: %v, want nur Partner", data)
	}
	if !exists(e.tr) || mustRead(t, e.tr) != srtLikeText() {
		t.Fatal("Original-Transkript muss unverändert liegen")
	}
	e.pass(now.Add(3 * time.Minute))
	if n := strings.Count(e.log.String(), line); n != 2 {
		t.Fatalf("nach Backoff Logzeile %d-mal, want 2", n)
	}
	if data, _ := e.inFailed(t); len(data) != 1 {
		t.Fatalf("nach Backoff _fehler: %v, want weiter nur Partner", data)
	}
}

// A4 end to end (Archiv): Kopier-Fallback erzwungen, Transkript nicht löschbar.
// Über 3 Durchläufe keine Kopie im Archiv, Original bleibt. Die Warnzeilen je
// Durchlauf werden nur gemeldet (kein Backoff im Archivpfad spezifiziert).
func TestQAPassArchiveFallbackRemoveFailsNoCopies(t *testing.T) {
	e := newCrashEnv(t, func(_ *crashEnv, env *cli.Env) { env.Mover.Rename = noRename })
	if err := e.app.ledger.Add(ledger.Entry{Hash: e.hash(), Status: ledger.StatusFertig, File: "x", ModTime: flowM}); err != nil {
		t.Fatal(err)
	}
	lockFile(t, e.tr)
	now := time.Now()
	for i := range 3 {
		e.pass(now.Add(time.Duration(i) * 10 * time.Second))
	}
	base := filepath.Base(e.tr)
	if n := qaWalkCount(t, e.app.cfg.ArchiveDir, base[:len(base)-len(".txt")]); n != 1 {
		t.Fatalf("Archiv hat %d Dateien mit Transkriptnamen/Partner, want 1 (nur Partner)", n)
	}
	if n := qaWalkCount(t, e.app.cfg.ArchiveDir, base); n != 0 {
		t.Fatalf("Kopie des Transkripts im Archiv: %d", n)
	}
	if !exists(e.tr) || mustRead(t, e.tr) != srtLikeText() {
		t.Fatal("Original muss unverändert liegen")
	}
	t.Logf("Archiv-Warnzeilen nach 3 Durchläufen: %d", strings.Count(e.log.String(), "konnte nicht archiviert werden"))
}
