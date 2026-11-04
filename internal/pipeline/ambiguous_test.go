package pipeline

// R-M3-17: keine zweite Notiz nach einem unklaren Fehler beim Anlegen.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"meetingsync/internal/domain"
	"meetingsync/internal/ledger"
	"meetingsync/internal/notion"
	"meetingsync/internal/watch"
)

// withSource: Notes mit Herkunftsspalte "Quelle" (Textspalte).
func withSource(o *Options, _ *Deps) {
	o.Notes = notion.NotesProps{Title: "Name", Source: "Quelle"}
	o.NotesTypes = notion.Schema{"Name": notion.KindTitle, "Quelle": notion.KindRichText}
}

// errUnklar: wie CreatePage bei abgeschnittener 2xx-Antwort.
var errUnklar = fmt.Errorf("%w: Antwort von Notion unvollständig", notion.ErrUnklar)

// testMarker ist die Herkunft von Sitzung.srt mit mtime testM (UTC).
const testMarker = "Sitzung.srt · 2026-09-30 12:00:00"

// ambiguousOnce: der erste Notes-Request legt an und scheitert mit err.
func ambiguousOnce(e *testEnv, err error) {
	e.notion.keepOnErr = true
	calls := 0
	e.notion.onCreate = func(ds string) error {
		if ds == "notes-ds" {
			if calls++; calls == 1 {
				return err
			}
		}
		return nil
	}
}

// advance stellt die Uhr um d vor und startet einen Durchlauf.
func (e *testEnv) advance(d time.Duration) {
	e.clock = e.clock.Add(d)
	e.p.Pass(context.Background(), e.clock)
}

// restart baut eine neue Pipeline wie nach einem Neustart: gleiche Ordner,
// gleiche Fakes, Protokoll neu von Platte geladen, kein Zustand im Speicher.
func (e *testEnv) restart(t *testing.T) {
	t.Helper()
	l, err := ledger.Load(filepath.Join(e.o.ArchiveDir, ".meetingsync-ledger.jsonl"), true)
	if err != nil {
		t.Fatal(err)
	}
	e.ledger = l
	e.p = New(e.o, Deps{
		Now: func() time.Time { return e.clock }, FS: watch.OS{}, LLM: e.llm, Notion: e.notion,
		Notifier: e.notifier, Ledger: l, Log: log.New(e.log, "", 0),
	})
}

// Fängt: Herkunft fehlt, falsches Format (Hash statt Name und mtime), falsche
// Spalte, Suche schon beim ersten Versuch.
func TestSourceMarkerWrittenOnCreate(t *testing.T) {
	e := newEnv(t, withSource)
	e.write(t, "Sitzung.srt", testSRT, testM)
	e.p.Pass(context.Background(), e.clock)
	if e.notion.count("notes-ds") != 1 || e.notion.finds != 0 {
		t.Fatalf("Seiten %+v, Suchen %d", e.notion.pages, e.notion.finds)
	}
	if got := richText(e.notion.pages[0].props["Quelle"]); got != testMarker {
		t.Fatalf("Quelle = %q, want %q", got, testMarker)
	}
	if b, _ := json.Marshal(e.notion.pages[0].props); strings.Contains(string(b), "sha256") || strings.Contains(string(b), sha256Hex([]byte(testSRT))) {
		t.Fatalf("Hash in Notion: %s", b)
	}
}

// Fängt: ErrUnklar geht auf dem Weg zu handleError verloren oder wird ohne Spalte genutzt.
func TestAmbiguousCreateReachesDecisionPoint(t *testing.T) {
	e := newEnv(t, nil)
	ambiguousOnce(e, errUnklar)
	p := e.write(t, "Sitzung.srt", testSRT, testM)
	e.p.Pass(context.Background(), e.clock)
	if st := e.p.States()[p]; st == nil || st.ambiguous == nil || st.ambiguous.res == nil || st.Attempts != 1 {
		t.Fatalf("state = %+v, want ambiguous mit LLM-Ergebnis nach ErrUnklar", st)
	}
	// ohne Herkunftsspalte: keine Suche, zweite Seite wie bisher (mit dem gespeicherten Ergebnis)
	e.advance(20 * time.Minute)
	if e.notion.finds != 0 || e.notion.count("notes-ds") != 2 || len(e.llm.got) != 1 {
		t.Fatalf("ohne Spalte: Suchen %d, Notizen %d, LLM %d; want 0, 2, 1", e.notion.finds, e.notion.count("notes-ds"), len(e.llm.got))
	}
	if len(e.p.States()) != 0 {
		t.Fatalf("Zustand nach Erfolg: %v", e.p.States())
	}
}

// Fängt: zweite Notiz nach unklarem Fehler, To-dos fehlen, Datei nicht fertig/archiviert,
// Rest der Blöcke nicht angehängt.
func TestAmbiguousCreateReusesPage(t *testing.T) {
	e := newEnv(t, withSource)
	ambiguousOnce(e, errUnklar)
	p := e.write(t, "Sitzung.srt", testSRT, testM)
	e.p.Pass(context.Background(), e.clock)
	if e.notion.count("notes-ds") != 1 || e.notion.count("tasks-ds") != 0 || !exists(p) {
		t.Fatalf("nach 1. Versuch: %+v", e.notion.pages)
	}
	e.advance(20 * time.Minute)
	if e.notion.count("notes-ds") != 1 || e.notion.count("tasks-ds") != 1 || e.notion.finds != 1 {
		t.Fatalf("Notizen %d, To-dos %d, Suchen %d; want 1, 1, 1", e.notion.count("notes-ds"), e.notion.count("tasks-ds"), e.notion.finds)
	}
	if len(e.notion.appends) != 1 || !strings.HasPrefix(e.notion.appends[0], "page-1:") {
		t.Fatalf("AppendRest = %v", e.notion.appends)
	}
	lines := e.ledgerLines(t)
	if ln := lines[len(lines)-1]; ln["status"] != nil || ln["url"] != "https://notion.so/page-1" {
		t.Fatalf("Ledger = %v", lines)
	}
	if exists(p) || !exists(e.archived("Sitzung.srt")) || len(e.p.States()) != 0 {
		t.Fatal("Datei nicht archiviert oder Zustand bleibt")
	}
	if !strings.Contains(e.log.String(), "↺ Notiz existiert schon in Notion (Quelle) – wird verwendet") {
		t.Fatalf("Log:\n%s", e.log)
	}
}

// Fängt: Suche nach eindeutigem Fehler, Suche findet nichts → keine Seite.
func TestLookupOnlyAfterAmbiguous(t *testing.T) {
	t.Run("eindeutiger Fehler", func(t *testing.T) {
		e := newEnv(t, withSource)
		ambiguousOnce(e, errors.New("Notion HTTP 400 validation_error: x"))
		e.notion.keepOnErr = false
		e.write(t, "Sitzung.srt", testSRT, testM)
		e.p.Pass(context.Background(), e.clock)
		e.advance(20 * time.Minute)
		if e.notion.finds != 0 || e.notion.count("notes-ds") != 1 {
			t.Fatalf("Suchen %d, Notizen %d", e.notion.finds, e.notion.count("notes-ds"))
		}
	})
	t.Run("nichts gefunden", func(t *testing.T) {
		e := newEnv(t, withSource)
		ambiguousOnce(e, errUnklar)
		e.notion.keepOnErr = false // Notion hat nicht angelegt
		e.write(t, "Sitzung.srt", testSRT, testM)
		e.p.Pass(context.Background(), e.clock)
		e.advance(20 * time.Minute)
		if e.notion.finds != 1 || e.notion.count("notes-ds") != 1 || richText(e.notion.pages[0].props["Quelle"]) != testMarker {
			t.Fatalf("Suchen %d, Seiten %+v", e.notion.finds, e.notion.pages)
		}
	})
}

// Fängt: Suche findet kurz nach dem unklaren Fehler nichts (Verzögerung in
// Notion) und legt sofort an; nach Ablauf der 5 Minuten nie angelegt.
func TestLookupLagWaitsBeforeCreate(t *testing.T) {
	e := newEnv(t, withSource)
	ambiguousOnce(e, errUnklar)
	e.notion.keepOnErr = false
	p := e.write(t, "Sitzung.srt", testSRT, testM)
	e.p.Pass(context.Background(), e.clock) // unklar, neuer Versuch in 2 min
	e.advance(2 * time.Minute)
	st := e.p.States()[p]
	if e.notion.finds != 1 || e.notion.count("notes-ds") != 0 || st == nil || st.Attempts != 2 || st.Transient != 1 {
		t.Fatalf("nach 2 min: Suchen %d, Notizen %d, state %+v", e.notion.finds, e.notion.count("notes-ds"), st)
	}
	if !strings.Contains(e.log.String(), "   ⏸  Notiz in Notion noch nicht auffindbar – neuer Versuch in 1m0s (Versuch 2/10)") {
		t.Fatalf("Log:\n%s", e.log)
	}
	if lines := e.ledgerLines(t); lines[len(lines)-1]["status"] != ledger.StatusVerworfen {
		t.Fatalf("Ledger = %v, want verworfen", lines)
	}
	e.advance(2*time.Minute + 59*time.Second) // 4:59 nach dem Fehler: noch warten
	if e.notion.count("notes-ds") != 0 || e.notion.finds != 2 {
		t.Fatalf("nach 4:59: Suchen %d, Notizen %d", e.notion.finds, e.notion.count("notes-ds"))
	}
	e.advance(2 * time.Minute) // NextTry (+2 min) erreicht, > 5 min
	if e.notion.count("notes-ds") != 1 || e.notion.finds != 3 || exists(p) || len(e.llm.got) != 1 {
		t.Fatalf("nach 6:59: Suchen %d, Notizen %d, LLM %d, Datei bleibt %v", e.notion.finds, e.notion.count("notes-ds"), len(e.llm.got), exists(p))
	}
}

// Fängt: nach Neustart (kein Zustand) wird nicht gesucht → zweite Notiz; die
// 5 Minuten gelten nicht ab der Zeit von "verworfen" im Protokoll.
func TestLookupAfterRestart(t *testing.T) {
	t.Run("Seite existiert", func(t *testing.T) {
		e := newEnv(t, withSource)
		ambiguousOnce(e, errUnklar)
		p := e.write(t, "Sitzung.srt", testSRT, testM)
		e.p.Pass(context.Background(), e.clock)
		e.restart(t)
		e.advance(time.Minute)
		if e.notion.finds != 1 || e.notion.count("notes-ds") != 1 || e.notion.count("tasks-ds") != 1 || exists(p) {
			t.Fatalf("Suchen %d, Notizen %d, To-dos %d, Datei bleibt %v\n%s", e.notion.finds, e.notion.count("notes-ds"), e.notion.count("tasks-ds"), exists(p), e.log)
		}
		if lines := e.ledgerLines(t); lines[len(lines)-1]["url"] != "https://notion.so/page-1" {
			t.Fatalf("Ledger = %v", lines)
		}
	})
	t.Run("umbenannt, Seite existiert", func(t *testing.T) {
		e := newEnv(t, withSource)
		ambiguousOnce(e, errUnklar)
		p := e.write(t, "Sitzung.srt", testSRT, testM)
		e.p.Pass(context.Background(), e.clock)
		e.restart(t)
		if err := os.Rename(p, filepath.Join(e.o.WatchDir, "Sitzung2.srt")); err != nil {
			t.Fatal(err)
		}
		e.advance(time.Minute) // innerhalb der 5 min: Suche nach alter Herkunft findet die Seite
		if e.notion.finds != 1 || e.notion.count("notes-ds") != 1 || e.notion.count("tasks-ds") != 1 || !exists(e.archived("Sitzung2.srt")) {
			t.Fatalf("Suchen %d, Notizen %d, To-dos %d\n%s", e.notion.finds, e.notion.count("notes-ds"), e.notion.count("tasks-ds"), e.log)
		}
		if lines := e.ledgerLines(t); lines[len(lines)-1]["url"] != "https://notion.so/page-1" {
			t.Fatalf("Ledger = %v", lines)
		}
		e.advance(10 * time.Minute)
		if e.notion.count("notes-ds") != 1 {
			t.Fatalf("Notizen %d, want 1", e.notion.count("notes-ds"))
		}
	})
	t.Run("umbenannt, Seite fehlt", func(t *testing.T) {
		e := newEnv(t, withSource)
		ambiguousOnce(e, errUnklar)
		e.notion.keepOnErr = false
		p := e.write(t, "Sitzung.srt", testSRT, testM)
		e.p.Pass(context.Background(), e.clock)
		e.restart(t)
		if err := os.Rename(p, filepath.Join(e.o.WatchDir, "Sitzung2.srt")); err != nil {
			t.Fatal(err)
		}
		e.advance(time.Minute)
		// je Versuch zwei Suchen: alte Herkunft (aus "verworfen") und aktuelle
		if e.notion.finds != 2 || e.notion.count("notes-ds") != 0 {
			t.Fatalf("nach 1 min: Suchen %d, Notizen %d", e.notion.finds, e.notion.count("notes-ds"))
		}
		e.advance(10 * time.Minute)
		// LLM-Ergebnis des Versuchs nach dem Neustart bleibt trotz "noch nicht auffindbar"
		if e.notion.finds != 4 || e.notion.count("notes-ds") != 1 || len(e.llm.got) != 2 {
			t.Fatalf("nach 11 min: Suchen %d, Notizen %d, LLM %d", e.notion.finds, e.notion.count("notes-ds"), len(e.llm.got))
		}
		if got := richText(e.notion.pages[0].props["Quelle"]); got != "Sitzung2.srt · 2026-09-30 12:00:00" {
			t.Fatalf("Quelle = %q", got)
		}
	})
}

// Fängt: neuer LLM-Lauf nach unklarem Fehler (Rest der Blöcke aus einer
// anderen Notiz als die ersten 100), Projektliste neu gefragt.
func TestAmbiguousRetryReusesLLMResult(t *testing.T) {
	e := newEnv(t, withSource)
	runs := 0
	e.llm.note = func() *domain.MeetingNote {
		runs++
		n := testNote()
		for i := range 150 {
			n.Entscheidungen = append(n.Entscheidungen, fmt.Sprintf("Lauf %d, Entscheidung %d", runs, i))
		}
		return n
	}
	ambiguousOnce(e, errUnklar)
	e.write(t, "Sitzung.srt", testSRT, testM)
	e.p.Pass(context.Background(), e.clock)
	e.advance(20 * time.Minute)
	if len(e.llm.got) != 1 || e.notion.projCalls != 1 || e.notion.count("notes-ds") != 1 || e.notion.count("tasks-ds") != 1 {
		t.Fatalf("LLM %d, Projektlisten %d, Notizen %d, To-dos %d; want 1, 1, 1, 1", len(e.llm.got), e.notion.projCalls, e.notion.count("notes-ds"), e.notion.count("tasks-ds"))
	}
	first, _ := json.Marshal(e.notion.pages[0].blocks)
	if len(e.notion.appended) != 1 || len(e.notion.pages[0].blocks) <= 100 {
		t.Fatalf("AppendRest %d, Blöcke %d", len(e.notion.appended), len(e.notion.pages[0].blocks))
	}
	if rest, _ := json.Marshal(e.notion.appended[0]); string(rest) != string(first) {
		t.Fatalf("angehängte Blöcke aus anderer Notiz:\n%s\n%s", rest, first)
	}
}

// Fängt: geänderte Datei am selben Pfad nutzt Suche oder LLM-Ergebnis der alten.
func TestAmbiguousClearedOnHashChange(t *testing.T) {
	e := newEnv(t, withSource)
	ambiguousOnce(e, errUnklar)
	e.notion.keepOnErr = false
	p := e.write(t, "Sitzung.srt", testSRT, testM)
	e.p.Pass(context.Background(), e.clock)
	e.write(t, "Sitzung.srt", testSRT+"\n3\n00:00:10,000 --> 00:00:12,000\nSpeaker 1: Neu.\n", testM)
	e.advance(20 * time.Minute)
	if e.notion.finds != 0 || len(e.llm.got) != 2 || e.notion.count("notes-ds") != 1 || exists(p) {
		t.Fatalf("Suchen %d, LLM %d, Notizen %d, Datei bleibt %v", e.notion.finds, len(e.llm.got), e.notion.count("notes-ds"), exists(p))
	}
}

// unklarOhneText ist unklar (wie unklarError in notion), ohne "Seite evtl. angelegt" im Text.
type unklarOhneText struct{ error }

func (unklarOhneText) Is(target error) bool { return target == notion.ErrUnklar }

// Fängt: .fehler.txt ohne Hinweis nach unklarem letzten Fehler, Hinweis
// doppelt, im Log oder nach eindeutigem Fehler.
func TestGiveUpAmbiguousHint(t *testing.T) {
	const hint = "Seite evtl. angelegt"
	for _, c := range []struct {
		name string
		err  error
		want int // Anzahl hint in .fehler.txt
		full bool
	}{
		{"unklar mit Text", errUnklar, 1, false},
		{"unklar ohne Text", unklarOhneText{errors.New("Notion HTTP 502 bad_gateway: x")}, 1, true},
		{"eindeutig", errors.New("Notion HTTP 400 validation_error: x"), 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, func(o *Options, d *Deps) { withSource(o, d); o.MaxAttempts = 1 })
			ambiguousOnce(e, c.err)
			e.write(t, "Sitzung.srt", testSRT, testM)
			e.p.Pass(context.Background(), e.clock)
			txt := mustRead(t, filepath.Join(e.o.FailedDir, "Sitzung.srt.fehler.txt"))
			if strings.Count(txt, hint) != c.want || strings.Contains(txt, hint+" – bitte in Notion prüfen") != c.full || !strings.Contains(txt, c.err.Error()) {
				t.Fatalf(".fehler.txt:\n%s", txt)
			}
			if strings.Contains(e.log.String(), "bitte in Notion prüfen") {
				t.Fatalf("Hinweis im Log:\n%s", e.log)
			}
		})
	}
}

// Fängt: letzter Versuch "noch nicht auffindbar" legt an, Datei bleibt liegen
// oder .fehler.txt ohne Hinweis.
func TestGiveUpDuringLookupLag(t *testing.T) {
	e := newEnv(t, func(o *Options, d *Deps) { withSource(o, d); o.MaxAttempts = 2 })
	ambiguousOnce(e, errUnklar)
	e.notion.keepOnErr = false
	p := e.write(t, "Sitzung.srt", testSRT, testM)
	e.p.Pass(context.Background(), e.clock) // Versuch 1: unklar
	e.advance(2 * time.Minute)              // Versuch 2: nichts gefunden, < 5 min → gebe auf
	if e.notion.finds != 1 || e.notion.count("notes-ds") != 0 || exists(p) || len(e.p.States()) != 0 {
		t.Fatalf("Suchen %d, Notizen %d, Datei bleibt %v, Zustand %v", e.notion.finds, e.notion.count("notes-ds"), exists(p), e.p.States())
	}
	txt := mustRead(t, filepath.Join(e.o.FailedDir, "Sitzung.srt.fehler.txt"))
	if !strings.Contains(txt, errNochNichtAuffindbar.Error()+" – Seite evtl. angelegt – bitte in Notion prüfen") {
		t.Fatalf(".fehler.txt:\n%s", txt)
	}
	e.advance(20 * time.Minute)
	if e.notion.count("notes-ds") != 0 {
		t.Fatalf("Notizen %d nach Aufgeben, want 0", e.notion.count("notes-ds"))
	}
}

// Fängt: gescheiterte Suche legt trotzdem an oder zählt nicht als Versuch.
func TestLookupErrorCountsAndDoesNotCreate(t *testing.T) {
	e := newEnv(t, withSource)
	ambiguousOnce(e, errUnklar)
	p := e.write(t, "Sitzung.srt", testSRT, testM)
	e.p.Pass(context.Background(), e.clock)
	e.notion.findErr = domain.Transient(errors.New("Notion nicht erreichbar: weg"))
	e.advance(20 * time.Minute)
	st := e.p.States()[p]
	if e.notion.count("notes-ds") != 1 || st == nil || st.Attempts != 2 || st.ambiguous == nil {
		t.Fatalf("Notizen %d, state %+v", e.notion.count("notes-ds"), st)
	}
	if lines := e.ledgerLines(t); lines[len(lines)-1]["status"] != ledger.StatusVerworfen {
		t.Fatalf("Ledger = %v, want verworfen", lines)
	}
	e.notion.findErr = nil
	e.advance(40 * time.Minute)
	if e.notion.count("notes-ds") != 1 || e.notion.count("tasks-ds") != 1 || exists(p) {
		t.Fatalf("3. Versuch: %+v", e.notion.pages)
	}
}

// Fängt: mehrere Treffer → neue Seite oder Löschen; Fehler beim Anhängen → neuer Versuch.
func TestLookupManyMatchesAndAppendError(t *testing.T) {
	e := newEnv(t, withSource)
	ambiguousOnce(e, errUnklar)
	// zweite, ältere Seite mit derselben Herkunft (z. B. von Hand kopiert)
	e.notion.pages = append(e.notion.pages, fakePage{ds: "notes-ds", props: notion.NoteProperties(e.o.NotesTypes, e.o.Notes, "x", nil, false, testMarker)})
	e.notion.appendErr = fmt.Errorf("%w: 400", notion.ErrUnvollstaendig)
	p := e.write(t, "Sitzung.srt", testSRT, testM)
	e.p.Pass(context.Background(), e.clock)
	e.advance(20 * time.Minute)
	if e.notion.count("notes-ds") != 2 || exists(p) {
		t.Fatalf("Notizen %d (want 2, keine neue), Datei bleibt %v", e.notion.count("notes-ds"), exists(p))
	}
	if len(e.notion.appends) != 1 || !strings.HasPrefix(e.notion.appends[0], "page-1:") {
		t.Fatalf("AppendRest = %v, want älteste page-1", e.notion.appends)
	}
	lines := e.ledgerLines(t)
	if ln := lines[len(lines)-1]; ln["status"] != nil || ln["bloecke_fehler"] != 1.0 || ln["url"] != "https://notion.so/page-1" {
		t.Fatalf("fertig-Zeile = %v", ln)
	}
	if !strings.Contains(e.log.String(), "2 Notizen mit dieser Herkunft in Notion (Quelle) – nehme die älteste") {
		t.Fatalf("Log:\n%s", e.log)
	}
}

// --- mit echtem notion.Client gegen einen HTTP-Fake ---------------------------

// fakeNotionHTTP legt Seiten an (auch wenn die Antwort scheitert) und
// beantwortet Suchen nach der Textspalte.
type fakeNotionHTTP struct {
	mu    sync.Mutex
	mode  string // Antwort auf das erste Anlegen einer Notiz
	notes []string
	tasks int
	sent  int // Requests auf POST /v1/pages für Notizen
}

func (f *fakeNotionHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/pages":
		var in struct {
			Parent     map[string]string `json:"parent"`
			Properties map[string]any    `json:"properties"`
		}
		_ = json.Unmarshal(b, &in)
		if in.Parent["data_source_id"] != "notes-ds" {
			f.tasks++
			_, _ = io.WriteString(w, `{"id":"task","url":"u"}`)
			return
		}
		f.sent++
		f.notes = append(f.notes, richText(in.Properties["Quelle"]))
		id := fmt.Sprintf("note-%d", len(f.notes))
		ok := `{"id":"` + id + `","url":"https://notion.so/` + id + `"}`
		if f.sent > 1 {
			_, _ = io.WriteString(w, ok)
			return
		}
		switch f.mode {
		case "5xx":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"code":"bad_gateway","message":"x"}`)
		case "unlesbar":
			w.Header().Set("Content-Length", "1000")
			_, _ = io.WriteString(w, `{"id":"`)
		case "kein JSON":
			_, _ = io.WriteString(w, `<html>ok</html>`)
		default: // Netzwerkfehler und Zeitüberschreitung: Transport verwirft die Antwort
			_, _ = io.WriteString(w, ok)
		}
	case r.Method == http.MethodPost && r.URL.Path == "/v1/data_sources/notes-ds/query":
		var q struct {
			Filter struct {
				Property string            `json:"property"`
				RichText map[string]string `json:"rich_text"`
			} `json:"filter"`
		}
		_ = json.Unmarshal(b, &q)
		var res []any
		for i, n := range f.notes {
			if q.Filter.Property == "Quelle" && q.Filter.RichText["equals"] != "" && n == q.Filter.RichText["equals"] {
				id := fmt.Sprintf("note-%d", i+1)
				res = append(res, map[string]any{"id": id, "url": "https://notion.so/" + id})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": res})
	default:
		http.Error(w, `{"code":"not_found"}`, http.StatusNotFound)
	}
}

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type timeoutErr struct{}

func (timeoutErr) Error() string { return "Client.Timeout exceeded while awaiting headers" }
func (timeoutErr) Timeout() bool { return true }

// Fängt: einer der unklaren Fehler (5xx, Netzwerk, Zeitüberschreitung,
// unlesbare oder ungültige 2xx-Antwort) erzeugt beim neuen Versuch eine zweite Notiz.
func TestAmbiguousFailuresGiveOnePage(t *testing.T) {
	for _, mode := range []string{"5xx", "Netzwerk", "Zeitüberschreitung", "unlesbar", "kein JSON"} {
		t.Run(mode, func(t *testing.T) {
			f := &fakeNotionHTTP{mode: mode}
			srv := httptest.NewServer(f)
			t.Cleanup(srv.Close)
			first := true
			tr := rtFunc(func(r *http.Request) (*http.Response, error) {
				resp, err := http.DefaultTransport.RoundTrip(r)
				if err == nil && first && r.URL.Path == "/v1/pages" && (mode == "Netzwerk" || mode == "Zeitüberschreitung") {
					f.mu.Lock()
					isNote := f.sent == 1
					f.mu.Unlock()
					if isNote {
						first = false
						_ = resp.Body.Close()
						if mode == "Netzwerk" {
							return nil, errors.New("connection reset by peer")
						}
						return nil, timeoutErr{}
					}
				}
				return resp, err
			})
			client := notion.NewClient(srv.URL, "2025-09-03", "tok", tr)
			e := newEnv(t, func(o *Options, d *Deps) {
				withSource(o, d)
				o.ProjectsDS = ""
				d.Notion = client
			})
			p := e.write(t, "Sitzung.srt", testSRT, testM)
			e.p.Pass(context.Background(), e.clock)
			if st := e.p.States()[p]; st == nil || st.ambiguous == nil || len(f.notes) != 1 {
				t.Fatalf("nach 1. Versuch: state %+v, Notizen %d\n%s", st, len(f.notes), e.log)
			}
			e.advance(20 * time.Minute)
			if len(f.notes) != 1 || f.tasks != 1 || exists(p) || !exists(e.archived("Sitzung.srt")) {
				t.Fatalf("Notizen %d, To-dos %d, Datei bleibt %v\n%s", len(f.notes), f.tasks, exists(p), e.log)
			}
			if lines := e.ledgerLines(t); lines[len(lines)-1]["url"] != "https://notion.so/note-1" {
				t.Fatalf("Ledger = %v", lines)
			}
		})
	}
}
