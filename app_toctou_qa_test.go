package main

// QA-Ergänzungen zu R-M1-05 (TOCTOU-Recheck): Folgedurchläufe für Paar und
// reine Zusammenfassung sowie Dry-Run.

import (
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Stabiles Fragment der Warnung, nicht der volle Wortlaut.
const toctouFragment = "seit dem Einlesen geändert"

func (e *toctouEnv) resetLog() {
	e.log = &hookLog{}
	e.app.log = log.New(e.log, "", 0)
}

// Paar, Transkript während des LLM-Laufs geändert: der zweite Durchlauf
// verarbeitet die neue Fassung genau einmal (2 Notizen), der Exportordner ist
// danach leer, keine Neustart-Regel, nichts in _fehler. Fängt: Schleife über
// Durchläufe, Waise durch liegengelassenen Partner, "unterbrochen" für die
// neue Fassung.
func TestQATOCTOUPairTranscriptChangedNextPass(t *testing.T) {
	e := newTOCTOUEnv(t)
	tr, _ := e.writePair(t)
	e.onLLM(func() { rewrite(t, tr, srtLikeText()+"Nachtrag.\n", mtime(t, tr).Add(time.Minute)) })

	e.app.Pass(t.Context(), time.Now())
	if n := e.notes(); n != 1 {
		t.Fatalf("Notizen nach Durchlauf 1 = %d, will 1", n)
	}
	e.resetLog()
	e.app.Pass(t.Context(), time.Now())
	if n := e.notes(); n != 2 {
		t.Fatalf("Notizen nach Durchlauf 2 = %d, will 2", n)
	}
	if r := remaining(t, e.dir); len(r) != 0 {
		t.Fatalf("Exportordner nicht leer: %v", r)
	}
	if strings.Contains(e.log.String(), "unterbrochen") || strings.Contains(e.log.String(), toctouFragment) {
		t.Fatalf("Neustart-Regel oder Warnung im 2. Durchlauf:\n%s", e.log)
	}
	if f := remaining(t, e.app.cfg.FailedDir); len(f) != 0 {
		t.Fatalf("Dateien in _fehler: %v", f)
	}
	e.app.Pass(t.Context(), time.Now().Add(time.Hour))
	if n := e.notes(); n != 2 {
		t.Fatalf("Notizen nach Durchlauf 3 = %d, will 2", n)
	}
}

// Reine Zusammenfassung (primär, orphan_summary_action=note) während des
// LLM-Laufs geändert. newOffset ist die Verschiebung der neuen mtime gegenüber
// der alten.
func TestQATOCTOUSummaryOnlyChanged(t *testing.T) {
	cases := []struct {
		name      string
		inWindow  bool
		wantNotes int
	}{
		{"neue mtime im Paarfenster: Archiv, keine zweite Notiz", true, 1},
		{"neue mtime außerhalb: genau eine neue Notiz", false, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newTOCTOUEnv(t)
			if e.app.cfg.OrphanSummaryAction != "note" {
				t.Fatalf("orphan_summary_action = %q, will note", e.app.cfg.OrphanSummaryAction)
			}
			window := time.Duration(e.app.cfg.PairWindowSeconds) * time.Second
			sum := filepath.Join(e.dir, "Meeting 2026-10-28 14-00 (1).txt")
			writeOld(t, sum, toctouSummary, time.Hour)
			oldMod := mtime(t, sum)
			shift := window / 2
			if !c.inWindow {
				shift = window + time.Minute
			}
			e.onLLM(func() { rewrite(t, sum, toctouSummary+"Nachtrag.\n", oldMod.Add(shift)) })

			e.app.Pass(t.Context(), time.Now())
			if n := e.notes(); n != 1 {
				t.Fatalf("Notizen nach Durchlauf 1 = %d, will 1", n)
			}
			if !strings.Contains(e.log.String(), "(1).txt wurde "+toctouFragment) {
				t.Fatalf("Warnung fehlt:\n%s", e.log)
			}
			if r := remaining(t, e.dir); len(r) != 1 {
				t.Fatalf("geänderte Zusammenfassung nicht liegen geblieben: %v", r)
			}

			e.resetLog()
			e.app.Pass(t.Context(), time.Now())
			if n := e.notes(); n != c.wantNotes {
				t.Fatalf("Notizen nach Durchlauf 2 = %d, will %d\n%s", n, c.wantNotes, e.log)
			}
			if r := remaining(t, e.dir); len(r) != 0 {
				t.Fatalf("Exportordner nicht leer: %v", r)
			}
			for i := 0; i < 2; i++ {
				e.app.Pass(t.Context(), time.Now().Add(time.Duration(i+1)*time.Hour))
			}
			if n := e.notes(); n != c.wantNotes {
				t.Fatalf("Notizen nach Durchlauf 4 = %d, will %d", n, c.wantNotes)
			}
			if strings.Contains(e.log.String(), "unterbrochen") || strings.Contains(e.log.String(), toctouFragment) {
				t.Fatalf("Neustart-Regel oder Warnung:\n%s", e.log)
			}
			if f := remaining(t, e.app.cfg.FailedDir); len(f) != 0 {
				t.Fatalf("Dateien in _fehler: %v", f)
			}
		})
	}
}

// Dry-Run: keine TOCTOU-Warnung, nichts wird verschoben, auch wenn die primäre
// Datei sich ändert. Fängt: Recheck ohne a.dry-Prüfung.
func TestQATOCTOUDryRunNoWarning(t *testing.T) {
	e := newTOCTOUEnv(t)
	e.app.dry = true
	tr, sum := e.writePair(t)
	e.onLLM(func() { rewrite(t, tr, srtLikeText()+"Nachtrag.\n", mtime(t, tr).Add(time.Minute)) })

	e.app.Pass(t.Context(), time.Now())

	if strings.Contains(e.log.String(), toctouFragment) {
		t.Fatalf("Warnung im Dry-Run:\n%s", e.log)
	}
	r := remaining(t, e.dir)
	if len(r) != 2 {
		t.Fatalf("Dry-Run hat Dateien bewegt: %v (%s, %s)", r, filepath.Base(tr), filepath.Base(sum))
	}
	if a := e.archived(t); len(a) != 0 {
		t.Fatalf("Dry-Run hat archiviert: %v", a)
	}
}
