package ledger

// Tests der Dateirechte (R-M3-04, S-12, OD-10): neue Protokolldatei 0600, neue
// Ordner 0700, unabhängig von der umask; vorhandene behalten ihre Rechte. Kein
// t.Parallel: die umask gilt für den ganzen Prozess.

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// permissiveUmask setzt die umask auf 0 und stellt sie am Testende wieder her.
func permissiveUmask(t *testing.T) {
	t.Helper()
	old := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(old) })
}

func wantMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != want {
		t.Fatalf("%s: Rechte %#o, want %#o", path, got, want)
	}
}

// Fängt: neue Protokolldatei mit 0644 bzw. abhängig von der umask, neue
// Ordner mit 0755.
func TestLedgerAddNewFileAndDirsPrivate(t *testing.T) {
	permissiveUmask(t)
	root := t.TempDir()
	archiv := filepath.Join(root, "_archiv")
	path := filepath.Join(archiv, "sub", "l.jsonl")
	rootMode := dirMode(t, root)
	l, _ := Load(path, true)
	for _, h := range []string{"h1", "h2"} {
		if err := l.Add(Entry{Hash: h, ModTime: flowM}); err != nil {
			t.Fatal(err)
		}
	}
	wantMode(t, path, 0o600)
	wantMode(t, archiv, 0o700)
	wantMode(t, filepath.Dir(path), 0o700)
	wantMode(t, root, rootMode) // vorhandener Ordner unverändert
	if l2, err := Load(path, true); err != nil || !l2.Has("h1") || !l2.Has("h2") {
		t.Fatalf("beide Einträge erwartet, err=%v", err)
	}
}

// Fängt: chmod einer vorhandenen Protokolldatei oder eines vorhandenen
// Ordners (OD-10: nur neue).
func TestLedgerAddExistingKeepsModes(t *testing.T) {
	permissiveUmask(t)
	dir := filepath.Join(t.TempDir(), "_archiv")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "l.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	l, _ := Load(path, true)
	if err := l.Add(Entry{Hash: "h", ModTime: flowM}); err != nil {
		t.Fatal(err)
	}
	wantMode(t, path, 0o644)
	wantMode(t, dir, 0o755)
	if !lineCount(t, path, 1) {
		t.Fatal("eine Zeile erwartet")
	}
}

// Fängt: Ordnerpfad ist eine Datei; Fehler wie bei os.MkdirAll (ENOTDIR).
func TestLedgerAddDirIsFile(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "_archiv")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	l := &Ledger{path: filepath.Join(blocker, "l.jsonl"), persist: true, byHash: map[string]Entry{}}
	if err := l.Add(Entry{Hash: "h"}); !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("err = %v, want ENOTDIR", err)
	}
}

// Fängt: fehlender Chmod nach Mkdir/OpenFile; mit umask 0277 wären Ordner 0400
// und Datei 0400, nur die expliziten Chmod-Aufrufe liefern 0700/0600.
func TestLedgerAddRestrictiveUmask(t *testing.T) {
	root := t.TempDir() // vor der umask-Änderung: TempDir braucht normale Rechte
	old := syscall.Umask(0o277)
	t.Cleanup(func() { syscall.Umask(old) })
	path := filepath.Join(root, "_archiv", "sub", "l.jsonl")
	l, _ := Load(path, true)
	if err := l.Add(Entry{Hash: "h", ModTime: flowM}); err != nil {
		t.Fatal(err)
	}
	wantMode(t, filepath.Dir(filepath.Dir(path)), 0o700)
	wantMode(t, filepath.Dir(path), 0o700)
	wantMode(t, path, 0o600)
}

// Fängt: mkdirPrivate meldet bei hängendem Symlink, leerem oder unbereinigtem
// Pfad nil statt eines Fehlers wie os.MkdirAll; vorhandener Ordner bleibt.
func TestMkdirPrivateLikeMkdirAll(t *testing.T) {
	root := t.TempDir()
	dangling := filepath.Join(root, "link")
	if err := os.Symlink(filepath.Join(root, "gibtsnicht"), dangling); err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{
		"hängender Symlink": dangling,
		"leer":              "",
		"unbereinigt":       root + "/x1/../x2", // Join würde bereinigen,
	} {
		if err := mkdirPrivate(dir); err == nil {
			t.Errorf("%s: Fehler erwartet", name)
		}
	}
	existing := filepath.Join(root, "da")
	if err := os.Mkdir(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := mkdirPrivate(existing); err != nil {
		t.Fatal(err)
	}
	wantMode(t, existing, 0o755)
}

func dirMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func lineCount(t *testing.T, path string, want int) bool {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // G304: Pfad unter t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, c := range b {
		if c == '\n' {
			n++
		}
	}
	return n == want
}
