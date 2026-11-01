package main

import (
	"context"
	"encoding/json"
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
	"unicode/utf8"
)

// fakeServer simuliert LM Studio und die Notion-API in einem Server.
type fakeServer struct {
	t  *testing.T
	mu sync.Mutex

	pages         []map[string]any // POST /v1/pages
	appends       map[string]int   // PATCH children: blockID → Anzahl Blöcke
	llmCalls      int
	sawSchema     bool
	rejectSchema  bool // simuliert ein LM Studio ohne Structured Output
	themesInReply int
}

func (f *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	var in map[string]any
	_ = json.Unmarshal(body, &in)

	if strings.HasPrefix(r.URL.Path, "/v1/") && !strings.HasPrefix(r.URL.Path, "/v1/chat") && r.URL.Path != "/v1/models" {
		if r.Header.Get("Authorization") != "Bearer secret-test" || r.Header.Get("Notion-Version") != "2025-09-03" {
			http.Error(w, `{"code":"unauthorized","message":"bad headers"}`, http.StatusUnauthorized)
			return
		}
	}

	switch {
	case r.URL.Path == "/v1/chat/completions":
		f.llmCalls++
		_, hasSchema := in["response_format"]
		if hasSchema && f.rejectSchema {
			http.Error(w, `{"error":"response_format json_schema is not supported"}`, 400)
			return
		}
		if hasSchema {
			f.sawSchema = true
			s, _ := json.Marshal(in["response_format"])
			if !strings.Contains(string(s), "AI-First") {
				f.t.Errorf("Projektliste fehlt im Schema: %s", s)
			}
		}
		msgs, _ := json.Marshal(in["messages"])
		if !strings.Contains(string(msgs), "Meetingdatum") {
			f.t.Errorf("Meetingdatum fehlt im Prompt")
		}
		note := map[string]any{
			"titel": "Sirius Sync", "typ": "Team",
			"teilnehmer":     []string{"Mark", "Sascha"},
			"kontext_ziel":   "Status der Pipeline klären.",
			"themen":         []any{map[string]any{"titel": "Pipeline", "punkte": []string{"Build dauert 12 min", "Cache fehlt"}}},
			"entscheidungen": []string{"Cache wird eingeführt"},
			"next_actions": []any{
				map[string]any{"aufgabe": "Budget für Runner klären", "owner": "Mark", "deadline": "2026-10-30", "fuer_mich": true},
				map[string]any{"aufgabe": "Cache bauen", "owner": "Sascha", "deadline": "", "fuer_mich": false},
			},
			"offene_fragen":     []string{"Wer reviewt?"},
			"naechstes_meeting": map[string]any{"datum": "Freitag", "themen": []string{"Review"}},
			"projekt":           "AI-First",
			"pruef_gruende":     []string{},
			"pruefen":           false,
		}
		if f.themesInReply > 0 {
			var th []any
			for i := 0; i < f.themesInReply; i++ {
				th = append(th, map[string]any{"titel": fmt.Sprintf("Thema %d", i), "punkte": []string{"a", "b", "c"}})
			}
			note["themen"] = th
		}
		content, _ := json.Marshal(note)
		writeJSON(w, map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"role": "assistant", "content": string(content)}, "finish_reason": "stop",
		}}})

	case r.URL.Path == "/v1/models":
		writeJSON(w, map[string]any{"data": []any{map[string]any{"id": "gemma-test"}}})

	case r.Method == "GET" && r.URL.Path == "/v1/data_sources/notes-ds":
		writeJSON(w, props(map[string]string{"Name": "title", "Status": "status", "Projects": "relation", "Prüfen": "checkbox", "Tasks": "relation"}))
	case r.Method == "GET" && r.URL.Path == "/v1/data_sources/tasks-ds":
		writeJSON(w, props(map[string]string{"Name": "title", "Description": "rich_text", "Due Date": "date", "Notes": "relation", "Projects": "relation", "Completed": "checkbox"}))
	case r.Method == "GET" && r.URL.Path == "/v1/data_sources/projects-ds":
		writeJSON(w, props(map[string]string{"Name": "title", "status": "status", "Archive": "checkbox"}))
	case r.Method == "POST" && r.URL.Path == "/v1/data_sources/projects-ds/query":
		filter, _ := json.Marshal(in["filter"])
		if !strings.Contains(string(filter), "does_not_equal") || !strings.Contains(string(filter), "Archive") {
			f.t.Errorf("Projektfilter unvollständig: %s", filter)
		}
		writeJSON(w, map[string]any{"has_more": false, "results": []any{
			map[string]any{"id": "proj-1", "properties": map[string]any{"Name": map[string]any{"title": []any{map[string]any{"plain_text": "AI-First"}}}}},
		}})

	case r.Method == "POST" && r.URL.Path == "/v1/pages":
		checkPayload(f.t, in)
		f.pages = append(f.pages, in)
		id := fmt.Sprintf("page-%d", len(f.pages))
		writeJSON(w, map[string]any{"id": id, "url": "https://notion.so/" + id})

	case r.Method == "PATCH" && strings.HasSuffix(r.URL.Path, "/children"):
		checkPayload(f.t, in)
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/blocks/"), "/children")
		children, _ := in["children"].([]any)
		f.appends[id] += len(children)
		writeJSON(w, map[string]any{"results": []any{}})

	default:
		f.t.Errorf("unerwarteter Request %s %s", r.Method, r.URL.Path)
		http.Error(w, `{"code":"object_not_found","message":"nope"}`, 404)
	}
}

func props(m map[string]string) map[string]any {
	p := map[string]any{}
	for k, v := range m {
		p[k] = map[string]any{"type": v}
	}
	return map[string]any{"properties": p}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// checkPayload prüft die harten Notion-Limits: ≤100 Blöcke pro Ebene,
// ≤2000 Zeichen pro Text-Objekt.
func checkPayload(t *testing.T, v any) {
	var walk func(any)
	walk = func(x any) {
		switch n := x.(type) {
		case map[string]any:
			if c, ok := n["children"].([]any); ok && len(c) > 100 {
				t.Errorf("%d Blöcke in einem children-Array (max 100)", len(c))
			}
			if txt, ok := n["text"].(map[string]any); ok {
				if s, _ := txt["content"].(string); utf8.RuneCountInString(s) > 2000 {
					t.Errorf("Text-Objekt mit %d Zeichen", utf8.RuneCountInString(s))
				}
			}
			for _, v := range n {
				walk(v)
			}
		case []any:
			for _, v := range n {
				walk(v)
			}
		}
	}
	walk(v)
}

func setup(t *testing.T, f *fakeServer) (*App, string) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	t.Setenv("NOTION_TOKEN", "secret-test")

	dir := t.TempDir()
	cfg := defaultConfig()
	cfg.WatchDir = dir
	cfg.Notify = false
	cfg.IncludeTranscript = true
	cfg.LLM.BaseURL = srv.URL + "/v1"
	cfg.LLM.Model = "gemma-test"
	cfg.Notion.BaseURL = srv.URL
	cfg.Notion.NotesDataSourceID = "notes-ds"
	cfg.Notion.TasksDataSourceID = "tasks-ds"
	cfg.Notion.ProjectsDataSourceID = "projects-ds"
	if err := cfg.finalize(); err != nil {
		t.Fatal(err)
	}
	logger := log.New(io.Discard, "", 0)
	if testing.Verbose() {
		logger = log.New(os.Stdout, "    ", 0)
	}
	app, err := NewApp(cfg, logger, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	return app, dir
}

func writeOld(t *testing.T, path, content string, age time.Duration) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().Add(-age)
	if err := os.Chtimes(path, ts, ts); err != nil {
		t.Fatal(err)
	}
}

func remaining(t *testing.T, dir string) []string {
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestEndToEnd(t *testing.T) {
	f := &fakeServer{t: t, appends: map[string]int{}}
	app, dir := setup(t, f)
	ctx := context.Background()

	// MacWhisper-Export: gleicher Name, Inhalt entscheidet
	writeOld(t, filepath.Join(dir, "Meeting 2026-10-28 14-00.txt"), srtLikeText(), 5*time.Minute)
	writeOld(t, filepath.Join(dir, "Meeting 2026-10-28 14-00 (1).txt"), "Zusammenfassung\n\nEs ging um die Pipeline. Cache kommt.\n", 5*time.Minute)

	// Archivmonat wie in der App: mtime in der Config-Zeitzone
	st, err := os.Stat(filepath.Join(dir, "Meeting 2026-10-28 14-00.txt"))
	if err != nil {
		t.Fatal(err)
	}
	month := st.ModTime().In(app.cfg.loc).Format("2006-01")

	app.Pass(ctx, time.Now())

	if len(f.pages) != 2 {
		t.Fatalf("erwartet 1 Notiz + 1 Task, bekommen %d Seiten", len(f.pages))
	}
	note, task := f.pages[0], f.pages[1]

	parent := note["parent"].(map[string]any)
	if parent["type"] != "data_source_id" || parent["data_source_id"] != "notes-ds" {
		t.Errorf("falscher Parent: %v", parent)
	}
	np := note["properties"].(map[string]any)
	if got := mustJSON(np["Status"]); !strings.Contains(got, `"Inbox"`) {
		t.Errorf("Status nicht Inbox: %s", got)
	}
	if got := mustJSON(np["Name"]); !strings.Contains(got, "2026-10-28 – Sirius Sync") {
		t.Errorf("Titel: %s", got)
	}
	if got := mustJSON(np["Projects"]); !strings.Contains(got, "proj-1") {
		t.Errorf("Projekt nicht verknüpft: %s", got)
	}
	if got := mustJSON(np["Prüfen"]); !strings.Contains(got, "false") {
		t.Errorf("Prüfen: %s", got)
	}
	body := strings.ReplaceAll(mustJSON(note["children"]), `\u0026`, "&")
	for _, h := range []string{"📅 Meeting-Info", "👥 Teilnehmer", "🎯 Kontext & Ziel des Meetings", "📝 Besprochene Themen", "✅ Entscheidungen", "🚀 Next Actions", "❓ Offene Fragen / Parked", "📅 Nächstes Meeting", "🎙️ Transkript"} {
		if !strings.Contains(body, h) {
			t.Errorf("Abschnitt fehlt: %s", h)
		}
	}
	if !strings.Contains(body, "14:00–14:47") {
		t.Errorf("Uhrzeit aus Dateiname + Dauer fehlt")
	}

	tp := task["properties"].(map[string]any)
	if task["parent"].(map[string]any)["data_source_id"] != "tasks-ds" {
		t.Errorf("Task in falscher Datenbank")
	}
	if got := mustJSON(tp["Name"]); !strings.Contains(got, "Budget für Runner klären") {
		t.Errorf("Task-Titel: %s", got)
	}
	if got := mustJSON(tp["Notes"]); !strings.Contains(got, "page-1") {
		t.Errorf("Task nicht mit Notiz verknüpft: %s", got)
	}
	if got := mustJSON(tp["Due Date"]); !strings.Contains(got, "2026-10-30") {
		t.Errorf("Deadline fehlt: %s", got)
	}
	if !f.sawSchema {
		t.Error("Structured Output wurde nicht genutzt")
	}

	if left := remaining(t, dir); len(left) != 0 {
		t.Errorf("Dateien nicht archiviert: %v", left)
	}
	archived, _ := os.ReadDir(filepath.Join(dir, "_archiv", month))
	if len(archived) != 2 {
		t.Errorf("Archiv enthält %d Dateien, erwartet 2", len(archived))
	}

	// zweiter Durchlauf: nichts Neues
	app.Pass(ctx, time.Now())
	if len(f.pages) != 2 {
		t.Fatalf("Doppelverarbeitung: %d Seiten", len(f.pages))
	}

	// dieselbe Datei taucht nochmal auf → wird erkannt und nur archiviert
	writeOld(t, filepath.Join(dir, "Kopie.txt"), srtLikeText(), 5*time.Minute)
	app.Pass(ctx, time.Now())
	if len(f.pages) != 2 {
		t.Fatalf("Duplikat nicht erkannt: %d Seiten", len(f.pages))
	}
	if left := remaining(t, dir); len(left) != 0 {
		t.Errorf("Duplikat nicht archiviert: %v", left)
	}
}

func TestWaitsForUnstableFiles(t *testing.T) {
	f := &fakeServer{t: t, appends: map[string]int{}}
	app, dir := setup(t, f)
	writeOld(t, filepath.Join(dir, "frisch.txt"), srtLikeText(), 2*time.Second)
	app.Pass(context.Background(), time.Now())
	if f.llmCalls != 0 || len(f.pages) != 0 {
		t.Fatal("Datei wurde verarbeitet, obwohl sie noch geschrieben werden könnte")
	}
}

func TestOrphanSummaryAndFallbackAndLongPage(t *testing.T) {
	f := &fakeServer{t: t, appends: map[string]int{}, rejectSchema: true, themesInReply: 40}
	app, dir := setup(t, f)
	app.cfg.OrphanSummaryMinutes = 1
	app.cfg.Notion.CreateTasksFor = "none"

	writeOld(t, filepath.Join(dir, "Notiz.md"), "# Zusammenfassung\n\nNur Text ohne Zeitmarken.\n", 5*time.Minute)
	app.Pass(context.Background(), time.Now())

	if len(f.pages) != 1 {
		t.Fatalf("erwartet 1 Notiz, bekommen %d", len(f.pages))
	}
	if f.llmCalls != 2 {
		t.Errorf("Fallback ohne Structured Output: %d LLM-Aufrufe, erwartet 2", f.llmCalls)
	}
	body := mustJSON(f.pages[0]["children"])
	if !strings.Contains(body, "Bitte prüfen") {
		t.Error("Prüf-Hinweis fehlt bei Notiz ohne Transkript")
	}
	if got := mustJSON(f.pages[0]["properties"].(map[string]any)["Prüfen"]); !strings.Contains(got, "true") {
		t.Errorf("Prüfen-Checkbox nicht gesetzt: %s", got)
	}
	if f.appends["page-1"] == 0 {
		t.Error("Lange Seite: restliche Blöcke wurden nicht angehängt")
	}
}

func srtLikeText() string {
	var b strings.Builder
	speakers := []string{"Speaker 1", "Speaker 2"}
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&b, "[00:%02d:%02d] %s: Satz Nummer %d über die Pipeline, Caching und Runner-Kosten.\n", i*47/60, (i*13)%60, speakers[i%2], i)
	}
	b.WriteString("[00:47:00] Speaker 1: Danke Sascha, bis Freitag.\n")
	return b.String()
}
