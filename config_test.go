package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestExampleConfigLoads(t *testing.T) {
	for _, f := range []string{"config.example.json", "config.example.yaml"} {
		cfg, err := LoadConfig(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if cfg.Notion.Notes.StatusValue != "Inbox" || cfg.Notion.Tasks.DueDate != "Due Date" || !cfg.LLM.StructuredOutput {
			t.Fatalf("%s: Standardwerte gehen beim Laden verloren: %+v", f, cfg.Notion)
		}
		if cfg.ArchiveDir == "" || cfg.loc == nil {
			t.Fatalf("%s: Archivordner/Zeitzone nicht gesetzt", f)
		}
	}
}

func TestExampleConfigsIdentical(t *testing.T) {
	j, err := LoadConfig("config.example.json")
	if err != nil {
		t.Fatal(err)
	}
	y, err := LoadConfig("config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if j.loc.String() != y.loc.String() {
		t.Fatalf("Zeitzone verschieden: %v vs %v", j.loc, y.loc)
	}
	j.loc, y.loc = nil, nil
	if !reflect.DeepEqual(j, y) {
		t.Fatalf("Beispiele verschieden:\njson: %+v\nyaml: %+v", j, y)
	}
}

func TestConfigPathIn(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := configPathIn(dir); filepath.Base(got) != "config.json" {
		t.Fatalf("nur json: %s", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := configPathIn(dir); filepath.Base(got) != "config.yaml" {
		t.Fatalf("beide: %s", got)
	}
}

func TestInvalidYAML(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.YML")
	if err := os.WriteFile(p, []byte("watch_dir: [unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(p)
	if err == nil || !strings.Contains(err.Error(), "ungültiges YAML") {
		t.Fatalf("erwartet YAML-Fehler, bekam %v", err)
	}
}

func TestYAMLUnknownFieldIgnored(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte("watch_dir: /x\nfoo: 1\nllm:\n  model: m\nnotion:\n  notes_data_source_id: n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("unbekanntes Feld darf nicht scheitern: %v", err)
	}
	if cfg.WatchDir != "/x" {
		t.Fatalf("watch_dir = %q", cfg.WatchDir)
	}
}

func TestYAMLErrorsHaveNoValues(t *testing.T) {
	tests := []struct {
		name, yaml string
		zeile      string // exakte Klammer; leer: yaml-Lib nennt keine Zeile (Anker, Tag)
	}{
		{"Typfehler", "watch_dir: /x\nnotify: ntn_geh1\n", "(Zeile 2)"},
		{"doppelter Key", "watch_dir: /x\nnotify: true\nnotify: true\n", "(Zeile 3, 2)"},
		{"unbekannter Anker", "watch_dir: /x\nmy_name: *ntn_geh2\n", ""},
		{"expliziter Tag", "watch_dir: /x\nmax_attempts: !!int ntn_geh3\n", ""},
		{"Syntaxfehler", "watch_dir: /x\nmy_name: \"ntn_geh4\n", "(Zeile 2)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(p, []byte(tc.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadConfig(p)
			if err == nil {
				t.Fatal("Fehler erwartet")
			}
			msg := err.Error()
			if !strings.Contains(msg, "ungültiges YAML") || !strings.Contains(msg, tc.zeile) {
				t.Fatalf("erwartet YAML-Fehler mit %q, bekam %v", tc.zeile, err)
			}
			if strings.Contains(msg, "ntn_geh") || strings.Contains(msg, "`") {
				t.Fatalf("Wert im Fehlertext: %v", err)
			}
		})
	}
}

func TestYAMLErrorMessage(t *testing.T) {
	got := yamlErrorMessage("p", errors.New("line 2: a\nline 2: b\nline 5: c"))
	if want := "ungültiges YAML in p (Zeile 2, 5)"; got != want {
		t.Fatalf("doppelte Zeilen: %q, erwartet %q", got, want)
	}
	got = yamlErrorMessage("p", errors.New("irgendein Fehler ntn_geh5"))
	if want := "ungültiges YAML in p"; got != want {
		t.Fatalf("ohne Zeile: %q, erwartet %q", got, want)
	}
}
