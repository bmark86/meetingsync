package domain

import (
	"encoding/json"
	"testing"
	"time"
)

func TestKindString(t *testing.T) {
	for k, want := range map[Kind]string{
		KindUnsupported: "nicht unterstützt",
		KindTranscript:  "Transkript",
		KindSummary:     "Zusammenfassung",
		Kind(99):        "nicht unterstützt",
	} {
		if got := k.String(); got != want {
			t.Errorf("Kind(%d).String() = %q, erwartet %q", int(k), got, want)
		}
	}
}

func TestTimingLabels(t *testing.T) {
	start := time.Date(2026, 10, 28, 14, 0, 0, 0, time.UTC) // Mittwoch
	cases := []struct {
		name string
		t    Timing
		want string
	}{
		{"ohne Uhrzeit", Timing{Start: start}, "–"},
		{"nur Start", Timing{Start: start, HasTime: true}, "14:00"},
		{"Start und Ende", Timing{Start: start, End: start.Add(45 * time.Minute), HasTime: true}, "14:00–14:45 (45 min)"},
		{"Dauer rundet auf (44m30s)", Timing{Start: start, End: start.Add(44*time.Minute + 30*time.Second), HasTime: true}, "14:00–14:44 (45 min)"},
		{"Dauer rundet ab (44m29s)", Timing{Start: start, End: start.Add(44*time.Minute + 29*time.Second), HasTime: true}, "14:00–14:44 (44 min)"},
		{"geschätzt mit Dauer", Timing{Start: start, End: start.Add(30 * time.Minute), HasTime: true, Estimated: true}, "14:00–14:30 (30 min), geschätzt"},
		{"geschätzt ohne Dauer", Timing{Start: start, End: start, HasTime: true, Estimated: true}, "ca. 14:00 (Export)"},
	}
	for _, c := range cases {
		if got := c.t.TimeLabel(); got != c.want {
			t.Errorf("%s: TimeLabel = %q, erwartet %q", c.name, got, c.want)
		}
	}
	if got, want := (Timing{Start: start}).DateLabel(), "28.10.2026 (Mi)"; got != want {
		t.Errorf("DateLabel = %q, erwartet %q", got, want)
	}
}

// Fängt: geänderte JSON-Tags oder Feldreihenfolge (Antwort des Modells).
func TestMeetingNoteJSON(t *testing.T) {
	n := MeetingNote{
		Themen:           []Thema{{Titel: "T", Punkte: []string{"p"}}},
		NextActions:      []Action{{Aufgabe: "A", Owner: "O", Deadline: "D", FuerMich: true}},
		NaechstesMeeting: Naechstes{Datum: "X", Themen: []string{"t"}},
	}
	b, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"titel":"","typ":"","teilnehmer":null,"kontext_ziel":"","themen":[{"titel":"T","punkte":["p"]}],"entscheidungen":null,` +
		`"next_actions":[{"aufgabe":"A","owner":"O","deadline":"D","fuer_mich":true}],"offene_fragen":null,` +
		`"naechstes_meeting":{"datum":"X","themen":["t"]},"projekt":"","pruef_gruende":null,"pruefen":false}`
	if string(b) != want {
		t.Errorf("JSON =\n%s\nerwartet\n%s", b, want)
	}
}
