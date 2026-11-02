package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"meetingsync/internal/ledger"
)

// QA R-M1-04: Integrationstests über Pass und Parallelität (A1, A4, A5).

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
	setRename(t, noRename)
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
	setRename(t, noRename)
	e := newCrashEnv(t)
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

// A4/A5: fremde Datei ersetzt die eigene Kopie, danach scheitert Remove(src).
// Die fremde Datei darf nie gelöscht werden.
func TestQAForeignFileNeverDeleted(t *testing.T) {
	swap := func(t *testing.T, dst string) {
		writeFile(t, dst+".tmp", "fremd")
		if err := os.Rename(dst+".tmp", dst); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("RemoveSrcFails", func(t *testing.T) {
		setRename(t, noRename)
		src, dir := moveEnv(t)
		dst := filepath.Join(dir, "a.txt")
		setCopy(t, func(w io.Writer, r io.Reader) (int64, error) {
			n, err := io.Copy(w, r)
			swap(t, dst)
			lockFile(t, src)
			return n, err
		})
		if _, err := moveFile(src, dir); err == nil {
			t.Fatal("want Fehler")
		}
		if got := mustRead(t, dst); got != "fremd" {
			t.Fatalf("fremde Datei = %q", got)
		}
		if mustRead(t, src) != "neu" {
			t.Fatal("Original verändert")
		}
	})
	t.Run("PartialCopy", func(t *testing.T) {
		setRename(t, noRename)
		src, dir := moveEnv(t)
		dst := filepath.Join(dir, "a.txt")
		setCopy(t, func(w io.Writer, r io.Reader) (int64, error) {
			swap(t, dst)
			return 0, errors.New("Platte voll")
		})
		if _, err := moveFile(src, dir); err == nil {
			t.Fatal("want Fehler")
		}
		if got := mustRead(t, dst); got != "fremd" {
			t.Fatalf("fremde Datei = %q", got)
		}
		if mustRead(t, src) != "neu" {
			t.Fatal("Original verändert")
		}
	})
}

// A1: gleichzeitige Verschiebungen gleichnamiger Quellen ins selbe Ziel; weder
// Überschreiben noch Verlust, mit echtem RENAME_EXCL und im Kopierpfad.
// Fängt: fehlende Atomarität (zwei Quellen landen im selben Namen).
func TestQAConcurrentSameNameNoLoss(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rename func(string, string) error
	}{{"rename", renameFn}, {"kopie", noRename}, {"enotsup", func(s, d string) error {
		return &os.LinkError{Op: "renamex_np", Old: s, New: d, Err: syscall.ENOTSUP}
	}}} {
		t.Run(tc.name, func(t *testing.T) {
			setRename(t, tc.rename)
			root := t.TempDir()
			dir := filepath.Join(root, "ziel")
			writeFile(t, filepath.Join(dir, "a.txt"), "vorhanden")
			const n = 12
			var wg sync.WaitGroup
			errs := make(chan error, n)
			for i := range n {
				src := filepath.Join(root, fmt.Sprintf("q%d", i), "a.txt")
				writeFile(t, src, fmt.Sprintf("inhalt-%d", i))
				wg.Add(1)
				go func() {
					defer wg.Done()
					if _, err := moveFile(src, dir); err != nil {
						errs <- err
					}
				}()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				t.Fatal(err)
			}
			if mustRead(t, filepath.Join(dir, "a.txt")) != "vorhanden" {
				t.Fatal("vorhandene Datei überschrieben")
			}
			seen := map[string]bool{}
			entries, _ := os.ReadDir(dir)
			if len(entries) != n+1 {
				t.Fatalf("%d Dateien im Ziel, want %d", len(entries), n+1)
			}
			for _, d := range entries {
				seen[mustRead(t, filepath.Join(dir, d.Name()))] = true
			}
			for i := range n {
				if !seen[fmt.Sprintf("inhalt-%d", i)] {
					t.Fatalf("Inhalt %d verloren", i)
				}
			}
		})
	}
}

// A3 über fail(): .fehler.txt wird bei mehrfachem Fehlschlag derselben
// Namen nie überschrieben (a.txt.fehler.txt, a (2).txt.fehler.txt gehören zu
// verschiedenen Zielnamen, vorhandener Hinweis bleibt).
func TestQAFehlerTxtParallelCreatedNotOverwritten(t *testing.T) {
	a, _ := failApp(t)
	src, _ := moveEnv(t)
	hint := filepath.Join(a.cfg.FailedDir, "a.txt.fehler.txt")
	writeFile(t, hint, "alt")
	writeFile(t, filepath.Join(a.cfg.FailedDir, "a.txt (2).fehler.txt"), "alt2")
	if !a.fail(errors.New("neu"), &entry{Path: src, Name: "a.txt"}) {
		t.Fatal("fail = false")
	}
	if mustRead(t, hint) != "alt" || mustRead(t, filepath.Join(a.cfg.FailedDir, "a.txt (2).fehler.txt")) != "alt2" {
		t.Fatal("Hinweis überschrieben")
	}
	if !strings.Contains(mustRead(t, filepath.Join(a.cfg.FailedDir, "a.txt (3).fehler.txt")), "neu") {
		t.Fatal("neuer Hinweis fehlt")
	}
}
