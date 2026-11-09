package pipeline

// Tests der Sperre und des Nachlesens im Durchlauf (R-M3-14).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"meetingsync/internal/ledger"
)

const lockMsg = "Anderer Lauf aktiv (-file oder zweiter Dienst) – Durchlauf übersprungen"

// Fängt: Pass läuft trotz gehaltener Sperre / meldet sie bei jedem Durchlauf /
// nimmt die Arbeit nach der Freigabe nicht wieder auf / meldet eine neue
// Sperrzeit nicht.
func TestPassSkippedWhileLocked(t *testing.T) {
	e := newEnv(t, nil)
	e.write(t, "Sitzung.srt", testSRT, testM)
	lk, err := ledger.TryLock(e.o.WatchDir) // anderer Halter (eigener Dateideskriptor)
	if err != nil {
		t.Fatal(err)
	}
	e.p.Pass(context.Background(), e.clock)
	e.p.Pass(context.Background(), e.clock)
	if len(e.notion.pages) != 0 || len(e.llm.got) != 0 || strings.Count(e.log.String(), lockMsg) != 1 {
		t.Fatalf("gesperrt: Seiten %d, LLM %d, Log:\n%s", len(e.notion.pages), len(e.llm.got), e.log)
	}
	if err := lk.Unlock(); err != nil {
		t.Fatal(err)
	}
	e.p.Pass(context.Background(), e.clock)
	if e.notion.count("notes-ds") != 1 || !exists(e.archived("Sitzung.srt")) {
		t.Fatalf("nach Freigabe: Notizen %d, Log:\n%s", e.notion.count("notes-ds"), e.log)
	}
	// Sperre wieder frei? Ein zweiter Halter bekommt sie sofort.
	lk, err = ledger.TryLock(e.o.WatchDir)
	if err != nil {
		t.Fatalf("Pass hat die Sperre nicht freigegeben: %v", err)
	}
	defer func() { _ = lk.Unlock() }()
	e.p.Pass(context.Background(), e.clock)
	if strings.Count(e.log.String(), lockMsg) != 2 {
		t.Fatalf("neue Sperrzeit nicht gemeldet, Log:\n%s", e.log)
	}
}

// Fängt: Pass läuft ohne Sperre weiter, ohne es zu melden, wenn sie aus
// einem anderen Grund als "belegt" oder "Ordner fehlt" scheitert / meldet es
// bei jedem Durchlauf.
func TestPassWarnsWhenLockImpossible(t *testing.T) {
	e := newEnv(t, nil)
	if err := os.Chmod(e.o.WatchDir, 0o000); err != nil { // Öffnen scheitert (EACCES)
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(e.o.WatchDir, 0o755) }()
	e.p.Pass(context.Background(), e.clock)
	e.p.Pass(context.Background(), e.clock)
	const msg = "⚠️  Sperre auf dem Exportordner nicht möglich ("
	if strings.Count(e.log.String(), msg) != 1 || !strings.Contains(e.log.String(), ") – Schutz gegen gleichzeitige Läufe aus") || strings.Contains(e.log.String(), lockMsg) {
		t.Fatalf("Log:\n%s", e.log)
	}
}

// Fängt: der Dry-Run wartet auf die Sperre (er schreibt nichts).
func TestPassDryIgnoresLock(t *testing.T) {
	e := newEnv(t, func(o *Options, _ *Deps) { o.Dry = true })
	e.write(t, "Sitzung.srt", testSRT, testM)
	lk, err := ledger.TryLock(e.o.WatchDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lk.Unlock() }()
	e.p.Pass(context.Background(), e.clock)
	if len(e.llm.got) != 1 || strings.Contains(e.log.String(), lockMsg) {
		t.Fatalf("Dry-Run: LLM %d, Log:\n%s", len(e.llm.got), e.log)
	}
}

// Fängt: Pass liest Zeilen eines anderen Prozesses nicht nach (zweite Seite).
func TestPassRefreshesLedger(t *testing.T) {
	e := newEnv(t, nil)
	e.write(t, "Sitzung.srt", testSRT, testM)
	other, err := ledger.Load(filepath.Join(e.o.ArchiveDir, ".meetingsync-ledger.jsonl"), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Add(ledger.Entry{Hash: sha256Hex([]byte(testSRT)), File: "Sitzung.srt", ModTime: testM, URL: "https://notion.so/x", At: testM}); err != nil {
		t.Fatal(err)
	}
	e.p.Pass(context.Background(), e.clock)
	if len(e.notion.pages) != 0 || !exists(e.archived("Sitzung.srt")) || !strings.Contains(e.log.String(), "↺ Sitzung.srt wurde schon verarbeitet → Archiv") {
		t.Fatalf("Seiten %d, Log:\n%s", len(e.notion.pages), e.log)
	}
}
