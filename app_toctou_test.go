package main

// TOCTOU-Recheck (R-M1-05, S-15): archiviert wird nur die eingelesene Fassung.
// Der LM-Studio-Fake ändert die Datei während des LLM-Laufs.

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"meetingsync/internal/ledger"
)

const toctouWarn = "seit dem Einlesen geändert → nicht archiviert"

type toctouEnv struct {
	f     *fakeServer
	app   *App
	dir   string
	log   *hookLog
	month string
}

func newTOCTOUEnv(t *testing.T) *toctouEnv {
	t.Helper()
	f := &fakeServer{t: t, appends: map[string]int{}}
	app, dir := setup(t, f)
	e := &toctouEnv{f: f, app: app, dir: dir, log: &hookLog{}}
	app.log = log.New(e.log, "", 0)
	return e
}

// onLLM ruft fn einmal beim ersten LLM-Aufruf, bevor der Fake antwortet.
func (e *toctouEnv) onLLM(fn func()) {
	done := false
	e.app.llm = newLLMClient(e.app.cfg, e.app.log, &hookRT{before: func(r *http.Request) (*http.Response, error) {
		if !done && r.URL.Path == "/v1/chat/completions" {
			done = true
			fn()
		}
		return nil, nil
	}})
}

func (e *toctouEnv) notes() int {
	e.f.mu.Lock()
	defer e.f.mu.Unlock()
	n := 0
	for _, p := range e.f.pages {
		if p["parent"].(map[string]any)["data_source_id"] == "notes-ds" {
			n++
		}
	}
	return n
}

func (e *toctouEnv) status(t *testing.T, hash string) string {
	t.Helper()
	return diskStatus(t, ledgerPath(e.app.cfg), hash)
}

func (e *toctouEnv) archived(t *testing.T) []string {
	return remaining(t, filepath.Join(e.app.cfg.ArchiveDir, e.month))
}

func mtime(t *testing.T, path string) time.Time {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.ModTime()
}

// rewrite schreibt content nach path und setzt die mtime auf ts.
func rewrite(t *testing.T, path, content string, ts time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil { //nolint:gosec // G703: Pfad unter t.TempDir()
		t.Error(err)
		return
	}
	if err := os.Chtimes(path, ts, ts); err != nil {
		t.Error(err)
	}
}

// Datei während des LLM-Laufs geändert → bleibt liegen, Warnung, fertig für den
// alten Hash; der nächste Durchlauf legt genau eine Seite für die neue Fassung an.
// Fängt: kein Recheck (Datei im Archiv); nur Größe verglichen (Fall gleiche
// Größe); nur mtime verglichen (Fall gleiche mtime); fertig nur bei unveränderter
// Datei (Status alt != fertig, Neustart-Regel schickt sie später nach _fehler).
func TestTOCTOUChangedDuringLLM(t *testing.T) {
	old := srtLikeText()
	cases := []struct {
		name    string
		content string // neue Fassung
		sameMod bool   // mtime wieder auf den alten Wert
	}{
		{"andere Größe und mtime", old + "[00:48:00] Speaker 2: Nachtrag.\n", false},
		{"gleiche Größe, andere mtime", strings.Replace(old, "Danke Sascha", "Danke Saschi", 1), false},
		{"andere Größe, gleiche mtime", old + "[00:48:00] Speaker 2: Nachtrag.\n", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newTOCTOUEnv(t)
			tr := filepath.Join(e.dir, "Meeting 2026-10-28 14-00.txt")
			writeOld(t, tr, old, 5*time.Minute)
			oldMod := mtime(t, tr)
			e.month = oldMod.In(e.app.cfg.loc).Format("2006-01")
			e.onLLM(func() {
				ts := oldMod.Add(time.Minute)
				if c.sameMod {
					ts = oldMod
				}
				rewrite(t, tr, c.content, ts)
			})

			e.app.Pass(t.Context(), time.Now())

			got, err := os.ReadFile(tr) //nolint:gosec // G304: Pfad unter t.TempDir()
			if err != nil {
				t.Fatalf("Datei nicht mehr im Exportordner: %v", err)
			}
			if string(got) != c.content {
				t.Fatal("Exportordner enthält nicht die neue Fassung")
			}
			if a := e.archived(t); len(a) != 0 {
				t.Fatalf("geänderte Datei archiviert: %v", a)
			}
			if !strings.Contains(e.log.String(), "Meeting 2026-10-28 14-00.txt wurde "+toctouWarn) {
				t.Fatalf("Warnung fehlt:\n%s", e.log)
			}
			if s := e.status(t, sha256Hex([]byte(old))); s != ledger.StatusFertig {
				t.Fatalf("Status alter Hash = %q, will fertig", s)
			}
			if n := e.notes(); n != 1 {
				t.Fatalf("Notizen nach Durchlauf 1 = %d, will 1", n)
			}

			// Nächster Durchlauf: neue Fassung = neuer Hash, normal verarbeitet.
			e.log = &hookLog{}
			e.app.log = log.New(e.log, "", 0)
			e.app.Pass(t.Context(), time.Now())
			if n := e.notes(); n != 2 {
				t.Fatalf("Notizen nach Durchlauf 2 = %d, will 2", n)
			}
			if s := e.status(t, sha256Hex([]byte(c.content))); s != ledger.StatusFertig {
				t.Fatalf("Status neuer Hash = %q, will fertig", s)
			}
			if strings.Contains(e.log.String(), "unterbrochen") || strings.Contains(e.log.String(), toctouWarn) {
				t.Fatalf("Neustart-Regel oder Warnung im 2. Durchlauf:\n%s", e.log)
			}
			if f := remaining(t, e.app.cfg.FailedDir); len(f) != 0 {
				t.Fatalf("Dateien in _fehler: %v", f)
			}
			if a := e.archived(t); len(a) != 1 || a[0] != filepath.Base(tr) {
				t.Fatalf("Archiv = %v, will die neue Fassung", a)
			}
			if r := remaining(t, e.dir); len(r) != 0 {
				t.Fatalf("Exportordner nicht leer: %v", r)
			}

			// Kein dritter Lauf legt etwas an.
			e.app.Pass(t.Context(), time.Now())
			if n := e.notes(); n != 2 {
				t.Fatalf("Notizen nach Durchlauf 3 = %d, will 2", n)
			}
		})
	}
}

const toctouSummary = "Zusammenfassung\n\nEs ging um die Pipeline. Cache kommt.\n"

// writePair legt Transkript und Zusammenfassung (5 min alt) an.
func (e *toctouEnv) writePair(t *testing.T) (tr, sum string) {
	t.Helper()
	tr = filepath.Join(e.dir, "Meeting 2026-10-28 14-00.txt")
	sum = filepath.Join(e.dir, "Meeting 2026-10-28 14-00 (1).txt")
	writeOld(t, tr, srtLikeText(), 5*time.Minute)
	writeOld(t, sum, toctouSummary, 5*time.Minute)
	e.month = mtime(t, tr).In(e.app.cfg.loc).Format("2006-01")
	return tr, sum
}

// Paar, Transkript (primär) geändert → bleibt liegen, die Zusammenfassung geht
// wie bisher ins Archiv. Fängt: kein Recheck der primären Datei; bei geänderter
// primärer Datei auch den Partner behalten (files[1:] → nichts archivieren).
func TestTOCTOUPairTranscriptChanged(t *testing.T) {
	e := newTOCTOUEnv(t)
	tr, sum := e.writePair(t)
	e.onLLM(func() { rewrite(t, tr, srtLikeText()+"Nachtrag.\n", mtime(t, tr).Add(time.Minute)) })

	e.app.Pass(t.Context(), time.Now())

	if a := e.archived(t); len(a) != 1 || a[0] != filepath.Base(sum) {
		t.Fatalf("Archiv = %v, will nur %s", a, filepath.Base(sum))
	}
	if r := remaining(t, e.dir); len(r) != 1 || r[0] != filepath.Base(tr) {
		t.Fatalf("Exportordner = %v, will nur %s", r, filepath.Base(tr))
	}
	logs := e.log.String()
	if !strings.Contains(logs, filepath.Base(tr)+" wurde "+toctouWarn) || strings.Count(logs, toctouWarn) != 1 {
		t.Fatalf("genau eine Warnung für %s erwartet:\n%s", filepath.Base(tr), logs)
	}
	if s := e.status(t, sha256Hex([]byte(srtLikeText()))); s != ledger.StatusFertig {
		t.Fatalf("Status = %q, will fertig", s)
	}
}

// Paar, Zusammenfassung (Partner) während des LLM-Laufs angefasst, neue mtime
// außerhalb pair_window_seconds → wird wie bisher archiviert. Der zweite
// Durchlauf nach orphan_summary_minutes legt keine zweite Notiz an
// (orphan_summary_action=note). Fängt: Recheck auch für Partner (in archive
// oder je Datei) → Waise → zweite Notiz.
func TestTOCTOUPairSummaryChangedArchived(t *testing.T) {
	for _, c := range []struct{ name, content string }{
		{"gleicher Inhalt", toctouSummary},
		{"anderer Inhalt", toctouSummary + "Nachtrag.\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newTOCTOUEnv(t)
			if e.app.cfg.OrphanSummaryAction != "note" {
				t.Fatalf("orphan_summary_action = %q, will note", e.app.cfg.OrphanSummaryAction)
			}
			tr, sum := e.writePair(t)
			window := time.Duration(e.app.cfg.PairWindowSeconds) * time.Second
			e.onLLM(func() { rewrite(t, sum, c.content, mtime(t, tr).Add(window+time.Minute)) })

			now := time.Now()
			e.app.Pass(t.Context(), now)

			if a := e.archived(t); len(a) != 2 {
				t.Fatalf("Archiv = %v, will Transkript und Zusammenfassung", a)
			}
			if r := remaining(t, e.dir); len(r) != 0 {
				t.Fatalf("Exportordner nicht leer: %v", r)
			}
			if strings.Contains(e.log.String(), toctouWarn) {
				t.Fatalf("Warnung für den Partner:\n%s", e.log)
			}

			later := now.Add(time.Duration(e.app.cfg.OrphanSummaryMinutes+1) * time.Minute)
			e.app.Pass(t.Context(), later)
			if n := e.notes(); n != 1 {
				t.Fatalf("Notizen insgesamt = %d, will 1", n)
			}
		})
	}
}

// Primäre Datei vor dem Recheck entfernt (Lstat schlägt fehl): kein Panic,
// gilt als unverändert, keine TOCTOU-Warnung; moveFile meldet wie bisher
// "konnte nicht archiviert werden", der Partner geht ins Archiv, fertig steht.
// Fängt: Lstat-Fehler als "geändert" werten (Warnung statt moveFile-Fehler).
func TestTOCTOUPrimaryRemoved(t *testing.T) {
	e := newTOCTOUEnv(t)
	tr, sum := e.writePair(t)
	e.onLLM(func() {
		if err := os.Remove(tr); err != nil {
			t.Error(err)
		}
	})

	e.app.Pass(t.Context(), time.Now())

	logs := e.log.String()
	if strings.Contains(logs, toctouWarn) {
		t.Fatalf("TOCTOU-Warnung bei fehlender Datei:\n%s", logs)
	}
	if !strings.Contains(logs, filepath.Base(tr)+" konnte nicht archiviert werden") {
		t.Fatalf("moveFile-Fehler fehlt:\n%s", logs)
	}
	if a := e.archived(t); len(a) != 1 || a[0] != filepath.Base(sum) {
		t.Fatalf("Archiv = %v, will nur %s", a, filepath.Base(sum))
	}
	if s := e.status(t, sha256Hex([]byte(srtLikeText()))); s != ledger.StatusFertig {
		t.Fatalf("Status = %q, will fertig", s)
	}
}

// Weg "schon verarbeitet" (Has): Hash im Ledger, Datei danach geändert →
// bleibt liegen mit Warnung. Fängt: Recheck nur auf dem Verarbeitungsweg.
func TestTOCTOUAlreadyProcessedChanged(t *testing.T) {
	e := newTOCTOUEnv(t)
	p := filepath.Join(e.dir, "a.txt")
	writeOld(t, p, "alt\n", 5*time.Minute)
	st, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	stale := &entry{Path: p, Name: "a.txt", ModTime: st.ModTime(), Size: st.Size(), Content: []byte("alt\n")}
	hash := sha256Hex(stale.Content)
	if err := e.app.ledger.Add(ledger.Entry{Hash: hash, File: "a.txt", ModTime: st.ModTime(), At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	rewrite(t, p, "neu, länger\n", st.ModTime().Add(time.Second))

	e.app.handle(t.Context(), job{primary: stale}, time.Now())

	if r := remaining(t, e.dir); len(r) != 1 {
		t.Fatalf("geänderte Datei archiviert, Exportordner = %v", r)
	}
	if !strings.Contains(e.log.String(), "a.txt wurde "+toctouWarn) {
		t.Fatalf("Warnung fehlt:\n%s", e.log)
	}
}

// archive selbst prüft nicht (HasNear, orphan_summary_action=archive): auch ein
// veralteter Eintrag geht ins Archiv wie bisher. Fängt: Recheck in archive.
func TestTOCTOUArchiveNoRecheck(t *testing.T) {
	e := newTOCTOUEnv(t)
	p := filepath.Join(e.dir, "a.txt")
	writeOld(t, p, "alt\n", 5*time.Minute)
	st, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	stale := &entry{Path: p, Name: "a.txt", ModTime: st.ModTime(), Size: st.Size()}
	e.month = st.ModTime().In(e.app.cfg.loc).Format("2006-01")
	rewrite(t, p, "neu, länger\n", st.ModTime().Add(time.Second))

	e.app.archive(stale)
	if a := e.archived(t); len(a) != 1 || a[0] != "a.txt" {
		t.Fatalf("Archiv = %v, will a.txt", a)
	}
	if strings.Contains(e.log.String(), toctouWarn) {
		t.Fatalf("Warnung in archive:\n%s", e.log)
	}
}
