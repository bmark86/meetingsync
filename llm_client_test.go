package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
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

func lcLLM(t *testing.T, mod func(*LLMConfig), replies ...lcReply) (*LLM, *lcServer) {
	t.Helper()
	s := &lcServer{replies: replies}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	cfg := LLMConfig{BaseURL: srv.URL + "/", Model: "gemma", Temperature: 0.2, TimeoutMinutes: 1, MaxChunkChars: 100000}
	if mod != nil {
		mod(&cfg)
	}
	return NewLLM(cfg, log.New(io.Discard, "", 0)), s
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
	if isTransient(err) {
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
	l, s := lcLLM(t, func(c *LLMConfig) { c.StructuredOutput = true; c.APIKey = "key" }, lcChat(lcNote, "stop"))
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
	l, s := lcLLM(t, func(c *LLMConfig) { c.StructuredOutput = true },
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

	l, s = lcLLM(t, func(c *LLMConfig) { c.StructuredOutput = true }, lcReply{503, "down"}, lcChat(lcNote, "stop"))
	_, err = l.Summarize(context.Background(), lcIn("hallo"))
	if err == nil || !isTransient(err) || len(s.reqs) != 1 {
		t.Errorf("err = %v, Requests = %d, erwartet transient nach 1 Request", err, len(s.reqs))
	}
}

// Fängt: Chunking wird nicht ausgelöst, falscher Chunk-Prompt, Teilnotizen
// fehlen im finalen Request, <think> wird nicht entfernt.
func TestSummarizeChunksLongTranscript(t *testing.T) {
	text := "zeile eins aaaa\nzeile zwei bbbb\nzeile drei cccc\n"
	l, s := lcLLM(t, func(c *LLMConfig) { c.MaxChunkChars = 20 },
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
	l, s := lcLLM(t, func(c *LLMConfig) { c.MaxChunkChars = 20 }, lcChat(lcNote, "stop"))
	if _, err := l.Summarize(context.Background(), lcIn(text)); err != nil || len(s.reqs) != 1 {
		t.Errorf("Text == max darf nicht gechunkt werden: err = %v, Requests = %d", err, len(s.reqs))
	}

	l, s = lcLLM(t, func(c *LLMConfig) { c.MaxChunkChars = 10 }, lcReply{503, "busy"})
	_, err := l.Summarize(context.Background(), lcIn(strings.Repeat("y", 25)))
	if err == nil || !isTransient(err) || len(s.reqs) != 1 {
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
		if err == nil || !strings.Contains(err.Error(), c.contains) || isTransient(err) != c.transient {
			t.Errorf("%s: err = %v (transient %v), erwartet %q / transient %v", c.name, err, err != nil && isTransient(err), c.contains, c.transient)
		}
	}
}

// Fängt: nicht erreichbarer Server wird nicht als transient markiert.
func TestChatUnreachableIsTransient(t *testing.T) {
	l, _ := lcLLM(t, func(c *LLMConfig) { c.BaseURL = "http://127.0.0.1:1" })
	_, err := l.chat(context.Background(), nil, nil)
	if err == nil || !isTransient(err) || !strings.Contains(err.Error(), "nicht erreichbar") {
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
	l := NewLLM(LLMConfig{BaseURL: srv.URL + "/", APIKey: "k", TimeoutMinutes: 1}, log.New(io.Discard, "", 0))
	ids, err := l.Models(context.Background())
	if err != nil || len(ids) != 2 || ids[0] != "gemma" || ids[1] != "qwen" {
		t.Fatalf("ids = %v, err = %v", ids, err)
	}
	if path != "/models" || auth != "Bearer k" {
		t.Errorf("Pfad %q, Auth %q", path, auth)
	}
}

// Charakterisiert HEUTIGES Verhalten (Finding A-08): Models prüft den
// Statuscode nicht. Ein 401 mit JSON-Body liefert eine leere Liste ohne Fehler,
// ein 500 mit Nicht-JSON einen Dekodierfehler. M1 darf diesen Test bewusst ändern.
// Fängt: unbeabsichtigte Verhaltensänderung in Models vor M1.
func TestModelsIgnoresStatusCodeToday(t *testing.T) {
	l, _ := lcLLM(t, nil, lcReply{401, `{"error":"unauthorized"}`})
	ids, err := l.Models(context.Background())
	if err != nil || len(ids) != 0 {
		t.Errorf("401 mit JSON: ids = %v, err = %v; heute leere Liste ohne Fehler", ids, err)
	}
	l, _ = lcLLM(t, nil, lcReply{500, "Internal Server Error"})
	if _, err := l.Models(context.Background()); err == nil {
		t.Error("500 mit Nicht-JSON: heute ein Dekodierfehler")
	}
}

// Fängt: Netzfehler in Models wird verschluckt.
func TestModelsUnreachable(t *testing.T) {
	l, _ := lcLLM(t, func(c *LLMConfig) { c.BaseURL = "http://127.0.0.1:1" })
	if _, err := l.Models(context.Background()); err == nil {
		t.Error("nicht erreichbarer Server muss einen Fehler liefern")
	}
}
