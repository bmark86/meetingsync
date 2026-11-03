package watch

import (
	"fmt"
	"io"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// maxDateiBytes ist die Obergrenze für eine Datei aus dem Exportordner (20 MB).
const maxDateiBytes = 20 << 20

// FS ist die Leseseite des Dateisystems für den Exportordner. Verschieben
// läuft nicht über FS, sondern über Mover (Identitätsprüfungen mit
// os.SameFile und renamex_np brauchen das echte Dateisystem).
type FS interface {
	// ReadDir liefert die Einträge nach Namen sortiert (wie os.ReadDir);
	// DirEntry.Info folgt keinem Symlink.
	ReadDir(dir string) ([]fs.DirEntry, error)
	ReadFile(path string) ([]byte, error)
	Lstat(path string) (fs.FileInfo, error)
}

// OS ist das echte Dateisystem.
type OS struct{}

// ReadDir ruft os.ReadDir.
func (OS) ReadDir(dir string) ([]fs.DirEntry, error) { return os.ReadDir(dir) }

// ReadFile liest nur eine reguläre Datei und folgt keinem Symlink: Öffnen mit
// O_NOFOLLOW (Symlink → ELOOP) und O_NONBLOCK (ein FIFO blockiert nicht),
// dann fstat auf der offenen Datei. Mehr als maxDateiBytes → Fehler ohne Inhalt.
// Schützt vor einem Austausch zwischen Ordnerliste und Lesen.
func (OS) ReadFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0) //nolint:gosec // G304: Pfad aus der eigenen Liste des Exportordners, O_NOFOLLOW und fstat prüfen die Datei
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() //nolint:errcheck // nur gelesen, Close-Fehler ohne Folgen
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s ist keine reguläre Datei", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxDateiBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxDateiBytes {
		return nil, fmt.Errorf("%s ist größer als 20 MB", path)
	}
	return data, nil
}

// Lstat ruft os.Lstat.
func (OS) Lstat(path string) (fs.FileInfo, error) { return os.Lstat(path) }
