package watch

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
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

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // G304: Pfad unter t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func remaining(t *testing.T, dir string) []string {
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// noRename simuliert ein anderes Volume: Umbenennen nicht möglich.
func noRename(src, dst string) error {
	return &os.LinkError{Op: "renamex_np", Old: src, New: dst, Err: syscall.EXDEV}
}

// lockFile setzt uchg (wie chflags uchg): Löschen und Umbenennen scheitern,
// Lesen geht.
func lockFile(t *testing.T, path string) {
	t.Helper()
	if err := unix.Chflags(path, unix.UF_IMMUTABLE); err != nil {
		t.Fatalf("chflags: %v", err)
	}
	t.Cleanup(func() { _ = unix.Chflags(path, 0) })
}

func checkMoved(t *testing.T, src, dst, want string) {
	t.Helper()
	if exists(src) {
		t.Fatal("Quelle muss gelöscht sein")
	}
	if got := mustRead(t, dst); got != "neu" {
		t.Fatalf("%s = %q, want %q", dst, got, "neu")
	}
	if filepath.Base(dst) != want {
		t.Fatalf("Ziel = %s, want %s", filepath.Base(dst), want)
	}
}

// Fängt: vorhandenes Ziel wird überschrieben (z. B. os.Rename ohne Namensprüfung
// oder ohne RENAME_EXCL); EEXIST beim Umbenennen nicht als "nächster Name".
func TestMoveFileExistingTargetNextName(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rename func(string, string) error
	}{{"rename", renameExcl}, {"kopie", noRename}} {
		t.Run(tc.name, func(t *testing.T) {
			m := Mover{Rename: tc.rename}
			src, dir := moveEnv(t)
			writeFile(t, filepath.Join(dir, "a.txt"), "alt")
			writeFile(t, filepath.Join(dir, "a (2).txt"), "alt2")
			dst, err := m.Move(src, dir)
			if err != nil {
				t.Fatal(err)
			}
			checkMoved(t, src, dst, "a (3).txt")
			if mustRead(t, filepath.Join(dir, "a.txt")) != "alt" || mustRead(t, filepath.Join(dir, "a (2).txt")) != "alt2" {
				t.Fatal("vorhandene Datei überschrieben")
			}
		})
	}
}

// Ziel entsteht zwischen Namenswahl und Anlegen (parallel).
// Fängt: Umbenennen ohne RENAME_EXCL bzw. Kopie ohne O_EXCL (überschreibt),
// EEXIST nicht als "nächster Name".
func TestMoveFileParallelTargetNoOverwrite(t *testing.T) {
	for _, tc := range []struct {
		name string
		then func(string, string) error
	}{{"rename", renameExcl}, {"kopie", noRename}} {
		t.Run(tc.name, func(t *testing.T) {
			src, dir := moveEnv(t)
			race := filepath.Join(dir, "a.txt")
			then := tc.then
			m := Mover{Rename: func(oldname, newname string) error {
				if newname == race && !exists(race) {
					writeFile(t, race, "parallel")
				}
				return then(oldname, newname)
			}}
			dst, err := m.Move(src, dir)
			if err != nil {
				t.Fatal(err)
			}
			checkMoved(t, src, dst, "a (2).txt")
			if got := mustRead(t, race); got != "parallel" {
				t.Fatalf("parallel angelegte Datei überschrieben: %q", got)
			}
		})
	}
}

// Fängt: EXDEV (anderes Volume) ohne Kopier-Fallback.
func TestMoveFileCopyFallback(t *testing.T) {
	m := Mover{Rename: noRename}
	src, dir := moveEnv(t)
	dst, err := m.Move(src, dir)
	if err != nil {
		t.Fatal(err)
	}
	checkMoved(t, src, dst, "a.txt")
}

// Fängt: Quelle nicht löschbar → die eigene Kopie bleibt im Ziel liegen bzw.
// (rename) ein Fehler wird übergangen oder führt zur Kopie.
func TestMoveFileRemoveSrcFailsRemovesOwnCopy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rename func(string, string) error
		want   string
	}{{"rename", renameExcl, "permission denied"}, {"kopie", noRename, "Original bleibt liegen"}} {
		t.Run(tc.name, func(t *testing.T) {
			m := Mover{Rename: tc.rename}
			src, dir := moveEnv(t)
			writeFile(t, filepath.Join(dir, "a.txt"), "alt")
			// Quellordner schreibgeschützt: Kopie geht, Umbenennen und Löschen nicht.
			srcDir := filepath.Dir(src)
			if err := os.Chmod(srcDir, 0o555); err != nil { //nolint:gosec // G302: Testordner
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(srcDir, 0o755) }) //nolint:gosec // G302: Testordner
			if _, err := m.Move(src, dir); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want Fehler mit Hinweis", err)
			}
			if got := mustRead(t, src); got != "neu" {
				t.Fatalf("Original = %q", got)
			}
			if left := remaining(t, dir); len(left) != 1 || left[0] != "a.txt" || mustRead(t, filepath.Join(dir, "a.txt")) != "alt" {
				t.Fatalf("Ziel: %v, want nur die vorhandene a.txt", left)
			}
		})
	}
}

// Quelle fehlt: Fehler, kein leeres Ziel, keine Endlosschleife über Namen.
// Fängt: jeder Fehler als "Name belegt" behandelt (Schleife) bzw. Ziel vor dem
// Öffnen der Quelle angelegt.
func TestMoveFileMissingSourceError(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rename func(string, string) error
	}{{"rename", renameExcl}, {"kopie", noRename}} {
		t.Run(tc.name, func(t *testing.T) {
			m := Mover{Rename: tc.rename}
			src, dir := moveEnv(t)
			if err := os.Remove(src); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Move(src, dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("err = %v, want ErrNotExist", err)
			}
			if left := remaining(t, dir); len(left) != 0 {
				t.Fatalf("Ziel: %v, want leer", left)
			}
		})
	}
}

// Quelle wird während des Kopierens durch eine andere Datei ersetzt.
// Fängt: os.Remove(src) ohne Identitätsprüfung (löscht die fremde Datei).
func TestMoveFileCopySourceReplacedKept(t *testing.T) {
	src, dir := moveEnv(t)
	m := Mover{Rename: noRename, Copy: func(w io.Writer, r io.Reader) (int64, error) {
		n, err := io.Copy(w, r)
		// erst anlegen, dann ersetzen: die alte Inode ist noch offen
		writeFile(t, src+".tmp", "fremd")
		if rerr := os.Rename(src+".tmp", src); rerr != nil {
			t.Fatal(rerr)
		}
		return n, err
	}}
	// "(<nil>)": Fehlerteil angehängt, obwohl Lstat gelungen ist.
	if _, err := m.Move(src, dir); err == nil || !strings.Contains(err.Error(), "ersetzt oder entfernt") || strings.Contains(err.Error(), "<nil>") {
		t.Fatalf("err = %v, want Fehler zur ersetzten Quelle", err)
	}
	if got := mustRead(t, src); got != "fremd" {
		t.Fatalf("Quelle = %q, want fremd", got)
	}
	if got := mustRead(t, filepath.Join(dir, "a.txt")); got != "neu" {
		t.Fatalf("Kopie = %q, want neu", got)
	}
}

// Quelle ist ein Symlink, Umbenennen nicht möglich (Kopie).
// Fängt: Lstat-Prüfung in copyExcl fehlt (Kopie folgt dem Symlink, die
// Identitätsprüfung danach scheitert, je Versuch bleibt eine Kopie liegen).
func TestMoveFileCopySymlinkSourceRefused(t *testing.T) {
	m := Mover{Rename: noRename}
	target, dir := moveEnv(t)
	src := filepath.Join(filepath.Dir(target), "link.txt")
	if err := os.Symlink(target, src); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if _, err := m.Move(src, dir); err == nil || !strings.Contains(err.Error(), "keine reguläre Datei") {
			t.Fatalf("Versuch %d: err = %v, want Fehler zum Symlink", i+1, err)
		}
		if left := remaining(t, dir); len(left) != 0 {
			t.Fatalf("Versuch %d: Ziel %v, want leer", i+1, left)
		}
	}
	if got, err := os.Readlink(src); err != nil || got != target {
		t.Fatalf("Symlink = %q, %v", got, err)
	}
	if got := mustRead(t, target); got != "neu" {
		t.Fatalf("Symlink-Ziel = %q", got)
	}
}

// Fehler des Umbenennens: nur EXDEV und ENOTSUP führen zur Kopie.
// Fängt: ENOTSUP nicht als Kopierfall (Fehler statt Fallback); EINVAL als
// Kopierfall (kopiert statt Fehler); Kopierschleife beginnt nach EEXIST
// wieder beim ersten Namen ("a.txt" statt "a (2).txt").
func TestMoveFileRenameErrors(t *testing.T) {
	linkErr := func(src, dst string, e syscall.Errno) error {
		return &os.LinkError{Op: "renamex_np", Old: src, New: dst, Err: e}
	}
	for _, tc := range []struct {
		name string
		errs []syscall.Errno // je Aufruf; letzter gilt weiter
		want string          // "" = Fehler erwartet
	}{
		{"enotsup", []syscall.Errno{syscall.ENOTSUP}, "a.txt"},
		{"einval", []syscall.Errno{syscall.EINVAL}, ""},
		{"eexist-exdev", []syscall.Errno{syscall.EEXIST, syscall.EXDEV}, "a (2).txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := 0
			m := Mover{Rename: func(src, dst string) error {
				e := tc.errs[min(n, len(tc.errs)-1)]
				n++
				return linkErr(src, dst, e)
			}}
			src, dir := moveEnv(t)
			dst, err := m.Move(src, dir)
			if tc.want == "" {
				if !errors.Is(err, syscall.EINVAL) {
					t.Fatalf("err = %v, want EINVAL", err)
				}
				if got := mustRead(t, src); got != "neu" {
					t.Fatalf("Quelle = %q", got)
				}
				if left := remaining(t, dir); len(left) != 0 {
					t.Fatalf("Ziel: %v, want leer", left)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			checkMoved(t, src, dst, tc.want)
		})
	}
}

// Kopierfehler nach den ersten Bytes.
// Fängt: Teilkopie bleibt im Ziel liegen bzw. Quelle wird trotzdem gelöscht.
func TestMoveFilePartialCopyRemoved(t *testing.T) {
	m := Mover{Rename: noRename, Copy: func(w io.Writer, _ io.Reader) (int64, error) {
		n, _ := w.Write([]byte("ne"))
		return int64(n), errors.New("Platte voll")
	}}
	src, dir := moveEnv(t)
	if _, err := m.Move(src, dir); err == nil || !strings.Contains(err.Error(), "Teilkopie entfernt") {
		t.Fatalf("err = %v, want Kopierfehler", err)
	}
	if got := mustRead(t, src); got != "neu" {
		t.Fatalf("Quelle = %q", got)
	}
	if left := remaining(t, dir); len(left) != 0 {
		t.Fatalf("Ziel: %v, want leer", left)
	}
}

// Nullwert: echtes renamex_np (gleiches Volume, kein Kopieren).
// Fängt: Nullwert ohne Standard-Naht (nil-Aufruf) oder Kopie statt Umbenennen.
func TestMoverZeroValueRenames(t *testing.T) {
	src, dir := moveEnv(t)
	before, err := os.Lstat(src)
	if err != nil {
		t.Fatal(err)
	}
	dst, err := Mover{}.Move(src, dir)
	if err != nil {
		t.Fatal(err)
	}
	checkMoved(t, src, dst, "a.txt")
	if after, err := os.Lstat(dst); err != nil || !os.SameFile(before, after) {
		t.Fatalf("Ziel ist eine Kopie, kein Umbenennen: %v", err)
	}
}
