package watch

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
)

// QA R-M1-04: Parallelität und fremde Dateien (A1, A4, A5).

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
		src, dir := moveEnv(t)
		dst := filepath.Join(dir, "a.txt")
		m := Mover{Rename: noRename, Copy: func(w io.Writer, r io.Reader) (int64, error) {
			n, err := io.Copy(w, r)
			swap(t, dst)
			lockFile(t, src)
			return n, err
		}}
		if _, err := m.Move(src, dir); err == nil {
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
		src, dir := moveEnv(t)
		dst := filepath.Join(dir, "a.txt")
		m := Mover{Rename: noRename, Copy: func(w io.Writer, r io.Reader) (int64, error) {
			swap(t, dst)
			return 0, errors.New("Platte voll")
		}}
		if _, err := m.Move(src, dir); err == nil {
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
	}{{"rename", renameExcl}, {"kopie", noRename}, {"enotsup", func(s, d string) error {
		return &os.LinkError{Op: "renamex_np", Old: s, New: d, Err: syscall.ENOTSUP}
	}}} {
		t.Run(tc.name, func(t *testing.T) {
			m := Mover{Rename: tc.rename}
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
					if _, err := m.Move(src, dir); err != nil {
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
