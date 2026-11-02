package transcript

import (
	"testing"
	"time"

	"meetingsync/internal/domain"
)

func TestTextJSON(t *testing.T) {
	js := `{"segments":[{"start":1.4,"speaker":"A","text":" Hallo "},{"startTime":"00:01:05","text":"ohne Sprecher"},{"start_time":"12.5","speaker_name":"B","text":"Ende"},{"text":""},"kaputt"]}`
	text, dur := Text("x.JSON", []byte(js))
	want := "[00:00:01] A: Hallo\n[00:01:05] ohne Sprecher\n[00:00:13] B: Ende"
	if text != want {
		t.Fatalf("got %q, want %q", text, want)
	}
	if dur != 65*time.Second {
		t.Fatalf("dur = %s", dur)
	}
}

func TestTextJSONFallbackToPlain(t *testing.T) {
	text, dur := Text("x.json", []byte(`{"summary":"x"}`))
	if text != `{"summary":"x"}` || dur != 0 {
		t.Fatalf("got %q %s", text, dur)
	}
	text, _ = Text("x.json", []byte("kein json"))
	if text != "kein json" {
		t.Fatalf("got %q", text)
	}
}

func TestJSONSegmentsShapes(t *testing.T) {
	if got := jsonSegments([]byte(`[{"text":"a","speaker":7}]`)); len(got) != 1 || got[0].Speaker != "7" {
		t.Fatalf("Array-Wurzel: %+v", got)
	}
	if got := jsonSegments([]byte(`{"utterances":[{"text":"u","speaker":3.0}]}`)); len(got) != 1 {
		t.Fatalf("utterances: %+v", got)
	}
	if got := jsonSegments([]byte(`{"segments":"x"}`)); got != nil {
		t.Fatalf("segments kein Array: %+v", got)
	}
	if got := jsonSegments([]byte(`"str"`)); got != nil {
		t.Fatalf("Skalar: %+v", got)
	}
	if got := jsonSegments([]byte(`{"transcript":[{"text":"t","speaker":4}]}`)); len(got) != 1 {
		t.Fatalf("transcript: %+v", got)
	}
}

func TestTextPlain(t *testing.T) {
	in := "\uFEFF[00:01:30] Speaker 1: a\r\n[1:02:03] Speaker 2: b\r\n[05:07] c\r\n"
	text, dur := Text("n.txt", []byte(in))
	if text != "[00:01:30] Speaker 1: a\n[1:02:03] Speaker 2: b\n[05:07] c" {
		t.Fatalf("got %q", text)
	}
	if dur != time.Hour+2*time.Minute+3*time.Second {
		t.Fatalf("dur = %s", dur)
	}
}

func TestMaxLeadingTimestamp(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"kein Zeitstempel", 0},
		{"12:34 Text", 12*time.Minute + 34*time.Second},
		{"(1:02:03.456) Text", time.Hour + 2*time.Minute + 3*time.Second},
		{"00:00:01,000 --> 00:00:09,500", 9*time.Second + 500*time.Millisecond},
		{"[00:01] a\n[00:10] b\n[00:05] c", 10 * time.Second},
	}
	for _, c := range cases {
		if got := maxLeadingTimestamp(c.in); got != c.want {
			t.Errorf("%q: got %s, want %s", c.in, got, c.want)
		}
	}
}

func TestAnyFloatAndString(t *testing.T) {
	if anyFloat(2.5) != 2.5 || anyFloat("3.25") != 3.25 || anyFloat("00:01:30") != 90 {
		t.Fatal("anyFloat Grundfälle")
	}
	if anyFloat("zz") != 0 || anyFloat("01:30") != 0 || anyFloat(nil) != 0 || anyFloat(true) != 0 {
		t.Fatal("anyFloat ungültig muss 0 sein")
	}
	if anyString("a") != "a" || anyString(3.0) != "3" || anyString(1.5) != "1.5" || anyString(nil) != "" || anyString(true) != "" {
		t.Fatal("anyString")
	}
	if firstOf(map[string]any{"a": nil, "b": "x"}, "a", "b", "c") != "x" || firstOf(map[string]any{}, "a") != nil {
		t.Fatal("firstOf")
	}
}

func TestSecsFmtDur(t *testing.T) {
	if secs(1.5) != 1500*time.Millisecond {
		t.Fatal("secs")
	}
	if got := fmtDur(100*time.Hour + 59*time.Second + 600*time.Millisecond); got != "100:01:00" {
		t.Fatalf("fmtDur = %s", got)
	}
}

func TestOptionsForcedExtensions(t *testing.T) {
	o := Options{TranscriptExtensions: []string{".log"}, SummaryExtensions: []string{".notes"}}
	if Classify("a.LOG", []byte("egal"), o) != domain.KindTranscript {
		t.Error("erzwungenes Transkript")
	}
	if Classify("a.notes", []byte("[00:00:01] Speaker 1: x"), o) != domain.KindSummary {
		t.Error("erzwungene Zusammenfassung")
	}
	if Classify("a.log", []byte("x"), Options{}) != domain.KindUnsupported {
		t.Error("ohne Option unsupported")
	}
	if !IsSupportedExt(".LOG", o) || !IsSupportedExt(".notes", o) || !IsSupportedExt(".SRT", Options{}) {
		t.Error("IsSupportedExt positiv")
	}
	if IsSupportedExt(".m4a", o) || IsSupportedExt("", Options{}) {
		t.Error("IsSupportedExt negativ")
	}
	// Transkript-Endung hat Vorrang, wenn beide gesetzt sind.
	both := Options{TranscriptExtensions: []string{".txt"}, SummaryExtensions: []string{".txt"}}
	if Classify("a.txt", nil, both) != domain.KindTranscript {
		t.Error("Vorrang Transkript")
	}
}

func TestClassifyHeuristicEdges(t *testing.T) {
	if Classify("e.txt", nil, Options{}) != domain.KindSummary {
		t.Error("leer")
	}
	// Nur 2 Treffer: unter Mindestzahl 3.
	if Classify("a.txt", []byte("[00:01] a\n[00:02] b\n"), Options{}) != domain.KindSummary {
		t.Error("2 Treffer")
	}
	// 3 Treffer, aber unter 15 %.
	long := "[00:01] a\n[00:02] b\n[00:03] c\n"
	for i := 0; i < 30; i++ {
		long += "Fließtext Zeile\n"
	}
	if Classify("a.md", []byte(long), Options{}) != domain.KindSummary {
		t.Error("Anteil < 15 %")
	}
	// Sprecher-Label weit hinten zählt nicht.
	far := "Das ist eine ziemlich lange Zeile mit Speaker 1 weit hinten\n"
	if hasLeadingSpeaker(far) || !hasLeadingSpeaker("Speaker 1: hi") {
		t.Error("hasLeadingSpeaker")
	}
}

func TestDateFromName(t *testing.T) {
	cases := []struct {
		name            string
		y, mo, d, h, mi int
		hasTime, ok     bool
	}{
		{"Sync 2026-10-28 14-00.txt", 2026, 10, 28, 14, 0, true, true},
		{"2026-10-28.txt", 2026, 10, 28, 0, 0, false, true},
		{"2026_10_28_09.30.txt", 2026, 10, 28, 9, 30, true, true},
		{"2026-10-28 at 9:05.txt", 2026, 10, 28, 9, 5, true, true},
		{"2026-10-28T14h15.txt", 2026, 10, 28, 14, 15, true, true},
		{"2026-10-28 25-00.txt", 2026, 10, 28, 25, 0, false, true},
		{"2026-10-28 10-75.txt", 2026, 10, 28, 10, 75, false, true},
		{"2026-02-30.txt", 2026, 2, 30, 0, 0, false, false},
		{"2026-13-01.txt", 2026, 13, 1, 0, 0, false, false},
		{"2026-00-10.txt", 2026, 0, 10, 0, 0, false, false},
		{"27.10.2026.txt", 2026, 10, 27, 0, 0, false, true},
		{"Weekly 7.1.2026 15:30.txt", 2026, 1, 7, 15, 30, true, true},
		{"31.04.2026.txt", 2026, 4, 31, 0, 0, false, false},
		{"29.02.2024.txt", 2024, 2, 29, 0, 0, false, true},
		{"29.02.2026.txt", 2026, 2, 29, 0, 0, false, false},
		{"dir/2026-10-28/Aufnahme.txt", 0, 0, 0, 0, 0, false, false},
		{"Aufnahme 7.txt", 0, 0, 0, 0, 0, false, false},
		{"1999-10-28.txt", 0, 0, 0, 0, 0, false, false},
	}
	for _, c := range cases {
		y, mo, d, h, mi, ht, ok := dateFromName(c.name)
		if y != c.y || mo != c.mo || d != c.d || h != c.h || mi != c.mi || ht != c.hasTime || ok != c.ok {
			t.Errorf("%q: got %d-%d-%d %d:%d ht=%v ok=%v, want %d-%d-%d %d:%d ht=%v ok=%v",
				c.name, y, mo, d, h, mi, ht, ok, c.y, c.mo, c.d, c.h, c.mi, c.hasTime, c.ok)
		}
	}
}

func TestMeetingTimingBranches(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	mtime := time.Date(2026, 10, 28, 15, 0, 0, 0, time.UTC) // 16:00 Berlin
	// Datum + Zeit, ohne Dauer: kein Ende.
	tm := MeetingTiming("2026-10-28 14-00.txt", mtime, 0, loc)
	if !tm.HasTime || !tm.End.IsZero() || tm.Start.Hour() != 14 {
		t.Errorf("ohne Dauer: %+v", tm)
	}
	// Nur Datum, gleicher Tag wie mtime: geschätzt aus mtime.
	tm = MeetingTiming("2026-10-28.txt", mtime, 30*time.Minute, loc)
	if !tm.Estimated || tm.Start.Format("15:04") != "15:30" {
		t.Errorf("gleicher Tag: %+v", tm)
	}
	// Nur Datum, anderer Tag: ganztägig.
	tm = MeetingTiming("2026-10-20.txt", mtime, 30*time.Minute, loc)
	if tm.HasTime || tm.Estimated || tm.Start.Day() != 20 {
		t.Errorf("anderer Tag: %+v", tm)
	}
	// Ungültiges Datum im Namen: Schätzung.
	tm = MeetingTiming("2026-02-30.txt", mtime, 0, loc)
	if !tm.Estimated || !tm.Start.Equal(mtime) {
		t.Errorf("ungültig: %+v", tm)
	}
}

func TestCueTextEdges(t *testing.T) {
	// Mehrzeilige Cues, Tags, NOTE-Block, Cue ohne Text.
	vtt := "WEBVTT\n\nNOTE hallo\n\n00:00:01.2 --> 00:00:02.0\n<c.x>Zeile</c>\nzwei\n\n00:00:03.000 --> 00:00:04.000\n\n00:00:05.000 --> 00:01:00.000\n<b></b>\n"
	text, dur := Text("a.vtt", []byte(vtt))
	if text != "[00:00:01] Zeile zwei" {
		t.Fatalf("got %q", text)
	}
	if dur != time.Minute {
		t.Fatalf("dur = %s", dur)
	}
	if text, dur := Text("a.srt", nil); text != "" || dur != 0 {
		t.Fatalf("leer: %q %s", text, dur)
	}
}
