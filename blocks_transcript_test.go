package main

// Kürzung des Transkript-Toggles (progress.md Punkt 12, R-M0-08): höchstens
// 100 Blöcke je Request, davon der letzte der Kürzungshinweis.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
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
	blocks := buildNoteBlocks(&MeetingNote{}, NoteMeta{Transcript: truncLines(150)})
	raw, _ := json.Marshal(blocks)
	s := string(raw)
	if !strings.Contains(s, "🎙️ Transkript") || !strings.Contains(s, truncMarker) {
		t.Fatal("Toggle mit Kürzungshinweis erwartet")
	}
	if strings.Contains(s, fmt.Sprintf("%04d", 99)+"aaa") {
		t.Fatal("Absatz 100 darf nicht mehr im Toggle stehen")
	}
}
