package transcript

import (
	"strings"
	"testing"
	"time"

	"meetingsync/internal/domain"
)

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
	cases := []struct {
		name, content string
		want          domain.Kind
	}{
		{"a.srt", srtSample, domain.KindTranscript},
		{"b.txt", "[00:00:01] Speaker 1: Hallo\n[00:00:05] Speaker 2: Hi Mark\n[00:00:09] Speaker 1: Los geht's\n[00:00:15] Speaker 2: Okay\n", domain.KindTranscript},
		{"c.txt", "Speaker 1  00:00:01\nHallo zusammen\n\nSpeaker 2  00:00:07\nHi Mark\n\nSpeaker 1  00:00:12\nWir starten\n", domain.KindTranscript},
		{"d.md", "# Zusammenfassung\n\nDas Team hat über Sirius gesprochen.\n\n## Entscheidungen\n- Release am 15.11.\n- Mark klärt Budget\n\n## Nächste Schritte\n- Sascha baut Pipeline\n", domain.KindSummary},
		{"e.txt", "Kurze Zusammenfassung: Es ging um 10:30 Uhr Termine und das Budget.\nWeitere Punkte folgen.\n", domain.KindSummary},
		{"f.json", `{"segments":[{"start":0.5,"speaker":"SPEAKER_00","text":"Hallo"},{"start":3.2,"speaker":"SPEAKER_01","text":"Hi"}]}`, domain.KindTranscript},
		{"g.json", `{"summary":"Nur eine Zusammenfassung"}`, domain.KindSummary},
		{"h.m4a", "binary", domain.KindUnsupported},
	}
	for _, c := range cases {
		if got := Classify(c.name, []byte(c.content), Options{}); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestClassifyForcedExtension(t *testing.T) {
	if got := Classify("x.txt", []byte(srtSample), Options{SummaryExtensions: []string{".txt"}}); got != domain.KindSummary {
		t.Fatalf("erzwungene Endung ignoriert: %s", got)
	}
}

func TestCueText(t *testing.T) {
	text, dur := Text("a.srt", []byte(srtSample))
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
	text, _ := Text("a.vtt", []byte(vtt))
	if text != "[00:00:01] Speaker 1: Guten Morgen" {
		t.Fatalf("got %q", text)
	}
}

func TestMeetingTiming(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Berlin")
	mtime := time.Date(2026, 10, 28, 15, 0, 0, 0, loc)

	tm := MeetingTiming("Sirius Sync 2026-10-28 14-00.txt", mtime, 45*time.Minute, loc)
	if tm.Estimated || tm.Start.Hour() != 14 || tm.TimeLabel() != "14:00–14:45 (45 min)" {
		t.Fatalf("aus Dateiname: %+v %q", tm, tm.TimeLabel())
	}
	tm = MeetingTiming("Aufnahme 7.txt", mtime, 30*time.Minute, loc)
	if !tm.Estimated || tm.Start.Format("15:04") != "14:30" || !strings.Contains(tm.TimeLabel(), "geschätzt") {
		t.Fatalf("geschätzt: %+v %q", tm, tm.TimeLabel())
	}
	tm = MeetingTiming("Weekly 27.10.2026.txt", mtime, 0, loc)
	if tm.Start.Day() != 27 || tm.HasTime {
		t.Fatalf("deutsches Datum: %+v", tm)
	}
	if tm.DateLabel() != "27.10.2026 (Di)" {
		t.Fatalf("Datumslabel: %q", tm.DateLabel())
	}
}
