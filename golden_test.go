package main

// Golden-Harness (R-M0-05): friert das heutige Verhalten ein. Fakes für
// LM Studio und Notion zeichnen jeden Request auf; Dateibaum, Ledger und Log
// werden normalisiert mit testdata/golden/<szenario>/ verglichen.
// Neu schreiben: go test -run Golden ./... -update
// Szenarien kommen per registerGolden (in init) aus beliebigen Dateien dazu,
// z. B. golden_flow_test.go und golden_cli_test.go.
//
// Normalisierung, abschließende Liste (alles andere bleibt roh):
//   - Temp-Pfade (auch über /private aufgelöst)  → <tmp>
//   - Host:Port der Fakes                         → <llm>, <notion>
//   - Ledger-Feld "at", erste Zeile .fehler.txt   → <now>, nur wenn RFC3339 und
//     zwischen Start und Ende des Laufs, sonst roh
//   - Dauer in "LLM fertig nach …"                → <dur>
//   - Log-Zeitstempel                             → entfernt
//   - Authorization-Header                        → nur "vorhanden"/"Token korrekt"
//   - Notion-Bodies: JSON mit sortierten Schlüsseln; LLM-Bodies: Schlüsselreihenfolge
//     wie gesendet (Schema-Reihenfolge zählt). Beide nur eingerückt.
//   - Hash im Dateibaum: Ledger und .fehler.txt nach obiger Normalisierung, alle
//     anderen Dateien roh und mit mtime (UTC, RFC3339).

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
		_, _ = io.WriteString(w, `{"object":"error","status":404,"code":"object_not_found","message":"kein Fake-Skript für `+key+`"}`) //nolint:gosec // G705: Fake-Server im Test, Antwort ist JSON, kein HTML
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

// goldenNormalizer: from/to ist das Zeitfenster des Laufs. Nur Zeitpunkte darin
// werden zu <now>; ein falscher Zeitwert (z. B. die mtime) bleibt sichtbar.
type goldenNormalizer struct {
	repl     *strings.Replacer
	from, to time.Time
}

var (
	reGoldenLogTS  = regexp.MustCompile(`(?m)^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} `)
	reGoldenDur    = regexp.MustCompile(`(LLM fertig nach )\S+?(: )`)
	reGoldenAt     = regexp.MustCompile(`"at":"([^"]*)"`)
	reGoldenFehler = regexp.MustCompile(`^[^\n]*`)
)

func newGoldenNormalizer(root, llmHost, notionHost string, from, to time.Time) *goldenNormalizer {
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
	// RFC3339 in .fehler.txt hat nur Sekunden, darum from abrunden.
	return &goldenNormalizer{repl: strings.NewReplacer(pairs...), from: from.Truncate(time.Second), to: to}
}

func (n *goldenNormalizer) text(s string) string { return n.repl.Replace(s) }

// isNow: s ist ein RFC3339-Zeitpunkt innerhalb des Laufs.
func (n *goldenNormalizer) isNow(s string) bool {
	ts, err := time.Parse(time.RFC3339Nano, s)
	return err == nil && !ts.Before(n.from) && !ts.After(n.to)
}

func (n *goldenNormalizer) log(s string) string {
	s = reGoldenLogTS.ReplaceAllString(n.text(s), "")
	return reGoldenDur.ReplaceAllString(s, "${1}<dur>${2}")
}

func (n *goldenNormalizer) ledger(s string) string {
	return reGoldenAt.ReplaceAllStringFunc(n.text(s), func(m string) string {
		if n.isNow(reGoldenAt.FindStringSubmatch(m)[1]) {
			return `"at":"<now>"`
		}
		return m
	})
}

func (n *goldenNormalizer) fehler(s string) string {
	return reGoldenFehler.ReplaceAllStringFunc(n.text(s), func(m string) string {
		if n.isNow(m) {
			return "<now>"
		}
		return m
	})
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
// Roh gehashte Dateien zeigen ihre mtime (Eingaben haben feste mtimes),
// normalisierte (Ledger, .fehler.txt) nicht, die schreibt die App gerade erst.
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
		data, err := os.ReadFile(p) //nolint:gosec // G304: Pfad aus WalkDir unter t.TempDir()
		if err != nil {
			return err
		}
		if nd, ok := n.file(rel, data); ok {
			fmt.Fprintf(&b, "%x  %s (normalized)\n", sha256.Sum256(nd), rel)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "%x  %s  %s\n", sha256.Sum256(data), info.ModTime().UTC().Format(time.RFC3339), rel)
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
// Liefert die Umgebung für Re-Exec-Kindprozesse (mit TZ=UTC und Netzschutz-Proxy;
// im Testprozess setzt TestMain beides).
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
	t.Setenv("LLM_API_KEY", "") // Golden ohne LLM-Key (R-M3-15), auch wenn die Shell einen setzt
	t.Cleanup(func() {
		if e, _ := os.ReadDir(calls); len(e) > 0 {
			var names []string
			for _, x := range e {
				names = append(names, x.Name())
			}
			t.Errorf("Test hat externe Programme aufgerufen: %v", names)
		}
	})
	return append([]string{"HOME=" + home, "PATH=" + bin, "NOTION_TOKEN=" + goldenToken, "TZ=UTC"}, goldenProxyEnv...)
}

// --- Szenarien --------------------------------------------------------------

type goldenInput struct {
	name, content string
	mtime         time.Time
}

// goldenScenario ist entweder ein Pass-Szenario (passes) oder ein CLI-Szenario
// (args, Re-Exec von main()). CLI-Szenarien schreiben zusätzlich stdout.txt,
// stderr.txt und exit.txt.
type goldenScenario struct {
	name   string
	config string // YAML; Platzhalter {{watch}}, {{llm}}, {{notion}}
	files  []goldenInput
	llm    map[string][]fakeReply
	notion map[string][]fakeReply
	passes []time.Time // je ein Pass(ctx, now)
	args   []string    // CLI-Argumente; Platzhalter {{config}}, {{watch}}
}

var goldenRegistry = map[string]goldenScenario{}

// registerGolden nimmt Szenarien auf (aus init() jeder Szenario-Datei).
// Läuft nur aus init() (sequenziell), daher ohne Mutex. Doppelte Namen sind ein Programmierfehler.
func registerGolden(scs ...goldenScenario) {
	for _, sc := range scs {
		if _, dup := goldenRegistry[sc.name]; dup {
			panic("Golden-Szenario doppelt registriert: " + sc.name)
		}
		goldenRegistry[sc.name] = sc
	}
}

// goldenScenarios liefert alle registrierten Szenarien, nach Name sortiert.
func goldenScenarios() []goldenScenario {
	out := make([]goldenScenario, 0, len(goldenRegistry))
	for _, sc := range goldenRegistry {
		out = append(out, sc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// runGolden führt ein Szenario aus und liefert die normalisierten Ergebnisse.
func runGolden(t *testing.T, sc goldenScenario) map[string]string {
	t.Helper()
	if (len(sc.passes) > 0) == (sc.args != nil) {
		t.Fatalf("Szenario %s: genau eins von passes und args setzen", sc.name)
	}
	root := t.TempDir()
	env := goldenEnv(t, root)
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

	start := time.Now()
	out := map[string]string{}
	var logBuf bytes.Buffer
	var stdout, stderr string
	exit := 0
	if sc.args != nil {
		args := make([]string, len(sc.args))
		for i, a := range sc.args {
			args[i] = strings.NewReplacer("{{config}}", cfgPath, "{{watch}}", watch).Replace(a)
		}
		stdout, stderr, exit = goldenRunCLI(t, env, args...)
	} else {
		ctx := context.Background()
		app, err := goldenStart(ctx, cfgPath, log.New(&logBuf, "", 0), false)
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		for _, now := range sc.passes {
			app.Pass(ctx, now)
		}
	}
	end := time.Now()

	n := newGoldenNormalizer(root, llm.host(), notion.host(), start, end)
	out["requests.json"] = n.text(rec.json())
	out["tree.txt"] = goldenTree(t, watch, n)
	if sc.args != nil {
		out["stdout.txt"] = n.log(stdout)
		out["stderr.txt"] = n.log(stderr)
		out["exit.txt"] = strconv.Itoa(exit) + "\n"
	} else {
		out["log.txt"] = n.log(logBuf.String())
	}
	if b, err := os.ReadFile(filepath.Join(watch, "_archiv", ".meetingsync-ledger.jsonl")); err == nil { //nolint:gosec // G304: Pfad unter t.TempDir()
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
		want, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // G304: Pfad unter testdata/golden
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

// --- Gemeinsame Builder der Szenario-Dateien (flow und cli) ---

const goldenSummaryMD = `# Planung Q4

Kurze Notizen zum Gespräch: Das Team will den Build beschleunigen und führt dafür einen Cache ein.
Mark klärt das Budget für die Runner bis Freitag, Sascha baut den Cache.

Offen bleibt, wer den Cache reviewt.
`

// goldenCfg erweitert die Basis-Config: top am Anfang (oberste Ebene), llm nach
// dem Modell, notion am Ende des notion-Blocks.
func goldenCfg(top, llm, notion string) string {
	return top + strings.Replace(goldenBaseConfig, "  model: gemma-test\n", "  model: gemma-test\n"+llm, 1) + notion
}

// goldenNoteWith liefert die Modellantwort für eine Notiz; mod passt Felder an.
func goldenNoteWith(mod func(map[string]any)) string {
	n := map[string]any{
		"titel": "Planung Q4", "typ": "Team",
		"teilnehmer":     []string{"Mark", "Sascha"},
		"kontext_ziel":   "Build beschleunigen.",
		"themen":         []any{map[string]any{"titel": "Cache", "punkte": []string{"Cache fehlt", "Build dauert 12 Minuten"}}},
		"entscheidungen": []string{"Cache wird eingeführt"},
		"next_actions": []any{
			map[string]any{"aufgabe": "Budget für Runner klären", "owner": "Mark", "deadline": "2026-10-02", "fuer_mich": true},
		},
		"offene_fragen":     []string{"Wer reviewt den Cache?"},
		"naechstes_meeting": map[string]any{"datum": "", "themen": []string{}},
		"projekt":           "",
		"pruef_gruende":     []string{},
		"pruefen":           false,
	}
	if mod != nil {
		mod(n)
	}
	return goldenNoteJSON(n)
}

// goldenLLM: Chat-Antworten in Reihenfolge, jede mit finish_reason=stop.
func goldenLLM(replies ...string) map[string][]fakeReply {
	var rs []fakeReply
	for _, r := range replies {
		rs = append(rs, fakeReply{Body: goldenChat(r, "stop")})
	}
	return map[string][]fakeReply{"POST /v1/chat/completions": rs}
}

// goldenLLMError: jede Chat-Anfrage bekommt denselben Fehlerstatus.
func goldenLLMError(status int, body string) map[string][]fakeReply {
	return map[string][]fakeReply{"POST /v1/chat/completions": {{Status: status, Body: body}}}
}

func goldenSRTInput(name string, at time.Time) goldenInput {
	return goldenInput{name: name, content: goldenSRT, mtime: at}
}

func goldenSummaryInput(name string, at time.Time) goldenInput {
	return goldenInput{name: name, content: goldenSummaryMD, mtime: at}
}

// goldenNotFound: realistische Notion-404-Antwort für eine nicht verbundene Datenbank.
func goldenNotFound(id string) string {
	return `{"object":"error","status":404,"code":"object_not_found","message":"Could not find database with ID: ` + id +
		`. Make sure the relevant pages and databases are shared with your integration."}`
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
my_role: Head of Engineering
example_name: Sascha
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

func init() {
	registerGolden(
		goldenScenario{
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
	)
}

// TestGolden ist der gemeinsame Runner für alle registrierten Szenarien.
func TestGolden(t *testing.T) {
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
	n := newGoldenNormalizer(root, "", "", time.Time{}, time.Time{})
	want := "Konfiguration: kann <tmp>/fehlt.yaml nicht lesen: open <tmp>/fehlt.yaml: no such file or directory\n"
	if got := n.log(stdout); got != want || exit != 1 || stderr != "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, got, stderr)
	}
	// Usage wie beim gebauten Binary: Text aus main(), kein "Usage of …".
	stdout, stderr, exit = goldenRunCLI(t, env, "-bogus")
	if exit != 2 || stdout != "" || !strings.HasPrefix(stderr, "flag provided but not defined: -bogus\nmeetingsync – ") || strings.Contains(stderr, "test.") {
		t.Fatalf("-bogus: exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}
}

// Runner für CLI-Szenarien: Platzhalter, stdout/stderr/exit landen im Ergebnis.
func TestGoldenHarnessCLIScenario(t *testing.T) {
	got := runGolden(t, goldenScenario{
		name: "cli-selbsttest", config: goldenBaseConfig,
		args: []string{"-config", "{{watch}}/fehlt.yaml", "-check"},
	})
	want := "Konfiguration: kann <tmp>/watch/fehlt.yaml nicht lesen: open <tmp>/watch/fehlt.yaml: no such file or directory\n"
	if got["stdout.txt"] != want || got["stderr.txt"] != "" || got["exit.txt"] != "1\n" || got["tree.txt"] != "" {
		t.Fatalf("%q", got)
	}
	if _, ok := got["log.txt"]; ok {
		t.Error("CLI-Szenario darf kein log.txt haben")
	}
}

// Netzschutz: Requests an fremde Hosts gehen an den toten Proxy, Loopback nicht.
func TestGoldenHarnessNoNetwork(t *testing.T) {
	proxy := http.DefaultTransport.(*http.Transport).Proxy
	for _, u := range []string{"https://api.notion.com/v1/pages", "http://example.com/"} {
		req, _ := http.NewRequest(http.MethodGet, u, nil)
		if p, err := proxy(req); err != nil || p == nil || p.String() != goldenProxy {
			t.Errorf("%s: Proxy %v, %v", u, p, err)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:1234/v1", nil)
	if p, _ := proxy(req); p != nil {
		t.Errorf("Loopback darf nicht über den Proxy gehen: %v", p)
	}
}

func TestGoldenHarnessNormalizer(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 11, 1, 23, 4, 5, 500_000_000, time.UTC)
	n := newGoldenNormalizer(root, "127.0.0.1:1111", "127.0.0.1:2222", now, now.Add(time.Second))
	real, _ := filepath.EvalSymlinks(root)

	in := "2026/11/01 23:04:05 📦 a → " + real + "/x\n2026/11/01 23:04:05    LLM fertig nach 1m3s: \"T\" http://127.0.0.1:1111/v1 " + root + "\n"
	want := "📦 a → <tmp>/x\n   LLM fertig nach <dur>: \"T\" http://<llm>/v1 <tmp>\n"
	if got := n.log(in); got != want {
		t.Errorf("log:\n%q\n%q", got, want)
	}
	led := `{"hash":"h","file":"f","mtime":"2026-09-30T22:47:00Z","url":"https://127.0.0.1:2222/p","at":"2026-11-02T00:04:05.9+01:00"}` + "\n"
	if got := n.ledger(led); got != `{"hash":"h","file":"f","mtime":"2026-09-30T22:47:00Z","url":"https://<notion>/p","at":"<now>"}`+"\n" {
		t.Errorf("ledger: %s", got)
	}
	if got := n.fehler("2026-11-01T23:04:05Z\nkaputt in " + root + "\n"); got != "<now>\nkaputt in <tmp>\n" {
		t.Errorf("fehler: %q", got)
	}
	// Außerhalb des Laufs oder kein RFC3339: bleibt roh.
	for _, v := range []string{"2026-09-30T22:47:00Z", "2026-11-01T23:04:07Z", "gestern"} {
		if got := n.ledger(`{"at":"` + v + `"}`); got != `{"at":"`+v+`"}` {
			t.Errorf("ledger %s: %s", v, got)
		}
		if got := n.fehler(v + "\nx\n"); got != v+"\nx\n" {
			t.Errorf("fehler %s: %q", v, got)
		}
	}
	if _, ok := n.file("_archiv/2026-10/a.srt", []byte("x")); ok {
		t.Error("normale Dateien müssen roh gehasht werden")
	}
	// tree.txt: mtime nur für roh gehashte Dateien.
	dir := t.TempDir()
	mt := time.Date(2026, 9, 30, 22, 47, 0, 0, time.FixedZone("x", 3600))
	for _, name := range []string{"a.srt", "b.srt.fehler.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(filepath.Join(dir, name), mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	tree := goldenTree(t, dir, n)
	if !strings.Contains(tree, "  2026-09-30T21:47:00Z  a.srt\n") || strings.Count(tree, "2026-09-30") != 1 {
		t.Errorf("tree:\n%s", tree)
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
