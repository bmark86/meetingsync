package watch

// Tests der Dateirechte (R-M3-04, S-12, OD-10): neue Ordner 0700, neue Dateien
// 0600, unabhängig von der umask; vorhandene behalten ihre Rechte. Kein
// t.Parallel: die umask gilt für den ganzen Prozess.

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
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

// Fängt: neue Archiv-, Monats- oder Fehlerordner mit 0755 bzw. abhängig von
// der umask; Kopie mit 0666/0644; umbenannte Datei mit geänderten Rechten.
func TestMovePermissionsNew(t *testing.T) {
	permissiveUmask(t)
	for _, tc := range []struct {
		name     string
		rename   func(string, string) error
		wantFile os.FileMode
	}{
		{"rename", renameExcl, 0o644}, // Inode wandert mit, Rechte unverändert
		{"kopie", noRename, 0o600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src, _ := moveEnv(t)
			archiv := filepath.Join(filepath.Dir(src), "_archiv")
			monat := filepath.Join(archiv, "2026-11")
			dst, err := Mover{Rename: tc.rename}.Move(src, monat)
			if err != nil {
				t.Fatal(err)
			}
			wantMode(t, archiv, 0o700)
			wantMode(t, monat, 0o700)
			wantMode(t, dst, tc.wantFile)
		})
	}
}

// Fängt: chmod vorhandener Ordner (OD-10: nur neue), auch über MkdirAll.
func TestMovePermissionsExistingDirUnchanged(t *testing.T) {
	permissiveUmask(t)
	src, _ := moveEnv(t)
	archiv := filepath.Join(filepath.Dir(src), "_archiv")
	if err := os.Mkdir(archiv, 0o755); err != nil {
		t.Fatal(err)
	}
	monat := filepath.Join(archiv, "2026-11")
	if err := os.Mkdir(monat, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := (Mover{}).Move(src, monat); err != nil {
		t.Fatal(err)
	}
	wantMode(t, archiv, 0o755)
	wantMode(t, monat, 0o750)
}

// Fängt: nur der fehlende Monatsordner ist neu, der vorhandene Archivordner
// wird nicht angefasst.
func TestMovePermissionsOnlyMissingLevel(t *testing.T) {
	permissiveUmask(t)
	src, _ := moveEnv(t)
	archiv := filepath.Join(filepath.Dir(src), "_archiv")
	if err := os.Mkdir(archiv, 0o755); err != nil {
		t.Fatal(err)
	}
	monat := filepath.Join(archiv, "2026-11")
	if _, err := (Mover{}).Move(src, monat); err != nil {
		t.Fatal(err)
	}
	wantMode(t, archiv, 0o755)
	wantMode(t, monat, 0o700)
}

// Fängt: Zielpfad ist eine Datei; Fehler wie bei os.MkdirAll (ENOTDIR),
// Quelle bleibt liegen.
func TestMoveTargetIsFile(t *testing.T) {
	src, _ := moveEnv(t)
	blocker := filepath.Join(filepath.Dir(src), "_fehler")
	writeFile(t, blocker, "x")
	if _, err := (Mover{}).Move(src, blocker); !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("err = %v, want ENOTDIR", err)
	}
	if _, err := (Mover{}).Move(src, filepath.Join(blocker, "2026-11")); !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("err = %v, want ENOTDIR", err)
	}
	if !exists(src) {
		t.Fatal("Quelle muss liegen bleiben")
	}
}

// Fängt: fehlender Chmod nach Mkdir/OpenFile; mit umask 0277 wären Ordner und
// Kopie 0400, nur die expliziten Chmod-Aufrufe liefern 0700/0600.
func TestMovePermissionsRestrictiveUmask(t *testing.T) {
	src, _ := moveEnv(t) // vor der umask-Änderung: TempDir braucht normale Rechte
	old := syscall.Umask(0o277)
	t.Cleanup(func() { syscall.Umask(old) })
	archiv := filepath.Join(filepath.Dir(src), "_archiv")
	monat := filepath.Join(archiv, "2026-11")
	dst, err := Mover{Rename: noRename}.Move(src, monat)
	if err != nil {
		t.Fatal(err)
	}
	wantMode(t, archiv, 0o700)
	wantMode(t, monat, 0o700)
	wantMode(t, dst, 0o600)
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

// Fängt: .fehler.txt mit 0644 oder abhängig von der umask, auch beim
// Ausweichnamen.
func TestWriteFehlerTxtPermissions(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "a.txt")
	old := syscall.Umask(0o277) // Datei entsteht 0400, erst Chmod macht 0600
	t.Cleanup(func() { syscall.Umask(old) })
	at := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	for _, name := range []string{dst + ".fehler.txt", dst + " (2).fehler.txt"} {
		if err := WriteFehlerTxt(dst, at, errors.New("kaputt")); err != nil {
			t.Fatal(err)
		}
		wantMode(t, name, 0o600)
	}
}
