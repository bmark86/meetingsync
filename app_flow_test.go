package main

// Unit-Tests Ablauf (R-M0-08): Ledger laden, Pass-Gating (nextTry, near,
// Wartezeit), verwaiste Zusammenfassungen, Run mit abgebrochenem Kontext.
// Kein Notion: die App läuft mit notion=nil. Jeder Pfad, der process()
// erreicht, trifft den LLM-Fake und wird dort gezählt (hits).

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"meetingsync/internal/ledger"
)

// flowM ist die mtime der Eingaben (UTC, Archivmonat 2026-09).
var flowM = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

type flowEnv struct {
	app     *App
	watch   string
	archive string
	logBuf  *bytes.Buffer
	hits    *atomic.Int32
	status  *atomic.Int32 // Antwortstatus des LLM-Fakes
}

// newFlowApp baut eine App ohne Notion mit LLM-Fake. Der Fake antwortet mit
// 503, solange status nicht auf 200 steht; 200 liefert eine gültige Notiz.
func newFlowApp(t *testing.T, mod func(*Config)) *flowEnv {
	t.Helper()
	hits, status := &atomic.Int32{}, &atomic.Int32{}
	status.Store(503)
	note, _ := json.Marshal(goldenChat(goldenNoteWith(nil), "stop"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(int(status.Load()))
		if status.Load() == 200 {
			_, _ = w.Write(note)
		} else {
			_, _ = w.Write([]byte(`{"error":"Model is loading"}`))
		}
	}))
	t.Cleanup(srv.Close)

	root := t.TempDir()
	cfg := defaultConfig()
	cfg.WatchDir = filepath.Join(root, "watch")
	cfg.ArchiveDir = filepath.Join(root, "archiv")
	cfg.FailedDir = filepath.Join(root, "fehler")
	cfg.Notify = false
	cfg.loc = time.UTC
	cfg.LLM.BaseURL = srv.URL + "/v1"
	cfg.LLM.Model = "m"
	if mod != nil {
		mod(cfg)
	}
	if err := os.MkdirAll(cfg.WatchDir, 0o755); err != nil {
		t.Fatal(err)
	}
	buf := &bytes.Buffer{}
	logger := log.New(buf, "", 0)
	l, err := ledger.Load(filepath.Join(cfg.ArchiveDir, ".meetingsync-ledger.jsonl"), false)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{
		cfg: cfg, log: logger, llm: newLLMClient(cfg, logger, nil), ledger: l,
		state: map[string]*fileState{}, warned: map[string]bool{}, cache: map[string]cachedKind{},
	}
	return &flowEnv{app: app, watch: cfg.WatchDir, archive: cfg.ArchiveDir, logBuf: buf, hits: hits, status: status}
}

func (e *flowEnv) write(t *testing.T, name, content string, mtime time.Time) string {
	t.Helper()
	p := filepath.Join(e.watch, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return p
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func (e *flowEnv) archived(name string) string { return filepath.Join(e.archive, "2026-09", name) }

// --- Neustart: Ledger gegen Doppelverarbeitung ---------------------------------------

// Fängt: Pass ignoriert das geladene Ledger (Doppelverarbeitung nach Neustart)
// oder HasNear (Zusammenfassung zu verarbeitetem Transkript wird neue Notiz).
func TestPassAfterRestartUsesLoadedLedger(t *testing.T) {
	env := newFlowApp(t, nil)
	tr := env.write(t, "Alt.srt", goldenSRT, flowM)
	sum := env.write(t, "Alt Notizen.md", goldenSummaryMD, flowM.Add(60*time.Second))
	late := env.write(t, "Spaet.md", goldenSummaryMD+"\nAndere Fassung.\n", flowM.Add(100*time.Second))
	far := env.write(t, "Weit.md", goldenSummaryMD+"\nWeit weg.\n", flowM.Add(2*time.Hour))

	ledgerPath := filepath.Join(env.archive, ".meetingsync-ledger.jsonl")
	l1, _ := ledger.Load(ledgerPath, true)
	if err := l1.Add(ledger.Entry{Hash: sha256Hex([]byte(goldenSRT)), File: "Alt.srt", ModTime: flowM}); err != nil {
		t.Fatal(err)
	}
	// Neustart: frisches Ledger aus der Datei
	l2, err := ledger.Load(ledgerPath, false)
	if err != nil {
		t.Fatal(err)
	}
	env.app.ledger = l2

	env.app.Pass(context.Background(), flowM.Add(3*time.Hour))
	if env.hits.Load() != 1 {
		t.Fatalf("LLM-Aufrufe = %d, want 1 (nur die weit entfernte Zusammenfassung)", env.hits.Load())
	}
	for _, p := range []string{tr, sum, late} {
		if exists(p) {
			t.Fatalf("%s sollte archiviert sein", filepath.Base(p))
		}
		if !exists(env.archived(filepath.Base(p))) {
			t.Fatalf("%s fehlt im Archiv", filepath.Base(p))
		}
	}
	if !exists(far) {
		t.Fatal("Zusammenfassung außerhalb des Fensters ist keine Altlast: sie wird neu verarbeitet (LLM antwortet 503, die Datei bleibt zum Retry liegen)")
	}
	if !strings.Contains(env.logBuf.String(), "wurde schon verarbeitet") || !strings.Contains(env.logBuf.String(), "gehört zu einem bereits verarbeiteten Transkript") {
		t.Fatalf("Log ohne ↺/📎:\n%s", env.logBuf.String())
	}
}

// --- Orphan-Zusammenfassungen -------------------------------------------------------

func orphanEnv(t *testing.T, action string) (*flowEnv, string) {
	env := newFlowApp(t, func(c *Config) { c.OrphanSummaryAction = action })
	return env, env.write(t, "Notizen.md", goldenSummaryMD, flowM)
}

// Fängt: action=archive greift nicht / Wartezeit mit <= statt < geprüft / Archivmonat falsch.
func TestPassOrphanArchive(t *testing.T) {
	env, p := orphanEnv(t, "archive")
	wait := 10 * time.Minute
	env.app.Pass(context.Background(), flowM.Add(wait-time.Second))
	if !exists(p) {
		t.Fatal("vor Ablauf der Wartezeit darf nichts passieren")
	}
	env.app.Pass(context.Background(), flowM.Add(wait))
	if exists(p) || !exists(env.archived("Notizen.md")) {
		t.Fatal("nach Ablauf der Wartezeit muss die Datei im Archiv 2026-09 liegen")
	}
	if env.hits.Load() != 0 {
		t.Fatalf("archive darf das LLM nicht fragen, hits=%d", env.hits.Load())
	}
	if !strings.Contains(env.logBuf.String(), "orphan_summary_action=archive") {
		t.Fatalf("Log ohne Archiv-Hinweis:\n%s", env.logBuf.String())
	}
}

// Fängt: keep archiviert/verarbeitet doch / Warnung kommt in jedem Pass.
func TestPassOrphanKeepWarnsOnce(t *testing.T) {
	env, p := orphanEnv(t, "keep")
	now := flowM.Add(time.Hour)
	for i := 0; i < 3; i++ {
		env.app.Pass(context.Background(), now.Add(time.Duration(i)*10*time.Second))
	}
	if !exists(p) || env.hits.Load() != 0 {
		t.Fatalf("keep: Datei bleibt, kein LLM (exists=%v hits=%d)", exists(p), env.hits.Load())
	}
	if n := strings.Count(env.logBuf.String(), "bleibt liegen"); n != 1 {
		t.Fatalf("Warnung %d-mal, want 1:\n%s", n, env.logBuf.String())
	}
}

// Fängt: Standardaktion "note" fragt das LLM nicht oder ignoriert die Wartezeit.
func TestPassOrphanNoteCallsLLMAfterWait(t *testing.T) {
	env, p := orphanEnv(t, "note")
	env.app.Pass(context.Background(), flowM.Add(9*time.Minute))
	if env.hits.Load() != 0 {
		t.Fatal("vor Ablauf der Wartezeit kein LLM-Aufruf")
	}
	env.app.Pass(context.Background(), flowM.Add(10*time.Minute))
	if env.hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", env.hits.Load())
	}
	if !exists(p) {
		t.Fatal("bei Fehler bleibt die Datei liegen (Retry)")
	}
}

// Fängt: nextTry wird im Waisen-Zweig ignoriert (Retry-Sturm).
func TestPassOrphanNoteRespectsNextTry(t *testing.T) {
	env, p := orphanEnv(t, "note")
	now := flowM.Add(time.Hour)
	retryAt := now.Add(2 * time.Minute)
	env.app.state[p] = &fileState{attempts: 1, nextTry: retryAt}
	env.app.Pass(context.Background(), now)
	env.app.Pass(context.Background(), retryAt.Add(-time.Second))
	if env.hits.Load() != 0 {
		t.Fatalf("vor nextTry hits=%d, want 0", env.hits.Load())
	}
	env.app.Pass(context.Background(), retryAt)
	if env.hits.Load() != 1 {
		t.Fatalf("ab nextTry hits=%d, want 1", env.hits.Load())
	}
}

// --- near: Wartefenster für unfertige Dateien ------------------------------------------

// Fängt: near-Prüfung beim Transkript entfernt / Fenstergrenze (<=) verschoben.
func TestPassTranscriptWaitsForUnstableNeighbour(t *testing.T) {
	window := 180 * time.Second
	cases := []struct {
		name     string
		offset   time.Duration // mtime der unfertigen Datei relativ zum Transkript
		wantHits int32
	}{
		{"im Fenster", 60 * time.Second, 0},
		{"genau an der Grenze", window, 0},
		{"knapp außerhalb", window + time.Second, 1},
		{"vor dem Transkript", -window, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := newFlowApp(t, nil)
			env.write(t, "Sitzung.srt", goldenSRT, flowM)
			unstableAt := flowM.Add(c.offset)
			env.write(t, "Notizen.md", goldenSummaryMD, unstableAt)
			// jetzt: Zusammenfassung 5 s alt (unfertig, StableSeconds=20)
			env.app.Pass(context.Background(), unstableAt.Add(5*time.Second))
			if env.hits.Load() != c.wantHits {
				t.Fatalf("hits = %d, want %d", env.hits.Load(), c.wantHits)
			}
		})
	}
}

// Fängt: Paarung/near so verändert, dass das Paar nach dem Warten getrennt wird.
func TestPassPairProcessedOnceNeighbourIsStable(t *testing.T) {
	env := newFlowApp(t, nil)
	env.write(t, "Sitzung.srt", goldenSRT, flowM)
	env.write(t, "Sitzung.md", goldenSummaryMD, flowM.Add(60*time.Second))
	env.app.Pass(context.Background(), flowM.Add(65*time.Second))
	if env.hits.Load() != 0 {
		t.Fatal("solange die Zusammenfassung unfertig ist, wird gewartet")
	}
	env.app.Pass(context.Background(), flowM.Add(90*time.Second))
	if env.hits.Load() != 1 {
		t.Fatalf("Paar = eine Notiz, hits=%d", env.hits.Load())
	}
}

// Fängt: near-Prüfung im Waisen-Zweig entfernt (Zusammenfassung wird archiviert,
// obwohl ihr Transkript gerade noch geschrieben wird).
func TestPassOrphanSummaryWaitsForUnstableTranscript(t *testing.T) {
	env := newFlowApp(t, func(c *Config) { c.OrphanSummaryAction = "archive"; c.OrphanSummaryMinutes = 0 })
	sum := env.write(t, "Notizen.md", goldenSummaryMD, flowM)
	trAt := flowM.Add(60 * time.Second)
	env.write(t, "Sitzung.srt", goldenSRT, trAt)
	env.app.Pass(context.Background(), trAt.Add(5*time.Second))
	if !exists(sum) {
		t.Fatal("Zusammenfassung muss auf das unfertige Transkript warten")
	}
	env.app.Pass(context.Background(), trAt.Add(30*time.Second))
	if env.hits.Load() != 1 {
		t.Fatalf("Paar wird danach als eine Notiz verarbeitet, hits=%d", env.hits.Load())
	}
}

// --- Pass-Gating über nextTry und State -----------------------------------------------

// Fängt: nextTry wird ignoriert / Grenze (Before) verschoben / Fehlversuch zählt nicht.
func TestPassGatingByNextTry(t *testing.T) {
	env := newFlowApp(t, nil)
	p := env.write(t, "Sitzung.srt", goldenSRT, flowM)
	t0 := flowM.Add(5 * time.Minute)

	env.app.Pass(context.Background(), t0) // 503 → transient, 1 min Wartezeit
	st := env.app.state[p]
	if env.hits.Load() != 1 || st == nil || st.attempts != 1 || !st.nextTry.Equal(t0.Add(time.Minute)) {
		t.Fatalf("nach Fehler: hits=%d state=%+v", env.hits.Load(), st)
	}
	env.app.Pass(context.Background(), t0.Add(59*time.Second))
	if env.hits.Load() != 1 {
		t.Fatal("vor nextTry darf nicht erneut versucht werden")
	}
	env.app.Pass(context.Background(), t0.Add(time.Minute))
	if env.hits.Load() != 2 || env.app.state[p].attempts != 2 {
		t.Fatalf("genau ab nextTry wird erneut versucht: hits=%d state=%+v", env.hits.Load(), env.app.state[p])
	}
}

// Fängt: Erfolg räumt state nicht auf / Ledger-Eintrag fehlt / Datei wird doppelt verarbeitet.
// Mit dry=true wird nicht archiviert (die Datei bleibt liegen, das Ledger im
// Speicher verhindert die Wiederholung). Der echte Erfolgspfad mit Archiv und
// Ledger auf Platte ist durch die Golden-Szenarien (G01 u. a.) abgedeckt.
func TestPassSuccessClearsStateAndFillsLedger(t *testing.T) {
	env := newFlowApp(t, func(c *Config) { c.MaxAttempts = 5 })
	env.app.dry = true // Notion fehlt: Dry-Run liefert "dry-run" als Ergebnis
	p := env.write(t, "Sitzung.srt", goldenSRT, flowM)
	env.app.state[p] = &fileState{attempts: 2, transient: 2}
	env.status.Store(200)

	now := flowM.Add(5 * time.Minute)
	env.app.Pass(context.Background(), now)
	if env.app.state[p] != nil {
		t.Fatalf("state nach Erfolg: %+v", env.app.state[p])
	}
	if !env.app.ledger.Has(sha256Hex([]byte(goldenSRT))) {
		t.Fatal("Ledger enthält den Hash nicht")
	}
	env.app.Pass(context.Background(), now.Add(time.Minute))
	if env.hits.Load() != 1 {
		t.Fatalf("zweiter Pass darf das LLM nicht erneut fragen, hits=%d", env.hits.Load())
	}
	if !strings.Contains(env.logBuf.String(), "wurde schon verarbeitet") {
		t.Fatalf("Log ohne ↺:\n%s", env.logBuf.String())
	}
}

// Fängt: max_attempts im Pass nicht beachtet; Datei und Hinweis landen in _fehler,
// state wird gelöscht.
func TestPassGivesUpAfterMaxAttempts(t *testing.T) {
	env := newFlowApp(t, func(c *Config) { c.MaxAttempts = 2 })
	p := env.write(t, "Sitzung.srt", goldenSRT, flowM)
	now := flowM.Add(5 * time.Minute)
	env.app.Pass(context.Background(), now)
	env.app.Pass(context.Background(), now.Add(time.Minute))
	if exists(p) || !exists(filepath.Join(env.app.cfg.FailedDir, "Sitzung.srt")) || !exists(filepath.Join(env.app.cfg.FailedDir, "Sitzung.srt.fehler.txt")) {
		t.Fatal("Datei und .fehler.txt müssen in _fehler liegen")
	}
	if env.app.state[p] != nil {
		t.Fatal("state muss gelöscht sein")
	}
}

// Fängt: Pass läuft mit abgebrochenem Kontext weiter (LLM-Aufruf, Verschieben).
// Transkript mit gepaarter Zusammenfassung: fängt die Prüfung im Transkript-Zweig
// (ohne sie läuft handle an, sichtbar an der Logzeile "→ Sitzung.srt ("; LLM-Aufruf und state
// bleiben wegen des abgebrochenen Kontexts leer). Die Prüfung im
// Zusammenfassungs-Zweig fängt TestPassCanceledContextSummaryBranch.
func TestPassCanceledContextDoesNothing(t *testing.T) {
	env := newFlowApp(t, func(c *Config) { c.OrphanSummaryAction = "archive" })
	tr := env.write(t, "Sitzung.srt", goldenSRT, flowM)
	sum := env.write(t, "Notizen.md", goldenSummaryMD, flowM.Add(time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	env.app.Pass(ctx, flowM.Add(3*time.Hour))
	if env.hits.Load() != 0 || !exists(tr) || !exists(sum) {
		t.Fatalf("nichts darf passieren: hits=%d tr=%v sum=%v", env.hits.Load(), exists(tr), exists(sum))
	}
	if len(env.app.state) != 0 {
		t.Fatalf("kein state erwartet: %v", env.app.state)
	}
	if strings.Contains(env.logBuf.String(), "→ Sitzung.srt (") {
		t.Fatalf("handle darf nicht anlaufen:\n%s", env.logBuf.String())
	}
}

// Fängt: ctx-Prüfung in der Zusammenfassungs-Schleife entfernt (verwaiste
// Zusammenfassung wird trotz Abbruch archiviert).
func TestPassCanceledContextSummaryBranch(t *testing.T) {
	env := newFlowApp(t, func(c *Config) { c.OrphanSummaryAction = "archive" })
	sum := env.write(t, "Notizen.md", goldenSummaryMD, flowM)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	env.app.Pass(ctx, flowM.Add(time.Hour)) // Wartezeit (10 min) längst abgelaufen
	if !exists(sum) || exists(env.archived("Notizen.md")) {
		t.Fatal("Zusammenfassung darf nicht archiviert werden")
	}
}

func TestPassUnreadableWatchDirLogs(t *testing.T) {
	env := newFlowApp(t, nil)
	if err := os.Remove(env.watch); err != nil {
		t.Fatal(err)
	}
	env.app.Pass(context.Background(), flowM)
	if !strings.Contains(env.logBuf.String(), "Ordner nicht lesbar") {
		t.Fatalf("Log: %q", env.logBuf.String())
	}
}

// --- Run ------------------------------------------------------------------------------------

// Prüft nur: Run kehrt mit abgebrochenem Kontext zurück und loggt Start und Ende.
// Fängt: Run ignoriert ctx.Done() (hängt) / Start- oder Endmeldung fehlt.
func TestRunWithCanceledContextReturns(t *testing.T) {
	env := newFlowApp(t, nil)
	p := env.write(t, "Sitzung.srt", goldenSRT, time.Now().Add(-time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() { env.app.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run kehrt mit abgebrochenem Kontext nicht zurück")
	}
	out := env.logBuf.String()
	if !strings.Contains(out, "Überwache "+env.watch) || !strings.Contains(out, "Beendet.") {
		t.Fatalf("Log ohne Start-/Endmeldung:\n%s", out)
	}
	if env.hits.Load() != 0 || !exists(p) {
		t.Fatalf("mit abgebrochenem Kontext keine Arbeit: hits=%d exists=%v", env.hits.Load(), exists(p))
	}
}

// Fängt: Run führt keinen ersten Pass sofort aus (wartet auf den Ticker, hier 1 h).
func TestRunPassesImmediatelyThenStopsOnCancel(t *testing.T) {
	env := newFlowApp(t, func(c *Config) { c.PollIntervalSeconds = 3600 })
	env.write(t, "Sitzung.srt", goldenSRT, time.Now().Add(-time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { env.app.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for env.hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run endet nach cancel nicht")
	}
	if env.hits.Load() != 1 {
		t.Fatalf("erster Pass sofort: hits=%d, want 1", env.hits.Load())
	}
}
