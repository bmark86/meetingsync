package main

import (
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"meetingsync/internal/domain"
)

// buildNoteBlocks baut die Seite exakt nach "📋 TEMPLATE - Meeting Notes".
func buildNoteBlocks(n *domain.MeetingNote, m domain.NoteMeta) []any {
	var b []any

	if len(m.ReviewReasons) > 0 {
		b = append(b, callout("⚠️", "Bitte prüfen: "+strings.Join(m.ReviewReasons, " · "), "yellow_background"))
	}

	// 📅 Meeting-Info
	sources := make([]string, len(m.SourceFiles))
	for i, s := range m.SourceFiles {
		sources[i] = filepath.Base(s)
	}
	b = append(b,
		h2("📅 Meeting-Info"),
		infoTable([][2]string{
			{"Datum", m.Timing.DateLabel()},
			{"Uhrzeit", m.Timing.TimeLabel()},
			{"Ort / Link", "–"},
			{"Typ", n.Typ},
			{"Quelle", strings.Join(sources, ", ")},
		}),
		divider(),
	)

	// 👥 Teilnehmer
	b = append(b, h2("👥 Teilnehmer"))
	b = append(b, bullets(n.Teilnehmer)...)
	b = append(b, divider())

	// 🎯 Kontext & Ziel
	b = append(b, h2("🎯 Kontext & Ziel des Meetings"), paragraph(orDash(n.KontextZiel)), divider())

	// 📝 Besprochene Themen
	b = append(b, h2("📝 Besprochene Themen"))
	if len(n.Themen) == 0 {
		b = append(b, paragraph("–"))
	}
	for _, t := range n.Themen {
		b = append(b, h3(orDash(t.Titel)))
		b = append(b, bullets(t.Punkte)...)
	}
	b = append(b, divider())

	// ✅ Entscheidungen
	b = append(b, h2("✅ Entscheidungen"))
	if len(n.Entscheidungen) == 0 {
		b = append(b, paragraph("–"))
	}
	for _, e := range n.Entscheidungen {
		b = append(b, todo(e))
	}
	b = append(b, divider())

	// 🚀 Next Actions
	b = append(b, h2("🚀 Next Actions"))
	rows := [][]string{{"Aufgabe", "Owner", "Deadline"}}
	for _, a := range n.NextActions {
		owner := a.Owner
		if a.FuerMich && !strings.Contains(owner, "⭐") {
			owner = strings.TrimSpace(owner + " ⭐")
		}
		rows = append(rows, []string{a.Aufgabe, owner, deadlineLabel(a.Deadline)})
	}
	if len(rows) == 1 {
		rows = append(rows, []string{"–", "", ""})
	}
	b = append(b, table(rows, true, false), divider())

	// ❓ Offene Fragen / Parked
	b = append(b, h2("❓ Offene Fragen / Parked"))
	b = append(b, bullets(n.OffeneFragen)...)
	b = append(b, divider())

	// 📅 Nächstes Meeting
	b = append(b,
		h2("📅 Nächstes Meeting"),
		bulletRich(labelled("Datum: ", orDash(n.NaechstesMeeting.Datum))),
		bulletRich(labelled("Themen: ", orDash(strings.Join(n.NaechstesMeeting.Themen, "; ")))),
	)

	if strings.TrimSpace(m.Transcript) != "" {
		b = append(b, divider(), toggle("🎙️ Transkript", transcriptParagraphs(m.Transcript)))
	}
	return b
}

func formatTitle(format string, n *domain.MeetingNote, t domain.Timing) string {
	r := strings.NewReplacer(
		"{date}", t.Start.Format("2006-01-02"),
		"{date_de}", t.Start.Format("02.01.2006"),
		"{title}", n.Titel,
		"{typ}", n.Typ,
	)
	return strings.TrimSpace(r.Replace(format))
}

func deadlineLabel(d string) string {
	if t, err := time.Parse("2006-01-02", d); err == nil {
		return t.Format("02.01.2006")
	}
	return ""
}

// --- Notion-Blöcke ---------------------------------------------------------------

const maxTextLen = 1900 // Notion erlaubt 2000 Zeichen pro Text-Objekt

func rt(s string) []any { return rtStyled(s, false) }

func rtStyled(s string, bold bool) []any {
	if s == "" {
		return []any{}
	}
	var out []any
	for _, part := range splitRunes(s, maxTextLen) {
		o := map[string]any{"type": "text", "text": map[string]any{"content": part}}
		if bold {
			o["annotations"] = map[string]any{"bold": true}
		}
		out = append(out, o)
		if len(out) == 100 {
			break
		}
	}
	if out == nil {
		out = []any{}
	}
	return out
}

func labelled(label, value string) []any {
	return append(rtStyled(label, true), rt(value)...)
}

func block(typ string, content map[string]any) map[string]any {
	return map[string]any{"object": "block", "type": typ, typ: content}
}

func h2(s string) any        { return block("heading_2", map[string]any{"rich_text": rt(s)}) }
func h3(s string) any        { return block("heading_3", map[string]any{"rich_text": rt(s)}) }
func paragraph(s string) any { return block("paragraph", map[string]any{"rich_text": rt(s)}) }
func divider() any           { return block("divider", map[string]any{}) }
func todo(s string) any {
	return block("to_do", map[string]any{"rich_text": rt(s), "checked": false})
}
func bulletRich(r []any) any { return block("bulleted_list_item", map[string]any{"rich_text": r}) }

func bullets(items []string) []any {
	if len(items) == 0 {
		return []any{bulletRich(rt("–"))}
	}
	out := make([]any, 0, len(items))
	for _, it := range items {
		out = append(out, bulletRich(rt(it)))
	}
	return out
}

func callout(emoji, text, color string) any {
	return block("callout", map[string]any{
		"rich_text": rt(text),
		"icon":      map[string]any{"type": "emoji", "emoji": emoji},
		"color":     color,
	})
}

func toggle(title string, children []any) any {
	return block("toggle", map[string]any{"rich_text": rt(title), "children": children})
}

func infoTable(rows [][2]string) any {
	tr := make([]any, 0, len(rows))
	for _, r := range rows {
		tr = append(tr, block("table_row", map[string]any{
			"cells": []any{rtStyled(r[0], true), rt(orDash(r[1]))},
		}))
	}
	return block("table", map[string]any{
		"table_width": 2, "has_column_header": false, "has_row_header": true, "children": tr,
	})
}

func table(rows [][]string, header, rowHeader bool) any {
	width := 0
	for _, r := range rows {
		if len(r) > width {
			width = len(r)
		}
	}
	tr := make([]any, 0, len(rows))
	for _, r := range rows {
		cells := make([]any, width)
		for i := 0; i < width; i++ {
			v := ""
			if i < len(r) {
				v = r[i]
			}
			cells[i] = rt(v)
		}
		tr = append(tr, block("table_row", map[string]any{"cells": cells}))
	}
	return block("table", map[string]any{
		"table_width": width, "has_column_header": header, "has_row_header": rowHeader, "children": tr,
	})
}

// transcriptParagraphs packt das Transkript zeilenweise in Absätze (max. 100,
// das Notion-Limit für verschachtelte Blöcke in einem Request).
func transcriptParagraphs(t string) []any {
	const maxBlocks = 100
	var out []any
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, paragraph(strings.TrimRight(cur.String(), "\n")))
			cur.Reset()
		}
	}
	for _, line := range strings.Split(t, "\n") {
		for _, part := range splitRunes(line, maxTextLen) {
			if utf8.RuneCountInString(cur.String())+utf8.RuneCountInString(part)+1 > maxTextLen {
				flush()
			}
			cur.WriteString(part + "\n")
		}
		if len(out) >= maxBlocks-1 {
			break
		}
	}
	flush()
	if len(out) >= maxBlocks {
		out = out[:maxBlocks-1]
		out = append(out, paragraph("… gekürzt. Das vollständige Transkript liegt im Archivordner."))
	}
	return out
}

func splitRunes(s string, max int) []string {
	if s == "" {
		return []string{""}
	}
	var parts []string
	r := []rune(s)
	for len(r) > max {
		parts = append(parts, string(r[:max]))
		r = r[max:]
	}
	return append(parts, string(r))
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "–"
	}
	return s
}
