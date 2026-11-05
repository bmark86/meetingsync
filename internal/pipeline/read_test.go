package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"meetingsync/internal/ledger"
	"meetingsync/internal/watch"
)

// R-M5-01: Inhalt nur bei Cache-Fehlschlag (Typerkennung) und bei der Verarbeitung lesen.

// countFS zählt ReadFile je Pfad auf dem echten Dateisystem. onRead
// (optional) läuft vor dem Lesen.
type countFS struct {
	watch.OS
	reads  map[string]int
	onRead func(path string)
	fail   map[string]bool // Pfade, deren Lesen mit Fehler endet
}

func (c *countFS) ReadFile(path string) ([]byte, error) {
	c.reads[path]++
	if c.onRead != nil {
		c.onRead(path)
	}
	if c.fail[path] {
		return nil, errors.New("Lesen verweigert")
	}
	return c.OS.ReadFile(path)
}

func (c *countFS) total() int {
	n := 0
	for _, v := range c.reads {
		n += v
	}
	return n
}

func countEnv(t *testing.T, mod func(*Options)) (*testEnv, *countFS) {
	t.Helper()
	cfs := &countFS{reads: map[string]int{}}
	e := newEnv(t, func(o *Options, d *Deps) {
		d.FS = cfs
		if mod != nil {
			mod(o)
		}
	})
	return e, cfs
}

// passReads ruft Pass und liefert die Zahl der ReadFile-Aufrufe darin.
func passReads(e *testEnv, cfs *countFS, now time.Time) int {
	before := cfs.total()
	e.p.Pass(context.Background(), now)
	return cfs.total() - before
}

// Fängt: Datei in der Wartezeit (NextTry) wird je Durchlauf gelesen;
// "Neu: …" fehlt, steht mehrfach oder erst nach dem Start der Verarbeitung.
func TestReadFileBackoffNoReads(t *testing.T) {
	e, cfs := countEnv(t, nil)
	e.write(t, "a.srt", testSRT, testM)
	e.llm.err = errTrans
	now := e.clock

	if n := passReads(e, cfs, now); n != 1 {
		t.Fatalf("Durchlauf 1: %d Lesevorgänge, want 1 (neue Datei, Inhalt für Typ und Hash)", n)
	}
	if st := e.p.states[e.o.WatchDir+"/a.srt"]; st == nil || !now.Before(st.NextTry) {
		t.Fatalf("keine Wartezeit: %+v", st)
	}
	for i, at := range []time.Duration{10 * time.Second, 30 * time.Second, 50 * time.Second} {
		if n := passReads(e, cfs, now.Add(at)); n != 0 {
			t.Fatalf("Durchlauf %d in der Wartezeit: %d Lesevorgänge, want 0", i+2, n)
		}
	}
	if len(e.llm.got) != 1 {
		t.Fatalf("LLM-Aufrufe = %d, want 1", len(e.llm.got))
	}

	e.llm.err = nil
	if n := passReads(e, cfs, now.Add(2*time.Minute)); n != 1 {
		t.Fatalf("nach der Wartezeit: %d Lesevorgänge, want 1 (Verarbeitung)", n)
	}
	if len(e.llm.got) != 2 || !strings.Contains(e.llm.got[1].Text, "Hallo zusammen.") {
		t.Fatalf("LLM ohne den nachgelesenen Inhalt: %+v", e.llm.got)
	}
	if e.notion.count("notes-ds") != 1 || !exists(e.archived("a.srt")) {
		t.Fatal("Notiz nicht angelegt oder Datei nicht archiviert")
	}
	logs := e.log.String()
	if strings.Count(logs, "Neu: a.srt → Transkript\n") != 1 || !strings.HasPrefix(logs, "Neu: a.srt → Transkript\n→ a.srt (Transkript, ") {
		t.Fatalf("Zeile \"Neu: …\" nicht genau einmal vor der Verarbeitung:\n%s", logs)
	}
}

// Fängt: bekannte, unveränderte Dateien, die nur warten (Zusammenfassung vor
// orphan_summary_minutes, orphan_summary_action=keep), werden je Durchlauf gelesen.
func TestReadFileKnownUnchangedNoReads(t *testing.T) {
	for _, tc := range []struct {
		name, action string
		mtime        time.Duration // vor e.clock
	}{
		{"wartet auf Transkript", "note", time.Minute},
		{"keep", "keep", time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, cfs := countEnv(t, func(o *Options) { o.OrphanAction = tc.action })
			e.write(t, "b.md", testSummary, e.clock.Add(-tc.mtime))
			if n := passReads(e, cfs, e.clock); n != 1 {
				t.Fatalf("Durchlauf 1: %d Lesevorgänge, want 1", n)
			}
			for i := range 3 {
				if n := passReads(e, cfs, e.clock.Add(time.Duration(i+1)*10*time.Second)); n != 0 {
					t.Fatalf("Durchlauf %d: %d Lesevorgänge, want 0", i+2, n)
				}
			}
			if len(e.llm.got) != 0 || strings.Count(e.log.String(), "Neu: b.md → Zusammenfassung") != 1 {
				t.Fatalf("LLM %d-mal, Log:\n%s", len(e.llm.got), e.log)
			}
		})
	}
}

// Ledger-Treffer mit nachgelesenem Inhalt: ein anderer Lauf (-file) hat den
// Inhalt während der Wartezeit verarbeitet → Archiv ohne zweite Seite.
// Fängt: Hash aus leerem Inhalt (Content nil) statt aus der Datei.
func TestReadFileLedgerHitAfterBackoff(t *testing.T) {
	e, cfs := countEnv(t, nil)
	e.write(t, "a.srt", testSRT, testM)
	e.llm.err = errTrans
	now := e.clock
	passReads(e, cfs, now)
	if err := e.ledger.Add(ledger.Entry{Hash: sha256Hex([]byte(testSRT)), File: "a.srt", ModTime: testM, URL: "https://notion.so/x", At: now}); err != nil {
		t.Fatal(err)
	}

	if n := passReads(e, cfs, now.Add(2*time.Minute)); n != 1 {
		t.Fatalf("%d Lesevorgänge, want 1", n)
	}
	if !strings.Contains(e.log.String(), "↺ a.srt wurde schon verarbeitet → Archiv") || !exists(e.archived("a.srt")) {
		t.Fatalf("Ledger-Treffer nicht erkannt:\n%s", e.log)
	}
	if e.notion.count("notes-ds") != 0 || len(e.llm.got) != 1 {
		t.Fatalf("zweite Verarbeitung: Seiten %d, LLM %d", e.notion.count("notes-ds"), len(e.llm.got))
	}
}

// Datei zwischen Ordnerliste und Nachlesen geändert: in diesem Durchlauf
// nicht verarbeiten, Zustand unverändert; der nächste stabile Durchlauf liest und
// verarbeitet den neuen Inhalt. Fängt: neuer (evtl. halb geschriebener) Inhalt
// mit Größe/mtime der alten Liste verarbeitet.
func TestReadFileChangedBeforeLazyRead(t *testing.T) {
	e, cfs := countEnv(t, nil)
	p := e.write(t, "a.srt", testSRT, testM)
	e.llm.err = errTrans
	now := e.clock
	passReads(e, cfs, now)
	e.llm.err = nil

	later := now.Add(2 * time.Minute)
	const neu = "1\n00:00:01,000 --> 00:00:05,000\nSpeaker 1: Neuer Inhalt.\n"
	cfs.onRead = func(string) {
		cfs.onRead = nil
		rewrite(t, p, neu, later)
	}
	if n := passReads(e, cfs, later); n != 1 {
		t.Fatalf("%d Lesevorgänge, want 1", n)
	}
	// Zustand des ersten Versuchs bleibt unverändert (kein neuer Versuch gezählt)
	if st := e.p.states[p]; len(e.llm.got) != 1 || st == nil || st.Attempts != 1 || !st.NextTry.Equal(now.Add(time.Minute)) || !exists(p) {
		t.Fatalf("geänderte Datei verarbeitet: LLM %d, Zustand %+v", len(e.llm.got), st)
	}

	if n := passReads(e, cfs, later.Add(e.o.Stable)); n != 1 {
		t.Fatalf("nächster Durchlauf: %d Lesevorgänge, want 1", n)
	}
	if len(e.llm.got) != 2 || !strings.Contains(e.llm.got[1].Text, "Neuer Inhalt.") || !exists(e.archived("a.srt")) {
		t.Fatalf("neuer Inhalt nicht verarbeitet: %+v", e.llm.got)
	}
	if strings.Count(e.log.String(), "Neu: a.srt → Transkript") != 2 {
		t.Fatalf("Log:\n%s", e.log)
	}
}

// Zusammenfassung als Kontext aus dem Cache: nur gelesen, wenn das LLM läuft.
// Fängt: Kontext fehlt, weil Content der Zusammenfassung nil bleibt.
func TestReadFileSummaryContextLoaded(t *testing.T) {
	e, cfs := countEnv(t, func(o *Options) { o.UseSummaryAsContext = true })
	e.write(t, "a.srt", testSRT, testM)
	e.write(t, "a.md", testSummary, testM)
	e.llm.err = errTrans
	now := e.clock
	passReads(e, cfs, now)
	if n := passReads(e, cfs, now.Add(30*time.Second)); n != 0 {
		t.Fatalf("Wartezeit: %d Lesevorgänge, want 0", n)
	}
	e.llm.err = nil
	if n := passReads(e, cfs, now.Add(2*time.Minute)); n != 2 {
		t.Fatalf("Verarbeitung: %d Lesevorgänge, want 2 (Transkript, Zusammenfassung)", n)
	}
	if len(e.llm.got) != 2 || e.llm.got[1].SummaryText != testSummary {
		t.Fatalf("Kontext = %q", e.llm.got[len(e.llm.got)-1].SummaryText)
	}
}

// Gecachtes Transkript wird unlesbar: bleibt liegen, Zustand unverändert,
// Warnung einmal, die Zusammenfassung wird nicht zur Waise mit eigener Notiz.
// Fängt: Lesefehler zählt als Versuch, Warnung je Durchlauf oder Waisen-Notiz.
func TestReadFileCachedTranscriptUnreadable(t *testing.T) {
	e, cfs := countEnv(t, nil)
	tr := e.write(t, "a.srt", testSRT, testM)
	sm := e.write(t, "a.md", testSummary, testM)
	e.llm.err = errTrans
	now := e.clock
	passReads(e, cfs, now)
	before := *e.p.states[tr]
	llmCalls := len(e.llm.got)

	cfs.fail = map[string]bool{tr: true}
	later := now.Add(2 * time.Minute)
	passReads(e, cfs, later)
	passReads(e, cfs, later.Add(10*time.Second))

	st := e.p.states[tr]
	if st == nil || st.Attempts != before.Attempts || !st.NextTry.Equal(before.NextTry) {
		t.Fatalf("Zustand geändert: vorher %+v, nachher %+v", before, st)
	}
	if !exists(tr) || !exists(sm) || len(e.llm.got) != llmCalls || e.notion.count("notes-ds") != 0 {
		t.Fatalf("Dateien/LLM/Seiten: LLM %d, Seiten %d", len(e.llm.got), e.notion.count("notes-ds"))
	}
	if n := strings.Count(e.log.String(), "⚠️  a.srt nicht lesbar: "); n != 1 {
		t.Fatalf("Warnung %d-mal, want 1:\n%s", n, e.log)
	}
}

// Zusammenfassung als Kontext beim Verarbeiten unlesbar: Notiz ohne
// SummaryText, Hinweis im Log, Zusammenfassung wandert mit ins Archiv.
func TestReadFileSummaryContextUnreadable(t *testing.T) {
	e, cfs := countEnv(t, func(o *Options) { o.UseSummaryAsContext = true })
	e.write(t, "a.srt", testSRT, testM)
	sm := e.write(t, "a.md", testSummary, testM)
	e.llm.err = errTrans
	now := e.clock
	passReads(e, cfs, now)
	e.llm.err = nil
	cfs.fail = map[string]bool{sm: true}
	passReads(e, cfs, now.Add(2*time.Minute))

	if len(e.llm.got) != 2 || e.llm.got[1].SummaryText != "" {
		t.Fatalf("LLM-Aufrufe %d, Kontext %q", len(e.llm.got), e.llm.got[len(e.llm.got)-1].SummaryText)
	}
	if e.notion.count("notes-ds") != 1 || !exists(e.archived("a.srt")) || !exists(e.archived("a.md")) || exists(sm) {
		t.Fatalf("Notiz/Archiv:\n%s", e.log)
	}
	if !strings.Contains(e.log.String(), "   ⚠️  a.md nicht lesbar oder geändert → ohne Zusammenfassung als Kontext\n") {
		t.Fatalf("Hinweis fehlt:\n%s", e.log)
	}
}

// use_summary_as_context=false: beim Verarbeiten nur das Transkript lesen.
// Fängt: Zusammenfassung wird trotzdem eingelesen.
func TestReadFileSummaryNotReadWithoutContext(t *testing.T) {
	e, cfs := countEnv(t, func(o *Options) { o.UseSummaryAsContext = false })
	tr := e.write(t, "a.srt", testSRT, testM)
	sm := e.write(t, "a.md", testSummary, testM)
	e.llm.err = errTrans
	now := e.clock
	passReads(e, cfs, now)
	e.llm.err = nil
	cfs.reads = map[string]int{}
	passReads(e, cfs, now.Add(2*time.Minute))

	if cfs.reads[tr] != 1 || cfs.reads[sm] != 0 {
		t.Fatalf("Lesevorgänge: Transkript %d (want 1), Zusammenfassung %d (want 0)", cfs.reads[tr], cfs.reads[sm])
	}
	if e.notion.count("notes-ds") != 1 {
		t.Fatalf("keine Notiz:\n%s", e.log)
	}
}
