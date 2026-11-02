package main

import (
	"bytes"
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"meetingsync/internal/ledger"
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

// setRename ersetzt renameFn für einen Test.
func setRename(t *testing.T, fn func(src, dst string) error) {
	t.Helper()
	old := renameFn
	renameFn = fn
	t.Cleanup(func() { renameFn = old })
}

// noRename simuliert ein anderes Volume: Umbenennen nicht möglich.
func noRename(src, dst string) error {
	return &os.LinkError{Op: "renamex_np", Old: src, New: dst, Err: syscall.EXDEV}
}

// setCopy ersetzt copyFn für einen Test.
func setCopy(t *testing.T, fn func(io.Writer, io.Reader) (int64, error)) {
	t.Helper()
	old := copyFn
	copyFn = fn
	t.Cleanup(func() { copyFn = old })
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
	}{{"rename", renameFn}, {"kopie", noRename}} {
		t.Run(tc.name, func(t *testing.T) {
			setRename(t, tc.rename)
			src, dir := moveEnv(t)
			writeFile(t, filepath.Join(dir, "a.txt"), "alt")
			writeFile(t, filepath.Join(dir, "a (2).txt"), "alt2")
			dst, err := moveFile(src, dir)
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
	}{{"rename", renameFn}, {"kopie", noRename}} {
		t.Run(tc.name, func(t *testing.T) {
			src, dir := moveEnv(t)
			race := filepath.Join(dir, "a.txt")
			then := tc.then
			setRename(t, func(oldname, newname string) error {
				if newname == race && !exists(race) {
					writeFile(t, race, "parallel")
				}
				return then(oldname, newname)
			})
			dst, err := moveFile(src, dir)
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
	setRename(t, noRename)
	src, dir := moveEnv(t)
	dst, err := moveFile(src, dir)
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
	}{{"rename", renameFn, "permission denied"}, {"kopie", noRename, "Original bleibt liegen"}} {
		t.Run(tc.name, func(t *testing.T) {
			setRename(t, tc.rename)
			src, dir := moveEnv(t)
			writeFile(t, filepath.Join(dir, "a.txt"), "alt")
			// Quellordner schreibgeschützt: Kopie geht, Umbenennen und Löschen nicht.
			srcDir := filepath.Dir(src)
			if err := os.Chmod(srcDir, 0o555); err != nil { //nolint:gosec // G302: Testordner
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(srcDir, 0o755) }) //nolint:gosec // G302: Testordner
			if _, err := moveFile(src, dir); err == nil || !strings.Contains(err.Error(), tc.want) {
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
	}{{"rename", renameFn}, {"kopie", noRename}} {
		t.Run(tc.name, func(t *testing.T) {
			setRename(t, tc.rename)
			src, dir := moveEnv(t)
			if err := os.Remove(src); err != nil {
				t.Fatal(err)
			}
			if _, err := moveFile(src, dir); !errors.Is(err, os.ErrNotExist) {
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
	setRename(t, noRename)
	src, dir := moveEnv(t)
	setCopy(t, func(w io.Writer, r io.Reader) (int64, error) {
		n, err := io.Copy(w, r)
		// erst anlegen, dann ersetzen: die alte Inode ist noch offen
		writeFile(t, src+".tmp", "fremd")
		if rerr := os.Rename(src+".tmp", src); rerr != nil {
			t.Fatal(rerr)
		}
		return n, err
	})
	// "(<nil>)": Fehlerteil angehängt, obwohl Lstat gelungen ist.
	if _, err := moveFile(src, dir); err == nil || !strings.Contains(err.Error(), "ersetzt oder entfernt") || strings.Contains(err.Error(), "<nil>") {
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
	setRename(t, noRename)
	target, dir := moveEnv(t)
	src := filepath.Join(filepath.Dir(target), "link.txt")
	if err := os.Symlink(target, src); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if _, err := moveFile(src, dir); err == nil || !strings.Contains(err.Error(), "keine reguläre Datei") {
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
			setRename(t, func(src, dst string) error {
				e := tc.errs[min(n, len(tc.errs)-1)]
				n++
				return linkErr(src, dst, e)
			})
			src, dir := moveEnv(t)
			dst, err := moveFile(src, dir)
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
	setRename(t, noRename)
	src, dir := moveEnv(t)
	setCopy(t, func(w io.Writer, _ io.Reader) (int64, error) {
		n, _ := w.Write([]byte("ne"))
		return int64(n), errors.New("Platte voll")
	})
	if _, err := moveFile(src, dir); err == nil || !strings.Contains(err.Error(), "Teilkopie entfernt") {
		t.Fatalf("err = %v, want Kopierfehler", err)
	}
	if got := mustRead(t, src); got != "neu" {
		t.Fatalf("Quelle = %q", got)
	}
	if left := remaining(t, dir); len(left) != 0 {
		t.Fatalf("Ziel: %v, want leer", left)
	}
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
	cfg := &Config{FailedDir: filepath.Join(t.TempDir(), "_fehler")}
	return &App{cfg: cfg, log: log.New(&buf, "", 0), cache: map[string]cachedKind{}}, &buf
}

// Fängt: vorhandene .fehler.txt wird überschrieben (os.WriteFile).
func TestFailFehlerTxtNoOverwrite(t *testing.T) {
	a, _ := failApp(t)
	src, _ := moveEnv(t)
	old := filepath.Join(a.cfg.FailedDir, "a.txt.fehler.txt")
	writeFile(t, old, "alt")
	if !a.fail(errors.New("Ursache neu"), &entry{Path: src, Name: "a.txt"}) {
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
	if !a.fail(errors.New("Ursache"), &entry{Path: src, Name: name}) {
		t.Fatal("fail = false")
	}
	if !exists(filepath.Join(a.cfg.FailedDir, name)) {
		t.Fatal("Datei nicht verschoben")
	}
	if !strings.Contains(buf.String(), "Fehlerhinweis zu "+name+" nicht geschrieben") {
		t.Fatalf("Schreibfehler nicht geloggt:\n%s", buf.String())
	}
}
