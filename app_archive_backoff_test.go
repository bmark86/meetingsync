package main

// R-M1-07: Archiv-Fehler warten 2 min (Retry-Zustand, nicht gegen
// max_attempts), Sync-Fehler des Zielordners warnt und verschiebt trotzdem,
// listEntries nimmt nur reguläre Dateien. Jeder Test läuft über 3 Durchläufe
// mit fester Uhr und zählt Dateien in Archiv, _fehler, Exportordner und Logzeilen.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const archiveWarn = "konnte nicht archiviert werden"

// unlockFile hebt chflags uchg vor dem Test-Ende auf.
func unlockFile(t *testing.T, path string) {
	t.Helper()
	if err := exec.Command("chflags", "nouchg", path).Run(); err != nil { //nolint:gosec // G204: feste Flags, Pfad unter t.TempDir()
		t.Fatal(err)
	}
}

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

// Remove(src) scheitert beim Archivieren (Kopierpfad, Quelle mit uchg).
// Fall "verarbeitet": das Transkript bleibt liegen; Wiederholung über ledger.Has.
// Fall "HasNear": die Zusammenfassung bleibt liegen und ist ab Durchlauf 2 eine
// Waise mit HasNear-Treffer.
// Fängt: kein Backoff nach Archiv-Fehler (Warnung 3-mal); delete(a.state)
// nach statt vor archiveJob im Erfolgsfall (Backoff gelöscht); Gate im
// Waisen-Zweig erst nach HasNear (📎 und Warnung je Durchlauf); Erfolg löscht
// den Zustand nicht; Backoff zählt gegen max_attempts.
func TestArchiveRemoveFailsBackoff(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lock  func(e *crashEnv) string
		retry string // Logzeile des erneuten Versuchs
	}{
		{"verarbeitet", func(e *crashEnv) string { return e.tr }, "wurde schon verarbeitet"},
		{"HasNear", func(e *crashEnv) string { return e.sum }, "gehört zu einem bereits verarbeiteten Transkript"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newCrashEnv(t)
			e.app.cfg.MaxAttempts = 1
			e.app.mover.Rename = noRename
			locked := tc.lock(e)
			lockFile(t, locked)
			now := time.Now()

			e.threePasses(now)
			if n := strings.Count(e.log.String(), archiveWarn); n != 1 {
				t.Fatalf("Warnung %d-mal, want 1\nLog:\n%s", n, e.log.String())
			}
			if n := strings.Count(e.log.String(), tc.retry); n != 0 {
				t.Fatalf("erneuter Versuch vor Ablauf der 2 min (%d-mal)", n)
			}
			if a, f, x := e.counts(t); a != 1 || f != 0 || x != 1 || !exists(locked) {
				t.Fatalf("Archiv %d, _fehler %d, Export %d; want 1, 0, 1 (gesperrte Datei bleibt)", a, f, x)
			}
			if notes, _ := e.pages(); notes != 1 {
				t.Fatalf("Notizen = %d, want 1", notes)
			}
			if st := e.app.states()[locked]; st == nil || !st.NextTry.Equal(now.Add(2*time.Minute)) || st.Attempts != 0 {
				t.Fatalf("state = %+v, want nextTry now+2min, attempts 0", st)
			}

			e.pass(now.Add(2 * time.Minute)) // genau ab nextTry: neuer Versuch, scheitert wieder
			if n := strings.Count(e.log.String(), archiveWarn); n != 2 {
				t.Fatalf("nach Backoff Warnung %d-mal, want 2", n)
			}
			if n := strings.Count(e.log.String(), tc.retry); n != 1 {
				t.Fatalf("erneuter Versuch %d-mal, want 1", n)
			}
			if _, f, _ := e.counts(t); f != 0 {
				t.Fatal("Archiv-Fehler darf nicht nach _fehler führen (max_attempts)")
			}

			unlockFile(t, locked)
			e.pass(now.Add(4 * time.Minute))
			if a, f, x := e.counts(t); a != 2 || f != 0 || x != 0 {
				t.Fatalf("nach Entsperren: Archiv %d, _fehler %d, Export %d; want 2, 0, 0", a, f, x)
			}
			if st := e.app.states()[locked]; st != nil {
				t.Fatalf("state nach Erfolg: %+v", st)
			}
			if notes, _ := e.pages(); notes != 1 {
				t.Fatalf("Notizen = %d, want 1", notes)
			}
		})
	}
}

// orphan_summary_action=archive, Remove(src) scheitert.
// Fängt: kein Backoff im Waisen-Archivzweig (📎 und Warnung je Durchlauf).
func TestArchiveOrphanRemoveFailsBackoff(t *testing.T) {
	env, p := orphanEnv(t, "archive")
	env.app.mover.Rename = noRename
	lockFile(t, p)
	now := flowM.Add(time.Hour)
	for i := range 3 {
		env.app.Pass(context.Background(), now.Add(time.Duration(i)*10*time.Second))
	}
	if n := strings.Count(env.logBuf.String(), archiveWarn); n != 1 {
		t.Fatalf("Warnung %d-mal, want 1\nLog:\n%s", n, env.logBuf.String())
	}
	if n := strings.Count(env.logBuf.String(), "orphan_summary_action=archive"); n != 1 {
		t.Fatalf("📎 %d-mal, want 1", n)
	}
	if !exists(p) || len(remaining(t, filepath.Dir(env.archived("x")))) != 0 {
		t.Fatal("Datei bleibt im Exportordner, Archiv leer")
	}
	unlockFile(t, p)
	env.app.Pass(context.Background(), now.Add(2*time.Minute))
	if exists(p) || !exists(env.archived("Notizen.md")) || env.app.states()[p] != nil {
		t.Fatalf("nach Backoff archiviert und state gelöscht: exists=%v state=%+v", exists(p), env.app.states()[p])
	}
}

// Sync des Zielordners scheitert nach der Kopie (Entscheidung Mark, 2026-11-02).
// Fängt: Sync-Fehler bricht ab (Original und Kopie bleiben liegen, Export 1,
// Archiv 1); Original ohne Identitätsprüfung oder gar nicht gelöscht; Warnung
// fehlt oder wiederholt sich; Sync-Fehler als Archiv-Fehler gewertet.
func TestArchiveDirSyncFailsWarnsAndMoves(t *testing.T) {
	e := newCrashEnv(t)
	if err := os.Remove(e.sum); err != nil {
		t.Fatal(err)
	}
	e.app.mover.Rename = noRename
	calls := 0
	e.app.mover.SyncDir = func(string) error { calls++; return errors.New("EIO") }
	e.threePasses(time.Now())

	if a, f, x := e.counts(t); a != 1 || f != 0 || x != 0 {
		t.Fatalf("Archiv %d, _fehler %d, Export %d; want 1, 0, 0", a, f, x)
	}
	if calls != 1 {
		t.Fatalf("SyncDir %d-mal, want 1", calls)
	}
	if got := mustRead(t, filepath.Join(e.app.cfg.ArchiveDir, e.month, filepath.Base(e.tr))); got != srtLikeText() {
		t.Fatal("Kopie im Archiv hat nicht den Inhalt des Originals")
	}
	if n := strings.Count(e.log.String(), "Zielordner nicht synchronisiert"); n != 1 {
		t.Fatalf("Warnung %d-mal, want 1\nLog:\n%s", n, e.log.String())
	}
	if strings.Contains(e.log.String(), archiveWarn) || !strings.Contains(e.log.String(), "📦") {
		t.Fatalf("Sync-Fehler gilt als Archiv-Fehler:\n%s", e.log.String())
	}
	if notes, _ := e.pages(); notes != 1 {
		t.Fatalf("Notizen = %d, want 1", notes)
	}
	if st := e.app.states()[e.tr]; st != nil {
		t.Fatalf("state = %+v, want nil", st)
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
