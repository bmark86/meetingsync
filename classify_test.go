package main

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func testCfg() *Config {
	c := defaultConfig()
	c.WatchDir = "/tmp/x"
	c.LLM.Model = "test"
	c.Notion.NotesDataSourceID = "notes"
	if err := c.finalize(); err != nil {
		panic(err)
	}
	return c
}

const srtSample = `1
00:00:01,000 --> 00:00:04,200
Speaker 1: Hallo zusammen, kurzer Sync zu Sirius.

2
00:00:04,500 --> 00:00:09,000
Speaker 2: Danke, Mark. Ich fange mit dem Status an.

3
00:47:10,000 --> 00:47:12,000
Speaker 1: Dann bis Freitag.
`

func TestClassify(t *testing.T) {
	cfg := testCfg()
	cases := []struct {
		name, content string
		want          Kind
	}{
		{"a.srt", srtSample, KindTranscript},
		{"b.txt", "[00:00:01] Speaker 1: Hallo\n[00:00:05] Speaker 2: Hi Mark\n[00:00:09] Speaker 1: Los geht's\n[00:00:15] Speaker 2: Okay\n", KindTranscript},
		{"c.txt", "Speaker 1  00:00:01\nHallo zusammen\n\nSpeaker 2  00:00:07\nHi Mark\n\nSpeaker 1  00:00:12\nWir starten\n", KindTranscript},
		{"d.md", "# Zusammenfassung\n\nDas Team hat über Sirius gesprochen.\n\n## Entscheidungen\n- Release am 15.11.\n- Mark klärt Budget\n\n## Nächste Schritte\n- Sascha baut Pipeline\n", KindSummary},
		{"e.txt", "Kurze Zusammenfassung: Es ging um 10:30 Uhr Termine und das Budget.\nWeitere Punkte folgen.\n", KindSummary},
		{"f.json", `{"segments":[{"start":0.5,"speaker":"SPEAKER_00","text":"Hallo"},{"start":3.2,"speaker":"SPEAKER_01","text":"Hi"}]}`, KindTranscript},
		{"g.json", `{"summary":"Nur eine Zusammenfassung"}`, KindSummary},
		{"h.m4a", "binary", KindUnsupported},
	}
	for _, c := range cases {
		if got := classify(c.name, []byte(c.content), cfg); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestClassifyForcedExtension(t *testing.T) {
	cfg := testCfg()
	cfg.SummaryExtensions = normExts([]string{"txt"})
	if got := classify("x.txt", []byte(srtSample), cfg); got != KindSummary {
		t.Fatalf("erzwungene Endung ignoriert: %s", got)
	}
}

func TestCueText(t *testing.T) {
	text, dur := transcriptText("a.srt", []byte(srtSample))
	if !strings.HasPrefix(text, "[00:00:01] Speaker 1: Hallo zusammen") {
		t.Fatalf("unerwarteter Text:\n%s", text)
	}
	if strings.Contains(text, "-->") || strings.Contains(text, "\n2\n") {
		t.Fatalf("SRT-Steuerzeilen nicht entfernt:\n%s", text)
	}
	if dur != 47*time.Minute+12*time.Second {
		t.Fatalf("Dauer: %s", dur)
	}
}

func TestVTTVoiceTags(t *testing.T) {
	vtt := "WEBVTT\n\n00:00:01.000 --> 00:00:03.000\n<v Speaker 1>Guten Morgen</v>\n"
	text, _ := transcriptText("a.vtt", []byte(vtt))
	if text != "[00:00:01] Speaker 1: Guten Morgen" {
		t.Fatalf("got %q", text)
	}
}

func TestMeetingTiming(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Berlin")
	mtime := time.Date(2026, 10, 28, 15, 0, 0, 0, loc)

	tm := meetingTiming("Sirius Sync 2026-10-28 14-00.txt", mtime, 45*time.Minute, loc)
	if tm.Estimated || tm.Start.Hour() != 14 || tm.TimeLabel() != "14:00–14:45 (45 min)" {
		t.Fatalf("aus Dateiname: %+v %q", tm, tm.TimeLabel())
	}
	tm = meetingTiming("Aufnahme 7.txt", mtime, 30*time.Minute, loc)
	if !tm.Estimated || tm.Start.Format("15:04") != "14:30" || !strings.Contains(tm.TimeLabel(), "geschätzt") {
		t.Fatalf("geschätzt: %+v %q", tm, tm.TimeLabel())
	}
	tm = meetingTiming("Weekly 27.10.2026.txt", mtime, 0, loc)
	if tm.Start.Day() != 27 || tm.HasTime {
		t.Fatalf("deutsches Datum: %+v", tm)
	}
	if tm.DateLabel() != "27.10.2026 (Di)" {
		t.Fatalf("Datumslabel: %q", tm.DateLabel())
	}
}

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
