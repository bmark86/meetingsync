package main

// Golden-Szenarien für CLI und Fehlerpfade (R-M0-07): G09 (-dry-run -file),
// G10a-d (-check), G11 (vorübergehender Fehler bis _fehler), G12a/b
// (dauerhafte Fehler bis _fehler), G12c (Schema-Fallback). Harness und
// Normalisierung: golden_test.go.
// Die Pass-Szenarien (G11, G12) laufen mit simuliertem now, ohne echte Wartezeit.

import (
	"strings"
	"time"
)

// goldenCLINote ist eine kleine, gültige LLM-Antwort für den Dry-Run.
func goldenCLINote() string {
	return goldenNoteJSON(map[string]any{
		"titel": "Sirius Sync", "typ": "Team",
		"teilnehmer":     []string{"Mark"},
		"kontext_ziel":   "Stand der Build-Pipeline klären.",
		"themen":         []any{map[string]any{"titel": "Pipeline", "punkte": []string{"Build dauert 12 Minuten"}}},
		"entscheidungen": []string{"Cache wird eingeführt"},
		"next_actions": []any{
			map[string]any{"aufgabe": "Budget für Runner klären", "owner": "Mark", "deadline": "2026-10-02", "fuer_mich": true},
		},
		"offene_fragen":     []string{},
		"naechstes_meeting": map[string]any{"datum": "", "themen": []string{}},
		"projekt":           "ai-first",
		"pruef_gruende":     []string{},
		"pruefen":           false,
	})
}

// goldenCLIModels: LM Studio meldet die angegebenen Modelle.
func goldenCLIModels(ids ...string) map[string][]fakeReply {
	var data []any
	for _, id := range ids {
		data = append(data, map[string]any{"id": id})
	}
	return map[string][]fakeReply{"GET /v1/models": {{Body: map[string]any{"data": data}}}}
}

// goldenCLIPasses: erster Pass 5 min nach der mtime (Datei ist stabil), jeder
// weitere um den jeweiligen Abstand später. Die Abstände liegen weit unter
// 15 min (Projekt-Cache nutzt die echte Uhr).
func goldenCLIPasses(gaps ...time.Duration) []time.Time {
	t := goldenMTime.Add(5 * time.Minute)
	out := []time.Time{t}
	for _, g := range gaps {
		t = t.Add(g)
		out = append(out, t)
	}
	return out
}

func init() {
	cfg := goldenBaseConfig
	// Nichts hört auf 127.0.0.1:1 → "connection refused", ohne Netz.
	cfgDeadLLM := strings.Replace(cfg, "{{llm}}/v1", "http://127.0.0.1:1/v1", 1)
	cfgNoSchema := strings.Replace(cfg, "  model: gemma-test\n", "  model: gemma-test\n  structured_output: false\n", 1)
	srt := []goldenInput{{name: "Sirius Sync.srt", content: goldenSRT, mtime: goldenMTime}}
	llm503 := map[string][]fakeReply{"POST /v1/chat/completions": {{Status: 503, Body: `{"error":"Model is loading"}`}}}
	llm400 := map[string][]fakeReply{"POST /v1/chat/completions": {{Status: 400, Body: `{"error":"bad request"}`}}}

	registerGolden(
		goldenScenario{
			name:   "G09",
			config: cfg,
			files:  srt,
			llm:    map[string][]fakeReply{"POST /v1/chat/completions": {{Body: goldenChat(goldenCLINote(), "stop")}}},
			notion: goldenNotionDefault(),
			args:   []string{"-config", "{{config}}", "-dry-run", "-file", "{{watch}}/Sirius Sync.srt"},
		},
		goldenScenario{
			name:   "G10a",
			config: cfg,
			llm:    goldenCLIModels("gemma-test", "andere-modell"),
			notion: goldenNotionDefault(),
			args:   []string{"-config", "{{config}}", "-check"},
		},
		goldenScenario{
			// Heute Exit-Code 0, obwohl LM Studio fehlt (Charakterisierung).
			name:   "G10b",
			config: cfgDeadLLM,
			notion: goldenNotionDefault(),
			args:   []string{"-config", "{{config}}", "-check"},
		},
		goldenScenario{
			name:   "G10c",
			config: cfg,
			llm:    goldenCLIModels("andere-modell", "noch-eins"),
			notion: goldenNotionDefault(),
			args:   []string{"-config", "{{config}}", "-check"},
		},
		goldenScenario{
			// Notes-Datenbank nicht lesbar (404): Exit-Code 1.
			name:   "G10d",
			config: cfg,
			llm:    goldenCLIModels("gemma-test"),
			notion: map[string][]fakeReply{
				"GET /v1/data_sources/notes-ds": {{Status: 404, Body: gfNotFound("notes-ds")}},
				"GET /v1/databases/notes-ds":    {{Status: 404, Body: gfNotFound("notes-ds")}},
			},
			args: []string{"-config", "{{config}}", "-check"},
		},
		goldenScenario{
			// 503 bei LM Studio: Wartezeit 1, 2, 4 min, beim 4. Versuch → _fehler.
			// Der Pass nach 30 s liegt im Backoff und tut nichts.
			name:   "G11",
			config: cfg + "max_attempts: 4\n",
			files:  srt,
			llm:    llm503,
			notion: goldenNotionDefault(),
			passes: goldenCLIPasses(30*time.Second, 30*time.Second, 2*time.Minute, 4*time.Minute),
		},
		goldenScenario{
			// 400 ohne Schema-Fallback (structured_output aus): dauerhafter Fehler,
			// Wartezeit je 2 min (Pass nach 1 min tut nichts), 3. Versuch → _fehler.
			name:   "G12a",
			config: cfgNoSchema + "max_attempts: 3\n",
			files:  srt,
			llm:    llm400,
			notion: goldenNotionDefault(),
			passes: goldenCLIPasses(time.Minute, time.Minute, 2*time.Minute),
		},
		goldenScenario{
			// 400 bei structured_output=true: Schema-Fallback (Schema im Prompt,
			// ohne response_format), zweiter Request liefert die Notiz.
			name:   "G12c",
			config: cfg,
			files:  srt,
			llm: map[string][]fakeReply{"POST /v1/chat/completions": {
				{Status: 400, Body: `{"error":"response_format json_schema wird nicht unterstützt"}`},
				{Body: goldenChat(goldenCLINote(), "stop")},
			}},
			notion: goldenNotionDefault(),
			passes: goldenCLIPasses(),
		},
		goldenScenario{
			// Datei nur mit Leerraum: "Datei ist leer", LLM wird nicht gefragt.
			name:   "G12b",
			config: cfg + "max_attempts: 2\n",
			files:  []goldenInput{{name: "Leer.srt", content: "\n  \n", mtime: goldenMTime}},
			notion: goldenNotionDefault(),
			passes: goldenCLIPasses(2 * time.Minute),
		},
	)
}
