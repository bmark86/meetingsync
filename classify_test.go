package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSplitHelpersKeepUTF8(t *testing.T) {
	long := strings.Repeat("äöü ", 2000)
	for _, c := range splitByLines(long, 5000) {
		if !utf8.ValidString(c) {
			t.Fatal("splitByLines zerschneidet UTF-8")
		}
	}
	parts := rt(long)
	for _, p := range parts {
		content := p.(map[string]any)["text"].(map[string]any)["content"].(string)
		if utf8.RuneCountInString(content) > 2000 {
			t.Fatal("Text-Objekt länger als 2000 Zeichen")
		}
	}
}

func TestParseNoteToleratesWrapping(t *testing.T) {
	raw := "<think>hmm</think>\n```json\n{\"titel\":\"Sirius Sync\",\"typ\":\"Team\",\"next_actions\":[{\"aufgabe\":\"Budget klären\",\"owner\":\"Mark\",\"deadline\":\"bis Freitag\",\"fuer_mich\":true}]}\n```"
	n, err := parseNote(raw)
	if err != nil {
		t.Fatal(err)
	}
	normalizeNote(n)
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
