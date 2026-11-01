package main

// Golden-Harness (R-M0-05): friert das heutige Verhalten ein. Fakes für
// LM Studio und Notion zeichnen jeden Request auf; Dateibaum, Ledger und Log
// werden normalisiert mit testdata/golden/<szenario>/ verglichen.
// Neu schreiben: go test -run Golden ./... -update
//
// Normalisierung, abschließende Liste (alles andere bleibt roh):
//   - Temp-Pfade (auch über /private aufgelöst)  → <tmp>
//   - Host:Port der Fakes                         → <llm>, <notion>
//   - Ledger-Feld "at", erste Zeile .fehler.txt   → <now>
//   - Dauer in "LLM fertig nach …"                → <dur>
//   - Log-Zeitstempel                             → entfernt
//   - Authorization-Header                        → nur "vorhanden"/"Token korrekt"
//   - Notion-Bodies: JSON mit sortierten Schlüsseln; LLM-Bodies: Schlüsselreihenfolge
//     wie gesendet (Schema-Reihenfolge zählt). Beide nur eingerückt.
//   - Hash im Dateibaum: Ledger und .fehler.txt nach obiger Normalisierung, alle
//     anderen Dateien roh.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var goldenUpdate = flag.Bool("update", false, "Golden-Dateien unter testdata/golden neu schreiben")

const goldenToken = "golden-test-token"

// --- Recorder und Fakes -------------------------------------------------------

type goldenRequest struct {
	Server  string            `json:"server"`
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

type goldenRecorder struct {
	mu   sync.Mutex
	reqs []goldenRequest
}

func (r *goldenRecorder) json() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	reqs := append([]goldenRequest{}, r.reqs...)
	_ = enc.Encode(reqs)
	return b.String()
}

// fakeReply ist eine geskriptete Antwort. Body: string = roh, sonst JSON.
// "{n}" im Body wird durch die laufende Nummer des Aufrufs dieser Route ersetzt.
type fakeReply struct {
	Status int // 0 = 200
	Body   any
	Header map[string]string
}

// goldenFake ist ein httptest-Server auf 127.0.0.1. script: "METHODE /pfad" →
// Antworten der Reihe nach, die letzte wiederholt sich. Ohne Skript: 404.
type goldenFake struct {
	name   string
	token  string // erwarteter Bearer-Token, "" = nur Vorhandensein prüfen
	rec    *goldenRecorder
	script map[string][]fakeReply
	calls  map[string]int
	srv    *httptest.Server
}

func newGoldenFake(t *testing.T, name, token string, rec *goldenRecorder, script map[string][]fakeReply) *goldenFake {
	f := &goldenFake{name: name, token: token, rec: rec, script: script, calls: map[string]int{}}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *goldenFake) host() string { return strings.TrimPrefix(f.srv.URL, "http://") }

func (f *goldenFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	f.rec.mu.Lock()
	defer f.rec.mu.Unlock()

	headers := map[string]string{}
	for k, v := range r.Header {
		headers[k] = strings.Join(v, ", ")
	}
	if a, ok := headers["Authorization"]; ok {
		headers["Authorization"] = "<present>"
		if f.token != "" && a == "Bearer "+f.token {
			headers["Authorization"] = "Bearer <token>"
		}
	}
	f.rec.reqs = append(f.rec.reqs, goldenRequest{
		Server: f.name, Method: r.Method, Path: r.URL.RequestURI(), Headers: headers,
		Body: goldenBody(raw, f.name == "notion"),
	})

	key := r.Method + " " + r.URL.Path
	replies := f.script[key]
	if len(replies) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"object":"error","status":404,"code":"object_not_found","message":"kein Fake-Skript für `+key+`"}`)
		return
	}
	f.calls[key]++
	n := f.calls[key]
	rep := replies[min(n, len(replies))-1]
	body, ok := rep.Body.(string)
	if !ok {
		b, _ := json.Marshal(rep.Body)
		body = string(b)
	}
	body = strings.ReplaceAll(body, "{n}", strconv.Itoa(n))
	w.Header().Set("Content-Type", "application/json")
	for k, v := range rep.Header {
		w.Header().Set(k, v)
	}
	if rep.Status != 0 {
		w.WriteHeader(rep.Status)
	}
	_, _ = io.WriteString(w, body)
}

// goldenBody: sorted=true dekodiert und kodiert neu (sortierte Schlüssel),
// sonst bleibt die Reihenfolge wie gesendet. Kein JSON → als String.
func goldenBody(raw []byte, sorted bool) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if !json.Valid(raw) {
		b, _ := json.Marshal(string(raw))
		return b
	}
	if !sorted {
		return raw
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		b, _ := json.Marshal(string(raw))
		return b
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimSpace(b.Bytes())
}

// --- Normalisierung ---------------------------------------------------------

type goldenNormalizer struct{ repl *strings.Replacer }

var (
	reGoldenLogTS  = regexp.MustCompile(`(?m)^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} `)
	reGoldenDur    = regexp.MustCompile(`(LLM fertig nach )\S+?(: )`)
	reGoldenAt     = regexp.MustCompile(`("at":)"[^"]*"`)
	reGoldenFehler = regexp.MustCompile(`^[^\n]*`)
)

func newGoldenNormalizer(root, llmHost, notionHost string) *goldenNormalizer {
	roots := []string{root}
	if r, err := filepath.EvalSymlinks(root); err == nil && r != root {
		roots = append(roots, r)
	}
	sort.Slice(roots, func(i, j int) bool { return len(roots[i]) > len(roots[j]) })
	var pairs []string
	for _, r := range roots {
		pairs = append(pairs, r, "<tmp>")
	}
	if llmHost != "" {
		pairs = append(pairs, llmHost, "<llm>")
	}
	if notionHost != "" {
		pairs = append(pairs, notionHost, "<notion>")
	}
	return &goldenNormalizer{strings.NewReplacer(pairs...)}
}

func (n *goldenNormalizer) text(s string) string { return n.repl.Replace(s) }

func (n *goldenNormalizer) log(s string) string {
	s = reGoldenLogTS.ReplaceAllString(n.text(s), "")
	return reGoldenDur.ReplaceAllString(s, "${1}<dur>${2}")
}

func (n *goldenNormalizer) ledger(s string) string {
	return reGoldenAt.ReplaceAllString(n.text(s), `${1}"<now>"`)
}

func (n *goldenNormalizer) fehler(s string) string {
	return reGoldenFehler.ReplaceAllLiteralString(n.text(s), "<now>")
}

// file normalisiert Dateiinhalte vor dem Hashen; ok=false heißt: roh hashen.
func (n *goldenNormalizer) file(name string, data []byte) ([]byte, bool) {
	switch {
	case filepath.Base(name) == ".meetingsync-ledger.jsonl":
		return []byte(n.ledger(string(data))), true
	case strings.HasSuffix(name, ".fehler.txt"):
		return []byte(n.fehler(string(data))), true
	}
	return data, false
}

// goldenTree listet Ordner und Dateien unter root mit SHA-256 (ohne Rechte).
func goldenTree(t *testing.T, root string, n *goldenNormalizer) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			fmt.Fprintf(&b, "%s/\n", rel)
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		mark := ""
		if nd, ok := n.file(rel, data); ok {
			data, mark = nd, " (normalized)"
		}
		fmt.Fprintf(&b, "%x  %s%s\n", sha256.Sum256(data), rel, mark)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// --- Umgebung ---------------------------------------------------------------

// goldenEnv setzt HOME, PATH (nur Stubs) und NOTION_TOKEN auf Temp-Werte.
// security und osascript sind Stubs, die einen Marker schreiben und mit 1 enden.
// Liefert die Umgebung für Re-Exec-Kindprozesse.
func goldenEnv(t *testing.T, root string) []string {
	t.Helper()
	home, bin, calls := filepath.Join(root, "home"), filepath.Join(root, "bin"), filepath.Join(root, "calls")
	for _, d := range []string{home, bin, calls} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"security", "osascript"} {
		script := "#!/bin/sh\n: > '" + filepath.Join(calls, name) + "'\nexit 1\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin)
	t.Setenv("NOTION_TOKEN", goldenToken)
	t.Cleanup(func() {
		if e, _ := os.ReadDir(calls); len(e) > 0 {
			var names []string
			for _, x := range e {
				names = append(names, x.Name())
			}
			t.Errorf("Test hat externe Programme aufgerufen: %v", names)
		}
	})
	return []string{"HOME=" + home, "PATH=" + bin, "NOTION_TOKEN=" + goldenToken, "TZ=UTC"}
}

// goldenInUTCChild sorgt für Prozess-TZ=UTC: time.Local wird nur beim Start
// gelesen, darum läuft der Test bei anderer TZ einmal als Kindprozess.
// true = der Kindprozess hat den Test erledigt.
func goldenInUTCChild(t *testing.T) bool {
	t.Helper()
	if os.Getenv("TZ") == "UTC" {
		return false
	}
	args := []string{"-test.run=^" + t.Name() + "$", "-test.count=1"}
	if testing.Verbose() {
		args = append(args, "-test.v")
	}
	if *goldenUpdate {
		args = append(args, "-update")
	}
	cmd := exec.Command(os.Args[0], args...)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TZ=") && !strings.HasPrefix(kv, goldenMainEnv+"=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "TZ=UTC")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Golden-Test mit TZ=UTC fehlgeschlagen: %v\n%s", err, out)
	}
	if testing.Verbose() {
		t.Logf("Kindprozess mit TZ=UTC:\n%s", out)
	}
	return true
}

// --- Szenarien --------------------------------------------------------------

type goldenInput struct {
	name, content string
	mtime         time.Time
}

type goldenScenario struct {
	name   string
	config string // YAML; Platzhalter {{watch}}, {{llm}}, {{notion}}
	files  []goldenInput
	llm    map[string][]fakeReply
	notion map[string][]fakeReply
	passes []time.Time // je ein Pass(ctx, now)
}

// runGolden führt ein Pass-Szenario aus und liefert die normalisierten Ergebnisse.
func runGolden(t *testing.T, sc goldenScenario) map[string]string {
	t.Helper()
	root := t.TempDir()
	goldenEnv(t, root)
	rec := &goldenRecorder{}
	llm := newGoldenFake(t, "llm", "", rec, sc.llm)
	notion := newGoldenFake(t, "notion", goldenToken, rec, sc.notion)

	watch := filepath.Join(root, "watch")
	if err := os.MkdirAll(watch, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range sc.files {
		p := filepath.Join(watch, f.name)
		if err := os.WriteFile(p, []byte(f.content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, f.mtime, f.mtime); err != nil {
			t.Fatal(err)
		}
	}
	cfgPath := filepath.Join(root, "config.yaml")
	cfg := strings.NewReplacer("{{watch}}", watch, "{{llm}}", llm.srv.URL, "{{notion}}", notion.srv.URL).Replace(sc.config)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	var logBuf bytes.Buffer
	ctx := context.Background()
	app, err := goldenStart(ctx, cfgPath, log.New(&logBuf, "", 0), false)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	for _, now := range sc.passes {
		app.Pass(ctx, now)
	}

	n := newGoldenNormalizer(root, llm.host(), notion.host())
	out := map[string]string{
		"requests.json": n.text(rec.json()),
		"log.txt":       n.log(logBuf.String()),
		"tree.txt":      goldenTree(t, watch, n),
	}
	if b, err := os.ReadFile(filepath.Join(watch, "_archiv", ".meetingsync-ledger.jsonl")); err == nil {
		out["ledger.jsonl"] = n.ledger(string(b))
	}
	return out
}

// checkGolden vergleicht mit testdata/golden/<szenario>/ bzw. schreibt neu (-update).
// Veraltete Dateien werden gemeldet, nie gelöscht.
func checkGolden(t *testing.T, scenario string, got map[string]string) {
	t.Helper()
	dir := filepath.Join("testdata", "golden", scenario)
	names := make([]string, 0, len(got))
	for k := range got {
		names = append(names, k)
	}
	sort.Strings(names)
	if *goldenUpdate {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range names {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(got[name]), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, name := range names {
		want, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("%s/%s fehlt (neu schreiben: go test -run Golden ./... -update): %v", scenario, name, err)
			continue
		}
		if string(want) != got[name] {
			t.Errorf("%s/%s weicht ab:\n%s", scenario, name, goldenDiff(string(want), got[name]))
		}
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if _, ok := got[e.Name()]; !ok {
			t.Errorf("veraltete Golden-Datei %s/%s, bitte prüfen und löschen", scenario, e.Name())
		}
	}
}

// goldenDiff zeigt die erste abweichende Zeile mit etwas Kontext.
func goldenDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	i := 0
	for i < len(w) && i < len(g) && w[i] == g[i] {
		i++
	}
	line := func(l []string, k int) string {
		if k < len(l) {
			return l[k]
		}
		return "<Ende>"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "erste Abweichung in Zeile %d\n", i+1)
	for k := max(0, i-2); k < i; k++ {
		fmt.Fprintf(&b, "  %s\n", w[k])
	}
	fmt.Fprintf(&b, "- %s\n+ %s\n", line(w, i), line(g, i))
	return b.String()
}

// --- Bausteine für Antworten der Fakes -----------------------------------------

func goldenSchema(types map[string]string) map[string]any {
	p := map[string]any{}
	for k, v := range types {
		p[k] = map[string]any{"name": k, "type": v}
	}
	return map[string]any{"object": "data_source", "properties": p}
}

func goldenProjects(names ...string) map[string]any {
	var res []any
	for i, name := range names {
		res = append(res, map[string]any{
			"object": "page", "id": fmt.Sprintf("proj-%d", i+1),
			"properties": map[string]any{"Name": map[string]any{"type": "title", "title": []any{map[string]any{"plain_text": name}}}},
		})
	}
	return map[string]any{"object": "list", "results": res, "has_more": false, "next_cursor": nil}
}

func goldenChat(content, finish string) map[string]any {
	return map[string]any{"choices": []any{map[string]any{
		"index": 0, "message": map[string]any{"role": "assistant", "content": content}, "finish_reason": finish,
	}}}
}

func goldenNoteJSON(note map[string]any) string {
	b, _ := json.Marshal(note)
	return string(b)
}

// goldenNotionDefault: Notes, Tasks und Projects lesbar, Seiten anlegen klappt.
func goldenNotionDefault() map[string][]fakeReply {
	return map[string][]fakeReply{
		"GET /v1/data_sources/notes-ds": {{Body: goldenSchema(map[string]string{
			"Name": "title", "Status": "status", "Projects": "relation", "Prüfen": "checkbox"})}},
		"GET /v1/data_sources/tasks-ds": {{Body: goldenSchema(map[string]string{
			"Name": "title", "Description": "rich_text", "Due Date": "date", "Notes": "relation", "Projects": "relation", "Completed": "checkbox"})}},
		"GET /v1/data_sources/projects-ds": {{Body: goldenSchema(map[string]string{
			"Name": "title", "status": "status", "Archive": "checkbox"})}},
		"POST /v1/data_sources/projects-ds/query": {{Body: goldenProjects("AI-First", "Plattform")}},
		"POST /v1/pages": {{Body: `{"object":"page","id":"page-{n}","url":"https://www.notion.so/page-{n}"}`}},
	}
}

const goldenBaseConfig = `watch_dir: "{{watch}}"
notify: false
timezone: Europe/Berlin
llm:
  base_url: "{{llm}}/v1"
  model: gemma-test
notion:
  base_url: "{{notion}}"
  notes_data_source_id: notes-ds
  tasks_data_source_id: tasks-ds
  projects_data_source_id: projects-ds
`

// Feste mtime in der Vergangenheit: 22:47 UTC ist in Berlin schon der 1. Oktober.
// Archiv 2026-10 statt 2026-09 zeigt, dass nur die Config-Zeitzone zählt.
var goldenMTime = time.Date(2026, 9, 30, 22, 47, 0, 0, time.UTC)

const goldenSRT = `1
00:00:01,000 --> 00:00:05,000
Speaker 1: Hallo zusammen, kurzer Stand zur Build-Pipeline.

2
00:00:06,000 --> 00:00:12,500
Speaker 2: Der Build dauert gerade 12 Minuten, der Cache fehlt.

3
00:12:00,000 --> 00:12:08,000
Speaker 1: Dann führen wir den Cache ein. Ich kläre das Budget für die Runner bis Freitag.

4
00:46:50,000 --> 00:47:00,000
Speaker 2: Danke Mark, ich baue den Cache. Bis Freitag.
`

func goldenScenarios() []goldenScenario {
	return []goldenScenario{
		{
			name:   "G01",
			config: goldenBaseConfig,
			files:  []goldenInput{{name: "Sirius Sync.srt", content: goldenSRT, mtime: goldenMTime}},
			llm: map[string][]fakeReply{
				"POST /v1/chat/completions": {{Body: goldenChat(goldenNoteJSON(map[string]any{
					"titel": "Sirius Sync", "typ": "Team",
					"teilnehmer":     []string{"Mark", "Speaker 2 (vermutlich Sascha?)"},
					"kontext_ziel":   "Stand der Build-Pipeline klären.",
					"themen":         []any{map[string]any{"titel": "Pipeline", "punkte": []string{"Build dauert 12 Minuten", "Cache fehlt"}}},
					"entscheidungen": []string{"Cache wird eingeführt"},
					"next_actions": []any{
						map[string]any{"aufgabe": "Budget für Runner klären", "owner": "Mark", "deadline": "2026-10-02", "fuer_mich": true},
						map[string]any{"aufgabe": "Cache bauen", "owner": "Speaker 2", "deadline": "", "fuer_mich": false},
					},
					"offene_fragen":     []string{"Wer reviewt den Cache?"},
					"naechstes_meeting": map[string]any{"datum": "Freitag", "themen": []string{"Review Cache"}},
					"projekt":           "AI-First",
					"pruef_gruende":     []string{"Speaker 2 nicht sicher zugeordnet"},
					"pruefen":           true,
				}), "stop")}},
			},
			notion: goldenNotionDefault(),
			passes: []time.Time{goldenMTime.Add(5 * time.Minute)},
		},
	}
}

func TestGolden(t *testing.T) {
	if goldenInUTCChild(t) {
		return
	}
	for _, sc := range goldenScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			checkGolden(t, sc.name, runGolden(t, sc))
		})
	}
}

// --- Selbsttests der Harness ------------------------------------------------

// Re-Exec: main() läuft im Kindprozess, Exit-Code und stdout kommen an,
// Zeitstempel und Temp-Pfade werden normalisiert.
func TestGoldenHarnessCLI(t *testing.T) {
	root := t.TempDir()
	env := goldenEnv(t, root)
	missing := filepath.Join(root, "fehlt.yaml")
	stdout, stderr, exit := goldenRunCLI(t, env, "-config", missing, "-check")
	n := newGoldenNormalizer(root, "", "")
	want := "Konfiguration: kann <tmp>/fehlt.yaml nicht lesen: open <tmp>/fehlt.yaml: no such file or directory\n"
	if got := n.log(stdout); got != want || exit != 1 || stderr != "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, got, stderr)
	}
}

func TestGoldenHarnessNormalizer(t *testing.T) {
	root := t.TempDir()
	n := newGoldenNormalizer(root, "127.0.0.1:1111", "127.0.0.1:2222")
	real, _ := filepath.EvalSymlinks(root)

	in := "2026/11/01 23:04:05 📦 a → " + real + "/x\n2026/11/01 23:04:05    LLM fertig nach 1m3s: \"T\" http://127.0.0.1:1111/v1 " + root + "\n"
	want := "📦 a → <tmp>/x\n   LLM fertig nach <dur>: \"T\" http://<llm>/v1 <tmp>\n"
	if got := n.log(in); got != want {
		t.Errorf("log:\n%q\n%q", got, want)
	}
	led := `{"hash":"h","file":"f","mtime":"2026-09-30T22:47:00Z","url":"https://127.0.0.1:2222/p","at":"2026-11-01T23:04:05.123+01:00"}` + "\n"
	if got := n.ledger(led); got != `{"hash":"h","file":"f","mtime":"2026-09-30T22:47:00Z","url":"https://<notion>/p","at":"<now>"}`+"\n" {
		t.Errorf("ledger: %s", got)
	}
	if got := n.fehler("2026-11-01T23:04:05Z\nkaputt in " + root + "\n"); got != "<now>\nkaputt in <tmp>\n" {
		t.Errorf("fehler: %q", got)
	}
	if _, ok := n.file("_archiv/2026-10/a.srt", []byte("x")); ok {
		t.Error("normale Dateien müssen roh gehasht werden")
	}
	// Notion: sortierte Schlüssel; LLM: Reihenfolge wie gesendet.
	if got := string(goldenBody([]byte(`{"b":1,"a":"x & y"}`), true)); got != `{"a":"x & y","b":1}` {
		t.Errorf("sortiert: %s", got)
	}
	if got := string(goldenBody([]byte(`{"b":1,"a":2}`), false)); got != `{"b":1,"a":2}` {
		t.Errorf("Reihenfolge: %s", got)
	}
	if got := string(goldenBody([]byte(`kein json`), false)); got != `"kein json"` {
		t.Errorf("roh: %s", got)
	}
}
