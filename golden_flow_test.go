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

const gfSummaryMD = `# Planung Q4

Kurze Notizen zum Gespräch: Das Team will den Build beschleunigen und führt dafür einen Cache ein.
Mark klärt das Budget für die Runner bis Freitag, Sascha baut den Cache.

Offen bleibt, wer den Cache reviewt.
`

// gfCfg erweitert die Basis-Config: top am Anfang (oberste Ebene), llm nach
// dem Modell, notion am Ende des notion-Blocks.
func gfCfg(top, llm, notion string) string {
	return top + strings.Replace(goldenBaseConfig, "  model: gemma-test\n", "  model: gemma-test\n"+llm, 1) + notion
}

// gfNoteJSON liefert die Modellantwort für eine Notiz; mod passt Felder an.
func gfNoteJSON(mod func(map[string]any)) string {
	n := map[string]any{
		"titel": "Planung Q4", "typ": "Team",
		"teilnehmer":     []string{"Mark", "Sascha"},
		"kontext_ziel":   "Build beschleunigen.",
		"themen":         []any{map[string]any{"titel": "Cache", "punkte": []string{"Cache fehlt", "Build dauert 12 Minuten"}}},
		"entscheidungen": []string{"Cache wird eingeführt"},
		"next_actions": []any{
			map[string]any{"aufgabe": "Budget für Runner klären", "owner": "Mark", "deadline": "2026-10-02", "fuer_mich": true},
		},
		"offene_fragen":     []string{"Wer reviewt den Cache?"},
		"naechstes_meeting": map[string]any{"datum": "", "themen": []string{}},
		"projekt":           "",
		"pruef_gruende":     []string{},
		"pruefen":           false,
	}
	if mod != nil {
		mod(n)
	}
	return goldenNoteJSON(n)
}

func gfLLM(replies ...string) map[string][]fakeReply {
	var rs []fakeReply
	for _, r := range replies {
		rs = append(rs, fakeReply{Body: goldenChat(r, "stop")})
	}
	return map[string][]fakeReply{"POST /v1/chat/completions": rs}
}

// gfNotion: Standard-Fake, mod darf Routen ändern.
func gfNotion(mod func(map[string][]fakeReply)) map[string][]fakeReply {
	m := goldenNotionDefault()
	if mod != nil {
		mod(m)
	}
	return m
}

func gfSRT(name string, at time.Time) goldenInput {
	return goldenInput{name: name, content: goldenSRT, mtime: at}
}

func gfSummary(name string, at time.Time) goldenInput {
	return goldenInput{name: name, content: gfSummaryMD, mtime: at}
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

// gfNotFound: realistische Notion-404-Antwort für eine nicht verbundene Datenbank.
func gfNotFound(id string) string {
	return `{"object":"error","status":404,"code":"object_not_found","message":"Could not find database with ID: ` + id +
		`. Make sure the relevant pages and databases are shared with your integration."}`
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
		m["GET /v1/databases/projects-ds"] = []fakeReply{{Status: 404, Body: gfNotFound("projects-ds")}}
		m["GET /v1/data_sources/projects-ds"] = []fakeReply{{Status: 404, Body: gfNotFound("projects-ds")}}
		delete(m, "POST /v1/data_sources/projects-ds/query")
	}
	projNote := func(p string) string {
		return gfNoteJSON(func(n map[string]any) { n["projekt"] = p })
	}
	tasksNote := gfNoteJSON(func(n map[string]any) { n["next_actions"] = gfActions() })

	registerGolden(
		// G02: .md allein, note → Waisen-Notiz mit Prüfgrund
		goldenScenario{
			name: "G02", config: goldenBaseConfig,
			files:  []goldenInput{gfSummary("Planung Q4.md", goldenMTime)},
			llm:    gfLLM(gfNoteJSON(nil)),
			notion: gfNotion(nil),
			passes: []time.Time{gfNow},
		},
		// G03: .md allein, archive → nur Archiv, kein LLM/Notion-Schreibzugriff
		goldenScenario{
			name: "G03", config: gfCfg("orphan_summary_action: archive\n", "", ""),
			files:  []goldenInput{gfSummary("Planung Q4.md", goldenMTime)},
			notion: gfNotion(nil),
			passes: []time.Time{gfNow},
		},
		// G04: .md allein, keep → bleibt liegen, Warnung nur einmal (zwei Pässe)
		goldenScenario{
			name: "G04", config: gfCfg("orphan_summary_action: keep\n", "", ""),
			files:  []goldenInput{gfSummary("Planung Q4.md", goldenMTime)},
			notion: gfNotion(nil),
			passes: []time.Time{gfNow, gfNow.Add(10 * time.Second)},
		},
		// G05a/b: Paar mit gleichem Namensstamm, Zusammenfassung als Kontext an/aus
		goldenScenario{
			name: "G05a", config: gfCfg("use_summary_as_context: true\n", "", ""),
			files:  []goldenInput{gfSRT("Planung Q4.srt", goldenMTime), gfSummary("Planung Q4.md", goldenMTime.Add(40*time.Second))},
			llm:    gfLLM(gfNoteJSON(nil)),
			notion: gfNotion(nil),
			passes: []time.Time{gfNow},
		},
		goldenScenario{
			name: "G05b", config: gfCfg("use_summary_as_context: false\n", "", ""),
			files:  []goldenInput{gfSRT("Planung Q4.srt", goldenMTime), gfSummary("Planung Q4.md", goldenMTime.Add(40*time.Second))},
			llm:    gfLLM(gfNoteJSON(nil)),
			notion: gfNotion(nil),
			passes: []time.Time{gfNow},
		},
		// G06: Paar ohne gleichen Stamm, 90 s Abstand (Fenster 180 s)
		goldenScenario{
			name: "G06", config: goldenBaseConfig,
			files:  []goldenInput{gfSRT("Sirius Sync.srt", goldenMTime), gfSummary("Notizen Planung.md", goldenMTime.Add(90*time.Second))},
			llm:    gfLLM(gfNoteJSON(nil)),
			notion: gfNotion(nil),
			passes: []time.Time{gfNow},
		},
		// G06b: 240 s Abstand, außerhalb des Fensters → zwei getrennte Notizen
		goldenScenario{
			name: "G06b", config: goldenBaseConfig,
			files:  []goldenInput{gfSRT("Sirius Sync.srt", goldenMTime), gfSummary("Notizen Planung.md", goldenMTime.Add(240*time.Second))},
			llm:    gfLLM(gfNoteJSON(nil), gfNoteJSON(func(n map[string]any) { n["titel"] = "Planung Notizen" })),
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
			llm:    gfLLM(gfNoteJSON(nil)),
			notion: gfNotion(nil),
			passes: []time.Time{gfNow},
		},
		// G08: Transkript > max_chunk_chars → zwei Abschnitts-Prompts, dann Endprompt
		goldenScenario{
			name: "G08", config: gfCfg("", "  max_chunk_chars: 5000\n", ""),
			files: []goldenInput{{name: "Lang.txt", content: gfLongTranscript(), mtime: goldenMTime}},
			llm: gfLLM(
				"<think>überlege</think>\n- Thema Pipeline\n- Cache fehlt",
				"- Entscheidung: Cache wird eingeführt",
				gfNoteJSON(nil)),
			notion: gfNotion(nil),
			passes: []time.Time{gfNow},
		},
		// G13a: gleicher Inhalt unter anderem Namen (mit eigener Zusammenfassung) → ↺, beide archiviert
		goldenScenario{
			name: "G13a", config: goldenBaseConfig,
			files: []goldenInput{
				gfSRT("Erst.srt", goldenMTime),
				gfSRT("Zweit.srt", goldenMTime.Add(300*time.Second)),
				gfSummary("Zweit.md", goldenMTime.Add(310*time.Second)),
			},
			llm:    gfLLM(gfNoteJSON(nil)),
			notion: gfNotion(nil),
			passes: []time.Time{gfNow},
		},
		// G13b: zweite Zusammenfassung nahe am verarbeiteten Transkript → 📎, Archiv
		goldenScenario{
			name: "G13b", config: goldenBaseConfig,
			files: []goldenInput{
				gfSRT("Planung Q4.srt", goldenMTime),
				gfSummary("Planung A.md", goldenMTime.Add(30*time.Second)),
				{name: "Planung B.md", content: gfSummaryMD + "\nZweite Fassung.\n", mtime: goldenMTime.Add(60 * time.Second)},
			},
			llm:    gfLLM(gfNoteJSON(nil)),
			notion: gfNotion(nil),
			passes: []time.Time{gfNow},
		},
		// G14a: über 100 Blöcke (POST + 2 PATCH), Text über 2000 Zeichen wird geteilt
		goldenScenario{
			name: "G14a", config: goldenBaseConfig,
			files: []goldenInput{gfSRT("Planung Q4.srt", goldenMTime)},
			llm: gfLLM(gfNoteJSON(func(n map[string]any) {
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
			name: "G14b", config: gfCfg("include_transcript: true\n", "", ""),
			files:  []goldenInput{{name: "Lang.txt", content: gfLongTranscriptTxt(), mtime: goldenMTime}},
			llm:    gfLLM(gfNoteJSON(nil)),
			notion: gfNotion(nil),
			passes: []time.Time{gfNow},
		},
		// G15a: Projekt-Treffer unabhängig von Groß-/Kleinschreibung → Relation in Notiz und Task
		goldenScenario{
			name: "G15a", config: goldenBaseConfig,
			files: []goldenInput{gfSRT("Planung Q4.srt", goldenMTime)},
			llm:   gfLLM(projNote("ai-first")), notion: gfNotion(nil),
			passes: []time.Time{gfNow},
		},
		// G15b: Projekt ohne Treffer → keine Relation
		goldenScenario{
			name: "G15b", config: goldenBaseConfig,
			files: []goldenInput{gfSRT("Planung Q4.srt", goldenMTime)},
			llm:   gfLLM(projNote("Unbekannt")), notion: gfNotion(nil),
			passes: []time.Time{gfNow},
		},
		// G15c: Projects-DB nicht lesbar → Hinweis, keine Projektliste
		goldenScenario{
			name: "G15c", config: goldenBaseConfig,
			files: []goldenInput{gfSRT("Planung Q4.srt", goldenMTime)},
			llm:   gfLLM(projNote("AI-First")), notion: gfNotion(noProjects),
			passes: []time.Time{gfNow},
		},
		// G15d: Projects-DB lesbar, Abfrage scheitert → Hinweis, weiter ohne Projekte
		goldenScenario{
			name: "G15d", config: goldenBaseConfig,
			files: []goldenInput{gfSRT("Planung Q4.srt", goldenMTime)},
			llm:   gfLLM(projNote("AI-First")),
			notion: gfNotion(func(m map[string][]fakeReply) {
				m["POST /v1/data_sources/projects-ds/query"] = []fakeReply{{Status: 400, Body: `{"object":"error","status":400,"code":"validation_error","message":"kaputt"}`}}
			}),
			passes: []time.Time{gfNow},
		},
		// G16a/b/c: create_tasks_for me / all / none
		goldenScenario{
			name: "G16a", config: gfCfg("", "", "  create_tasks_for: me\n"),
			files: []goldenInput{gfSRT("Planung Q4.srt", goldenMTime)},
			llm:   gfLLM(tasksNote), notion: gfNotion(nil),
			passes: []time.Time{gfNow},
		},
		goldenScenario{
			name: "G16b", config: gfCfg("", "", "  create_tasks_for: all\n"),
			files: []goldenInput{gfSRT("Planung Q4.srt", goldenMTime)},
			llm:   gfLLM(tasksNote), notion: gfNotion(nil),
			passes: []time.Time{gfNow},
		},
		goldenScenario{
			name: "G16c", config: gfCfg("", "", "  create_tasks_for: none\n"),
			files: []goldenInput{gfSRT("Planung Q4.srt", goldenMTime)},
			llm:   gfLLM(tasksNote), notion: gfNotion(nil),
			passes: []time.Time{gfNow},
		},
	)
}
