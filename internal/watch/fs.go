package watch

import (
	"io/fs"
	"os"
)

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

// ReadFile ruft os.ReadFile.
func (OS) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

// Lstat ruft os.Lstat.
func (OS) Lstat(path string) (fs.FileInfo, error) { return os.Lstat(path) }
