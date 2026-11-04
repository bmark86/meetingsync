package pipeline

// Unit-Tests des Ablaufs mit Fakes für LLM, Notion und Mitteilung auf echten
// Temp-Ordnern. Die Integrationstests über App, HTTP-Fakes und Absturz liegen
// im Paket main (app_*_test.go, e2e_test.go, golden).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"meetingsync/internal/domain"
	"meetingsync/internal/ledger"
	"meetingsync/internal/llm"
	"meetingsync/internal/notion"
	"meetingsync/internal/watch"
)

const testSummary = "Zusammenfassung\n\nEs ging um die Pipeline.\n"

// ledgerLines liest die Ledger-Datei roh (eine Map je Zeile).
func (e *testEnv) ledgerLines(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(mustRead(t, filepath.Join(e.o.ArchiveDir, ".meetingsync-ledger.jsonl"))), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

// Fängt: begonnen nach statt vor CreatePage, fertig fehlt, Zeit nicht von der
// injizierten Uhr, Partner nicht archiviert, Mitteilung, Projekt und
// Zusammenfassung als Kontext fehlen.
func TestPassPairLedgerOrderAndArchive(t *testing.T) {
	e := newEnv(t, func(o *Options, _ *Deps) { o.Notify = true; o.UseSummaryAsContext = true })
	e.notion.projects = []notion.Page{{ID: "p1", Name: "Sirius"}}
	var atCreate []map[string]any
	e.notion.onCreate = func(ds string) error {
		if ds == "notes-ds" {
			atCreate = e.ledgerLines(t)
		}
		return nil
	}
	tr := e.write(t, "Sitzung.srt", testSRT, testM)
	sum := e.write(t, "Sitzung.md", testSummary, testM.Add(time.Minute))

	e.p.Pass(context.Background(), e.clock)

	if len(atCreate) != 1 || atCreate[0]["status"] != ledger.StatusBegonnen {
		t.Fatalf("Ledger bei CreatePage = %v, want genau begonnen", atCreate)
	}
	lines := e.ledgerLines(t)
	if len(lines) != 2 || lines[1]["hash"] != sha256Hex([]byte(testSRT)) || lines[1]["url"] != "https://notion.so/page-1" || lines[1]["status"] != nil {
		t.Fatalf("Ledger = %v, want begonnen + fertig", lines)
	}
	for _, ln := range lines {
		if at, _ := time.Parse(time.RFC3339Nano, ln["at"].(string)); !at.Equal(e.clock) {
			t.Fatalf("at = %v, want Uhr der Pipeline %v", ln["at"], e.clock)
		}
	}
	if e.notion.count("notes-ds") != 1 || e.notion.count("tasks-ds") != 1 {
		t.Fatalf("Seiten = %+v, want 1 Notiz + 1 eigenes To-do", e.notion.pages)
	}
	if exists(tr) || exists(sum) || !exists(e.archived("Sitzung.srt")) || !exists(e.archived("Sitzung.md")) {
		t.Fatal("Paar nicht archiviert")
	}
	if len(e.llm.got) != 1 || e.llm.got[0].SummaryText != testSummary || len(e.llm.got[0].Projects) != 1 {
		t.Fatalf("LLM-Eingabe = %+v", e.llm.got)
	}
	if len(e.notifier.calls) != 1 || e.notifier.calls[0] != [2]string{"Neue Meeting-Notiz", "Sync · 1 To-do(s) für dich · bitte prüfen"} {
		t.Fatalf("Mitteilungen = %v", e.notifier.calls)
	}
	if len(e.p.States()) != 0 {
		t.Fatalf("Zustand nach Erfolg: %v", e.p.States())
	}

	// Zweiter Durchlauf mit identischer Kopie: schon verarbeitet → Archiv, kein LLM.
	e.write(t, "Kopie.srt", testSRT, testM)
	e.p.Pass(context.Background(), e.clock)
	if len(e.llm.got) != 1 || !exists(e.archived("Kopie.srt")) || !strings.Contains(e.log.String(), "↺ Kopie.srt wurde schon verarbeitet") {
		t.Fatalf("Kopie: LLM %d, Log:\n%s", len(e.llm.got), e.log)
	}
}

// Fängt: Neustart-Regel legt neu an, Reihenfolge Partner vor Transkript,
// verworfen fehlt, Kopie im selben Durchlauf läuft normal.
func TestPassInterruptedToFailed(t *testing.T) {
	e := newEnv(t, nil)
	hash := sha256Hex([]byte(testSRT))
	if err := e.ledger.Add(ledger.Entry{Hash: hash, Status: ledger.StatusBegonnen, File: "x", ModTime: testM}); err != nil {
		t.Fatal(err)
	}
	e.write(t, "A.srt", testSRT, testM)
	e.write(t, "A.md", testSummary, testM.Add(time.Second))
	e.write(t, "B.srt", testSRT, testM.Add(time.Hour)) // Kopie, eigenes Paar-Fenster
	e.p.Pass(context.Background(), e.clock.Add(time.Hour))

	if len(e.llm.got) != 0 || len(e.notion.pages) != 0 {
		t.Fatal("Neustart-Regel darf weder LLM noch Notion fragen")
	}
	for _, n := range []string{"A.srt", "A.md", "B.srt"} {
		if got := mustRead(t, filepath.Join(e.o.FailedDir, n+".fehler.txt")); !strings.Contains(got, ErrUnterbrochen.Error()) {
			t.Fatalf("%s.fehler.txt = %q", n, got)
		}
	}
	lines := e.ledgerLines(t)
	if len(lines) != 2 || lines[1]["status"] != ledger.StatusVerworfen {
		t.Fatalf("Ledger = %v, want begonnen + genau ein verworfen", lines)
	}
}

// Fängt: Verschieben nach _fehler scheitert, trotzdem verworfen; kein Backoff.
func TestInterruptedMoveFailsRetriesLater(t *testing.T) {
	e := newEnv(t, func(_ *Options, d *Deps) {
		d.Mover = watch.Mover{Rename: func(string, string) error { return os.ErrPermission }}
	})
	hash := sha256Hex([]byte(testSRT))
	if err := e.ledger.Add(ledger.Entry{Hash: hash, Status: ledger.StatusBegonnen, File: "x", ModTime: testM}); err != nil {
		t.Fatal(err)
	}
	tr := e.write(t, "A.srt", testSRT, testM)
	e.p.Pass(context.Background(), e.clock)
	st := e.p.States()[tr]
	if st == nil || st.Attempts != 0 || !st.NextTry.Equal(e.clock.Add(2*time.Minute)) || !e.ledger.Started(hash) {
		t.Fatalf("state = %+v, Started = %v; want NextTry +2 min, begonnen", st, e.ledger.Started(hash))
	}
	e.p.Pass(context.Background(), e.clock.Add(time.Minute))
	if n := strings.Count(e.log.String(), "unterbrochen"); n != 1 {
		t.Fatalf("Neustart-Regel vor Ablauf der Wartezeit erneut (%d)", n)
	}
}

// Fängt: Waisen-Aktionen vertauscht, Wartezeit ignoriert, HasNear ohne Archiv.
func TestPassOrphanActions(t *testing.T) {
	for _, tc := range []struct {
		action   string
		archived bool
		llm      int
		log      string
	}{
		{"archive", true, 0, "orphan_summary_action=archive"},
		{"keep", false, 0, "orphan_summary_action=keep"},
		{"note", true, 1, "→ Notizen.md (nur Zusammenfassung"},
	} {
		t.Run(tc.action, func(t *testing.T) {
			e := newEnv(t, func(o *Options, _ *Deps) { o.OrphanAction = tc.action })
			p := e.write(t, "Notizen.md", testSummary, testM)
			e.p.Pass(context.Background(), testM.Add(9*time.Minute))
			if !exists(p) || len(e.llm.got) != 0 {
				t.Fatal("vor Ablauf der Wartezeit passiert nichts")
			}
			e.p.Pass(context.Background(), testM.Add(10*time.Minute))
			if exists(e.archived("Notizen.md")) != tc.archived || len(e.llm.got) != tc.llm || !strings.Contains(e.log.String(), tc.log) {
				t.Fatalf("archiviert=%v LLM=%d Log:\n%s", exists(e.archived("Notizen.md")), len(e.llm.got), e.log)
			}
			if tc.llm == 1 && !strings.Contains(e.llm.got[0].Text, "Es ging um die Pipeline.") {
				t.Fatalf("Text = %q", e.llm.got[0].Text)
			}
		})
	}
	t.Run("HasNear", func(t *testing.T) {
		e := newEnv(t, nil)
		if err := e.ledger.Add(ledger.Entry{Hash: "h", File: "x.srt", ModTime: testM}); err != nil {
			t.Fatal(err)
		}
		e.write(t, "Notizen.md", testSummary, testM.Add(time.Minute))
		e.p.Pass(context.Background(), e.clock)
		if !exists(e.archived("Notizen.md")) || len(e.llm.got) != 0 {
			t.Fatal("Zusammenfassung zu verarbeitetem Transkript muss ins Archiv")
		}
	})
}

// Fängt: Dry-Run legt Seiten an, schreibt Protokoll oder verschiebt.
func TestPassDryRunCallsPrinter(t *testing.T) {
	var gotTitle string
	var gotProject *notion.Page
	e := newEnv(t, func(o *Options, d *Deps) {
		o.Dry = true
		d.DryRun = func(title string, project *notion.Page, _ *domain.MeetingNote, _ []string, blocks int) {
			gotTitle, gotProject = title, project
			if blocks == 0 {
				t.Error("keine Blöcke")
			}
		}
	})
	e.notion.projects = []notion.Page{{ID: "p1", Name: "sirius"}}
	p := e.write(t, "Sitzung.srt", testSRT, testM)
	e.p.Pass(context.Background(), e.clock)
	if gotTitle == "" || gotProject == nil || gotProject.ID != "p1" {
		t.Fatalf("DryRun: Titel %q, Projekt %+v", gotTitle, gotProject)
	}
	if len(e.notion.pages) != 0 || !exists(p) || exists(e.archived("Sitzung.srt")) {
		t.Fatal("Dry-Run hat Seiten angelegt oder verschoben")
	}
	if !e.ledger.Has(sha256Hex([]byte(testSRT))) {
		t.Fatal("im Speicher gilt der Hash als fertig")
	}
}

// Fängt: -file schreibt Protokoll oder erkennt Zusammenfassungen nicht.
func TestProcessFileNoLedger(t *testing.T) {
	e := newEnv(t, nil)
	p := e.write(t, "Notizen.md", testSummary, testM)
	url, err := e.p.ProcessFile(context.Background(), &watch.Entry{Path: p, Name: "Notizen.md", ModTime: testM, Kind: domain.KindSummary, Content: []byte(testSummary)})
	if err != nil || url != "https://notion.so/page-1" {
		t.Fatalf("url=%q err=%v", url, err)
	}
	if !e.llm.got[0].SummaryOnly || exists(filepath.Join(e.o.ArchiveDir, ".meetingsync-ledger.jsonl")) || !exists(p) {
		t.Fatal("-file: nur Zusammenfassung, kein Protokoll, Datei bleibt")
	}
	if _, err := e.p.ProcessFile(context.Background(), &watch.Entry{Path: p, Name: "leer.md", Kind: domain.KindSummary, Content: []byte(" \n")}); err == nil || err.Error() != "Datei ist leer" {
		t.Fatalf("leere Datei: err=%v", err)
	}
}

// Fängt: Cache der Projektliste ignoriert die Uhr; Fehler verwirft die alte Liste.
func TestActiveProjectsCache(t *testing.T) {
	e := newEnv(t, nil)
	e.notion.projects = []notion.Page{{ID: "p1", Name: "A"}}
	if got := e.p.ActiveProjects(context.Background()); len(got) != 1 || e.notion.projCalls != 1 {
		t.Fatalf("erster Abruf: %v, Aufrufe %d", got, e.notion.projCalls)
	}
	e.clock = e.clock.Add(projectsTTL - time.Second)
	e.p.ActiveProjects(context.Background())
	if e.notion.projCalls != 1 {
		t.Fatal("innerhalb der TTL kein neuer Abruf")
	}
	e.clock = e.clock.Add(time.Second)
	e.notion.projErr = errors.New("weg")
	if got := e.p.ActiveProjects(context.Background()); len(got) != 1 || e.notion.projCalls != 2 || !strings.Contains(e.log.String(), "Projektliste nicht abrufbar: weg") {
		t.Fatalf("nach TTL mit Fehler: %v, Aufrufe %d, Log:\n%s", got, e.notion.projCalls, e.log)
	}
	n := newEnv(t, func(_ *Options, d *Deps) { d.Notion = nil })
	if n.p.ActiveProjects(context.Background()) != nil {
		t.Fatal("ohne Notion keine Projekte")
	}
}

// Fängt: Fehlerantwort von CreatePage lässt begonnen stehen; Lücken
// (Blöcke, To-dos) nicht gezählt oder als Fehler gewertet.
func TestProcessCreatePageErrorsAndGaps(t *testing.T) {
	t.Run("Fehler", func(t *testing.T) {
		e := newEnv(t, nil)
		e.notion.onCreate = func(string) error { return errors.New("400") }
		p := e.write(t, "Sitzung.srt", testSRT, testM)
		e.p.Pass(context.Background(), e.clock)
		lines := e.ledgerLines(t)
		if len(lines) != 2 || lines[1]["status"] != ledger.StatusVerworfen || !exists(p) {
			t.Fatalf("Ledger = %v, Datei bleibt = %v", lines, exists(p))
		}
		if st := e.p.States()[p]; st == nil || st.Attempts != 1 || !st.NextTry.Equal(e.clock.Add(2*time.Minute)) {
			t.Fatalf("state = %+v", st)
		}
	})
	t.Run("Lücken", func(t *testing.T) {
		e := newEnv(t, func(o *Options, _ *Deps) { o.CreateTasksFor = "all" })
		taskCalls := 0
		e.notion.onCreate = func(ds string) error {
			if ds == "notes-ds" {
				return notion.ErrUnvollstaendig
			}
			if taskCalls++; taskCalls == 1 {
				return errors.New("400")
			}
			return nil
		}
		e.write(t, "Sitzung.srt", testSRT, testM)
		e.p.Pass(context.Background(), e.clock)
		lines := e.ledgerLines(t)
		if ln := lines[len(lines)-1]; ln["bloecke_fehler"] != 1.0 || ln["todos_fehlen"] != 1.0 {
			t.Fatalf("fertig-Zeile = %v", ln)
		}
		if !strings.Contains(e.log.String(), "Sitzung.srt: Notiz unvollständig – 1 To-do(s) nicht angelegt, 1 Fehler beim Anhängen von Blöcken") {
			t.Fatalf("Log:\n%s", e.log)
		}
	})
	t.Run("ohne Tasks", func(t *testing.T) {
		e := newEnv(t, func(o *Options, _ *Deps) { o.CreateTasksFor = "none" })
		e.write(t, "Sitzung.srt", testSRT, testM)
		e.p.Pass(context.Background(), e.clock)
		if e.notion.count("tasks-ds") != 0 || e.notion.count("notes-ds") != 1 {
			t.Fatalf("Seiten = %+v", e.notion.pages)
		}
	})
}

// Fängt: begonnen lässt sich nicht schreiben und CreatePage läuft trotzdem.
func TestProcessBeginLedgerErrorNoCreatePage(t *testing.T) {
	e := newEnv(t, nil)
	if err := os.MkdirAll(filepath.Join(e.o.ArchiveDir, ".meetingsync-ledger.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := e.write(t, "Sitzung.srt", testSRT, testM)
	e.p.Pass(context.Background(), e.clock)
	if len(e.notion.pages) != 0 || !strings.Contains(e.log.String(), "Protokoll nicht schreibbar, Notion-Seite nicht angelegt") {
		t.Fatalf("Seiten %d, Log:\n%s", len(e.notion.pages), e.log)
	}
	if e.ledger.Started(sha256Hex([]byte(testSRT))) || e.p.States()[p].Attempts != 1 {
		t.Fatal("Speicher muss verworfen sein, Fehler zählt als Versuch")
	}
}

// Fängt: Pass arbeitet trotz Abbruch oder bei unlesbarem Ordner weiter.
func TestPassCanceledAndUnreadable(t *testing.T) {
	e := newEnv(t, nil)
	p := e.write(t, "Sitzung.srt", testSRT, testM)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e.p.Pass(ctx, e.clock)
	if len(e.llm.got) != 0 || !exists(p) {
		t.Fatal("mit Abbruch keine Arbeit")
	}
	if err := os.RemoveAll(e.o.WatchDir); err != nil {
		t.Fatal(err)
	}
	e.p.Pass(context.Background(), e.clock)
	if !strings.Contains(e.log.String(), "Ordner nicht lesbar") {
		t.Fatalf("Log:\n%s", e.log)
	}
}

// statFS ist watch.OS mit fester Antwort von Lstat für einen Pfad.
type statFS struct {
	watch.OS
	path  string
	info  fs.FileInfo
	calls int
}

func (s *statFS) Lstat(path string) (fs.FileInfo, error) {
	if path == s.path {
		s.calls++
		return s.info, nil
	}
	return s.OS.Lstat(path)
}

// Fängt: changedSinceRead fragt nicht das injizierte FS (sondern os direkt).
func TestChangedSinceReadUsesFS(t *testing.T) {
	sfs := &statFS{}
	e := newEnv(t, func(_ *Options, d *Deps) { d.FS = sfs })
	p := e.write(t, "a.txt", "alt\n", testM)
	other := e.write(t, "b.txt", "länger\n", testM.Add(time.Second))
	info, err := os.Lstat(other)
	if err != nil {
		t.Fatal(err)
	}
	sfs.path, sfs.info = p, info
	if !e.p.changedSinceRead(&watch.Entry{Path: p, Size: 4, ModTime: testM}) || sfs.calls != 1 {
		t.Fatalf("geändert laut FS nicht erkannt (Aufrufe %d)", sfs.calls)
	}
	if e.p.changedSinceRead(&watch.Entry{Path: other, Size: info.Size(), ModTime: info.ModTime()}) {
		t.Fatal("unverändert als geändert gemeldet")
	}
	if e.p.changedSinceRead(&watch.Entry{Path: filepath.Join(e.o.WatchDir, "fehlt")}) {
		t.Fatal("fehlende Datei gilt als unverändert")
	}
}

// Fängt: Dry-Run verschiebt bei fail.
func TestFailDryRunMovesNothing(t *testing.T) {
	e := newEnv(t, func(o *Options, _ *Deps) { o.Dry = true })
	p := e.write(t, "a.txt", "x", testM)
	if !e.p.fail(errOther, &watch.Entry{Path: p, Name: "a.txt"}) || !exists(p) {
		t.Fatal("Dry-Run: fail ok und Datei bleibt")
	}
}

// Fängt: fehlender Notifier führt bei Options.Notify zu einem nil-Zugriff.
func TestNewNilNotifierIsNoop(t *testing.T) {
	e := newEnv(t, func(o *Options, d *Deps) { o.Notify = true; d.Notifier = nil })
	e.write(t, "Sitzung.srt", testSRT, testM)

	e.p.Pass(context.Background(), e.clock)

	if e.notion.count("notes-ds") != 1 {
		t.Fatalf("Notiz nicht angelegt, Log:\n%s", e.log.String())
	}
}

// Fängt: fehlendes DryRun führt im Dry-Run zu einem nil-Zugriff.
func TestNewNilDryRunIsNoop(t *testing.T) {
	e := newEnv(t, func(o *Options, d *Deps) { o.Dry = true; d.DryRun = nil })
	e.write(t, "Sitzung.srt", testSRT, testM)

	e.p.Pass(context.Background(), e.clock)

	if len(e.llm.got) != 1 || e.notion.count("notes-ds") != 0 {
		t.Fatalf("Dry-Run: LLM-Aufrufe %d, Notizen %d, Log:\n%s", len(e.llm.got), e.notion.count("notes-ds"), e.log.String())
	}
}

// noteWithBlocks: testNote mit so vielen Entscheidungen, dass die Notiz total
// Blöcke hat (ohne Transkript, mit Prüfhinweis wegen fuer_mich, wie notion.CountBlocks).
func noteWithBlocks(total int) func() *domain.MeetingNote {
	build := func(k int) *domain.MeetingNote {
		n := testNote()
		for i := range k {
			n.Entscheidungen = append(n.Entscheidungen, fmt.Sprintf("Entscheidung %d", i))
		}
		llm.Normalize(n)
		return n
	}
	k := total - notion.CountBlocks(build(1), domain.NoteMeta{ReviewReasons: []string{reasonToDoFuerMich}}) + 1
	return func() *domain.MeetingNote { return build(k) }
}

// Fängt: Notiz über der Blockgrenze legt eine Seite an, schreibt "begonnen",
// zählt gegen max_attempts statt sofort nach _fehler oder meldet nichts.
func TestPassTooManyBlocksToFailed(t *testing.T) {
	e := newEnv(t, func(o *Options, _ *Deps) { o.Notify = true })
	e.llm.note = noteWithBlocks(5000)
	tr := e.write(t, "Sitzung.srt", testSRT, testM)
	sum := e.write(t, "Sitzung.md", testSummary, testM.Add(time.Minute))

	e.p.Pass(context.Background(), e.clock)

	if len(e.notion.pages) != 0 {
		t.Fatalf("Seiten = %+v, want keine", e.notion.pages)
	}
	if exists(filepath.Join(e.o.ArchiveDir, ".meetingsync-ledger.jsonl")) || e.ledger.Started(sha256Hex([]byte(testSRT))) || e.ledger.Has(sha256Hex([]byte(testSRT))) {
		t.Fatal("kein Protokolleintrag erwartet")
	}
	if exists(tr) || exists(sum) {
		t.Fatal("Dateien müssen aus dem Exportordner weg sein")
	}
	for _, n := range []string{"Sitzung.srt", "Sitzung.md"} {
		if got := mustRead(t, filepath.Join(e.o.FailedDir, n+".fehler.txt")); !strings.Contains(got, "zu viele Blöcke (5000, höchstens 1000)") {
			t.Fatalf("%s.fehler.txt = %q", n, got)
		}
	}
	want := [2]string{"Meeting-Notiz nicht angelegt", "Sitzung.srt: zu viele Blöcke (5000, höchstens 1000)"}
	if len(e.notifier.calls) != 1 || e.notifier.calls[0] != want {
		t.Fatalf("Mitteilungen = %v", e.notifier.calls)
	}
	if len(e.p.States()) != 0 {
		t.Fatalf("Zustand = %v, want keiner (kein Versuch gezählt)", e.p.States())
	}
	if !strings.Contains(e.log.String(), "✗ zu viele Blöcke (5000, höchstens 1000) – Notion-Seite nicht angelegt") {
		t.Fatalf("Log:\n%s", e.log)
	}
	e.p.Pass(context.Background(), e.clock.Add(time.Hour))
	if len(e.llm.got) != 1 {
		t.Fatalf("LLM-Aufrufe = %d, want kein neuer Versuch", len(e.llm.got))
	}
}

// Fängt: Grenze um eins verschoben oder Transkript mitgezählt.
func TestPassBlockCapBoundary(t *testing.T) {
	transcriptOpt := func(o *Options, _ *Deps) { o.IncludeTranscript = true }
	e := newEnv(t, transcriptOpt)
	e.llm.note = noteWithBlocks(notion.MaxBloecke)
	e.write(t, "Sitzung.srt", testSRT, testM)
	e.p.Pass(context.Background(), e.clock)
	if e.notion.count("notes-ds") != 1 || failedFiles(e.o.FailedDir) != 0 {
		t.Fatalf("genau an der Grenze: Notizen %d, Log:\n%s", e.notion.count("notes-ds"), e.log)
	}

	e = newEnv(t, transcriptOpt)
	e.llm.note = noteWithBlocks(notion.MaxBloecke + 1)
	e.write(t, "Sitzung.srt", testSRT, testM)
	e.p.Pass(context.Background(), e.clock)
	if len(e.notion.pages) != 0 || !exists(filepath.Join(e.o.FailedDir, "Sitzung.srt.fehler.txt")) {
		t.Fatalf("eins über der Grenze: Seiten %d, Log:\n%s", len(e.notion.pages), e.log)
	}
}

// Fängt: Dry-Run über der Grenze verschiebt, druckt das Ergebnis, meldet oder
// merkt den Hash als fertig.
func TestPassTooManyBlocksDryRun(t *testing.T) {
	printed := false
	e := newEnv(t, func(o *Options, d *Deps) {
		o.Dry, o.Notify = true, true
		d.DryRun = func(string, *notion.Page, *domain.MeetingNote, []string, int) { printed = true }
	})
	e.llm.note = noteWithBlocks(5000)
	p := e.write(t, "Sitzung.srt", testSRT, testM)
	e.p.Pass(context.Background(), e.clock)
	if printed || !exists(p) || failedFiles(e.o.FailedDir) != 0 || len(e.notifier.calls) != 0 || e.ledger.Has(sha256Hex([]byte(testSRT))) {
		t.Fatalf("Dry-Run: gedruckt %v, Datei bleibt %v, Mitteilungen %v", printed, exists(p), e.notifier.calls)
	}
	if !strings.Contains(e.log.String(), "zu viele Blöcke (5000, höchstens 1000)") || strings.Contains(e.log.String(), "→ "+e.o.FailedDir) {
		t.Fatalf("Log:\n%s", e.log)
	}
	// Watch-Schleife: kein neuer LLM-Lauf im direkt folgenden Durchlauf
	e.p.Pass(context.Background(), e.clock.Add(10*time.Second))
	if len(e.llm.got) != 1 {
		t.Fatalf("LLM-Aufrufe = %d, want 1 (Wartezeit)", len(e.llm.got))
	}
}

// Fängt: Verschieben nach _fehler scheitert, trotzdem Zustand gelöscht (LLM-Lauf
// und Mitteilung bei jedem Durchlauf) oder Mitteilung ohne verschobene Datei.
func TestPassTooManyBlocksMoveFails(t *testing.T) {
	e := newEnv(t, func(o *Options, d *Deps) {
		o.Notify = true
		d.Mover = watch.Mover{Rename: func(string, string) error { return os.ErrPermission }}
	})
	e.llm.note = noteWithBlocks(5000)
	tr := e.write(t, "Sitzung.srt", testSRT, testM)
	e.p.Pass(context.Background(), e.clock)
	st := e.p.States()[tr]
	if !exists(tr) || len(e.notifier.calls) != 0 || st == nil || st.Attempts != 0 || !st.NextTry.Equal(e.clock.Add(2*time.Minute)) {
		t.Fatalf("Datei bleibt %v, Mitteilungen %v, state %+v", exists(tr), e.notifier.calls, st)
	}
	e.p.Pass(context.Background(), e.clock.Add(10*time.Second))
	if len(e.llm.got) != 1 {
		t.Fatalf("LLM-Aufrufe = %d, want 1 (Wartezeit)", len(e.llm.got))
	}
}

// Fängt: Blockgrenze verschiebt das Transkript vor dem Partner, macht nach dem
// ersten Fehler weiter oder meldet ohne verschobene Dateien.
func TestPassTooManyBlocksPairOrder(t *testing.T) {
	fm := &flakyMover{fails: func(src string) bool { return strings.HasSuffix(src, ".md") }}
	e := newEnv(t, func(o *Options, d *Deps) { o.Notify = true; d.Mover = fm.mover() })
	e.llm.note = noteWithBlocks(5000)
	tr := e.write(t, "Sitzung.srt", testSRT, testM)
	sum := e.write(t, "Sitzung.md", testSummary, testM.Add(time.Minute))

	e.p.Pass(context.Background(), e.clock)
	st := e.p.States()[tr]
	if len(fm.tried) != 1 || fm.tried[0] != sum || !exists(tr) || !exists(sum) || len(e.notifier.calls) != 0 {
		t.Fatalf("Reihenfolge %v, Mitteilungen %v", fm.tried, e.notifier.calls)
	}
	if st == nil || st.Attempts != 0 || !st.NextTry.Equal(e.clock.Add(2*time.Minute)) || e.p.States()[sum] != nil {
		t.Fatalf("states %v", e.p.States())
	}

	fm.fails = func(string) bool { return false }
	e.p.Pass(context.Background(), e.clock.Add(2*time.Minute))
	if exists(tr) || exists(sum) || len(e.notifier.calls) != 1 || len(e.p.States()) != 0 {
		t.Fatalf("Paar bleibt %v/%v, Mitteilungen %v, states %v", exists(tr), exists(sum), e.notifier.calls, e.p.States())
	}
	for _, n := range []string{"Sitzung.srt", "Sitzung.md"} {
		if !exists(filepath.Join(e.o.FailedDir, n+".fehler.txt")) {
			t.Fatalf("%s.fehler.txt fehlt", n)
		}
	}
}

// Fängt: -file über der Grenze legt an, verschiebt oder meldet.
func TestProcessFileTooManyBlocks(t *testing.T) {
	e := newEnv(t, func(o *Options, _ *Deps) { o.Notify = true })
	e.llm.note = noteWithBlocks(5000)
	p := e.write(t, "Sitzung.srt", testSRT, testM)
	_, err := e.p.ProcessFile(context.Background(), &watch.Entry{Path: p, Name: "Sitzung.srt", ModTime: testM, Kind: domain.KindTranscript, Content: []byte(testSRT)})
	if !errors.Is(err, notion.ErrZuVieleBloecke) || len(e.notion.pages) != 0 || !exists(p) || len(e.notifier.calls) != 0 {
		t.Fatalf("err=%v Seiten %d Datei bleibt %v Mitteilungen %v", err, len(e.notion.pages), exists(p), e.notifier.calls)
	}
}

// Fängt: To-do für Mark setzt den Prüfgrund nicht (genau einmal, auch bei create_tasks_for=none).
func TestReasonToDoFuerMich(t *testing.T) {
	cases := []struct {
		name     string
		tasksFor string
		note     func() *domain.MeetingNote
		want     int
	}{
		{"fuer_mich", "me", testNote, 1},
		{"ohne fuer_mich", "me", func() *domain.MeetingNote {
			n := testNote()
			n.NextActions = []domain.Action{{Aufgabe: "Fremdes To-do"}}
			return n
		}, 0},
		{"create_tasks_for none", "none", testNote, 1},
		{"LLM nennt denselben Text", "me", func() *domain.MeetingNote {
			n := testNote()
			n.PruefGruende = []string{reasonToDoFuerMich}
			return n
		}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			e := newEnv(t, func(o *Options, d *Deps) {
				o.Dry = true
				o.CreateTasksFor = c.tasksFor
				d.DryRun = func(_ string, _ *notion.Page, _ *domain.MeetingNote, reasons []string, _ int) { got = reasons }
			})
			e.llm.note = c.note
			e.write(t, "Sitzung.srt", testSRT, testM)
			e.p.Pass(context.Background(), e.clock)
			n := 0
			for _, r := range got {
				if r == reasonToDoFuerMich {
					n++
				}
			}
			if n != c.want {
				t.Fatalf("Grund %d-mal, erwartet %d: %q", n, c.want, got)
			}
		})
	}
}

// Fängt: Zähler "To-dos für dich" zählt alle angelegten To-dos statt nur fuer_mich=true,
// oder zählt ein fehlgeschlagenes To-do mit.
func TestToDoCountOnlyFuerMich(t *testing.T) {
	cases := []struct {
		name     string
		tasksFor string
		failMine bool
		want     string
		tasks    int
	}{
		{"all", "all", false, "(1 To-dos für dich,", 2},
		{"me", "me", false, "(1 To-dos für dich,", 1},
		{"none", "none", false, "(0 To-dos für dich,", 0},
		{"all, eigenes scheitert", "all", true, "(0 To-dos für dich,", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, func(o *Options, _ *Deps) { o.Notify = true; o.CreateTasksFor = c.tasksFor })
			if c.failMine {
				failed := false
				e.notion.onCreate = func(ds string) error {
					if ds == "tasks-ds" && !failed {
						failed = true
						return errors.New("kaputt")
					}
					return nil
				}
			}
			e.write(t, "Sitzung.srt", testSRT, testM)
			e.p.Pass(context.Background(), e.clock)
			if !strings.Contains(e.log.String(), c.want) || e.notion.count("tasks-ds") != c.tasks {
				t.Fatalf("Log:\n%s\nTo-dos %d, want %q / %d", e.log, e.notion.count("tasks-ds"), c.want, c.tasks)
			}
		})
	}
}
