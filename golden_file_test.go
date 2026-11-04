package main

// Golden-Szenario G09b (R-M3-14): -file ohne -dry-run legt die Notiz an,
// schreibt das Protokoll (begonnen, fertig) und lässt die Datei im
// Exportordner liegen. Harness und Normalisierung: golden_test.go.

func init() {
	registerGolden(goldenScenario{
		name:   "G09b",
		config: goldenBaseConfig,
		files:  []goldenInput{goldenSRTInput("Sirius Sync.srt", goldenMTime)},
		llm:    goldenLLM(goldenCLINote()),
		notion: goldenNotionDefault(),
		args:   []string{"-config", "{{config}}", "-file", "{{watch}}/Sirius Sync.srt"},
	})
}
