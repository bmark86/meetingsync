package main

import "testing"

func TestExampleConfigLoads(t *testing.T) {
	cfg, err := LoadConfig("config.example.json")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Notion.Notes.StatusValue != "Inbox" || cfg.Notion.Tasks.DueDate != "Due Date" || !cfg.LLM.StructuredOutput {
		t.Fatalf("Standardwerte gehen beim Laden verloren: %+v", cfg.Notion)
	}
	if cfg.ArchiveDir == "" || cfg.loc == nil {
		t.Fatal("Archivordner/Zeitzone nicht gesetzt")
	}
}
