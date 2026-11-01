package main

// Golden-Szenarien Ablauf (R-M0-06): G02 bis G08 und G13 bis G16.
// Alle Mehr-Pass-Szenarien bleiben unter 15 Minuten Abstand der now-Werte
// (Projekt-Cache nutzt die echte Uhr, siehe ADR 0003). Hilfsnamen beginnen
// mit gf, damit sie nicht mit anderen Szenario-Dateien kollidieren.

import (
	"fmt"
	"strings"
	"time"
)

// gfNow liegt weit genug nach den Eingabe-mtimes: stabil und Wartezeit für
// verwaiste Zusammenfassungen (10 min) vorbei.
var gfNow = goldenMTime.Add(20 * time.Minute)

// gfNotion: Standard-Fake, mod darf Routen ändern.
func gfNotion(mod func(map[string][]fakeReply)) map[string][]fakeReply {
	m := goldenNotionDefault()
	if mod != nil {
		mod(m)
	}
	return m
}

// gfLongTranscript: Zeilen mit Zeitmarke, insgesamt über 5000 Zeichen.
func gfLongTranscript() string {
	var b strings.Builder
	for i := 0; i < 70; i++ {
		fmt.Fprintf(&b, "[00:%02d:%02d] Speaker %d: Satz %03d zur Pipeline und zum Cache, damit der Text lang genug wird.\n",
			i/2, (i%2)*30, i%2+1, i)
	}
	return b.String()
}

// gfManyThemes: so viele Themen, dass die Seite weit über 100 Blöcke hat.
func gfManyThemes(n int) []any {
	var out []any
	for i := 1; i <= n; i++ {
		out = append(out, map[string]any{
			"titel":  fmt.Sprintf("Thema %02d", i),
			"punkte": []string{fmt.Sprintf("Punkt %02d a", i), fmt.Sprintf("Punkt %02d b", i)},
		})
	}
	return out
}

func gfLongTranscriptTxt() string {
	var b strings.Builder
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&b, "[00:%02d:00] Speaker 1: %s\n", i, strings.Repeat("Größe prüfen, Maß. ", 11))
	}
	// eine einzelne Zeile über 1900 Zeichen: wird hart geteilt
	fmt.Fprintf(&b, "[00:12:00] Speaker 2: %s\n", strings.Repeat("Sehr lange Zeile für Übergabe ohne Umbruch. ", 80))
	return b.String()
}

func gfActions() []any {
	return []any{
		map[string]any{"aufgabe": "Budget für Runner klären", "owner": "Mark", "deadline": "2026-10-02", "fuer_mich": true},
		map[string]any{"aufgabe": "Cache bauen", "owner": "Sascha", "deadline": "", "fuer_mich": false},
		map[string]any{"aufgabe": "Review ansetzen", "owner": "Mark", "deadline": "", "fuer_mich": true},
		map[string]any{"aufgabe": "Runner prüfen", "owner": "", "deadline": "nächste Woche", "fuer_mich": false},
	}
}

func init() {
	// Mehrbyte-Zeichen (ä, ö, ü, ß): Teilungsgrenzen fallen mitten in Zeichen,
	// ein Byte- statt Rune-Split würde die Goldens ändern.
	long := strings.Repeat("Größere Änderung für Übergabe. ", 155)
	noProjects := func(m map[string][]fakeReply) {
		m["GET /v1/databases/projects-ds"] = []fakeReply{{Status: 404, Body: goldenNotFound("projects-ds")}}
		m["GET /v1/data_sources/projects-ds"] = []fakeReply{{Status: 404, Body: goldenNotFound("projects-ds")}}
		delete(m, "POST /v1/data_sources/projects-ds/query")
	}
	projNote := func(p string) string {
		return goldenNoteWith(func(n map[string]any) { n["projekt"] = p })
	}
	tasksNote := goldenNoteWith(func(n map[string]any) { n["next_actions"] = gfActions() })

	registerGolden(
		// G02: .md allein, note → Waisen-Notiz mit Prüfgrund
		goldenScenario{
			name: "G02", config: goldenBaseConfig,
			files:  []goldenInput{goldenSummaryInput("Planung Q4.md", goldenMTime)},
			llm:    goldenLLM(goldenNoteWith(nil)),
			notion: gfNotion(nil),
			passes: []time.Time{gfNow},
		},
		// G03: .md allein, archive → nur Archiv, kein LLM/Notion-Schreibzugriff
		goldenScenario{
			name: "G03", config: goldenCfg("orphan_summary_action: archive\n", "", ""),
			files:  []goldenInput{goldenSummaryInput("Planung Q4.md", goldenMTime)},
			notion: gfNotion(nil),
			passes: []time.Time{gfNow},
		},
		// G04: .md allein, keep → bleibt liegen, Warnung nur einmal (zwei Pässe)
		goldenScenario{
			name: "G04", config: goldenCfg("orphan_summary_action: keep\n", "", ""),
			files:  []goldenInput{goldenSummaryInput("Planung Q4.md", goldenMTime)},
			notion: gfNotion(nil),
			passes: []time.Time{gfNow, gfNow.Add(10 * time.Second)},
		},
		// G05a/b: Paar mit gleichem Namensstamm, Zusammenfassung als Kontext an/aus
		goldenScenario{
			name: "G05a", config: goldenCfg("use_summary_as_context: true\n", "", ""),
			files:  []goldenInput{goldenSRTInput("Planung Q4.srt", goldenMTime), goldenSummaryInput("Planung Q4.md", goldenMTime.Add(40*time.Second))},
			llm:    goldenLLM(goldenNoteWith(nil)),
			notion: gfNotion(nil),
			passes: []time.Time{gfNow},
		},
		goldenScenario{
			name: "G05b", config: goldenCfg("use_summary_as_context: false\n", "", ""),
			files:  []goldenInput{goldenSRTInput("Planung Q4.srt", goldenMTime), goldenSummaryInput("Planung Q4.md", goldenMTime.Add(40*time.Second))},
			llm:    goldenLLM(goldenNoteWith(nil)),
			notion: gfNotion(nil),
			passes: []time.Time{gfNow},
		},
		// G06: Paar ohne gleichen Stamm, 90 s Abstand (Fenster 180 s)
		goldenScenario{
			name: "G06", config: goldenBaseConfig,
			files:  []goldenInput{goldenSRTInput("Sirius Sync.srt", goldenMTime), goldenSummaryInput("Notizen Planung.md", goldenMTime.Add(90*time.Second))},
			llm:    goldenLLM(goldenNoteWith(nil)),
			notion: gfNotion(nil),
			passes: []time.Time{gfNow},
		},
		// G06b: 240 s Abstand, außerhalb des Fensters → zwei getrennte Notizen
		goldenScenario{
			name: "G06b", config: goldenBaseConfig,
			files:  []goldenInput{goldenSRTInput("Sirius Sync.srt", goldenMTime), goldenSummaryInput("Notizen Planung.md", goldenMTime.Add(240*time.Second))},
			llm:    goldenLLM(goldenNoteWith(nil), goldenNoteWith(func(n map[string]any) { n["titel"] = "Planung Notizen" })),
			notion: gfNotion(nil),
			passes: []time.Time{gfNow},
		},
		// G07: .json mit Segmenten, Datum/Uhrzeit aus dem Dateinamen, Dauer aus start
		goldenScenario{
			name: "G07", config: goldenBaseConfig,
			files: []goldenInput{{
				name: "2026-09-30 10-00 Review.json", mtime: goldenMTime,
				content: `{"segments":[` +
					`{"start":0,"end":6.5,"speaker":"Speaker 1","text":"Hallo zusammen, kurzer Review."},` +
					`{"start":7.0,"end":14,"speaker":"Speaker 2","text":"Der Cache fehlt noch."},` +
					`{"start":1800.5,"end":1810,"speaker":"Speaker 1","text":"Dann bis Freitag."}]}`,
			}},
			llm:    goldenLLM(goldenNoteWith(nil)),
			notion: gfNotion(nil),
			passes: []time.Time{gfNow},
		},
		// G08: Transkript > max_chunk_chars → zwei Abschnitts-Prompts, dann Endprompt
		goldenScenario{
			name: "G08", config: goldenCfg("", "  max_chunk_chars: 5000\n", ""),
			files: []goldenInput{{name: "Lang.txt", content: gfLongTranscript(), mtime: goldenMTime}},
			llm: goldenLLM(
				"<think>überlege</think>\n- Thema Pipeline\n- Cache fehlt",
				"- Entscheidung: Cache wird eingeführt",
				goldenNoteWith(nil)),
			notion: gfNotion(nil),
			passes: []time.Time{gfNow},
		},
		// G13a: gleicher Inhalt unter anderem Namen (mit eigener Zusammenfassung) → ↺, beide archiviert
		goldenScenario{
			name: "G13a", config: goldenBaseConfig,
			files: []goldenInput{
				goldenSRTInput("Erst.srt", goldenMTime),
				goldenSRTInput("Zweit.srt", goldenMTime.Add(300*time.Second)),
				goldenSummaryInput("Zweit.md", goldenMTime.Add(310*time.Second)),
			},
			llm:    goldenLLM(goldenNoteWith(nil)),
			notion: gfNotion(nil),
			passes: []time.Time{gfNow},
		},
		// G13b: zweite Zusammenfassung nahe am verarbeiteten Transkript → 📎, Archiv
		goldenScenario{
			name: "G13b", config: goldenBaseConfig,
			files: []goldenInput{
				goldenSRTInput("Planung Q4.srt", goldenMTime),
				goldenSummaryInput("Planung A.md", goldenMTime.Add(30*time.Second)),
				{name: "Planung B.md", content: goldenSummaryMD + "\nZweite Fassung.\n", mtime: goldenMTime.Add(60 * time.Second)},
			},
			llm:    goldenLLM(goldenNoteWith(nil)),
			notion: gfNotion(nil),
			passes: []time.Time{gfNow},
		},
		// G14a: über 100 Blöcke (POST + 2 PATCH), Text über 2000 Zeichen wird geteilt
		goldenScenario{
			name: "G14a", config: goldenBaseConfig,
			files: []goldenInput{goldenSRTInput("Planung Q4.srt", goldenMTime)},
			llm: goldenLLM(goldenNoteWith(func(n map[string]any) {
				n["kontext_ziel"] = long
				n["themen"] = append(gfManyThemes(70), map[string]any{"titel": "Langer Punkt", "punkte": []string{long}})
			})),
			notion: gfNotion(func(m map[string][]fakeReply) {
				m["PATCH /v1/blocks/page-1/children"] = []fakeReply{{Body: `{"object":"list","results":[]}`}}
			}),
			passes: []time.Time{gfNow},
		},
		// G14b: include_transcript → Toggle mit Absätzen, lange Zeile wird geteilt
		goldenScenario{
			name: "G14b", config: goldenCfg("include_transcript: true\n", "", ""),
			files:  []goldenInput{{name: "Lang.txt", content: gfLongTranscriptTxt(), mtime: goldenMTime}},
			llm:    goldenLLM(goldenNoteWith(nil)),
			notion: gfNotion(nil),
			passes: []time.Time{gfNow},
		},
		// G15a: Projekt-Treffer unabhängig von Groß-/Kleinschreibung → Relation in Notiz und Task
		goldenScenario{
			name: "G15a", config: goldenBaseConfig,
			files: []goldenInput{goldenSRTInput("Planung Q4.srt", goldenMTime)},
			llm:   goldenLLM(projNote("ai-first")), notion: gfNotion(nil),
			passes: []time.Time{gfNow},
		},
		// G15b: Projekt ohne Treffer → keine Relation
		goldenScenario{
			name: "G15b", config: goldenBaseConfig,
			files: []goldenInput{goldenSRTInput("Planung Q4.srt", goldenMTime)},
			llm:   goldenLLM(projNote("Unbekannt")), notion: gfNotion(nil),
			passes: []time.Time{gfNow},
		},
		// G15c: Projects-DB nicht lesbar → Hinweis, keine Projektliste
		goldenScenario{
			name: "G15c", config: goldenBaseConfig,
			files: []goldenInput{goldenSRTInput("Planung Q4.srt", goldenMTime)},
			llm:   goldenLLM(projNote("AI-First")), notion: gfNotion(noProjects),
			passes: []time.Time{gfNow},
		},
		// G15d: Projects-DB lesbar, Abfrage scheitert → Hinweis, weiter ohne Projekte
		goldenScenario{
			name: "G15d", config: goldenBaseConfig,
			files: []goldenInput{goldenSRTInput("Planung Q4.srt", goldenMTime)},
			llm:   goldenLLM(projNote("AI-First")),
			notion: gfNotion(func(m map[string][]fakeReply) {
				m["POST /v1/data_sources/projects-ds/query"] = []fakeReply{{Status: 400, Body: `{"object":"error","status":400,"code":"validation_error","message":"kaputt"}`}}
			}),
			passes: []time.Time{gfNow},
		},
		// G16a/b/c: create_tasks_for me / all / none
		goldenScenario{
			name: "G16a", config: goldenCfg("", "", "  create_tasks_for: me\n"),
			files: []goldenInput{goldenSRTInput("Planung Q4.srt", goldenMTime)},
			llm:   goldenLLM(tasksNote), notion: gfNotion(nil),
			passes: []time.Time{gfNow},
		},
		goldenScenario{
			name: "G16b", config: goldenCfg("", "", "  create_tasks_for: all\n"),
			files: []goldenInput{goldenSRTInput("Planung Q4.srt", goldenMTime)},
			llm:   goldenLLM(tasksNote), notion: gfNotion(nil),
			passes: []time.Time{gfNow},
		},
		goldenScenario{
			name: "G16c", config: goldenCfg("", "", "  create_tasks_for: none\n"),
			files: []goldenInput{goldenSRTInput("Planung Q4.srt", goldenMTime)},
			llm:   goldenLLM(tasksNote), notion: gfNotion(nil),
			passes: []time.Time{gfNow},
		},
	)
}
