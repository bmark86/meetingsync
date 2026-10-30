package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Kind beschreibt, was in einer Exportdatei steckt.
type Kind int

const (
	KindUnsupported Kind = iota
	KindTranscript
	KindSummary
)

func (k Kind) String() string {
	switch k {
	case KindTranscript:
		return "Transkript"
	case KindSummary:
		return "Zusammenfassung"
	default:
		return "nicht unterstützt"
	}
}

var supportedExt = map[string]bool{
	".txt": true, ".md": true, ".markdown": true,
	".srt": true, ".vtt": true, ".json": true,
}

var (
	// Zeitstempel am Zeilenanfang: "00:01:23", "[01:23]", "(1:02:03.456)"
	reLeadingTS = regexp.MustCompile(`^\s*[\[(]?(\d{1,2}):(\d{2})(?::(\d{2}))?(?:[.,]\d{1,3})?[\])]?`)
	// SRT/VTT-Zeile: "00:00:01,000 --> 00:00:04,500"
	reCueTiming = regexp.MustCompile(`(\d{1,2}):(\d{2}):(\d{2})[.,](\d{1,3})\s*-->\s*(\d{1,2}):(\d{2}):(\d{2})[.,](\d{1,3})`)
	// "Speaker 1", "SPEAKER_02", "Sprecher 3"
	reSpeakerLabel = regexp.MustCompile(`(?i)\b(speaker|sprecher|spk)[ _-]?\d+\b`)
	// Kopfzeile im Stil "Mark  00:12:03" oder "Speaker 2 (01:22):"
	reNameTimeLine = regexp.MustCompile(`^[^\s\d\-*#>][^\n]{0,40}?\s+[\[(]?\d{1,2}:\d{2}(?::\d{2})?[\])]?\s*:?\s*$`)

	reVoiceTag = regexp.MustCompile(`<v(?:\.[^ >]+)?\s+([^>]+)>`)
	reAnyTag   = regexp.MustCompile(`</?[^>]+>`)

	reDateISO = regexp.MustCompile(`(20\d{2})[-_.](\d{2})[-_.](\d{2})(?:[ T_,-]+(?:at[ _])?(\d{1,2})[-_.:h](\d{2}))?`)
	reDateDE  = regexp.MustCompile(`(\d{1,2})\.(\d{1,2})\.(20\d{2})(?:[ _,-]+(\d{1,2})[-_.:h](\d{2}))?`)
)

// classify entscheidet anhand von Endung und Inhalt, ob eine Datei ein
// Transkript oder eine Zusammenfassung ist. Die Dateinamen von MacWhisper
// verraten das nicht, der Inhalt schon: nur Transkripte haben Zeitstempel
// bzw. Sprecher-Labels in nennenswerter Menge.
func classify(path string, content []byte, cfg *Config) Kind {
	ext := strings.ToLower(filepath.Ext(path))
	if contains(cfg.TranscriptExtensions, ext) {
		return KindTranscript
	}
	if contains(cfg.SummaryExtensions, ext) {
		return KindSummary
	}
	if !supportedExt[ext] {
		return KindUnsupported
	}
	switch ext {
	case ".srt", ".vtt":
		return KindTranscript
	case ".json":
		if len(jsonSegments(content)) > 0 {
			return KindTranscript
		}
		return KindSummary
	}
	if looksLikeTranscriptText(string(content)) {
		return KindTranscript
	}
	return KindSummary
}

func isSupportedExt(ext string, cfg *Config) bool {
	ext = strings.ToLower(ext)
	return supportedExt[ext] || contains(cfg.TranscriptExtensions, ext) || contains(cfg.SummaryExtensions, ext)
}

func looksLikeTranscriptText(s string) bool {
	lines, hits := 0, 0
	for _, l := range strings.Split(normalizeNewlines(s), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		lines++
		if reLeadingTS.MatchString(l) || reCueTiming.MatchString(l) ||
			reNameTimeLine.MatchString(l) || hasLeadingSpeaker(l) {
			hits++
		}
	}
	if lines == 0 {
		return false
	}
	return hits >= 3 && float64(hits)/float64(lines) >= 0.15
}

func hasLeadingSpeaker(l string) bool {
	loc := reSpeakerLabel.FindStringIndex(l)
	return loc != nil && loc[0] <= 25
}

type segment struct {
	Start   float64
	Speaker string
	Text    string
}

// jsonSegments liest gängige JSON-Transkriptformate (Whisper, WhisperX, MacWhisper).
func jsonSegments(b []byte) []segment {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil
	}
	var arr []any
	switch t := v.(type) {
	case []any:
		arr = t
	case map[string]any:
		for _, key := range []string{"segments", "transcription", "transcript", "utterances", "lines"} {
			if a, ok := t[key].([]any); ok {
				arr = a
				break
			}
		}
	}
	var out []segment
	for _, it := range arr {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		text := strings.TrimSpace(anyString(m["text"]))
		if text == "" {
			continue
		}
		out = append(out, segment{
			Start:   anyFloat(firstOf(m, "start", "startTime", "start_time")),
			Speaker: strings.TrimSpace(anyString(firstOf(m, "speaker", "speaker_label", "speakerName", "speaker_name"))),
			Text:    text,
		})
	}
	return out
}

// transcriptText macht aus einer Transkriptdatei kompakte Zeilen
// "[hh:mm:ss] Sprecher: Text" und liefert die ungefähre Dauer.
func transcriptText(path string, content []byte) (string, time.Duration) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".json":
		if segs := jsonSegments(content); len(segs) > 0 {
			var b strings.Builder
			var maxStart float64
			for _, s := range segs {
				if s.Start > maxStart {
					maxStart = s.Start
				}
				b.WriteString("[" + fmtDur(secs(s.Start)) + "] ")
				if s.Speaker != "" {
					b.WriteString(s.Speaker + ": ")
				}
				b.WriteString(s.Text + "\n")
			}
			return strings.TrimSpace(b.String()), secs(maxStart)
		}
	case ".srt", ".vtt":
		return cueText(string(content))
	}
	s := strings.TrimSpace(normalizeNewlines(string(content)))
	return s, maxLeadingTimestamp(s)
}

func cueText(s string) (string, time.Duration) {
	var b strings.Builder
	var cur []string
	var start, maxEnd time.Duration
	inCue := false
	flush := func() {
		if inCue && len(cur) > 0 {
			fmt.Fprintf(&b, "[%s] %s\n", fmtDur(start), strings.Join(cur, " "))
		}
		cur, inCue = nil, false
	}
	for _, raw := range strings.Split(normalizeNewlines(s), "\n") {
		line := strings.TrimSpace(raw)
		if m := reCueTiming.FindStringSubmatch(line); m != nil {
			flush()
			start = hmsms(m[1], m[2], m[3], m[4])
			if end := hmsms(m[5], m[6], m[7], m[8]); end > maxEnd {
				maxEnd = end
			}
			inCue = true
			continue
		}
		if line == "" {
			flush()
			continue
		}
		if !inCue { // Nummern, WEBVTT-Kopf, NOTE-Blöcke
			continue
		}
		line = reVoiceTag.ReplaceAllString(line, "$1: ")
		line = strings.TrimSpace(reAnyTag.ReplaceAllString(line, ""))
		if line != "" {
			cur = append(cur, line)
		}
	}
	flush()
	return strings.TrimSpace(b.String()), maxEnd
}

func maxLeadingTimestamp(s string) time.Duration {
	var max time.Duration
	for _, l := range strings.Split(s, "\n") {
		var d time.Duration
		if m := reCueTiming.FindStringSubmatch(l); m != nil {
			d = hmsms(m[5], m[6], m[7], m[8])
		} else if m := reLeadingTS.FindStringSubmatch(l); m != nil {
			a, _ := strconv.Atoi(m[1])
			b, _ := strconv.Atoi(m[2])
			if m[3] != "" { // h:mm:ss
				c, _ := strconv.Atoi(m[3])
				d = time.Duration(a)*time.Hour + time.Duration(b)*time.Minute + time.Duration(c)*time.Second
			} else { // mm:ss
				d = time.Duration(a)*time.Minute + time.Duration(b)*time.Second
			}
		}
		if d > max {
			max = d
		}
	}
	return max
}

// Timing beschreibt, wann das Meeting stattfand.
type Timing struct {
	Start     time.Time
	End       time.Time // Null, wenn unbekannt
	HasTime   bool
	Estimated bool
}

// meetingTiming nimmt Datum/Uhrzeit aus dem Dateinamen, sonst den
// Exportzeitpunkt minus Transkriptdauer.
func meetingTiming(name string, mtime time.Time, dur time.Duration, loc *time.Location) Timing {
	mtime = mtime.In(loc)
	if y, mo, d, h, mi, hasTime, ok := dateFromName(name); ok {
		if hasTime {
			start := time.Date(y, time.Month(mo), d, h, mi, 0, 0, loc)
			t := Timing{Start: start, HasTime: true}
			if dur > 0 {
				t.End = start.Add(dur)
			}
			return t
		}
		day := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, loc)
		if sameDay(day, mtime) {
			return estimatedTiming(mtime, dur)
		}
		return Timing{Start: day}
	}
	return estimatedTiming(mtime, dur)
}

func estimatedTiming(mtime time.Time, dur time.Duration) Timing {
	t := Timing{End: mtime, HasTime: true, Estimated: true, Start: mtime}
	if dur > 0 {
		t.Start = mtime.Add(-dur)
	}
	return t
}

func dateFromName(name string) (y, mo, d, h, mi int, hasTime, ok bool) {
	base := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	if m := reDateISO.FindStringSubmatch(base); m != nil {
		y, _ = strconv.Atoi(m[1])
		mo, _ = strconv.Atoi(m[2])
		d, _ = strconv.Atoi(m[3])
		if m[4] != "" {
			h, _ = strconv.Atoi(m[4])
			mi, _ = strconv.Atoi(m[5])
			hasTime = h < 24 && mi < 60
		}
		return y, mo, d, h, mi, hasTime, validDate(y, mo, d)
	}
	if m := reDateDE.FindStringSubmatch(base); m != nil {
		d, _ = strconv.Atoi(m[1])
		mo, _ = strconv.Atoi(m[2])
		y, _ = strconv.Atoi(m[3])
		if m[4] != "" {
			h, _ = strconv.Atoi(m[4])
			mi, _ = strconv.Atoi(m[5])
			hasTime = h < 24 && mi < 60
		}
		return y, mo, d, h, mi, hasTime, validDate(y, mo, d)
	}
	return 0, 0, 0, 0, 0, false, false
}

func validDate(y, mo, d int) bool {
	if mo < 1 || mo > 12 || d < 1 || d > 31 {
		return false
	}
	t := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC)
	return t.Day() == d
}

var weekdaysShort = []string{"So", "Mo", "Di", "Mi", "Do", "Fr", "Sa"}
var weekdaysLong = []string{"Sonntag", "Montag", "Dienstag", "Mittwoch", "Donnerstag", "Freitag", "Samstag"}

func (t Timing) DateLabel() string {
	return fmt.Sprintf("%s (%s)", t.Start.Format("02.01.2006"), weekdaysShort[t.Start.Weekday()])
}

func (t Timing) TimeLabel() string {
	if !t.HasTime {
		return "–"
	}
	var s string
	switch {
	case !t.End.IsZero() && !t.End.Equal(t.Start):
		s = fmt.Sprintf("%s–%s (%d min)", t.Start.Format("15:04"), t.End.Format("15:04"), int(t.End.Sub(t.Start).Minutes()+0.5))
	case t.Estimated:
		s = "ca. " + t.Start.Format("15:04") + " (Export)"
	default:
		s = t.Start.Format("15:04")
	}
	if t.Estimated && !strings.Contains(s, "Export") {
		s += ", geschätzt"
	}
	return s
}

// --- kleine Helfer ---------------------------------------------------------

func normalizeNewlines(s string) string {
	s = strings.TrimPrefix(s, "\uFEFF")
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

func hmsms(h, m, s, ms string) time.Duration {
	hi, _ := strconv.Atoi(h)
	mi, _ := strconv.Atoi(m)
	si, _ := strconv.Atoi(s)
	for len(ms) < 3 {
		ms += "0"
	}
	msi, _ := strconv.Atoi(ms)
	return time.Duration(hi)*time.Hour + time.Duration(mi)*time.Minute +
		time.Duration(si)*time.Second + time.Duration(msi)*time.Millisecond
}

func secs(f float64) time.Duration { return time.Duration(f * float64(time.Second)) }

func fmtDur(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d / time.Hour)
	m := int(d%time.Hour) / int(time.Minute)
	s := int(d%time.Minute) / int(time.Second)
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func firstOf(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			return v
		}
	}
	return nil
}

func anyString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return ""
	}
}

func anyFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		if f, err := strconv.ParseFloat(t, 64); err == nil {
			return f
		}
		if m := reLeadingTS.FindStringSubmatch(t); m != nil && m[3] != "" {
			return hmsms(m[1], m[2], m[3], "0").Seconds()
		}
	}
	return 0
}
