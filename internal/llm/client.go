// Package llm macht aus einem Transkript (oder einer Zusammenfassung) per
// lokalem LLM (LM Studio, OpenAI-kompatible API) eine strukturierte MeetingNote:
// Prompts, JSON-Schema, Abschnittsbildung langer Transkripte und Parse-Retry.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"meetingsync/internal/domain"
)

var weekdaysLong = []string{"Sonntag", "Montag", "Dienstag", "Mittwoch", "Donnerstag", "Freitag", "Samstag"}

var meetingTypes = []string{"1:1", "Team", "Extern", "Lenkungskreis", "Sonstiges"}

// NoteInput ist das Material und der Kontext für eine Notiz.
type NoteInput struct {
	Text        string
	SummaryText string // optional, MacWhisper-Zusammenfassung als Zusatzkontext
	SummaryOnly bool
	Timing      domain.Timing
	Projects    []string
	MyName      string
	MyLabel     string
}

// Options sind die Einstellungen des Clients (aus dem llm-Teil der Konfiguration).
// APIKey geht nur in den Authorization-Header und wird nie geloggt.
type Options struct {
	BaseURL          string
	Model            string
	APIKey           string
	Temperature      float64
	Timeout          time.Duration
	MaxChunkChars    int
	StructuredOutput bool
}

// Summarizer macht aus Transkript oder Zusammenfassung eine MeetingNote;
// *Client erfüllt es. Damit lässt sich der Ablauf später ohne HTTP testen (R-M2-09).
type Summarizer interface {
	Summarize(ctx context.Context, in NoteInput) (*domain.MeetingNote, error)
}

var _ Summarizer = (*Client)(nil)

// Client spricht mit LM Studio.
type Client struct {
	opts Options
	http *http.Client
	log  *log.Logger
}

// NewClient baut den Client. transport ist der HTTP-Transport; nil heißt
// http.DefaultTransport (Produktion), Tests setzen eigene Hooks.
func NewClient(opts Options, logger *log.Logger, transport http.RoundTripper) *Client {
	return &Client{
		opts: opts,
		http: &http.Client{Timeout: opts.Timeout, Transport: transport},
		log:  logger,
	}
}

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type httpError struct {
	Status int
	Body   string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("LM Studio HTTP %d: %s", e.Status, truncate(e.Body, 300))
}

// Summarize macht aus Transkript (oder Zusammenfassung) eine MeetingNote.
// Lange Transkripte werden erst abschnittsweise verdichtet.
func (l *Client) Summarize(ctx context.Context, in NoteInput) (*domain.MeetingNote, error) {
	material := in.Text
	label := "Transkript"
	if in.SummaryOnly {
		label = "Notizen/Zusammenfassung (kein Transkript mit Zeitmarken vorhanden)"
	}
	if len(material) > l.opts.MaxChunkChars {
		chunks := splitByLines(material, l.opts.MaxChunkChars)
		parts := make([]string, 0, len(chunks))
		for i, c := range chunks {
			l.log.Printf("   LLM: verdichte Abschnitt %d/%d …", i+1, len(chunks))
			out, err := l.chat(ctx, []chatMsg{
				{"system", chunkSystemPrompt(in)},
				{"user", fmt.Sprintf("Abschnitt %d von %d:\n<<<\n%s\n>>>", i+1, len(chunks), c)},
			}, nil)
			if err != nil {
				return nil, err
			}
			parts = append(parts, fmt.Sprintf("### Abschnitt %d\n%s", i+1, strings.TrimSpace(stripThink(out))))
		}
		material = strings.Join(parts, "\n\n")
		label = "Teilnotizen (aus einem langen Transkript abschnittsweise extrahiert)"
	}

	schema := noteSchema(in.Projects)
	sys := systemPrompt(in)
	useSchema := l.opts.StructuredOutput
	if !useSchema {
		sys += "\n\nJSON-Schema der Antwort:\n" + mustJSON(schema)
	}
	msgs := []chatMsg{{"system", sys}, {"user", userPrompt(in, label, material)}}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		var s any
		if useSchema {
			s = schema
		}
		raw, err := l.chat(ctx, msgs, s)
		if err != nil {
			var he *httpError
			if useSchema && errors.As(err, &he) && he.Status >= 400 && he.Status < 500 {
				l.log.Printf("   LLM: Structured Output abgelehnt (HTTP %d), versuche es mit Schema im Prompt", he.Status)
				useSchema = false
				msgs[0].Content += "\n\nJSON-Schema der Antwort:\n" + mustJSON(schema)
				lastErr = err
				continue
			}
			return nil, err
		}
		note, perr := parseNote(raw)
		if perr == nil {
			return note, nil
		}
		lastErr = fmt.Errorf("LLM-Antwort ist kein gültiges Notiz-JSON: %w", perr)
		l.log.Printf("   %v – neuer Versuch", lastErr)
	}
	return nil, lastErr
}

func (l *Client) chat(ctx context.Context, msgs []chatMsg, schema any) (string, error) {
	body := map[string]any{
		"model":       l.opts.Model,
		"messages":    msgs,
		"temperature": l.opts.Temperature,
		"stream":      false,
	}
	if schema != nil {
		body["response_format"] = map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "meeting_note",
				"strict": true,
				"schema": schema,
			},
		}
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(l.opts.BaseURL, "/")+"/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if l.opts.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+l.opts.APIKey)
	}
	resp, err := l.http.Do(req)
	if err != nil {
		return "", domain.Transient(fmt.Errorf("LM Studio nicht erreichbar (%s) – läuft der lokale Server? %w", l.opts.BaseURL, err))
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // nur gelesen, Close-Fehler ohne Folgen
	data, _ := io.ReadAll(resp.Body)         //nolint:errcheck // Lesefehler fuehrt unten ohnehin zu einem JSON-/Statusfehler

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return "", domain.Transient(&httpError{resp.StatusCode, string(data)})
	}
	if resp.StatusCode >= 400 {
		lower := strings.ToLower(string(data))
		if strings.Contains(lower, "no models loaded") || strings.Contains(lower, "not loaded") ||
			(strings.Contains(lower, "model") && strings.Contains(lower, "not found")) {
			return "", domain.Transient(fmt.Errorf("Modell %q ist in LM Studio nicht geladen: %s", l.opts.Model, truncate(string(data), 200)))
		}
		return "", &httpError{resp.StatusCode, string(data)}
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("unerwartete Antwort von LM Studio: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", errors.New("LM Studio hat keine Antwort geliefert")
	}
	if out.Choices[0].FinishReason == "length" {
		return "", errors.New("Antwort des Modells wurde abgeschnitten – Kontextlänge in LM Studio erhöhen oder llm.max_chunk_chars verkleinern")
	}
	return out.Choices[0].Message.Content, nil
}

// Models listet die Modell-IDs, die LM Studio anbietet (für -check).
func (l *Client) Models(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(l.opts.BaseURL, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	if l.opts.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+l.opts.APIKey)
	}
	resp, err := l.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // nur gelesen, Close-Fehler ohne Folgen
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Data))
	for _, d := range out.Data {
		ids = append(ids, d.ID)
	}
	return ids, nil
}

// --- Prompts ----------------------------------------------------------------

func systemPrompt(in NoteInput) string {
	me := in.MyName
	labelRule := fmt.Sprintf("Eines der Labels ist %s selbst (eigene Mikrofonspur); erkenne es am Kontext.", me)
	if in.MyLabel != "" {
		labelRule = fmt.Sprintf("Das Label %q ist %s.", in.MyLabel, me)
	}
	return fmt.Sprintf(`Du bist ein sorgfältiger Protokollant für %[1]s, Head of Engineering. Du bekommst das Material eines Meetings (automatisch transkribiert, kann Hörfehler enthalten) und erstellst daraus strukturierte Meeting-Notizen.

Regeln:
- Schreibe auf Deutsch, knapp und konkret. Fachbegriffe dürfen Englisch bleiben.
- Erfinde nichts. Was nicht im Material steht, bleibt leer ("" bzw. []).
- Sprecher: Das Transkript nutzt Labels wie "Speaker 1". %[2]s Ordne andere Labels nur dann echten Namen zu, wenn es eindeutig aus dem Gespräch hervorgeht (Anrede, Vorstellung, "Danke, Sascha"). Bei Unsicherheit schreibe z. B. "Speaker 2 (vermutlich Sascha?)", setze pruefen=true und nenne den Grund.
- titel: kurz und aussagekräftig, ohne Datum, höchstens 8 Wörter.
- typ: "1:1" (zwei Personen), "Team", "Extern" (Kunden, Partner, Dienstleister), "Lenkungskreis" oder "Sonstiges".
- teilnehmer: Namen bzw. Labels aller Sprechenden, %[1]s eingeschlossen.
- kontext_ziel: 1-3 Sätze, warum das Meeting stattfand und was am Ende klar sein sollte.
- themen: die wichtigsten 2-8 Themen, je 1-6 Stichpunkte mit konkreten Fakten, Zahlen und Argumenten.
- entscheidungen: nur ausdrücklich getroffene Entscheidungen, keine Meinungen.
- next_actions: nur konkrete Aufgaben oder Zusagen. owner = Name der verantwortlichen Person. fuer_mich=true, wenn %[1]s die Aufgabe übernimmt oder zugesagt hat. deadline nur, wenn genannt, im Format YYYY-MM-DD; relative Angaben ("bis Freitag", "nächste Woche") rechnest du ab dem Meetingdatum um. Sonst "".
- offene_fragen: ungeklärte oder vertagte Punkte.
- naechstes_meeting: nur, wenn ein Folgetermin oder Folgethemen erwähnt wurden.
- projekt: wähle aus der vorgegebenen Liste nur, wenn das Meeting eindeutig zu genau einem Projekt gehört, sonst "".
- pruef_gruende/pruefen: pruefen=true, wenn Sprecherzuordnung, Aufgaben oder Inhalt unsicher sind (z. B. schlechte Transkriptqualität); in pruef_gruende stehen kurze Gründe.
Antworte ausschließlich mit einem JSON-Objekt nach dem vorgegebenen Schema, ohne Erklärtext.`, me, labelRule)
}

func chunkSystemPrompt(in NoteInput) string {
	return fmt.Sprintf(`Du bekommst einen Abschnitt eines langen, automatisch erstellten Meeting-Transkripts. %s ist einer der Teilnehmenden.
Extrahiere stichpunktartig auf Deutsch, ohne etwas zu erfinden:
- Themen mit Kernaussagen, Fakten und Zahlen
- ausdrücklich getroffene Entscheidungen
- Aufgaben/Zusagen mit verantwortlicher Person und ggf. genannter Frist
- offene Fragen
- Hinweise darauf, welches Sprecher-Label zu welchem Namen gehört
Nur Stichpunkte, keine Einleitung.`, in.MyName)
}

func userPrompt(in NoteInput, label, material string) string {
	var b strings.Builder
	d := in.Timing.Start
	fmt.Fprintf(&b, "Meetingdatum: %s, %s (%s)", weekdaysLong[d.Weekday()], d.Format("02.01.2006"), d.Format("2006-01-02"))
	if in.Timing.HasTime {
		fmt.Fprintf(&b, ", Beginn ca. %s", d.Format("15:04"))
	}
	b.WriteString("\n\nMögliche Projekte:")
	if len(in.Projects) == 0 {
		b.WriteString(" keine\n")
	} else {
		b.WriteString("\n")
		for _, p := range in.Projects {
			b.WriteString("- " + p + "\n")
		}
	}
	if strings.TrimSpace(in.SummaryText) != "" {
		b.WriteString("\nZusätzlich die automatische Zusammenfassung von MacWhisper (nur als Hilfe, das Transkript hat Vorrang):\n<<<\n")
		b.WriteString(strings.TrimSpace(in.SummaryText))
		b.WriteString("\n>>>\n")
	}
	fmt.Fprintf(&b, "\n%s:\n<<<\n%s\n>>>", label, material)
	return b.String()
}

// --- Schema (Reihenfolge der Felder bleibt erhalten) -------------------------

type kv struct {
	K string
	V any
}

// omap ist ein JSON-Objekt mit fester Feldreihenfolge. Bei Structured Output
// generiert das Modell die Felder in dieser Reihenfolge – erst Inhalt, dann
// Bewertung (pruef_gruende vor pruefen).
type omap []kv

func (o omap) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, e := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(e.K) //nolint:errcheck // Schluessel ist ein String, Marshal kann nicht scheitern
		v, err := json.Marshal(e.V)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func object(props omap) omap {
	req := make([]string, len(props))
	for i, p := range props {
		req[i] = p.K
	}
	return omap{{"type", "object"}, {"properties", props}, {"required", req}, {"additionalProperties", false}}
}

func noteSchema(projects []string) omap {
	str := omap{{"type", "string"}}
	strArr := omap{{"type", "array"}, {"items", str}}
	boolean := omap{{"type", "boolean"}}
	projekt := omap{{"type", "string"}}
	if len(projects) > 0 {
		projekt = append(projekt, kv{"enum", append([]string{""}, projects...)})
	}
	return object(omap{
		{"titel", str},
		{"typ", omap{{"type", "string"}, {"enum", meetingTypes}}},
		{"teilnehmer", strArr},
		{"kontext_ziel", str},
		{"themen", omap{{"type", "array"}, {"items", object(omap{{"titel", str}, {"punkte", strArr}})}}},
		{"entscheidungen", strArr},
		{"next_actions", omap{{"type", "array"}, {"items", object(omap{
			{"aufgabe", str}, {"owner", str}, {"deadline", str}, {"fuer_mich", boolean},
		})}}},
		{"offene_fragen", strArr},
		{"naechstes_meeting", object(omap{{"datum", str}, {"themen", strArr}})},
		{"projekt", projekt},
		{"pruef_gruende", strArr},
		{"pruefen", boolean},
	})
}

// --- Antwort parsen -------------------------------------------------------------

var reThink = regexp.MustCompile(`(?s)<think>.*?</think>`)

func stripThink(s string) string { return reThink.ReplaceAllString(s, "") }

func parseNote(raw string) (*domain.MeetingNote, error) {
	s := strings.TrimSpace(stripThink(raw))
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j <= i {
		return nil, errors.New("kein JSON-Objekt gefunden")
	}
	var n domain.MeetingNote
	if err := json.Unmarshal([]byte(s[i:j+1]), &n); err != nil {
		return nil, err
	}
	if strings.TrimSpace(n.Titel) == "" {
		return nil, errors.New("Feld titel ist leer")
	}
	return &n, nil
}

// Normalize räumt die Modellantwort auf (Leerzeilen, Dubletten, Längen).
func Normalize(n *domain.MeetingNote) {
	n.Titel = truncate(strings.Join(strings.Fields(n.Titel), " "), 120)
	if !slices.Contains(meetingTypes, n.Typ) {
		n.Typ = "Sonstiges"
	}
	n.Teilnehmer = cleanList(n.Teilnehmer)
	n.KontextZiel = strings.TrimSpace(n.KontextZiel)
	themen := n.Themen[:0]
	for _, t := range n.Themen {
		t.Titel = strings.TrimSpace(t.Titel)
		t.Punkte = cleanList(t.Punkte)
		if t.Titel != "" || len(t.Punkte) > 0 {
			themen = append(themen, t)
		}
	}
	n.Themen = themen
	n.Entscheidungen = cleanList(n.Entscheidungen)
	acts := n.NextActions[:0]
	for _, a := range n.NextActions {
		a.Aufgabe = strings.TrimSpace(a.Aufgabe)
		a.Owner = strings.TrimSpace(a.Owner)
		a.Deadline = strings.TrimSpace(a.Deadline)
		if _, err := time.Parse("2006-01-02", a.Deadline); err != nil {
			a.Deadline = ""
		}
		if a.Aufgabe != "" {
			acts = append(acts, a)
		}
	}
	n.NextActions = acts
	n.OffeneFragen = cleanList(n.OffeneFragen)
	n.NaechstesMeeting.Datum = strings.TrimSpace(n.NaechstesMeeting.Datum)
	n.NaechstesMeeting.Themen = cleanList(n.NaechstesMeeting.Themen)
	n.Projekt = strings.TrimSpace(n.Projekt)
	n.PruefGruende = cleanList(n.PruefGruende)
	if n.Pruefen && len(n.PruefGruende) == 0 {
		n.PruefGruende = []string{"Vom Modell als unsicher markiert"}
	}
}

func cleanList(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(s), "-•* "))
		key := strings.ToLower(s)
		if s == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, s)
	}
	return out
}

func splitByLines(s string, max int) []string {
	var chunks []string
	var cur strings.Builder
	for _, line := range strings.Split(s, "\n") {
		for len(line) > max { // extrem lange Zeile hart teilen (an einer UTF-8-Grenze)
			if cur.Len() > 0 {
				chunks = append(chunks, cur.String())
				cur.Reset()
			}
			cut := max
			for cut > 0 && !utf8.RuneStart(line[cut]) {
				cut--
			}
			chunks = append(chunks, line[:cut])
			line = line[cut:]
		}
		if cur.Len()+len(line)+1 > max && cur.Len() > 0 {
			chunks = append(chunks, cur.String())
			cur.Reset()
		}
		cur.WriteString(line)
		cur.WriteByte('\n')
	}
	if strings.TrimSpace(cur.String()) != "" {
		chunks = append(chunks, cur.String())
	}
	return chunks
}

func mustJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ") //nolint:errcheck // Marshal von Schema/Notiz (nur Strings, Zahlen, Listen) scheitert nicht
	return string(b)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
