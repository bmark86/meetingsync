// Package watch liest den Exportordner (Stabilität, Dateityp, Paarbildung)
// und verschiebt Dateien ohne Überschreiben (Mover).
package watch

import (
	"log"
	"path/filepath"
	"strings"
	"time"

	"meetingsync/internal/domain"
	"meetingsync/internal/transcript"
)

// Entry ist eine Datei im Exportordner. Kind ist nur bei stabilen Dateien
// gesetzt, Content nur, wenn List die Datei gelesen hat (Typ nicht im Cache);
// sonst ist Content nil und der Aufrufer liest bei der Verarbeitung nach.
type Entry struct {
	Path    string
	Name    string
	ModTime time.Time
	Size    int64
	Stable  bool
	Kind    domain.Kind
	Content []byte
}

// Cache merkt sich den erkannten Dateityp je Pfad (gültig, solange mtime und
// Größe gleich bleiben). Der Aufrufer löscht Pfade, die verschoben wurden.
type Cache map[string]cachedKind

type cachedKind struct {
	modTime time.Time
	size    int64
	kind    domain.Kind
}

// Lister liest den Exportordner. Log bekommt die Zeile "Neu: …", WarnOnce die
// Hinweise je Pfad (einmal je Prozess, entscheidet der Aufrufer).
type Lister struct {
	FS       FS
	Dir      string
	Stable   time.Duration // stable_seconds
	Opts     transcript.Options
	Cache    Cache
	Log      *log.Logger
	WarnOnce func(key, format string, args ...any)
	// Seen (optional) bekommt den Pfad jeder Datei im Ordner, auch der
	// übersprungenen (keine Unterordner); der Aufrufer vergisst damit Pfade,
	// die nicht mehr da sind (A-19). Bei einem Fehler von ReadDir bleibt es leer.
	Seen map[string]bool
}

// List liefert die unterstützten regulären Dateien im Ordner. Stabil ist eine
// Datei, die seit Stable unverändert und nicht leer ist; nur dann wird ihr
// Typ erkannt. Gelesen wird sie nur, wenn der Typ nicht im Cache steht.
func (l Lister) List(now time.Time) ([]*Entry, error) {
	dirEntries, err := l.FS.ReadDir(l.Dir)
	if err != nil {
		return nil, err
	}
	var out []*Entry
	for _, d := range dirEntries {
		name := d.Name()
		if l.Seen != nil && !d.IsDir() {
			l.Seen[filepath.Join(l.Dir, name)] = true
		}
		if d.IsDir() || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "~$") || isTempName(name) {
			continue
		}
		path := filepath.Join(l.Dir, name)
		if !d.Type().IsRegular() {
			l.WarnOnce(path, "%s: keine reguläre Datei (z. B. Symlink) → übersprungen", name)
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		if !transcript.IsSupportedExt(ext, l.Opts) {
			l.WarnOnce(path, "Ignoriere %s (Dateityp %s wird nicht verarbeitet)", name, ext)
			continue
		}
		info, err := d.Info()
		if err != nil {
			continue
		}
		e := &Entry{Path: path, Name: name, ModTime: info.ModTime(), Size: info.Size()}
		e.Stable = now.Sub(e.ModTime) >= l.Stable && e.Size > 0
		if e.Stable {
			if e.Size > maxDateiBytes {
				l.WarnOnce(path, "Ignoriere %s (größer als 20 MB)", name)
				continue
			}
			if c, ok := l.Cache[path]; ok && c.modTime.Equal(e.ModTime) && c.size == e.Size {
				e.Kind = c.kind // Inhalt liest der Aufrufer erst bei der Verarbeitung (R-M5-01)
			} else {
				if e.Content, err = l.FS.ReadFile(path); err != nil {
					continue
				}
				e.Kind = transcript.Classify(path, e.Content, l.Opts)
				l.Cache[path] = cachedKind{e.ModTime, e.Size, e.Kind}
				l.Log.Printf("Neu: %s → %s", name, e.Kind)
			}
		}
		out = append(out, e)
	}
	return out, nil
}

func isTempName(name string) bool {
	l := strings.ToLower(name)
	for _, s := range []string{".tmp", ".part", ".crdownload", ".download", ".swp"} {
		if strings.HasSuffix(l, s) {
			return true
		}
	}
	return false
}

// PairSummary sucht die Zusammenfassung zum Transkript: gleicher Dateiname
// (ohne Endung) oder zeitlich am nächsten innerhalb des Fensters.
func PairSummary(t *Entry, summaries []*Entry, claimed map[string]bool, window time.Duration) *Entry {
	stem := func(e *Entry) string { return strings.ToLower(strings.TrimSuffix(e.Name, filepath.Ext(e.Name))) }
	for _, s := range summaries {
		if !claimed[s.Path] && stem(s) == stem(t) {
			return s
		}
	}
	var best *Entry
	var bestD time.Duration
	for _, s := range summaries {
		if claimed[s.Path] {
			continue
		}
		d := absDur(s.ModTime.Sub(t.ModTime))
		if d <= window && (best == nil || d < bestD) {
			best, bestD = s, d
		}
	}
	return best
}

// Near meldet, ob eine Datei aus list höchstens window von t entfernt ist.
func Near(list []*Entry, t time.Time, window time.Duration) bool {
	for _, e := range list {
		if absDur(e.ModTime.Sub(t)) <= window {
			return true
		}
	}
	return false
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
