package pipeline

// R-M5-02 (A-19): warned, cache und states nur für Pfade im Exportordner.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"meetingsync/internal/ledger"
)

// moveOut verschiebt path in den Unterordner sub des Exportordners (nicht Teil
// der Ordnerliste); mtime bleibt. Liefert den neuen Pfad.
func (e *testEnv) moveOut(t *testing.T, path string) string {
	t.Helper()
	dir := filepath.Join(e.o.WatchDir, "sub")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, filepath.Base(path))
	if err := os.Rename(path, dst); err != nil {
		t.Fatal(err)
	}
	return dst
}

// Fängt: Warnungen zu verschwundenen Dateien bleiben für immer; Warnung zu
// einer noch liegenden Datei erscheint je Durchlauf neu; andere Schlüssel
// (ledger-refresh) werden gelöscht. Die Warnung erscheint wieder, sobald die
// Datei zurückkommt.
func TestPruneWarnedVanishedFile(t *testing.T) {
	e := newEnv(t, nil)
	keep := e.write(t, "bleibt.pdf", "x", testM)
	gone := e.write(t, "weg.pdf", "x", testM)
	e.p.warned["ledger-refresh"] = true
	e.p.Pass(context.Background(), e.clock)

	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	e.p.Pass(context.Background(), e.clock.Add(10*time.Second))
	if e.p.warned[gone] || !e.p.warned[keep] || !e.p.warned["ledger-refresh"] {
		t.Fatalf("warned = %v", e.p.warned)
	}

	e.write(t, "weg.pdf", "x", testM)
	e.p.Pass(context.Background(), e.clock.Add(20*time.Second))
	logs := e.log.String()
	if strings.Count(logs, "Ignoriere weg.pdf") != 2 || strings.Count(logs, "Ignoriere bleibt.pdf") != 1 {
		t.Fatalf("Warnungen: weg.pdf want 2, bleibt.pdf want 1:\n%s", logs)
	}
}

// Fängt: "#read"-Warnung, Dateityp und Zustand einer Datei, die in einen
// Unterordner wandert, bleiben; kommt sie zurück, zählt max_attempts von vorn.
func TestPruneReadWarningCacheAndState(t *testing.T) {
	e, cfs := countEnv(t, nil)
	tr := e.write(t, "a.srt", testSRT, testM)
	e.llm.err = errTrans
	now := e.clock
	passReads(e, cfs, now) // Versuch 1, Typ im Cache
	cfs.fail = map[string]bool{tr: true}
	passReads(e, cfs, now.Add(2*time.Minute)) // Nachlesen scheitert: "#read"
	if _, cached := e.p.cache[tr]; !e.p.warned[tr+"#read"] || !cached || e.p.states[tr] == nil {
		t.Fatalf("Ausgangslage: warned %v, cache %v, states %v", e.p.warned, e.p.cache, e.p.states)
	}
	cfs.fail = nil

	sub := e.moveOut(t, tr)
	passReads(e, cfs, now.Add(3*time.Minute))
	for k := range e.p.warned {
		if strings.HasPrefix(k, tr) {
			t.Fatalf("Warnung %q bleibt", k)
		}
	}
	if len(e.p.cache) != 0 || len(e.p.states) != 0 {
		t.Fatalf("cache %v, states %v; want leer", e.p.cache, e.p.states)
	}

	if err := os.Rename(sub, tr); err != nil {
		t.Fatal(err)
	}
	passReads(e, cfs, now.Add(4*time.Minute))
	if st := e.p.states[tr]; st == nil || st.Attempts != 1 || len(e.llm.got) != 2 {
		t.Fatalf("nach Rückkehr: state %+v, LLM %d; want Versuch 1/10, LLM 2", st, len(e.llm.got))
	}
}

// Fängt: Ordner nicht lesbar (ReadDir-Fehler) löscht alle Einträge.
func TestPruneKeepsAllWhenListingFails(t *testing.T) {
	e := newEnv(t, nil)
	tr := e.write(t, "a.srt", testSRT, testM)
	e.write(t, "x.pdf", "x", testM)
	e.llm.err = errTrans
	e.p.Pass(context.Background(), e.clock)
	nw, nc, ns := len(e.p.warned), len(e.p.cache), len(e.p.states)
	if nw != 1 || nc != 1 || ns != 1 {
		t.Fatalf("Ausgangslage: warned %v, cache %v, states %v", e.p.warned, e.p.cache, e.p.states)
	}

	moved := e.o.WatchDir + ".weg"
	if err := os.Rename(e.o.WatchDir, moved); err != nil {
		t.Fatal(err)
	}
	e.p.Pass(context.Background(), e.clock.Add(5*time.Minute))
	if !strings.Contains(e.log.String(), "Ordner nicht lesbar") {
		t.Fatalf("kein Lesefehler:\n%s", e.log)
	}
	if len(e.p.warned) != nw || len(e.p.cache) != nc || e.p.states[tr] == nil {
		t.Fatalf("gelöscht: warned %v, cache %v, states %v", e.p.warned, e.p.cache, e.p.states)
	}
}

// Fängt: Zustand mit unklarem Anlegen wird beim Verschwinden gelöscht; nach
// der Rückkehr fehlen Marker und LLM-Ergebnis (zweite Seite ohne Suche bzw.
// zweiter LLM-Lauf).
func TestPruneKeepsAmbiguousState(t *testing.T) {
	e := newEnv(t, withSource)
	ambiguousOnce(e, errUnklar)
	tr := e.write(t, "Sitzung.srt", testSRT, testM)
	e.p.Pass(context.Background(), e.clock)
	if st := e.p.states[tr]; st == nil || st.ambiguous == nil || st.ambiguous.res == nil {
		t.Fatalf("state = %+v, want ambiguous", st)
	}

	sub := e.moveOut(t, tr)
	e.advance(time.Minute)
	if st := e.p.states[tr]; st == nil || st.ambiguous == nil || st.ambiguous.res == nil {
		t.Fatalf("ambiguous gelöscht: %+v", st)
	}
	if len(e.p.cache) != 0 {
		t.Fatalf("cache %v, want leer", e.p.cache)
	}

	if err := os.Rename(sub, tr); err != nil {
		t.Fatal(err)
	}
	e.advance(20 * time.Minute)
	if e.notion.count("notes-ds") != 1 || len(e.llm.got) != 1 || e.notion.finds != 1 || !exists(e.archived("Sitzung.srt")) || len(e.p.states) != 0 {
		t.Fatalf("Notizen %d, LLM %d, Suchen %d, Zustand %v; want 1, 1, 1, leer", e.notion.count("notes-ds"), len(e.llm.got), e.notion.finds, e.p.states)
	}
}

// Fängt: giveUp-Zustand (max_attempts erreicht, _fehler gescheitert) wird beim
// Verschwinden gelöscht; nach der Rückkehr liefe die Datei wieder mit LLM.
func TestPruneKeepsGiveUpState(t *testing.T) {
	fm := &flakyMover{fails: func(string) bool { return true }}
	e := newEnv(t, func(o *Options, d *Deps) { o.MaxAttempts = 1; d.Mover = fm.mover() })
	e.llm.err = errOther
	tr := e.write(t, "Sitzung.srt", testSRT, testM)
	e.p.Pass(context.Background(), e.clock)
	if st := e.p.states[tr]; st == nil || st.giveUp == nil {
		t.Fatalf("state %+v, want giveUp", st)
	}

	sub := e.moveOut(t, tr)
	e.advance(time.Minute)
	if st := e.p.states[tr]; st == nil || st.giveUp == nil {
		t.Fatalf("giveUp gelöscht: %+v", st)
	}

	if err := os.Rename(sub, tr); err != nil {
		t.Fatal(err)
	}
	fm.fails = func(string) bool { return false }
	e.advance(2 * time.Minute)
	if exists(tr) || !exists(filepath.Join(e.o.FailedDir, "Sitzung.srt")) || len(e.llm.got) != 1 || len(e.p.states) != 0 {
		t.Fatalf("Datei bleibt %v, LLM %d, state %v", exists(tr), len(e.llm.got), e.p.states)
	}
}

// Fängt: Ledger-Treffer behält den alten Zustand (Versuchszahl, unklares
// Anlegen), wenn das Archivieren scheitert.
func TestLedgerHitDeletesState(t *testing.T) {
	fm := &flakyMover{fails: func(string) bool { return true }}
	e := newEnv(t, func(_ *Options, d *Deps) { d.Mover = fm.mover() })
	tr := e.write(t, "Sitzung.srt", testSRT, testM)
	hash := sha256Hex([]byte(testSRT))
	e.p.states[tr] = &fileState{Attempts: 3, Transient: 2, ambiguous: &ambiguousCreate{hash: hash, marker: testMarker}}
	if err := e.ledger.Add(ledger.Entry{Hash: hash, File: "Sitzung.srt", ModTime: testM}); err != nil {
		t.Fatal(err)
	}
	e.p.Pass(context.Background(), e.clock)
	st := e.p.states[tr]
	if st == nil || st.Attempts != 0 || st.Transient != 0 || st.ambiguous != nil || !st.NextTry.Equal(e.clock.Add(retryLaterWait)) {
		t.Fatalf("state %+v, want nur neue Wartezeit", st)
	}

	fm.fails = func(string) bool { return false }
	e.advance(retryLaterWait)
	if !exists(e.archived("Sitzung.srt")) || len(e.p.states) != 0 || len(e.p.cache) != 0 || len(e.llm.got) != 0 {
		t.Fatalf("Archiv %v, state %v, cache %v, LLM %d", exists(e.archived("Sitzung.srt")), e.p.states, e.p.cache, len(e.llm.got))
	}
}

// Fängt: relatives watch_dir "." — globale Schlüssel (ledger-refresh,
// watch-lock) dürfen nicht als Dateischlüssel gelten und gelöscht werden;
// die Warnung einer verschwundenen Datei wird trotzdem vergessen.
func TestPruneRelativeWatchDir(t *testing.T) {
	var dir string
	e := newEnv(t, func(o *Options, _ *Deps) { dir = o.WatchDir; o.WatchDir = "." })
	t.Chdir(dir)
	gone := e.write(t, "weg.pdf", "x", testM)
	e.p.warned["ledger-refresh"] = true
	e.p.warned["watch-lock"] = true
	e.p.Pass(context.Background(), e.clock)
	if !e.p.warned[gone] {
		t.Fatalf("warned = %v, want %q", e.p.warned, gone)
	}

	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	e.p.Pass(context.Background(), e.clock.Add(10*time.Second))
	if e.p.warned[gone] || !e.p.warned["ledger-refresh"] || !e.p.warned["watch-lock"] {
		t.Fatalf("warned = %v", e.p.warned)
	}
}

// Fängt: Präfix-Verwechslung — die Warnung zu verschwundener "a.srt.pdf"
// (nicht unterstützt) fällt weg, "a.srt" bleibt.
func TestPruneSimilarNames(t *testing.T) {
	e := newEnv(t, nil)
	e.llm.err = errTrans // a.srt bleibt liegen
	keep := e.write(t, "a.srt", testSRT, testM)
	gone := e.write(t, "a.srt.pdf", "x", testM)
	e.p.Pass(context.Background(), e.clock)
	if !e.p.warned[gone] {
		t.Fatalf("Ausgangslage: warned = %v", e.p.warned)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	e.p.warned[keep+"#read"] = true
	e.p.Pass(context.Background(), e.clock.Add(10*time.Second))
	if e.p.warned[gone] || !e.p.warned[keep+"#read"] {
		t.Fatalf("warned = %v", e.p.warned)
	}
}
