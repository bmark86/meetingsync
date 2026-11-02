package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"meetingsync/internal/domain"
)

const lcSecret = "sk-synthetic-secret-123"

// A1: Summarizer wird vom *Client erfüllt (zusätzlich zur Assertion im Paket).
var _ Summarizer = (*Client)(nil)

// A3: Klassifizierung 5xx/Verbindungsfehler/4xx/Modell nicht geladen über domain.Transient.
func TestA3ClassificationViaDomainTransient(t *testing.T) {
	for _, c := range []struct {
		name      string
		reply     lcReply
		transient bool
	}{
		{"503", lcReply{503, "down"}, true},
		{"429", lcReply{429, "slow"}, true},
		{"400 normal", lcReply{400, `{"error":"bad request"}`}, false},
		{"401", lcReply{401, `{"error":"unauthorized"}`}, false},
		{"model not loaded", lcReply{400, `{"error":"model is not loaded"}`}, true},
	} {
		l, _ := lcLLM(t, nil, c.reply)
		_, err := l.Summarize(context.Background(), lcIn("x"))
		if err == nil || domain.IsTransient(err) != c.transient {
			t.Errorf("%s: err=%v transient=%v, erwartet %v", c.name, err, domain.IsTransient(err), c.transient)
		}
	}
}

// A3: httpError bleibt per errors.As erreichbar, auch durch Transient hindurch.
func TestA3HTTPErrorUnwrapsThroughTransient(t *testing.T) {
	l, _ := lcLLM(t, nil, lcReply{502, "bad gateway"})
	_, err := l.chat(context.Background(), nil, nil)
	var he *httpError
	if !errors.As(err, &he) || he.Status != 502 || !domain.IsTransient(err) {
		t.Errorf("err=%v", err)
	}
}

// A5: Key nur im Authorization-Header; nie in Fehlertexten.
func TestA5APIKeyOnlyInAuthorizationHeader(t *testing.T) {
	l, s := lcLLM(t, func(o *Options) { o.APIKey = lcSecret }, lcChat(lcNote, "stop"))
	if _, err := l.Summarize(context.Background(), lcIn("x")); err != nil {
		t.Fatal(err)
	}
	if len(s.auth) != 1 || s.auth[0] != "Bearer "+lcSecret {
		t.Errorf("auth = %v", s.auth)
	}
	raw := fmt.Sprint(s.reqs[0])
	if strings.Contains(raw, lcSecret) {
		t.Error("Key im Request-Body")
	}
	l2, s2 := lcLLM(t, nil, lcChat(lcNote, "stop"))
	if _, err := l2.Summarize(context.Background(), lcIn("x")); err != nil || s2.auth[0] != "" {
		t.Errorf("ohne Key darf kein Header gesetzt sein: %v %v", err, s2.auth)
	}
}

func TestA5APIKeyNeverInErrors(t *testing.T) {
	replies := map[string][]lcReply{
		"4xx":      {{422, `{"error":"zu lang"}`}},
		"5xx":      {{500, "kaputt"}},
		"notload":  {{400, "no models loaded"}},
		"badjson":  {{200, "<html>"}},
		"nochoice": {{200, `{"choices":[]}`}},
		"parse":    {lcChat("kein json", "stop")},
		"length":   {lcChat(lcNote, "length")},
	}
	for name, r := range replies {
		l, _ := lcLLM(t, func(o *Options) { o.APIKey = lcSecret }, r...)
		_, err := l.Summarize(context.Background(), lcIn("x"))
		if err == nil || strings.Contains(err.Error(), lcSecret) {
			t.Errorf("%s: err=%v", name, err)
		}
	}
	l, _ := lcLLM(t, func(o *Options) { o.APIKey = lcSecret; o.BaseURL = "http://127.0.0.1:1" })
	if _, err := l.Summarize(context.Background(), lcIn("x")); err == nil || strings.Contains(err.Error(), lcSecret) {
		t.Errorf("unreachable: %v", err)
	}
	if _, err := l.Models(context.Background()); err == nil || strings.Contains(err.Error(), lcSecret) {
		t.Errorf("models unreachable: %v", err)
	}
}

func TestModelsSendsBearer(t *testing.T) {
	l, s := lcLLM(t, func(o *Options) { o.APIKey = lcSecret }, lcReply{200, `{"data":[{"id":"a"}]}`})
	if _, err := l.Models(context.Background()); err != nil || s.auth[0] != "Bearer "+lcSecret {
		t.Errorf("%v %v", err, s.auth)
	}
}

func TestUserPromptVariants(t *testing.T) {
	d := time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC) // Montag
	in := NoteInput{Timing: domain.Timing{Start: d, HasTime: true}, SummaryText: "  Hilfe  "}
	got := userPrompt(in, "Transkript", "MAT")
	for _, w := range []string{"Meetingdatum: Montag, 02.11.2026 (2026-11-02), Beginn ca. 09:30", "Mögliche Projekte: keine\n", "<<<\nHilfe\n>>>", "Transkript:\n<<<\nMAT\n>>>"} {
		if !strings.Contains(got, w) {
			t.Errorf("fehlt %q in %q", w, got)
		}
	}
	in = NoteInput{Timing: domain.Timing{Start: d}, Projects: []string{"A", "B"}}
	got = userPrompt(in, "L", "M")
	if strings.Contains(got, "Beginn ca.") || !strings.Contains(got, "Projekte:\n- A\n- B\n") || strings.Contains(got, "MacWhisper") {
		t.Errorf("got %q", got)
	}
}

func TestSystemPromptLabel(t *testing.T) {
	if p := systemPrompt(NoteInput{MyName: "Mark", MyLabel: "Speaker 1"}); !strings.Contains(p, `Das Label "Speaker 1" ist Mark.`) {
		t.Error("MyLabel fehlt")
	}
	if p := systemPrompt(NoteInput{MyName: "Mark"}); !strings.Contains(p, "Eines der Labels ist Mark selbst") {
		t.Error("Default-Label fehlt")
	}
}

func TestNormalizeAndCleanList(t *testing.T) {
	n := &domain.MeetingNote{
		Titel: "  Ein   Titel  ", Typ: "Unbekannt",
		Teilnehmer:  []string{"- Anna", "anna", " ", "• Bob"},
		KontextZiel: " Ziel ",
		Themen:      []domain.Thema{{Titel: " ", Punkte: nil}, {Titel: " T ", Punkte: []string{"* p"}}},
		NextActions: []domain.Action{{Aufgabe: " tun ", Owner: " X ", Deadline: "morgen"}, {Aufgabe: " ", Deadline: "2026-01-02"}, {Aufgabe: "ok", Deadline: " 2026-01-02 "}},
		Pruefen:     true,
	}
	Normalize(n)
	if n.Titel != "Ein Titel" || n.Typ != "Sonstiges" || strings.Join(n.Teilnehmer, ",") != "Anna,Bob" || n.KontextZiel != "Ziel" {
		t.Errorf("%+v", n)
	}
	if len(n.Themen) != 1 || n.Themen[0].Titel != "T" || n.Themen[0].Punkte[0] != "p" {
		t.Errorf("themen %+v", n.Themen)
	}
	if len(n.NextActions) != 2 || n.NextActions[0].Deadline != "" || n.NextActions[0].Owner != "X" || n.NextActions[1].Deadline != "2026-01-02" {
		t.Errorf("actions %+v", n.NextActions)
	}
	if len(n.PruefGruende) != 1 {
		t.Errorf("pruefgruende %v", n.PruefGruende)
	}
	n2 := &domain.MeetingNote{Titel: strings.Repeat("ä", 200), Typ: "Team"}
	Normalize(n2)
	if len([]rune(n2.Titel)) != 120 || n2.Typ != "Team" || !strings.HasSuffix(n2.Titel, "…") {
		t.Errorf("titel len %d", len([]rune(n2.Titel)))
	}
}

func TestTruncateShort(t *testing.T) {
	if truncate("abc", 3) != "abc" || truncate("abcd", 3) != "ab…" {
		t.Error("truncate")
	}
}
