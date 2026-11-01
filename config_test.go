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

func loadWarn(t *testing.T, name, body string) []string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.warnings
}

// Fängt: Clamp ohne Warnung / falscher Schlüssel in der Warnung / Warnung ohne Clamp.
func TestConfigClampWarnings(t *testing.T) {
	tests := []struct {
		name, extra, want string
	}{
		{"poll", "poll_interval_seconds: 1\nllm:\n  model: m\n", "poll_interval_seconds=1 ist zu klein, verwende 2"},
		{"attempts", "max_attempts: 0\nllm:\n  model: m\n", "max_attempts=0 ist zu klein, verwende 1"},
		{"chunk", "llm:\n  model: m\n  max_chunk_chars: 100\n", "llm.max_chunk_chars=100 ist zu klein, verwende 5000"},
		{"timeout", "llm:\n  model: m\n  timeout_minutes: 0\n", "llm.timeout_minutes=0 ist zu klein, verwende 1"},
		{"timeout negativ", "llm:\n  model: m\n  timeout_minutes: -5\n", "llm.timeout_minutes=-5 ist zu klein, verwende 1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := "watch_dir: /x\nnotion:\n  notes_data_source_id: n\n" + tc.extra
			w := loadWarn(t, "config.yaml", body)
			if len(w) != 1 || w[0] != "Konfiguration: "+tc.want {
				t.Fatalf("warnings = %q, want genau %q", w, tc.want)
			}
		})
	}
}

// Fängt: Lauf ohne Timeout (0 bedeutet beim http.Client "kein Timeout").
func TestConfigTimeoutClampedToOne(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	body := "watch_dir: /x\nllm:\n  model: m\n  timeout_minutes: 0\nnotion:\n  notes_data_source_id: n\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.TimeoutMinutes != 1 {
		t.Fatalf("TimeoutMinutes = %d, want 1", cfg.LLM.TimeoutMinutes)
	}
}

// Fängt: unbekannte Schlüssel unbemerkt / Wert im Text / verschachtelte Pfade fehlen.
func TestConfigUnknownKeys(t *testing.T) {
	yamlBody := "watch_dir: /x\nfoo: ntn_geheim1\nllm:\n  model: m\n  modell: ntn_geheim2\nnotion:\n  notes_data_source_id: n\n  notes_properties:\n    titel: ntn_geheim3\n"
	jsonBody := `{"watch_dir":"/x","foo":"ntn_geheim1","llm":{"model":"m","modell":"ntn_geheim2"},` +
		`"notion":{"notes_data_source_id":"n","notes_properties":{"titel":"ntn_geheim3"}}}`
	want := []string{
		`Konfiguration: unbekannter Schlüssel "foo" wird ignoriert`,
		`Konfiguration: unbekannter Schlüssel "llm.modell" wird ignoriert`,
		`Konfiguration: unbekannter Schlüssel "notion.notes_properties.titel" wird ignoriert`,
	}
	for name, body := range map[string]string{"config.yaml": yamlBody, "config.json": jsonBody} {
		t.Run(name, func(t *testing.T) {
			w := loadWarn(t, name, body)
			if !reflect.DeepEqual(w, want) {
				t.Fatalf("warnings = %q, want %q", w, want)
			}
			if strings.Contains(strings.Join(w, "\n"), "ntn_geheim") {
				t.Fatalf("Wert aus der Datei in der Warnung: %q", w)
			}
		})
	}
}

// JSON ordnet Schlüssel ohne Beachtung der Schreibweise zu: kein Fehlalarm.
func TestConfigJSONKeyCaseNoWarning(t *testing.T) {
	w := loadWarn(t, "config.json", `{"Watch_Dir":"/x","llm":{"MODEL":"m"},"notion":{"notes_data_source_id":"n"}}`)
	if len(w) != 0 {
		t.Fatalf("warnings = %q, want keine", w)
	}
}

func TestConfigYAMLKeyCaseWarns(t *testing.T) {
	w := loadWarn(t, "config.yaml", "watch_dir: /x\nWatch_Dir: /y\nllm:\n  model: m\nnotion:\n  notes_data_source_id: n\n")
	if len(w) != 1 || !strings.Contains(w[0], `unbekannter Schlüssel "Watch_Dir"`) {
		t.Fatalf("warnings = %q, want eine Warnung für Watch_Dir", w)
	}
}

func TestConfigCLIWarningsOrder(t *testing.T) {
	home := t.TempDir()
	env := append([]string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}, goldenProxyEnv...)
	p := filepath.Join(t.TempDir(), "x.yaml")
	writeCfg(t, p, "watch_dir: "+filepath.Join(home, "w")+"\nllm:\n  model: m\nnotion:\n  token_keychain_service: \"\"\n  notes_data_source_id: n\nfoo: ntn_geheim\nmax_attempts: 0\n")
	stdout, stderr, _ := goldenRunCLI(t, env, "-config", p, "-check")
	out := stdout + stderr
	if strings.Contains(out, "ntn_geheim") {
		t.Fatalf("Wert in der Ausgabe:\n%s", out)
	}
	i := strings.Index(out, "Konfiguration: "+p+"\n")
	j := strings.Index(out, `unbekannter Schlüssel "foo"`)
	k := strings.Index(out, "max_attempts=0 ist zu klein, verwende 1")
	if i < 0 || j < 0 || k < 0 || i >= j || j >= k {
		t.Fatalf("Reihenfolge Pfad(%d) < unbekannt(%d) < zu klein(%d) verletzt:\n%s", i, j, k, out)
	}
}

func TestConfigNormalNoWarnings(t *testing.T) {
	for _, f := range []string{"config.example.json", "config.example.yaml"} {
		cfg, err := LoadConfig(f)
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.warnings) != 0 {
			t.Fatalf("%s: warnings = %q, want keine", f, cfg.warnings)
		}
	}
}
