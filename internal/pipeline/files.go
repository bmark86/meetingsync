package pipeline

import (
	"errors"
	"path/filepath"
	"strings"
	"time"

	"meetingsync/internal/watch"
)

// listEntries liest den Exportordner über internal/watch. Ist er lesbar,
// vergisst prune danach die Pfade, die nicht mehr darin liegen.
func (p *Pipeline) listEntries(now time.Time) ([]*watch.Entry, error) {
	seen := map[string]bool{}
	entries, err := watch.Lister{
		FS:       p.fs,
		Dir:      p.o.WatchDir,
		Stable:   p.o.Stable,
		Opts:     p.o.Transcript,
		Cache:    p.cache,
		Log:      p.log,
		WarnOnce: p.warnOnce,
		Seen:     seen,
	}.List(now)
	if err != nil {
		return nil, err // Ordner nicht lesbar: nichts vergessen
	}
	p.prune(seen)
	return entries, nil
}

// globalWarnKeys sind die Warnschlüssel, die zu keiner Datei gehören; alle
// anderen Schlüssel sind Dateipfade (ggf. mit "#"-Zusatz). Unabhängig von der
// Form von watch_dir (auch "." ergibt Pfade ohne Ordneranteil).
var globalWarnKeys = map[string]bool{"ledger-refresh": true, "watch-lock": true}

// prune hält warned, cache und states klein (A-19): Einträge zu Pfaden im
// Exportordner, die seen nicht enthält, werden gelöscht; Warnungen dazu
// erscheinen wieder, wenn die Datei zurückkommt. Andere Schlüssel (z. B.
// "ledger-refresh") bleiben. Ein Zustand mit unklarem Anlegen (R-M3-17) oder
// gescheitertem Aufgeben (R-M3-18) bleibt: sein Marker bzw. das Ziel _fehler
// schützt vor einer zweiten Notiz, wenn die Datei zurückkommt.
func (p *Pipeline) prune(seen map[string]bool) {
	for k := range p.warned {
		if globalWarnKeys[k] || seen[k] {
			continue
		}
		if i := strings.LastIndexByte(k, '#'); i >= 0 && seen[k[:i]] {
			continue // z. B. path+"#read"
		}
		delete(p.warned, k)
	}
	for k := range p.cache {
		if !seen[k] {
			delete(p.cache, k)
		}
	}
	for k, st := range p.states {
		if !seen[k] && st.ambiguous == nil && st.giveUp == nil {
			delete(p.states, k)
		}
	}
}

// archive verschiebt die Dateien ins Archiv und liefert die, die liegen
// blieben. Erfolg löscht den Retry-Zustand der Datei.
func (p *Pipeline) archive(files ...*watch.Entry) (failed []*watch.Entry) {
	if p.o.Dry {
		return nil
	}
	for _, f := range files {
		dir := filepath.Join(p.o.ArchiveDir, f.ModTime.In(p.o.Loc).Format("2006-01"))
		if dst, err := p.move(f, dir); err != nil {
			p.log.Printf("   ⚠️  %s konnte nicht archiviert werden: %v", f.Name, err)
			failed = append(failed, f)
		} else {
			delete(p.cache, f.Path)
			delete(p.states, f.Path)
			p.log.Printf("   📦 %s → %s", f.Name, dst)
		}
	}
	return failed
}

// archiveAt archiviert; bleibt eine Datei liegen, neuer Versuch erst in 2 min
// (retryLater, R-M1-07).
func (p *Pipeline) archiveAt(now time.Time, files ...*watch.Entry) {
	for _, f := range p.archive(files...) {
		p.retryLater(f.Path, now)
	}
}

// move ruft Mover.Move; scheitert nur der Sync des Zielordners, ist die Datei
// verschoben: Warnung, kein Fehler (Mark, 2026-11-02).
func (p *Pipeline) move(f *watch.Entry, dir string) (string, error) {
	dst, err := p.mover.Move(f.Path, dir)
	var se *watch.DirSyncError
	if errors.As(err, &se) {
		p.log.Printf("   ⚠️  %s: %v", f.Name, err)
		return dst, nil
	}
	return dst, err
}

// archiveJob archiviert die Dateien des Jobs. TOCTOU (S-15): nur die primäre
// Datei wird neu geprüft, denn der Ledger-Schlüssel ist ihr Hash. Geändert →
// bleibt liegen, der nächste Durchlauf sieht den neuen Hash. Partner gehen wie
// bisher ins Archiv (liegen gelassen würden sie zur Waise, ggf. zweite Notiz).
func (p *Pipeline) archiveJob(j job, now time.Time) {
	files := j.files()
	if !p.o.Dry && p.changedSinceRead(j.primary) {
		p.log.Printf("   ⚠️  %s wurde seit dem Einlesen geändert → nicht archiviert, bleibt im Exportordner (nächster Durchlauf prüft sie erneut)", j.primary.Name)
		files = files[1:]
	}
	p.archiveAt(now, files...)
}

// changedSinceRead meldet, ob Größe oder mtime von f nicht mehr zu den Werten
// aus listEntries passen (FS.Lstat wie d.Info). Fehlt die Datei, entscheidet
// Mover.Move wie bisher.
func (p *Pipeline) changedSinceRead(f *watch.Entry) bool {
	info, err := p.fs.Lstat(f.Path)
	if err != nil {
		return false
	}
	return info.Size() != f.Size || !info.ModTime().Equal(f.ModTime)
}

// loadContent liest den Inhalt von f nach, wenn List ihn nicht gelesen hat
// (Typ aus dem Cache, R-M5-01). ok=false: nicht lesbar oder seit der
// Ordnerliste geändert; die Datei bleibt liegen, Zustand unverändert, der nächste
// Durchlauf prüft sie neu (geändert: neue mtime, also erst wieder stabil).
func (p *Pipeline) loadContent(f *watch.Entry) (ok bool) {
	if f.Content != nil {
		return true
	}
	b, err := p.fs.ReadFile(f.Path)
	if err != nil {
		p.warnOnce(f.Path+"#read", "⚠️  %s nicht lesbar: %v", f.Name, err)
		return false
	}
	if int64(len(b)) != f.Size || p.changedSinceRead(f) {
		return false
	}
	f.Content = b
	return true
}

// fail verschiebt die Dateien nach _fehler; ok=false, wenn eine liegen blieb.
func (p *Pipeline) fail(cause error, files ...*watch.Entry) (ok bool) {
	ok = true
	if p.o.Dry {
		return ok
	}
	for _, f := range files {
		dst, err := p.move(f, p.o.FailedDir)
		if err != nil {
			p.log.Printf("   ⚠️  %s konnte nicht verschoben werden: %v", f.Name, err)
			ok = false
			continue
		}
		delete(p.cache, f.Path)
		if err := watch.WriteFehlerTxt(dst, p.now(), cause); err != nil {
			p.log.Printf("   ⚠️  Fehlerhinweis zu %s nicht geschrieben: %v", f.Name, err)
		}
	}
	return ok
}

// failJob verschiebt die Dateien des Jobs nach _fehler: Partner zuerst, das
// Transkript zuletzt, Stopp beim ersten Fehler. Bleibt etwas liegen, dann
// immer die primäre Datei mit ihrem Zustand, nie eine allein liegende
// Zusammenfassung, die als Waise eine zweite Seite anlegen würde.
func (p *Pipeline) failJob(j job, cause error) (ok bool) {
	files := j.files()
	for i := len(files) - 1; i >= 0; i-- {
		if !p.fail(cause, files[i]) {
			return false
		}
	}
	return true
}

func (p *Pipeline) warnOnce(key, format string, args ...any) {
	if p.warned[key] {
		return
	}
	p.warned[key] = true
	p.log.Printf(format, args...)
}
