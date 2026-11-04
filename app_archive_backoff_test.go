package main

// R-M1-07: Archiv-Fehler warten 2 min (Retry-Zustand, nicht gegen
// max_attempts), Sync-Fehler des Zielordners warnt und verschiebt trotzdem,
// listEntries nimmt nur reguläre Dateien. Jeder Test läuft über 3 Durchläufe
// mit fester Uhr und zählt Dateien in Archiv, _fehler, Exportordner und Logzeilen.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// counts: Dateien in Archivmonat, _fehler und Exportordner.
func (e *crashEnv) counts(t *testing.T) (archived, failed, export int) {
	t.Helper()
	data, _ := e.inFailed(t)
	return len(remaining(t, filepath.Join(e.app.cfg.ArchiveDir, e.month))), len(data), len(remaining(t, e.dir))
}

// threePasses: Durchläufe bei now, +10 s, +20 s (innerhalb der 2 min).
func (e *crashEnv) threePasses(now time.Time) {
	for i := range 3 {
		e.pass(now.Add(time.Duration(i) * 10 * time.Second))
	}
}

// Symlink im Exportordner, Ziel außerhalb mit gültigem Transkript.
// Fängt: listEntries ohne Prüfung auf reguläre Datei (liest durch den Link,
// Notiz 1, Link im Archiv); Hinweis per Printf statt warnOnce (3-mal).
func TestListEntriesSkipsSymlink(t *testing.T) {
	e := newCrashEnv(t)
	for _, p := range []string{e.tr, e.sum} {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(t.TempDir(), "Ziel.txt")
	writeOld(t, target, srtLikeText(), 5*time.Minute)
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(e.dir, "Meeting 2026-10-28 14-00.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	e.threePasses(time.Now())

	if notes, tasks := e.pages(); notes != 0 || tasks != 0 {
		t.Fatalf("Notizen %d, Tasks %d; want 0", notes, tasks)
	}
	if n := strings.Count(e.log.String(), "Meeting 2026-10-28 14-00.txt: keine reguläre Datei (z. B. Symlink) → übersprungen"); n != 1 {
		t.Fatalf("Hinweis %d-mal, want 1\nLog:\n%s", n, e.log.String())
	}
	if a, f, x := e.counts(t); a != 0 || f != 0 || x != 1 {
		t.Fatalf("Archiv %d, _fehler %d, Export %d; want 0, 0, 1", a, f, x)
	}
	if got, err := os.Readlink(link); err != nil || got != target {
		t.Fatalf("Symlink = %q, %v", got, err)
	}
	after, err := os.Stat(target)
	if err != nil || mustRead(t, target) != srtLikeText() || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("Symlink-Ziel verändert: %v", err)
	}
}
