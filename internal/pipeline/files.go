package pipeline

import (
	"errors"
	"path/filepath"
	"time"

	"meetingsync/internal/watch"
)

// listEntries liest den Exportordner über internal/watch.
func (p *Pipeline) listEntries(now time.Time) ([]*watch.Entry, error) {
	return watch.Lister{
		FS:       p.fs,
		Dir:      p.o.WatchDir,
		Stable:   p.o.Stable,
		Opts:     p.o.Transcript,
		Cache:    p.cache,
		Log:      p.log,
		WarnOnce: p.warnOnce,
	}.List(now)
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

func (p *Pipeline) warnOnce(key, format string, args ...any) {
	if p.warned[key] {
		return
	}
	p.warned[key] = true
	p.log.Printf(format, args...)
}
