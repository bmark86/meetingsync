package watch

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// countFS zählt die ReadFile-Aufrufe auf dem echten Dateisystem.
type countFS struct {
	OS
	reads []string
}

func (c *countFS) ReadFile(path string) ([]byte, error) {
	c.reads = append(c.reads, path)
	return c.OS.ReadFile(path)
}

// readWithin ruft OS.ReadFile mit Zeitlimit auf; ein Blockieren (FIFO) lässt
// den Test scheitern statt hängen.
func readWithin(t *testing.T, path string) ([]byte, error) {
	t.Helper()
	type res struct {
		b   []byte
		err error
	}
	ch := make(chan res, 1)
	go func() {
		b, err := OS{}.ReadFile(path)
		ch <- res{b, err}
	}()
	select {
	case r := <-ch:
		return r.b, r.err
	case <-time.After(5 * time.Second):
		t.Fatalf("ReadFile(%s) blockiert", path)
		return nil, nil
	}
}

// Symlinks auf eine Datei außerhalb des Exportordners und auf einen Ordner:
// übersprungen, Hinweis je Link genau einmal über 3 Durchläufe, nie gelesen.
// Fängt: Lister liest durch den Link (Ziel außerhalb landet im Inhalt);
// Symlink auf Ordner still oder mehrfach gemeldet.
func TestListOnOSSymlinkOutsideNotRead(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "export")
	geheim := filepath.Join(root, "geheim.srt")
	writeFile(t, geheim, srt)
	writeFile(t, filepath.Join(dir, "a.srt"), srt)
	if err := os.Mkdir(filepath.Join(root, "ordner"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	for _, p := range []string{geheim, filepath.Join(dir, "a.srt")} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{"link.srt": geheim, "ordner": filepath.Join(root, "ordner")} {
		if err := os.Symlink(target, filepath.Join(dir, link)); err != nil {
			t.Fatal(err)
		}
	}
	c := &countFS{}
	l, logs := lister(nil)
	l.FS, l.Dir = c, dir
	for range 3 {
		es := mustList(t, l, time.Now())
		if len(es) != 1 || es[0].Name != "a.srt" {
			t.Fatalf("Einträge = %v, want [a.srt]", names(es))
		}
	}
	for _, p := range c.reads {
		if p != filepath.Join(dir, "a.srt") {
			t.Fatalf("gelesen: %v, want nur a.srt", c.reads)
		}
	}
	for _, n := range []string{"link.srt", "ordner"} {
		if got := strings.Count(logs.String(), n+": keine reguläre Datei (z. B. Symlink) → übersprungen\n"); got != 1 {
			t.Errorf("%s: Hinweis %d-mal, want 1\n%s", n, got, logs.String())
		}
	}
}

// Reguläre Datei zwischen Ordnerliste/Lstat und ReadFile gegen einen Symlink
// getauscht: ELOOP, kein Inhalt. Fängt: ReadFile folgt dem Symlink.
func TestOSReadFileSwappedSymlink(t *testing.T) {
	root := t.TempDir()
	geheim := filepath.Join(root, "geheim.txt")
	writeFile(t, geheim, "geheim")
	p := filepath.Join(root, "export", "a.srt")
	writeFile(t, p, srt)
	if fi, err := (OS{}).Lstat(p); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("Lstat = %v, %v", fi, err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(geheim, p); err != nil {
		t.Fatal(err)
	}
	b, err := readWithin(t, p)
	if !errors.Is(err, unix.ELOOP) || b != nil {
		t.Fatalf("ReadFile = %q, %v; want ELOOP ohne Inhalt", b, err)
	}
}

// FIFO, Ordner statt Datei: Fehler ohne Inhalt, kein Blockieren.
// Fängt: Öffnen ohne O_NONBLOCK (FIFO hängt); fstat-Prüfung fehlt.
func TestOSReadFileNotRegular(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, "pipe.srt")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	ordner := filepath.Join(root, "ordner.srt")
	if err := os.Mkdir(ordner, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{fifo, ordner} {
		b, err := readWithin(t, p)
		if err == nil || b != nil || !strings.Contains(err.Error(), "keine reguläre Datei") {
			t.Errorf("ReadFile(%s) = %q, %v; want Fehler keine reguläre Datei", filepath.Base(p), b, err)
		}
	}
}

// Größenlimit beim Lesen: genau 20 MB geht, ein Byte mehr ist ein Fehler
// ohne Inhalt (Datei nach dem Lstat gewachsen). Dünn besetzte Dateien.
// Fängt: Lesen ohne Limit; Grenze um eins verschoben.
func TestOSReadFileSizeLimit(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		size int64
		ok   bool
	}{{maxDateiBytes, true}, {maxDateiBytes + 1, false}} {
		p := filepath.Join(root, "gross.srt")
		f, err := os.Create(p) //nolint:gosec // G304: Pfad unter t.TempDir()
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(tc.size); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		b, err := readWithin(t, p)
		if tc.ok && (err != nil || int64(len(b)) != tc.size) {
			t.Fatalf("Größe %d: len %d, %v", tc.size, len(b), err)
		}
		if !tc.ok && (err == nil || b != nil || !strings.Contains(err.Error(), "größer als 20 MB")) {
			t.Fatalf("Größe %d: len %d, %v; want Fehler", tc.size, len(b), err)
		}
	}
}

// Fehlende Datei: Fehler fs.ErrNotExist.
func TestOSReadFileMissing(t *testing.T) {
	if _, err := (OS{}).ReadFile(filepath.Join(t.TempDir(), "fehlt.srt")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v", err)
	}
}
