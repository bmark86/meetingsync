package watch

import (
	"io/fs"
	"path/filepath"
	"sort"
	"time"
)

// memFS ist ein Dateisystem im Speicher (nur Leseseite) für Unit-Tests von
// List. Schlüssel ist der volle Pfad; Unterordner sind Einträge mit ModeDir.
type memFS struct {
	files   map[string]*memFile
	dirErr  error            // ReadDir scheitert
	readErr map[string]error // ReadFile scheitert je Pfad
	infoErr map[string]error // DirEntry.Info scheitert je Pfad
	reads   []string         // gelesene Pfade (ReadFile)
}

type memFile struct {
	data    []byte
	mode    fs.FileMode
	modTime time.Time
	size    int64 // < 0: len(data)
}

func newMemFS() *memFS {
	return &memFS{files: map[string]*memFile{}, readErr: map[string]error{}, infoErr: map[string]error{}}
}

// add legt eine reguläre Datei an.
func (m *memFS) add(path, content string, mtime time.Time) *memFile {
	f := &memFile{data: []byte(content), modTime: mtime, size: -1}
	m.files[path] = f
	return f
}

func (m *memFS) ReadDir(dir string) ([]fs.DirEntry, error) {
	if m.dirErr != nil {
		return nil, m.dirErr
	}
	var out []fs.DirEntry
	for p, f := range m.files {
		if filepath.Dir(p) == dir {
			out = append(out, memEntry{fs: m, path: p, f: f})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}

func (m *memFS) ReadFile(path string) ([]byte, error) {
	m.reads = append(m.reads, path)
	if err := m.readErr[path]; err != nil {
		return nil, err
	}
	f, ok := m.files[path]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
	}
	return append([]byte(nil), f.data...), nil
}

func (m *memFS) Lstat(path string) (fs.FileInfo, error) {
	f, ok := m.files[path]
	if !ok {
		return nil, &fs.PathError{Op: "lstat", Path: path, Err: fs.ErrNotExist}
	}
	return memInfo{name: filepath.Base(path), f: f}, nil
}

type memEntry struct {
	fs   *memFS
	path string
	f    *memFile
}

func (e memEntry) Name() string      { return filepath.Base(e.path) }
func (e memEntry) IsDir() bool       { return e.f.mode.IsDir() }
func (e memEntry) Type() fs.FileMode { return e.f.mode.Type() }
func (e memEntry) Info() (fs.FileInfo, error) {
	if err := e.fs.infoErr[e.path]; err != nil {
		return nil, err
	}
	return e.fs.Lstat(e.path)
}

type memInfo struct {
	name string
	f    *memFile
}

func (i memInfo) Name() string { return i.name }
func (i memInfo) Size() int64 {
	if i.f.size >= 0 {
		return i.f.size
	}
	return int64(len(i.f.data))
}
func (i memInfo) Mode() fs.FileMode  { return i.f.mode }
func (i memInfo) ModTime() time.Time { return i.f.modTime }
func (i memInfo) IsDir() bool        { return i.f.mode.IsDir() }
func (i memInfo) Sys() any           { return nil }

var _ FS = (*memFS)(nil)
