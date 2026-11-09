package notion

// Kürzung des Transkript-Toggles (progress.md Punkt 12, R-M0-08): höchstens
// 100 Blöcke je Request, davon der letzte der Kürzungshinweis.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"meetingsync/internal/domain"
)

const truncMarker = "… gekürzt. Das vollständige Transkript liegt im Archivordner."

// truncLines: n Zeilen à 1500 Zeichen. Zwei passen nicht in einen Absatz
// (maxTextLen 1900), jede Zeile wird also ein eigener Absatz.
func truncLines(n int) string {
	ls := make([]string, n)
	for i := range ls {
		ls[i] = fmt.Sprintf("%04d", i) + strings.Repeat("a", 1496)
	}
	return strings.Join(ls, "\n")
}

func paraText(t *testing.T, b any) string {
	t.Helper()
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Type      string `json:"type"`
		Paragraph struct {
			RichText []struct {
				Text struct {
					Content string `json:"content"`
				} `json:"text"`
			} `json:"rich_text"`
		} `json:"paragraph"`
	}
	if err := json.Unmarshal(raw, &v); err != nil || v.Type != "paragraph" || len(v.Paragraph.RichText) == 0 {
		t.Fatalf("kein Absatz: %s (%v)", raw, err)
	}
	return v.Paragraph.RichText[0].Text.Content
}

// Fängt: Kürzungsschwelle verschoben (Hinweis schon bei 99 Absätzen) und
// falsches Abschneiden (Länge 99 statt Inhalt vollständig).
func TestTranscriptParagraphsBelowLimitNotTruncated(t *testing.T) {
	for _, n := range []int{1, 98, 99} {
		out := transcriptParagraphs(truncLines(n))
		if len(out) != n {
			t.Fatalf("%d Zeilen → %d Absätze, want %d", n, len(out), n)
		}
		for i, b := range out {
			if got := paraText(t, b); strings.Contains(got, "gekürzt") || !strings.HasPrefix(got, fmt.Sprintf("%04d", i)) {
				t.Fatalf("n=%d Absatz %d unerwartet: %.20q", n, i, got)
			}
		}
	}
}

// Fängt: Hinweis fehlt, 100 Inhalts-Absätze statt 99 + Hinweis (Notion-Limit),
// Hinweis nicht am Ende, Inhalt verschoben.
func TestTranscriptParagraphsTruncatedAt99(t *testing.T) {
	for _, n := range []int{100, 101, 300} {
		out := transcriptParagraphs(truncLines(n))
		if len(out) != 100 {
			t.Fatalf("%d Zeilen → %d Blöcke, want 100", n, len(out))
		}
		if got := paraText(t, out[99]); got != truncMarker {
			t.Fatalf("letzter Block = %.60q, want Kürzungshinweis", got)
		}
		for i := 0; i < 99; i++ {
			if got := paraText(t, out[i]); !strings.HasPrefix(got, fmt.Sprintf("%04d", i)) {
				t.Fatalf("n=%d Absatz %d = %.20q: Inhalt in falscher Reihenfolge", n, i, got)
			}
		}
	}
}

// Fängt: Aufteilung langer Zeilen zählt nicht gegen die Grenze (Teile sprengen 100).
func TestTranscriptParagraphsLongLineAtLimit(t *testing.T) {
	text := truncLines(97) + "\n" + strings.Repeat("b", 3*maxTextLen)
	out := transcriptParagraphs(text)
	if len(out) != 100 {
		t.Fatalf("Blöcke = %d, want 100", len(out))
	}
	if got := paraText(t, out[99]); got != truncMarker {
		t.Fatalf("letzter Block = %.60q", got)
	}
}

// Fängt: der Toggle in der Seite enthält den gekürzten Inhalt nicht (Verdrahtung).
func TestBuildNoteBlocksTranscriptToggleTruncated(t *testing.T) {
	blocks := NoteBlocks(&domain.MeetingNote{}, domain.NoteMeta{Transcript: truncLines(150)})
	raw, _ := json.Marshal(blocks)
	s := string(raw)
	if !strings.Contains(s, "🎙️ Transkript") || !strings.Contains(s, truncMarker) {
		t.Fatal("Toggle mit Kürzungshinweis erwartet")
	}
	if strings.Contains(s, fmt.Sprintf("%04d", 99)+"aaa") {
		t.Fatal("Absatz 100 darf nicht mehr im Toggle stehen")
	}
}

// Fängt: rt zerschneidet UTF-8 oder baut Text-Objekte über dem Notion-Limit
// (aus TestSplitHelpersKeepUTF8 in package main herausgelöst, R-M2-04).
func TestRichTextKeepsUTF8(t *testing.T) {
	long := strings.Repeat("äöü ", 2000)
	parts := rt(long)
	var joined strings.Builder
	for _, p := range parts {
		content := p.(map[string]any)["text"].(map[string]any)["content"].(string)
		if !utf8.ValidString(content) {
			t.Fatal("ungültiges UTF-8 im Teil")
		}
		if utf8.RuneCountInString(content) > 2000 {
			t.Fatal("Text-Objekt länger als 2000 Zeichen")
		}
		joined.WriteString(content)
	}
	if joined.String() != long {
		t.Fatalf("zusammengesetzte Teile (%d Bytes) != Eingabe (%d Bytes)", joined.Len(), len(long))
	}
}

// Fängt: Reihenfolge der Abschnitte, Hinweisbox, ⭐ für eigene To-dos und
// Deadline-Format in der Tabelle (Golden prüft die Bytes, hier die Struktur).
func TestNoteBlocksSections(t *testing.T) {
	n := &domain.MeetingNote{
		Typ: "Team", Teilnehmer: []string{"Mark", "Anna"},
		Themen:         []domain.Thema{{Titel: "Pipeline", Punkte: []string{"Cache"}}},
		Entscheidungen: []string{"Cache kommt"},
		NextActions:    []domain.Action{{Aufgabe: "Budget", Owner: "Mark", Deadline: "2026-10-30", FuerMich: true}},
	}
	blocks := NoteBlocks(n, domain.NoteMeta{ReviewReasons: []string{"a", "b"}, SourceFiles: []string{"/x/y.txt"}})
	var types []string
	for _, b := range blocks {
		types = append(types, b.(map[string]any)["type"].(string))
	}
	want := "callout heading_2 table divider heading_2 bulleted_list_item bulleted_list_item divider " +
		"heading_2 paragraph divider heading_2 heading_3 bulleted_list_item divider heading_2 to_do divider " +
		"heading_2 table divider heading_2 bulleted_list_item divider heading_2 bulleted_list_item bulleted_list_item"
	if got := strings.Join(types, " "); got != want {
		t.Fatalf("Blocktypen =\n%s\nerwartet\n%s", got, want)
	}
	raw, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, sub := range []string{`"Bitte prüfen: a · b"`, `"yellow_background"`, `"Mark ⭐"`, `"30.10.2026"`, `"y.txt"`, `"checked":false`} {
		if !strings.Contains(s, sub) {
			t.Errorf("%s fehlt in den Blöcken", sub)
		}
	}
}

// decisions: n eindeutige Entscheidungen (je ein to_do-Block).
func decisions(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("Entscheidung %d", i)
	}
	return out
}

// Fängt: verschachtelte Blöcke (table_row, Toggle-Absätze) nicht oder das
// Transkript mitgezählt.
func TestCountBlocksNestedWithoutTranscript(t *testing.T) {
	n := &domain.MeetingNote{}
	// Leere Notiz: 24 Blöcke oben, dazu 5 Zeilen Meeting-Info und 2 Zeilen Next Actions.
	if got := CountBlocks(n, domain.NoteMeta{}); got != 24+5+2 {
		t.Fatalf("CountBlocks = %d", got)
	}
	if countNested(NoteBlocks(n, domain.NoteMeta{Transcript: truncLines(150)})) <= CountBlocks(n, domain.NoteMeta{Transcript: truncLines(150)}) {
		t.Fatal("Transkript darf nicht mitzählen")
	}
}

// Fängt: Grenze um eins verschoben (1000 erlaubt, 1001 nicht) oder Fehler
// ohne Anzahl.
func TestCheckBlockCountCap(t *testing.T) {
	base := CountBlocks(&domain.MeetingNote{Entscheidungen: decisions(1)}, domain.NoteMeta{})
	at := &domain.MeetingNote{Entscheidungen: decisions(MaxBloecke - base + 1)}
	meta := domain.NoteMeta{Transcript: truncLines(150)}
	if got := CountBlocks(at, meta); got != MaxBloecke {
		t.Fatalf("CountBlocks = %d, want %d", got, MaxBloecke)
	}
	if err := CheckBlockCount(at, meta); err != nil {
		t.Fatalf("genau an der Grenze: %v", err)
	}
	over := &domain.MeetingNote{Entscheidungen: decisions(MaxBloecke - base + 2)}
	err := CheckBlockCount(over, meta)
	if !errors.Is(err, ErrZuVieleBloecke) || err.Error() != "zu viele Blöcke (1001, höchstens 1000)" {
		t.Fatalf("err = %v", err)
	}
}
