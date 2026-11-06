package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"meetingsync/internal/domain"
)

// Hilfsmittel für die LLM-Client-Tests, Präfix lc (kollisionsfrei zu den anderen Testdateien).

type lcReply struct {
	status int
	body   string
}

// lcChat baut eine Chat-Completions-Antwort.
func lcChat(content, finish string) lcReply {
	b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{
		"message": map[string]any{"content": content}, "finish_reason": finish,
	}}})
	return lcReply{200, string(b)}
}

const lcNote = `{"titel":"Planung","typ":"Team"}`

type lcServer struct {
	mu      sync.Mutex
	replies []lcReply
	reqs    []map[string]any // geparste Chat-Bodys
	auth    []string
}

func (s *lcServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	i := len(s.reqs)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	s.reqs = append(s.reqs, m)
	s.auth = append(s.auth, r.Header.Get("Authorization"))
	if i >= len(s.replies) {
		i = len(s.replies) - 1
	}
	rep := s.replies[i]
	s.mu.Unlock()
	w.WriteHeader(rep.status)
	_, _ = io.WriteString(w, rep.body)
}

func lcLLM(t *testing.T, mod func(*Options), replies ...lcReply) (*Client, *lcServer) {
	t.Helper()
	s := &lcServer{replies: replies}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	cfg := Options{BaseURL: srv.URL + "/", Model: "gemma", Temperature: 0.2, Timeout: time.Minute, MaxChunkChars: 100000}
	if mod != nil {
		mod(&cfg)
	}
	return NewClient(cfg, log.New(io.Discard, "", 0), nil), s
}

func lcIn(text string) NoteInput {
	return NoteInput{Text: text, MyName: "Mark", Projects: []string{"Alpha"}}
}

func lcMsgs(req map[string]any) (system, user string) {
	msgs, _ := req["messages"].([]any)
	for _, m := range msgs {
		mm := m.(map[string]any)
		switch mm["role"] {
		case "system":
			system = mm["content"].(string)
		case "user":
			user = mm["content"].(string)
		}
	}
	return
}

// Fängt: Retry-Schleife bricht nach dem ersten Parse-Fehler ab (Schleife < 3
// geändert) oder gibt die kaputte Antwort weiter.
func TestSummarizeRetriesOnParseError(t *testing.T) {
	l, s := lcLLM(t, nil, lcChat("das ist kein JSON", "stop"), lcChat(`{"titel":""}`, "stop"), lcChat(lcNote, "stop"))
	n, err := l.Summarize(context.Background(), lcIn("hallo"))
	if err != nil || n.Titel != "Planung" {
		t.Fatalf("note = %+v, err = %v", n, err)
	}
	if len(s.reqs) != 3 {
		t.Errorf("Requests = %d, erwartet 3", len(s.reqs))
	}
}

// Fängt: zu viele/zu wenige Versuche und verlorener Fehlertext.
func TestSummarizeParseErrorGivesUpAfterThreeAttempts(t *testing.T) {
	l, s := lcLLM(t, nil, lcChat("müll", "stop"))
	_, err := l.Summarize(context.Background(), lcIn("hallo"))
	if err == nil || !strings.Contains(err.Error(), "kein gültiges Notiz-JSON") {
		t.Fatalf("err = %v", err)
	}
	if domain.IsTransient(err) {
		t.Error("Parse-Fehler ist nicht transient (Datei soll nach max_attempts in _fehler)")
	}
	if len(s.reqs) != 3 {
		t.Errorf("Requests = %d, erwartet 3", len(s.reqs))
	}
}

// Fängt: finish_reason=length wird ignoriert (abgeschnittenes JSON würde
// verarbeitet) oder wiederholt (sinnlos, gleiches Ergebnis).
func TestSummarizeFinishReasonLengthFailsWithoutRetry(t *testing.T) {
	l, s := lcLLM(t, nil, lcChat(lcNote, "length"), lcChat(lcNote, "stop"))
	_, err := l.Summarize(context.Background(), lcIn("hallo"))
	if err == nil || !strings.Contains(err.Error(), "abgeschnitten") {
		t.Fatalf("err = %v", err)
	}
	if len(s.reqs) != 1 {
		t.Errorf("Requests = %d, erwartet 1 (kein Retry)", len(s.reqs))
	}
}

// Fängt: Structured Output sendet kein/ falsches response_format; ohne
// Structured Output steht das Schema stattdessen im System-Prompt.
func TestSummarizeSchemaPlacement(t *testing.T) {
	l, s := lcLLM(t, func(c *Options) { c.StructuredOutput = true; c.APIKey = "key" }, lcChat(lcNote, "stop"))
	if _, err := l.Summarize(context.Background(), lcIn("hallo")); err != nil {
		t.Fatal(err)
	}
	rf, ok := s.reqs[0]["response_format"].(map[string]any)
	if !ok || rf["type"] != "json_schema" {
		t.Fatalf("response_format = %v", s.reqs[0]["response_format"])
	}
	if sys, _ := lcMsgs(s.reqs[0]); strings.Contains(sys, "JSON-Schema der Antwort") {
		t.Error("bei Structured Output gehört das Schema nicht in den Prompt")
	}
	if s.auth[0] != "Bearer key" || s.reqs[0]["model"] != "gemma" || s.reqs[0]["stream"] != false {
		t.Errorf("Auth %q, Request %v", s.auth[0], s.reqs[0])
	}

	l, s = lcLLM(t, nil, lcChat(lcNote, "stop"))
	if _, err := l.Summarize(context.Background(), lcIn("hallo")); err != nil {
		t.Fatal(err)
	}
	if _, has := s.reqs[0]["response_format"]; has {
		t.Error("ohne Structured Output darf kein response_format gesendet werden")
	}
	if sys, _ := lcMsgs(s.reqs[0]); !strings.Contains(sys, "JSON-Schema der Antwort") {
		t.Error("Schema fehlt im System-Prompt")
	}
	if s.auth[0] != "" {
		t.Errorf("ohne APIKey darf kein Authorization-Header gesendet werden: %q", s.auth[0])
	}
}

// Fängt: kein Fallback bei 4xx (Structured Output abgelehnt) oder Fallback
// ohne das Schema im Prompt; der zweite Versuch muss ohne response_format laufen.
func TestSummarizeSchemaFallbackOn4xx(t *testing.T) {
	l, s := lcLLM(t, func(c *Options) { c.StructuredOutput = true },
		lcReply{400, `{"error":"response_format json_schema not supported"}`}, lcChat(lcNote, "stop"))
	n, err := l.Summarize(context.Background(), lcIn("hallo"))
	if err != nil || n.Titel != "Planung" {
		t.Fatalf("note = %+v, err = %v", n, err)
	}
	if len(s.reqs) != 2 {
		t.Fatalf("Requests = %d, erwartet 2", len(s.reqs))
	}
	if _, has := s.reqs[0]["response_format"]; !has {
		t.Error("erster Versuch muss response_format senden")
	}
	if _, has := s.reqs[1]["response_format"]; has {
		t.Error("zweiter Versuch darf kein response_format senden")
	}
	if sys, _ := lcMsgs(s.reqs[1]); !strings.Contains(sys, "JSON-Schema der Antwort") {
		t.Error("Fallback muss das Schema in den Prompt legen")
	}
}

// Fängt: Fallback auch ohne Structured Output (Endlosschleife/Doppelversuch),
// bzw. Fallback bei 5xx, das transient bleiben muss.
func TestSummarizeNoFallbackWithoutSchemaOrOn5xx(t *testing.T) {
	l, s := lcLLM(t, nil, lcReply{400, `{"error":"bad request"}`}, lcChat(lcNote, "stop"))
	_, err := l.Summarize(context.Background(), lcIn("hallo"))
	var he *httpError
	if !errors.As(err, &he) || he.Status != 400 || len(s.reqs) != 1 {
		t.Errorf("err = %v, Requests = %d, erwartet httpError 400 nach 1 Request", err, len(s.reqs))
	}

	l, s = lcLLM(t, func(c *Options) { c.StructuredOutput = true }, lcReply{503, "down"}, lcChat(lcNote, "stop"))
	_, err = l.Summarize(context.Background(), lcIn("hallo"))
	if err == nil || !domain.IsTransient(err) || len(s.reqs) != 1 {
		t.Errorf("err = %v, Requests = %d, erwartet transient nach 1 Request", err, len(s.reqs))
	}
}

// Fängt: Chunking wird nicht ausgelöst, falscher Chunk-Prompt, Teilnotizen
// fehlen im finalen Request, <think> wird nicht entfernt.
func TestSummarizeChunksLongTranscript(t *testing.T) {
	text := "zeile eins aaaa\nzeile zwei bbbb\nzeile drei cccc\n"
	l, s := lcLLM(t, func(c *Options) { c.MaxChunkChars = 20 },
		lcChat("<think>intern</think>- Punkt A", "stop"),
		lcChat("- Punkt B", "stop"),
		lcChat("- Punkt C", "stop"),
		lcChat(lcNote, "stop"))
	if _, err := l.Summarize(context.Background(), lcIn(text)); err != nil {
		t.Fatal(err)
	}
	if len(s.reqs) != 4 {
		t.Fatalf("Requests = %d, erwartet 3 Chunks + 1 Finale", len(s.reqs))
	}
	for i := 0; i < 3; i++ {
		sys, user := lcMsgs(s.reqs[i])
		if sys != chunkSystemPrompt(lcIn(text)) {
			t.Errorf("Chunk %d: falscher System-Prompt", i+1)
		}
		if !strings.HasPrefix(user, fmt.Sprintf("Abschnitt %d von 3:", i+1)) {
			t.Errorf("Chunk %d: user = %q", i+1, user)
		}
		if _, has := s.reqs[i]["response_format"]; has {
			t.Errorf("Chunk %d: Chunk-Requests nutzen kein Schema", i+1)
		}
	}
	_, final := lcMsgs(s.reqs[3])
	for _, want := range []string{"Teilnotizen", "### Abschnitt 1\n- Punkt A", "### Abschnitt 2\n- Punkt B", "### Abschnitt 3\n- Punkt C"} {
		if !strings.Contains(final, want) {
			t.Errorf("finaler Prompt enthält %q nicht:\n%s", want, final)
		}
	}
	if strings.Contains(final, "intern") || strings.Contains(final, "zeile eins") {
		t.Error("finaler Prompt darf weder <think>-Inhalt noch Rohtranskript enthalten")
	}
}

// Fängt: Chunking bei Text genau an der Grenze (len > max, nicht >=) und
// ein Fehler im Chunk-Request, der verschluckt statt weitergereicht wird.
func TestSummarizeChunkBoundaryAndChunkError(t *testing.T) {
	text := strings.Repeat("x", 20)
	l, s := lcLLM(t, func(c *Options) { c.MaxChunkChars = 20 }, lcChat(lcNote, "stop"))
	if _, err := l.Summarize(context.Background(), lcIn(text)); err != nil || len(s.reqs) != 1 {
		t.Errorf("Text == max darf nicht gechunkt werden: err = %v, Requests = %d", err, len(s.reqs))
	}

	l, s = lcLLM(t, func(c *Options) { c.MaxChunkChars = 10 }, lcReply{503, "busy"})
	_, err := l.Summarize(context.Background(), lcIn(strings.Repeat("y", 25)))
	if err == nil || !domain.IsTransient(err) || len(s.reqs) != 1 {
		t.Errorf("err = %v, Requests = %d, erwartet transienten Fehler nach 1 Request", err, len(s.reqs))
	}
}

// Fängt: Label für Nur-Zusammenfassung wird nicht gesetzt.
func TestSummarizeSummaryOnlyLabel(t *testing.T) {
	l, s := lcLLM(t, nil, lcChat(lcNote, "stop"))
	in := lcIn("kurz")
	in.SummaryOnly = true
	if _, err := l.Summarize(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if _, user := lcMsgs(s.reqs[0]); !strings.Contains(user, "kein Transkript mit Zeitmarken") {
		t.Errorf("user = %q", user)
	}
}

// Fängt: MyName fehlt im Chunk-Prompt, Anweisungen gehen verloren.
func TestChunkSystemPrompt(t *testing.T) {
	p := chunkSystemPrompt(NoteInput{MyName: "Mark"})
	for _, want := range []string{"Mark ist einer der Teilnehmenden", "Nur Stichpunkte", "Entscheidungen", "Sprecher-Label"} {
		if !strings.Contains(p, want) {
			t.Errorf("Prompt enthält %q nicht", want)
		}
	}
	if strings.Contains(p, "%s") {
		t.Error("Platzhalter nicht ersetzt")
	}
}

// Fängt: falsche Klassifizierung der Fehlerantworten in chat.
func TestChatErrorClassification(t *testing.T) {
	cases := []struct {
		name      string
		reply     lcReply
		transient bool
		contains  string
	}{
		{"429", lcReply{429, "limit"}, true, "HTTP 429"},
		{"500", lcReply{500, "kaputt"}, true, "HTTP 500"},
		{"Modell nicht geladen", lcReply{400, `{"error":"No models loaded."}`}, true, "nicht geladen"},
		{"Modell nicht gefunden", lcReply{404, `{"error":"Model gemma not found"}`}, true, "nicht geladen"},
		{"sonstiger 4xx", lcReply{422, `{"error":"zu lang"}`}, false, "HTTP 422"},
		{"kein JSON", lcReply{200, "<html>"}, false, "unerwartete Antwort"},
		{"keine Choices", lcReply{200, `{"choices":[]}`}, false, "keine Antwort"},
	}
	for _, c := range cases {
		l, _ := lcLLM(t, nil, c.reply)
		_, err := l.chat(context.Background(), []chatMsg{{"user", "x"}}, nil)
		if err == nil || !strings.Contains(err.Error(), c.contains) || domain.IsTransient(err) != c.transient {
			t.Errorf("%s: err = %v (transient %v), erwartet %q / transient %v", c.name, err, err != nil && domain.IsTransient(err), c.contains, c.transient)
		}
	}
}

// Fängt: nicht erreichbarer Server wird nicht als transient markiert.
func TestChatUnreachableIsTransient(t *testing.T) {
	l, _ := lcLLM(t, func(c *Options) { c.BaseURL = "http://127.0.0.1:1" })
	_, err := l.chat(context.Background(), nil, nil)
	if err == nil || !domain.IsTransient(err) || !strings.Contains(err.Error(), "nicht erreichbar") {
		t.Errorf("err = %v", err)
	}
}

// Fängt: Models liest Pfad/Header falsch oder liefert IDs in falscher Reihenfolge.
func TestModelsListsIDs(t *testing.T) {
	var path, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"data":[{"id":"gemma"},{"id":"qwen"}]}`)
	}))
	defer srv.Close()
	l := NewClient(Options{BaseURL: srv.URL + "/", APIKey: "k", Timeout: time.Minute}, log.New(io.Discard, "", 0), nil)
	ids, err := l.Models(context.Background())
	if err != nil || len(ids) != 2 || ids[0] != "gemma" || ids[1] != "qwen" {
		t.Fatalf("ids = %v, err = %v", ids, err)
	}
	if path != "/models" || auth != "Bearer k" {
		t.Errorf("Pfad %q, Auth %q", path, auth)
	}
}

// Fängt: Models wertet den Statuscode nicht aus (Finding A-08).
func TestModelsChecksStatusCode(t *testing.T) {
	for _, rep := range []lcReply{{401, `{"error":"unauthorized"}`}, {500, "Internal Server Error"}} {
		l, _ := lcLLM(t, nil, rep)
		ids, err := l.Models(context.Background())
		if err == nil || !strings.Contains(err.Error(), "HTTP "+strconv.Itoa(rep.status)) || len(ids) != 0 {
			t.Errorf("Status %d: ids = %v, err = %v", rep.status, ids, err)
		}
		if err != nil && strings.Contains(err.Error(), rep.body) {
			t.Errorf("Fehler enthält den Body: %v", err)
		}
	}
}

// Fängt: fehlendes Antwortlimit, Body-Inhalt im Fehler, falsche Einstufung
// bei Statusfehlern, verschluckte Lesefehler.
func TestResponseLimitsAndReadErrors(t *testing.T) {
	big := strings.Repeat("x", 200)
	l, _ := lcLLM(t, nil, lcReply{200, big})
	l.maxBytes = 100
	if _, err := l.chat(context.Background(), nil, nil); !errors.Is(err, errAntwortZuGross) || strings.Contains(err.Error(), "xxx") || domain.IsTransient(err) {
		t.Errorf("2xx zu groß: err = %v", err)
	}
	// genau am Limit ist erlaubt
	exact := lcChat("ok", "stop")
	l, _ = lcLLM(t, nil, exact)
	l.maxBytes = int64(len(exact.body))
	if out, err := l.chat(context.Background(), nil, nil); err != nil || out != "ok" {
		t.Errorf("Antwort genau am Limit: %q, %v", out, err)
	}
	// Fehlerstatus mit zu großem Body: Status bleibt maßgeblich, Einstufung unverändert
	l, _ = lcLLM(t, nil, lcReply{500, big})
	l.maxBytes = 100
	if _, err := l.chat(context.Background(), nil, nil); err == nil || !domain.IsTransient(err) || !strings.Contains(err.Error(), "HTTP 500") || strings.Contains(err.Error(), "xxx") {
		t.Errorf("500 zu groß: err = %v", err)
	}
	l, _ = lcLLM(t, nil, lcReply{422, big})
	l.maxBytes = 100
	if _, err := l.chat(context.Background(), nil, nil); err == nil || domain.IsTransient(err) || !strings.Contains(err.Error(), "HTTP 422") {
		t.Errorf("422 zu groß: err = %v", err)
	}
	// Models
	l, _ = lcLLM(t, nil, lcReply{200, big})
	l.maxBytes = 100
	if _, err := l.Models(context.Background()); !errors.Is(err, errAntwortZuGross) {
		t.Errorf("Models zu groß: err = %v", err)
	}
}

// Fängt: abgeschnittene 2xx-Antwort (Verbindung bricht mitten im Body ab) wird
// als leere/kaputte Antwort verschluckt.
func TestTruncatedBodyIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{"choices":[`)
	}))
	defer srv.Close()
	l := NewClient(Options{BaseURL: srv.URL, Timeout: time.Minute}, log.New(io.Discard, "", 0), nil)
	_, err := l.chat(context.Background(), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "nicht vollständig lesbar") {
		t.Errorf("chat: err = %v", err)
	}
	if _, err := l.Models(context.Background()); err == nil || !strings.Contains(err.Error(), "nicht vollständig lesbar") {
		t.Errorf("Models: err = %v", err)
	}
}

// Fängt: Netzfehler in Models wird verschluckt.
func TestModelsUnreachable(t *testing.T) {
	l, _ := lcLLM(t, func(c *Options) { c.BaseURL = "http://127.0.0.1:1" })
	if _, err := l.Models(context.Background()); err == nil {
		t.Error("nicht erreichbarer Server muss einen Fehler liefern")
	}
}

// Fängt: LM Studio nimmt die Verbindung an, antwortet auf /models aber nie.
// Ohne eigenen Timeout hinge -check bis zum Timeout von llm.timeout_minutes.
func TestModelsTimesOutOnHangingServer(t *testing.T) {
	old := modelsTimeout
	modelsTimeout = 50 * time.Millisecond
	t.Cleanup(func() { modelsTimeout = old })
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	l := NewClient(Options{BaseURL: srv.URL, Timeout: time.Minute}, log.New(io.Discard, "", 0), nil)
	start := time.Now()
	ids, err := l.Models(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) || ids != nil {
		t.Fatalf("ids = %v, err = %v; want DeadlineExceeded", ids, err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Models brauchte %v, Timeout ist %v", d, modelsTimeout)
	}
}

func TestSplitHelpersKeepUTF8(t *testing.T) {
	long := strings.Repeat("äöü ", 2000)
	for _, c := range splitByLines(long, 5000) {
		if !utf8.ValidString(c) {
			t.Fatal("splitByLines zerschneidet UTF-8")
		}
	}
}

func TestParseNoteToleratesWrapping(t *testing.T) {
	raw := "<think>hmm</think>\n```json\n{\"titel\":\"Sirius Sync\",\"typ\":\"Team\",\"next_actions\":[{\"aufgabe\":\"Budget klären\",\"owner\":\"Mark\",\"deadline\":\"bis Freitag\",\"fuer_mich\":true}]}\n```"
	n, err := parseNote(raw)
	if err != nil {
		t.Fatal(err)
	}
	Normalize(n)
	if n.Titel != "Sirius Sync" || n.NextActions[0].Deadline != "" {
		t.Fatalf("%+v", n)
	}
}

func TestSchemaOrderAndEnum(t *testing.T) {
	s := mustJSON(noteSchema([]string{"AI-First"}))
	if strings.Index(s, `"titel"`) > strings.Index(s, `"pruefen"`) {
		t.Fatal("Feldreihenfolge stimmt nicht")
	}
	if strings.Index(s, `"pruef_gruende"`) > strings.Index(s, `"pruefen":`) {
		t.Fatal("pruef_gruende sollte vor pruefen stehen")
	}
	if !strings.Contains(s, `"AI-First"`) {
		t.Fatal("Projekt-Enum fehlt")
	}
}
