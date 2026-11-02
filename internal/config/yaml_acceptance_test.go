package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeCfg(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

const minimalYAML = "watch_dir: /x\nllm:\n  model: m\nnotion:\n  notes_data_source_id: n\n"

// AC-2: Endung steuert das Format, Groß-/Kleinschreibung egal.
func TestAC2_ExtensionSelectsFormat(t *testing.T) {
	for _, name := range []string{"c.yaml", "c.yml", "c.YAML", "c.Yml"} {
		p := filepath.Join(t.TempDir(), name)
		writeCfg(t, p, minimalYAML)
		cfg, _, err := Load(p)
		if err != nil || cfg.LLM.Model != "m" {
			t.Fatalf("%s: err=%v cfg=%v", name, err, cfg)
		}
	}
	// Alles andere ist JSON: YAML-Inhalt in .json/.txt/ohne Endung muss scheitern.
	for _, name := range []string{"c.json", "c.txt", "config"} {
		p := filepath.Join(t.TempDir(), name)
		writeCfg(t, p, minimalYAML)
		_, _, err := Load(p)
		if err == nil || !strings.Contains(err.Error(), "ungültiges JSON") {
			t.Fatalf("%s: JSON-Fehler erwartet, bekam %v", name, err)
		}
		writeCfg(t, p, `{"watch_dir":"/x","llm":{"model":"m"},"notion":{"notes_data_source_id":"n"}}`)
		if _, _, err := Load(p); err != nil {
			t.Fatalf("%s: JSON muss laden: %v", name, err)
		}
	}
}

// AC-3: Teil-YAML behält geschachtelte Defaults.
func TestAC3_PartialYAMLKeepsNestedDefaults(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	writeCfg(t, p, "watch_dir: /x\nllm:\n  model: m\n  temperature: 0.9\nnotion:\n  notes_data_source_id: n\n  notes_properties:\n    title: Titel\n  tasks_properties:\n    due_date: Fällig\n")
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	d := Default()
	if cfg.LLM.Temperature != 0.9 || cfg.LLM.Model != "m" {
		t.Fatalf("gesetzte llm-Werte fehlen: %+v", cfg.LLM)
	}
	if cfg.LLM.BaseURL != d.LLM.BaseURL || cfg.LLM.TimeoutMinutes != d.LLM.TimeoutMinutes ||
		cfg.LLM.MaxChunkChars != d.LLM.MaxChunkChars || !cfg.LLM.StructuredOutput {
		t.Fatalf("llm-Defaults verloren: %+v", cfg.LLM)
	}
	if cfg.Notion.Notes.Title != "Titel" || cfg.Notion.Tasks.DueDate != "Fällig" {
		t.Fatalf("gesetzte Properties fehlen: %+v", cfg.Notion)
	}
	if cfg.Notion.Notes.Status != d.Notion.Notes.Status || cfg.Notion.Notes.StatusValue != "Inbox" ||
		cfg.Notion.Notes.Review != d.Notion.Notes.Review {
		t.Fatalf("notes_properties-Defaults verloren: %+v", cfg.Notion.Notes)
	}
	if cfg.Notion.Tasks.Title != "Name" || cfg.Notion.Tasks.Completed != "Completed" {
		t.Fatalf("tasks_properties-Defaults verloren: %+v", cfg.Notion.Tasks)
	}
	if !reflect.DeepEqual(cfg.Notion.Projects, d.Notion.Projects) {
		t.Fatalf("projects_properties-Defaults verloren: %+v", cfg.Notion.Projects)
	}
	if cfg.Notion.BaseURL != d.Notion.BaseURL || cfg.Notion.APIVersion != d.Notion.APIVersion || cfg.Notion.CreateTasksFor != "me" {
		t.Fatalf("notion-Defaults verloren: %+v", cfg.Notion)
	}
	if cfg.MaxAttempts != d.MaxAttempts || cfg.PairWindowSeconds != d.PairWindowSeconds || !cfg.Notify {
		t.Fatalf("Top-Level-Defaults verloren")
	}
}

// AC-4: Unbekannte Felder (auch geschachtelt) werden ignoriert.
func TestAC4_UnknownFieldsIgnored(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	writeCfg(t, p, minimalYAML+"unbekannt: 1\nliste: [a, b]\nllm2:\n  x: y\n")
	if _, _, err := Load(p); err != nil {
		t.Fatal(err)
	}
	writeCfg(t, p, "watch_dir: /x\nllm:\n  model: m\n  foo: bar\nnotion:\n  notes_data_source_id: n\n  notes_properties:\n    baz: 1\n")
	if _, _, err := Load(p); err != nil {
		t.Fatalf("geschachtelt: %v", err)
	}
}

// AC-5: Vorrang config.yaml vor config.json über HOME (Temp-HOME).
func TestAC5_DefaultPathPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".config", "meetingsync")
	if got := DefaultPath(); got != filepath.Join(dir, "config.json") {
		t.Fatalf("ohne Dateien: %s", got)
	}
	writeCfg(t, filepath.Join(dir, "config.json"), "{}")
	if got := DefaultPath(); got != filepath.Join(dir, "config.json") {
		t.Fatalf("nur json: %s", got)
	}
	writeCfg(t, filepath.Join(dir, "config.yaml"), "{}")
	if got := DefaultPath(); got != filepath.Join(dir, "config.yaml") {
		t.Fatalf("beide: %s", got)
	}
	// config.yml wird bewusst nicht automatisch gesucht (Kriterium nennt nur config.yaml).
	_ = os.Remove(filepath.Join(dir, "config.yaml"))
	writeCfg(t, filepath.Join(dir, "config.yml"), "{}")
	if got := DefaultPath(); got != filepath.Join(dir, "config.json") {
		t.Fatalf("yml: %s", got)
	}
}

// AC-7: Fehlertexte enthalten nur Pfad und optional Zeile, nie Dateiinhalt.
func TestAC7_YAMLErrorFormat(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	cases := []string{
		"my_name: [ntn_secret1\n",
		"notify: ntn_secret2\n",
		"llm:\n  temperature: ntn_secret3\n",
		"\tmy_name: ntn_secret4\n",
		"my_name: 'ntn_secret5\n",
		"notion:\n  token: ntn_secret6\n  token: ntn_secret6\n",
		"- ntn_secret7\n- x\n",
		"max_attempts: 99999999999999999999 # ntn_secret8\nnotify: ntn_secret8\n",
	}
	re := func(s string) bool {
		rest := strings.TrimPrefix(s, "ungültiges YAML in "+p)
		if rest == "" {
			return true
		}
		return strings.HasPrefix(rest, " (Zeile ") && strings.HasSuffix(rest, ")") &&
			strings.Trim(rest[len(" (Zeile "):len(rest)-1], "0123456789, ") == ""
	}
	for _, c := range cases {
		writeCfg(t, p, c)
		_, _, err := Load(p)
		if err == nil {
			t.Fatalf("Fehler erwartet für %q", c)
		}
		if strings.Contains(err.Error(), "ntn_secret") || !re(err.Error()) {
			t.Fatalf("Format verletzt für %q: %q", c, err.Error())
		}
	}
}
