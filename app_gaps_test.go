package main

// Lücken nach dem Anlegen der Notiz (R-M1-03, OD-04): fehlende To-dos und
// Fehler beim Anhängen der restlichen Blöcke werden gezählt, geloggt und im
// fertig-Eintrag festgehalten, aber nicht nachgeholt.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"meetingsync/internal/ledger"
)

func isTaskPost(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == "/v1/pages" && !isNotePost(r)
}

func badRequest(r *http.Request) *http.Response {
	return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader(`{"code":"validation_error","message":"x"}`)), Header: http.Header{}, Request: r}
}

// lastLedgerLine liest die letzte Zeile der Ledger-Datei roh.
func lastLedgerLine(t *testing.T, e *crashEnv) map[string]any {
	t.Helper()
	b := mustRead(t, ledgerPath(e.app.cfg))
	lines := strings.Split(strings.TrimSpace(b), "\n")
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// Fängt (Mutation: ctx.Err()-Prüfung in createTasks entfernt): nach einem
// Abbruch gehen weitere To-do-Anfragen raus. Fängt außerdem (Mutation: process
// gibt bei Abbruch nach CreatePage einen Fehler zurück bzw. handle schreibt kein
// fertig): begonnen bleibt stehen und der Neustart schickt die angelegte Notiz
// nach _fehler.
func TestCreateTasksCancelStopsRequests(t *testing.T) {
	e := newCrashEnv(t)
	e.app.cfg.Notion.CreateTasksFor = "all" // 2 To-dos
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	taskPosts := 0
	e.rt.before = func(r *http.Request) (*http.Response, error) {
		if isTaskPost(r) {
			mu.Lock()
			taskPosts++
			mu.Unlock()
		}
		return nil, nil
	}
	e.rt.after = func(r *http.Request) {
		if isTaskPost(r) {
			cancel() // nach dem ersten To-do: Ctrl+C / launchd stop
		}
	}
	e.app.Pass(ctx, time.Now())

	if taskPosts != 1 {
		t.Fatalf("To-do-Anfragen = %d, want 1 (nach dem Abbruch keine weitere)", taskPosts)
	}
	if n, k := e.pages(); n != 1 || k != 1 {
		t.Fatalf("%d Notizen, %d Tasks; want 1, 1", n, k)
	}
	if got := e.lastStatus(t, e.hash()); got != ledger.StatusFertig {
		t.Fatalf("Status = %q, want fertig (Seite existiert)", got)
	}
	ln := lastLedgerLine(t, e)
	if ln["url"] != "https://notion.so/page-1" || ln["todos_fehlen"] != 1.0 || ln["bloecke_fehler"] != nil {
		t.Fatalf("fertig-Zeile = %v, want url page-1, todos_fehlen 1, ohne bloecke_fehler", ln)
	}
	if left := remaining(t, e.dir); len(left) != 0 {
		t.Fatalf("nicht archiviert: %v", left)
	}
	if !strings.Contains(e.log.String(), "Meeting 2026-10-28 14-00.txt: Notiz unvollständig – 1 To-do(s) nicht angelegt, 0 Fehler beim Anhängen von Blöcken") {
		t.Fatalf("Log ohne Lücken-Hinweis:\n%s", e.log.String())
	}

	e.restart(t)
	e.pass(time.Now())
	if n, _ := e.pages(); n != 1 {
		t.Fatalf("Notizen nach Neustart = %d, want 1", n)
	}
	if data, _ := e.inFailed(t); len(data) != 0 {
		t.Fatalf("unerwartet in _fehler: %v", data)
	}
}

// Fängt (Mutation: missing++ im Fehlerzweig von createTasks entfernt): ein
// abgelehntes To-do wird nicht gezählt. Das zweite To-do läuft trotzdem.
func TestCreateTasksErrorCounted(t *testing.T) {
	e := newCrashEnv(t)
	e.app.cfg.Notion.CreateTasksFor = "all"
	first := true
	e.rt.before = func(r *http.Request) (*http.Response, error) {
		if isTaskPost(r) && first {
			first = false
			return badRequest(r), nil
		}
		return nil, nil
	}
	e.pass(time.Now())
	if n, k := e.pages(); n != 1 || k != 1 {
		t.Fatalf("%d Notizen, %d Tasks; want 1, 1", n, k)
	}
	if ln := lastLedgerLine(t, e); ln["hash"] != e.hash() || ln["todos_fehlen"] != 1.0 {
		t.Fatalf("fertig-Zeile = %v, want todos_fehlen 1", ln)
	}
	if !strings.Contains(e.log.String(), "1 To-do(s) nicht angelegt, 0 Fehler") {
		t.Fatalf("Log:\n%s", e.log.String())
	}
	if left := remaining(t, e.dir); len(left) != 0 {
		t.Fatalf("nicht archiviert: %v", left)
	}
}

// Fängt (Mutation: CreatePage verschluckt den Fehler von AppendChildren wie
// bisher / process wertet errUnvollstaendig als Fehler): der Block-Fehler fehlt
// im fertig-Eintrag bzw. die Datei läuft erneut und legt eine zweite Seite an.
func TestAppendBlocksErrorCounted(t *testing.T) {
	e := newCrashEnv(t)
	e.f.themesInReply = 40 // > 100 Blöcke → Rest per PATCH
	patches := 0
	e.rt.before = func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPatch {
			patches++
			return badRequest(r), nil
		}
		return nil, nil
	}
	now := time.Now()
	e.pass(now)
	e.pass(now.Add(3 * time.Minute))
	if patches != 1 {
		t.Fatalf("PATCH = %d, want 1 (kein Retry)", patches)
	}
	if n, k := e.pages(); n != 1 || k != 1 {
		t.Fatalf("%d Notizen, %d Tasks; want 1, 1 (To-dos laufen trotzdem)", n, k)
	}
	ln := lastLedgerLine(t, e)
	if ln["hash"] != e.hash() || ln["bloecke_fehler"] != 1.0 || ln["todos_fehlen"] != nil {
		t.Fatalf("fertig-Zeile = %v, want bloecke_fehler 1, ohne todos_fehlen", ln)
	}
	out := e.log.String()
	if !strings.Contains(out, "⚠️  Seite angelegt, aber nicht der ganze Inhalt:") || !strings.Contains(out, "0 To-do(s) nicht angelegt, 1 Fehler beim Anhängen von Blöcken") {
		t.Fatalf("Log:\n%s", out)
	}
	if left := remaining(t, e.dir); len(left) != 0 {
		t.Fatalf("nicht archiviert: %v", left)
	}
}

// Fängt (Mutation: process wertet errUnvollstaendig nur ohne Abbruch als
// Lücke, z. B. "&& ctx.Err() == nil"): ein Abbruch während des Anhängens lässt
// begonnen stehen, und der Neustart schickt die angelegte Notiz nach _fehler.
func TestAppendBlocksCancelCounted(t *testing.T) {
	e := newCrashEnv(t)
	e.f.themesInReply = 40 // > 100 Blöcke → Rest per PATCH
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	taskPosts := 0
	e.rt.before = func(r *http.Request) (*http.Response, error) {
		if isTaskPost(r) {
			mu.Lock()
			taskPosts++
			mu.Unlock()
		}
		if r.Method == http.MethodPatch {
			// Ctrl+C / launchd stop während des Anhängens: der PATCH scheitert
			// mit context canceled (ein Abbruch erst nach erfolgreichem PATCH
			// ließe das Anhängen gelingen).
			cancel()
		}
		return nil, nil
	}
	e.app.Pass(ctx, time.Now())

	if taskPosts != 0 {
		t.Fatalf("To-do-Anfragen = %d, want 0 (nach dem Abbruch keine)", taskPosts)
	}
	if got := e.lastStatus(t, e.hash()); got != ledger.StatusFertig {
		t.Fatalf("Status = %q, want fertig (Seite existiert)\nLog:\n%s", got, e.log.String())
	}
	ln := lastLedgerLine(t, e)
	if ln["url"] != "https://notion.so/page-1" || ln["bloecke_fehler"] != 1.0 || ln["todos_fehlen"] != 1.0 {
		t.Fatalf("fertig-Zeile = %v, want url page-1, bloecke_fehler 1, todos_fehlen 1", ln)
	}

	e.restart(t)
	e.pass(time.Now())
	if n, _ := e.pages(); n != 1 {
		t.Fatalf("Notizen nach Neustart = %d, want 1", n)
	}
	if data, _ := e.inFailed(t); len(data) != 0 {
		t.Fatalf("unerwartet in _fehler: %v", data)
	}
}

// Fängt (Mutation: CreatePage liefert bei Anhänge-Fehler leere ID/URL oder nil):
// der Aufrufer kann die angelegte Seite nicht von einem Fehlschlag unterscheiden.
func TestCreatePageReturnsAppendError(t *testing.T) {
	var gotPatch int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			gotPatch++
			http.Error(w, `{"code":"validation_error","message":"x"}`, http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"id": "p1", "url": "https://notion.so/p1"})
	}))
	defer srv.Close()
	n := NewNotion(NotionConfig{BaseURL: srv.URL, APIVersion: "2025-09-03"}, "tok")
	blocks := make([]any, 150)
	for i := range blocks {
		blocks[i] = map[string]any{"type": "divider", "divider": map[string]any{}}
	}
	id, url, err := n.CreatePage(context.Background(), "ds", map[string]any{}, blocks)
	if id != "p1" || url != "https://notion.so/p1" || !errors.Is(err, errUnvollstaendig) || gotPatch != 1 {
		t.Fatalf("CreatePage = %q, %q, %v (PATCH %d); want p1, url, errUnvollstaendig, 1", id, url, err, gotPatch)
	}
	if id, _, err := n.CreatePage(context.Background(), "ds", map[string]any{}, blocks[:100]); id != "p1" || err != nil {
		t.Fatalf("ohne Rest: %q, %v; want p1, nil", id, err)
	}
}
