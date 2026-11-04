package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"meetingsync/internal/domain"
	"meetingsync/internal/ledger"
	"meetingsync/internal/notion"
	"meetingsync/internal/watch"
)

// Aus dem Root-Paket hierher verschoben (R-M4-04): Diese Tests setzen den
// Wiederholungszustand direkt oder lesen ihn, daher bleibt er unexportiert.

// Fängt: nextTry wird ignoriert / Grenze (Before) verschoben / Fehlversuch zählt nicht.
func TestPassGatingByNextTry(t *testing.T) {
	e := newEnv(t, nil)
	e.llm.err = domain.Transient(os.ErrDeadlineExceeded)
	p := e.write(t, "Sitzung.srt", testSRT, testM)
	t0 := testM.Add(5 * time.Minute)

	e.p.Pass(context.Background(), t0) // transient, 1 min Wartezeit
	st := e.p.states[p]
	if len(e.llm.got) != 1 || st == nil || st.Attempts != 1 || !st.NextTry.Equal(t0.Add(time.Minute)) {
		t.Fatalf("nach Fehler: hits=%d state=%+v", len(e.llm.got), st)
	}
	e.p.Pass(context.Background(), t0.Add(59*time.Second))
	if len(e.llm.got) != 1 {
		t.Fatal("vor nextTry darf nicht erneut versucht werden")
	}
	e.p.Pass(context.Background(), t0.Add(time.Minute))
	if len(e.llm.got) != 2 || e.p.states[p].Attempts != 2 {
		t.Fatalf("genau ab nextTry wird erneut versucht: hits=%d state=%+v", len(e.llm.got), e.p.states[p])
	}
}

// Fängt: nextTry wird im Waisen-Zweig ignoriert (Retry-Sturm).
func TestPassOrphanNoteRespectsNextTry(t *testing.T) {
	e := newEnv(t, nil)
	p := e.write(t, "Notizen.md", testSummary, testM)
	now := testM.Add(time.Hour)
	retryAt := now.Add(2 * time.Minute)
	e.p.states[p] = &fileState{Attempts: 1, NextTry: retryAt}
	e.p.Pass(context.Background(), now)
	e.p.Pass(context.Background(), retryAt.Add(-time.Second))
	if len(e.llm.got) != 0 {
		t.Fatalf("vor nextTry hits=%d, want 0", len(e.llm.got))
	}
	e.p.Pass(context.Background(), retryAt)
	if len(e.llm.got) != 1 {
		t.Fatalf("ab nextTry hits=%d, want 1", len(e.llm.got))
	}
}

// Fängt: Erfolg räumt state nicht auf / Ledger-Eintrag fehlt / Datei wird doppelt verarbeitet.
// Mit Dry bleibt die Datei liegen; das Ledger im Speicher verhindert die Wiederholung.
func TestPassSuccessClearsStateAndFillsLedger(t *testing.T) {
	var dl *ledger.Ledger
	e := newEnv(t, func(o *Options, d *Deps) {
		o.Dry = true
		o.MaxAttempts = 5
		d.DryRun = func(string, *notion.Page, *domain.MeetingNote, []string, int) {}
		dl = dryLedger(t, o)
		d.Ledger = dl
	})
	p := e.write(t, "Sitzung.srt", testSRT, testM)
	e.p.states[p] = &fileState{Attempts: 2, Transient: 2}

	now := testM.Add(5 * time.Minute)
	e.p.Pass(context.Background(), now)
	if e.p.states[p] != nil {
		t.Fatalf("state nach Erfolg: %+v", e.p.states[p])
	}
	if !dl.Has(sha256Hex([]byte(testSRT))) {
		t.Fatal("Ledger enthält den Hash nicht")
	}
	e.p.Pass(context.Background(), now.Add(time.Minute))
	if len(e.llm.got) != 1 {
		t.Fatalf("zweiter Pass darf das LLM nicht erneut fragen, hits=%d", len(e.llm.got))
	}
	if !strings.Contains(e.log.String(), "wurde schon verarbeitet") {
		t.Fatalf("Log ohne ↺:\n%s", e.log.String())
	}
}

// Fängt: max_attempts im Pass nicht beachtet; Datei und Hinweis landen in _fehler,
// state wird gelöscht.
func TestPassGivesUpAfterMaxAttempts(t *testing.T) {
	e := newEnv(t, func(o *Options, _ *Deps) { o.MaxAttempts = 2 })
	e.llm.err = domain.Transient(os.ErrDeadlineExceeded)
	p := e.write(t, "Sitzung.srt", testSRT, testM)
	now := testM.Add(5 * time.Minute)
	e.p.Pass(context.Background(), now)
	e.p.Pass(context.Background(), now.Add(time.Minute))
	if exists(p) || !exists(filepath.Join(e.o.FailedDir, "Sitzung.srt")) || !exists(filepath.Join(e.o.FailedDir, "Sitzung.srt.fehler.txt")) {
		t.Fatal("Datei und .fehler.txt müssen in _fehler liegen")
	}
	if e.p.states[p] != nil {
		t.Fatal("state muss gelöscht sein")
	}
}

// Fängt: die Neustart-Regel löscht den Fehlerzähler der Datei nicht. Eine von
// Mark zurückgelegte Datei hätte sonst schon Versuche verbraucht.
func TestRestartRuleClearsRetryState(t *testing.T) {
	e := newEnv(t, nil)
	if err := e.ledger.Add(ledger.Entry{Hash: sha256Hex([]byte(testSRT)), Status: ledger.StatusBegonnen, File: "x", ModTime: testM}); err != nil {
		t.Fatal(err)
	}
	tr := e.write(t, "A.srt", testSRT, testM)
	e.p.states[tr] = &fileState{Attempts: e.o.MaxAttempts - 1}
	e.p.Pass(context.Background(), e.clock)
	if exists(tr) {
		t.Fatal("Transkript muss nach _fehler verschoben sein")
	}
	if st := e.p.states[tr]; st != nil {
		t.Fatalf("Fehlerzähler nicht gelöscht: %+v", st)
	}
}

// --- Archiv- und Verschiebe-Fehler (aus app_archive_backoff*_test.go) ------------------

const archiveWarn = "konnte nicht archiviert werden"

// noRename simuliert ein anderes Volume: Umbenennen nicht möglich (Kopierpfad).
func noRename(src, dst string) error {
	return &os.LinkError{Op: "renamex_np", Old: src, New: dst, Err: syscall.EXDEV}
}

// ufImmutable ist UF_IMMUTABLE aus sys/stat.h (syscall exportiert es nicht).
const ufImmutable = 0x2

// lockFile setzt uchg (UF_IMMUTABLE): Löschen und Umbenennen scheitern, Lesen
// geht. Ohne os/exec, weil die Architekturregel es im Paket verbietet.
func lockFile(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Chflags(path, ufImmutable); err != nil {
		t.Fatalf("chflags: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Chflags(path, 0) })
}

func unlockFile(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Chflags(path, 0); err != nil {
		t.Fatal(err)
	}
}

// remaining listet die Dateien (keine Ordner) in dir.
func remaining(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, d := range entries {
		if !d.IsDir() {
			out = append(out, d.Name())
		}
	}
	return out
}

// inFailed trennt _fehler in Daten und .fehler.txt-Hinweise.
func (e *testEnv) inFailed(t *testing.T) (data, notes []string) {
	t.Helper()
	for _, n := range remaining(e.o.FailedDir) {
		if strings.HasSuffix(n, ".fehler.txt") {
			notes = append(notes, mustRead(t, filepath.Join(e.o.FailedDir, n)))
		} else {
			data = append(data, n)
		}
	}
	return data, notes
}

// counts: Dateien in Archivmonat, _fehler und Exportordner.
func (e *testEnv) counts(t *testing.T) (archived, failed, export int) {
	t.Helper()
	data, _ := e.inFailed(t)
	return len(remaining(filepath.Dir(e.archived("x")))), len(data), len(remaining(e.o.WatchDir))
}

// threePasses: Durchläufe bei now, +10 s, +20 s (innerhalb der 2 min).
func (e *testEnv) threePasses(now time.Time) {
	for i := range 3 {
		e.p.Pass(context.Background(), now.Add(time.Duration(i)*10*time.Second))
	}
}

// writePair legt Transkript und Zusammenfassung an, die zusammen eine Notiz ergeben.
func (e *testEnv) writePair(t *testing.T) (tr, sum string) {
	t.Helper()
	return e.write(t, "Sitzung.srt", testSRT, testM), e.write(t, "Sitzung.md", testSummary, testM.Add(time.Minute))
}

// Remove(src) scheitert beim Archivieren (Kopierpfad, Quelle mit uchg).
// Fall "verarbeitet": das Transkript bleibt liegen; Wiederholung über ledger.Has.
// Fall "HasNear": die Zusammenfassung bleibt liegen und ist ab Durchlauf 2 eine
// Waise mit HasNear-Treffer.
// Fängt: kein Backoff nach Archiv-Fehler (Warnung 3-mal); delete(state)
// nach statt vor archiveJob im Erfolgsfall (Backoff gelöscht); Gate im
// Waisen-Zweig erst nach HasNear (📎 und Warnung je Durchlauf); Erfolg löscht
// den Zustand nicht; Backoff zählt gegen max_attempts.
func TestArchiveRemoveFailsBackoff(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lock  func(tr, sum string) string
		retry string // Logzeile des erneuten Versuchs
	}{
		{"verarbeitet", func(tr, _ string) string { return tr }, "wurde schon verarbeitet"},
		{"HasNear", func(_, sum string) string { return sum }, "gehört zu einem bereits verarbeiteten Transkript"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, func(o *Options, d *Deps) {
				o.MaxAttempts = 1
				d.Mover = watch.Mover{Rename: noRename}
			})
			tr, sum := e.writePair(t)
			locked := tc.lock(tr, sum)
			lockFile(t, locked)
			now := e.clock

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
			if n := e.notion.count("notes-ds"); n != 1 {
				t.Fatalf("Notizen = %d, want 1", n)
			}
			if st := e.p.states[locked]; st == nil || !st.NextTry.Equal(now.Add(2*time.Minute)) || st.Attempts != 0 {
				t.Fatalf("state = %+v, want nextTry now+2min, attempts 0", st)
			}

			e.p.Pass(context.Background(), now.Add(2*time.Minute)) // genau ab nextTry: neuer Versuch, scheitert wieder
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
			e.p.Pass(context.Background(), now.Add(4*time.Minute))
			if a, f, x := e.counts(t); a != 2 || f != 0 || x != 0 {
				t.Fatalf("nach Entsperren: Archiv %d, _fehler %d, Export %d; want 2, 0, 0", a, f, x)
			}
			if st := e.p.states[locked]; st != nil {
				t.Fatalf("state nach Erfolg: %+v", st)
			}
			if n := e.notion.count("notes-ds"); n != 1 {
				t.Fatalf("Notizen = %d, want 1", n)
			}
		})
	}
}

// orphan_summary_action=archive, Remove(src) scheitert.
// Fängt: kein Backoff im Waisen-Archivzweig (📎 und Warnung je Durchlauf).
func TestArchiveOrphanRemoveFailsBackoff(t *testing.T) {
	e := newEnv(t, func(o *Options, d *Deps) {
		o.OrphanAction = "archive"
		d.Mover = watch.Mover{Rename: noRename}
	})
	p := e.write(t, "Notizen.md", testSummary, testM)
	lockFile(t, p)
	now := testM.Add(time.Hour)
	e.threePasses(now)
	if n := strings.Count(e.log.String(), archiveWarn); n != 1 {
		t.Fatalf("Warnung %d-mal, want 1\nLog:\n%s", n, e.log.String())
	}
	if n := strings.Count(e.log.String(), "orphan_summary_action=archive"); n != 1 {
		t.Fatalf("📎 %d-mal, want 1", n)
	}
	if !exists(p) || len(remaining(filepath.Dir(e.archived("x")))) != 0 {
		t.Fatal("Datei bleibt im Exportordner, Archiv leer")
	}
	unlockFile(t, p)
	e.p.Pass(context.Background(), now.Add(2*time.Minute))
	if exists(p) || !exists(e.archived("Notizen.md")) || e.p.states[p] != nil {
		t.Fatalf("nach Backoff archiviert und state gelöscht: exists=%v state=%+v", exists(p), e.p.states[p])
	}
}

// Sync des Zielordners scheitert nach der Kopie (Entscheidung Mark, 2026-11-02).
// Fängt: Sync-Fehler bricht ab (Original und Kopie bleiben liegen, Export 1,
// Archiv 1); Original ohne Identitätsprüfung oder gar nicht gelöscht; Warnung
// fehlt oder wiederholt sich; Sync-Fehler als Archiv-Fehler gewertet.
func TestArchiveDirSyncFailsWarnsAndMoves(t *testing.T) {
	calls := 0
	e := newEnv(t, func(_ *Options, d *Deps) {
		d.Mover = watch.Mover{Rename: noRename, SyncDir: func(string) error { calls++; return errors.New("EIO") }}
	})
	tr := e.write(t, "Sitzung.srt", testSRT, testM)
	e.threePasses(e.clock)

	if a, f, x := e.counts(t); a != 1 || f != 0 || x != 0 {
		t.Fatalf("Archiv %d, _fehler %d, Export %d; want 1, 0, 0", a, f, x)
	}
	if calls != 1 {
		t.Fatalf("SyncDir %d-mal, want 1", calls)
	}
	if got := mustRead(t, e.archived(filepath.Base(tr))); got != testSRT {
		t.Fatal("Kopie im Archiv hat nicht den Inhalt des Originals")
	}
	if n := strings.Count(e.log.String(), "Zielordner nicht synchronisiert"); n != 1 {
		t.Fatalf("Warnung %d-mal, want 1\nLog:\n%s", n, e.log.String())
	}
	if strings.Contains(e.log.String(), archiveWarn) || !strings.Contains(e.log.String(), "📦") {
		t.Fatalf("Sync-Fehler gilt als Archiv-Fehler:\n%s", e.log.String())
	}
	if n := e.notion.count("notes-ds"); n != 1 {
		t.Fatalf("Notizen = %d, want 1", n)
	}
	if st := e.p.states[tr]; st != nil {
		t.Fatalf("state = %+v, want nil", st)
	}
}

// Kopierpfad + Sync-Fehler beim Verschieben nach _fehler: Warnung, Datei liegt
// in _fehler (genau eine, mit Hinweis), kein Verschieben-Fehler, kein Backoff.
// Auslöser (a) Neustart-Regel, (b) handleError bei max_attempts.
func TestFailPathDirSyncWarnsAndMoves(t *testing.T) {
	const syncWarn = "Zielordner nicht synchronisiert"
	for _, tc := range []struct {
		name string
		arm  func(t *testing.T, e *testEnv)
		want string // Ursache im .fehler.txt
	}{
		{"Neustart-Regel", func(t *testing.T, e *testEnv) {
			if err := e.ledger.Add(ledger.Entry{Hash: sha256Hex([]byte(testSRT)), Status: ledger.StatusBegonnen, File: "x", ModTime: testM}); err != nil {
				t.Fatal(err)
			}
		}, errUnterbrochen.Error()},
		{"max_attempts", func(_ *testing.T, e *testEnv) { e.llm.err = errors.New("kaputt") }, "gebe auf"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			e := newEnv(t, func(o *Options, d *Deps) {
				o.MaxAttempts = 1
				d.Mover = watch.Mover{Rename: noRename, SyncDir: func(string) error { calls++; return errors.New("EIO") }}
			})
			tr := e.write(t, "Sitzung.srt", testSRT, testM)
			tc.arm(t, e)

			e.threePasses(e.clock)

			data, notes := e.inFailed(t)
			if len(data) != 1 || len(notes) != 1 {
				t.Fatalf("_fehler: %v + %d Hinweise, want 1 Datei mit Hinweis\nLog:\n%s", data, len(notes), e.log.String())
			}
			if tc.name == "Neustart-Regel" && !strings.Contains(notes[0], tc.want) {
				t.Fatalf(".fehler.txt ohne Ursache:\n%s", notes[0])
			}
			if a, _, x := e.counts(t); a != 0 || x != 0 {
				t.Fatalf("Archiv %d, Export %d; want 0, 0", a, x)
			}
			if calls != 1 {
				t.Fatalf("SyncDir %d-mal, want 1", calls)
			}
			if n := strings.Count(e.log.String(), syncWarn); n != 1 {
				t.Fatalf("Sync-Warnung %d-mal, want 1\nLog:\n%s", n, e.log.String())
			}
			if strings.Contains(e.log.String(), "konnte nicht verschoben werden") {
				t.Fatalf("Sync-Fehler gilt als Verschiebe-Fehler:\n%s", e.log.String())
			}
			if st := e.p.states[tr]; st != nil {
				t.Fatalf("state = %+v, want nil", st)
			}
			if tc.name == "max_attempts" && strings.Count(e.log.String(), "gebe auf") != 1 {
				t.Fatalf("gebe auf nicht genau einmal:\n%s", e.log.String())
			}
			if tc.name == "Neustart-Regel" && strings.Count(e.log.String(), "unterbrochen") != 1 {
				t.Fatalf("Unterbrochen-Zeile wiederholt:\n%s", e.log.String())
			}
			if n := e.notion.count("notes-ds"); n != 0 {
				t.Fatalf("Notizen = %d, want 0", n)
			}
		})
	}
}

// Fängt: Prüfung auf abgebrochenen Kontext im Transkript-Zweig entfernt
// (ohne sie läuft handle an, sichtbar an der Logzeile "→ Sitzung.srt ("; LLM-Aufruf
// und state bleiben wegen des abgebrochenen Kontexts leer).
func TestPassCanceledContextDoesNothing(t *testing.T) {
	e := newEnv(t, func(o *Options, _ *Deps) { o.OrphanAction = "archive" })
	tr := e.write(t, "Sitzung.srt", testSRT, testM)
	sum := e.write(t, "Notizen.md", testSummary, testM.Add(time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e.p.Pass(ctx, testM.Add(3*time.Hour))
	if len(e.llm.got) != 0 || !exists(tr) || !exists(sum) {
		t.Fatalf("nichts darf passieren: hits=%d tr=%v sum=%v", len(e.llm.got), exists(tr), exists(sum))
	}
	if len(e.p.states) != 0 {
		t.Fatalf("kein state erwartet: %v", e.p.states)
	}
	if strings.Contains(e.log.String(), "→ Sitzung.srt (") {
		t.Fatalf("handle darf nicht anlaufen:\n%s", e.log.String())
	}
}

// --- Protokoll- und Abbruch-Fehler (aus app_crash_test.go) ----------------------------

// lastStatus liest den letzten Status zu hash aus dem Protokoll auf Platte.
func (e *testEnv) lastStatus(t *testing.T, hash string) string {
	t.Helper()
	status := ""
	for _, ln := range e.ledgerLines(t) {
		h, _ := ln["hash"].(string)
		if h == "" {
			h, _ = ln["sha256"].(string)
		}
		if h != hash {
			continue
		}
		status, _ = ln["status"].(string)
		if status == "" {
			status = ledger.StatusFertig
		}
	}
	return status
}

// Fängt: Add(begonnen) scheitert und CreatePage läuft trotzdem / der Fehler
// zählt nicht als Versuch / der Retry landet als Abbruch in _fehler.
func TestBeginLedgerErrorNoNotionRequest(t *testing.T) {
	e := newEnv(t, nil)
	tr, _ := e.writePair(t)
	// Ledger-Pfad durch einen Ordner ersetzen: Add scheitert (EISDIR).
	path := filepath.Join(e.o.ArchiveDir, ".meetingsync-ledger.jsonl")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	now := e.clock
	e.p.Pass(context.Background(), now)
	if len(e.notion.pages) != 0 {
		t.Fatalf("Seiten angelegt = %d, want 0", len(e.notion.pages))
	}
	if st := e.p.states[tr]; st == nil || st.Attempts != 1 {
		t.Fatalf("Fehler muss als Versuch zählen: %+v", st)
	}
	if !exists(tr) || !strings.Contains(e.log.String(), "Protokoll nicht schreibbar, Notion-Seite nicht angelegt") {
		t.Fatalf("Datei muss liegen bleiben, Log:\n%s", e.log.String())
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	e.p.Pass(context.Background(), now.Add(2*time.Minute))
	if n := e.notion.count("notes-ds"); n != 1 || len(e.notion.pages) != 2 {
		t.Fatalf("Retry: %d Notizen, %d Seiten; want 1 Notiz + 1 Task\nLog:\n%s", n, len(e.notion.pages), e.log.String())
	}
	if left := remaining(e.o.WatchDir); len(left) != 0 {
		t.Fatalf("nicht archiviert: %v", left)
	}
}

// Fängt: Abbruch per Kontext während CreatePage setzt verworfen (die Anfrage kann
// Notion erreicht haben; ein Neustart würde eine zweite Seite anlegen).
func TestCreatePageCanceledKeepsBegonnen(t *testing.T) {
	e := newEnv(t, nil)
	tr, _ := e.writePair(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.notion.onCreate = func(ds string) error {
		if ds != "notes-ds" {
			return nil
		}
		cancel()
		return context.Canceled
	}
	e.p.Pass(ctx, e.clock)
	if got := e.lastStatus(t, sha256Hex([]byte(testSRT))); got != ledger.StatusBegonnen {
		t.Fatalf("Status = %q, want begonnen", got)
	}
	if st := e.p.states[tr]; st != nil && st.Attempts != 0 {
		t.Fatalf("Abbruch darf nicht zählen: %+v", st)
	}
	e.restart(t)
	e.advance(time.Minute)
	if data, _ := e.inFailed(t); len(data) != 2 {
		t.Fatalf("Neustart-Regel: _fehler = %v, want 2 Dateien", data)
	}
	if n := e.notion.count("notes-ds"); n != 0 {
		t.Fatalf("Notizen = %d, want 0", n)
	}
}
