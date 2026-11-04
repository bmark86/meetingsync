package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Beispiel-Configs liegen im Modul-Root.
var examples = []string{"../../config.example.json", "../../config.example.yaml"}

func TestExampleConfigLoads(t *testing.T) {
	for _, f := range examples {
		cfg, _, err := Load(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if cfg.Notion.Notes.StatusValue != "Inbox" || cfg.Notion.Tasks.DueDate != "Due Date" || !cfg.LLM.StructuredOutput {
			t.Fatalf("%s: Standardwerte gehen beim Laden verloren: %+v", f, cfg.Notion)
		}
		// Die Zeitzone löst main auf (R-M2-07); hier: gesetzt und auflösbar.
		if _, err := time.LoadLocation(cfg.Timezone); cfg.ArchiveDir == "" || cfg.Timezone == "" || err != nil {
			t.Fatalf("%s: Archivordner/Zeitzone nicht gesetzt (%v)", f, err)
		}
	}
}

func TestExampleConfigsIdentical(t *testing.T) {
	j, _, err := Load(examples[0])
	if err != nil {
		t.Fatal(err)
	}
	y, _, err := Load(examples[1])
	if err != nil {
		t.Fatal(err)
	}
	if j.Timezone != y.Timezone {
		t.Fatalf("Zeitzone verschieden: %v vs %v", j.Timezone, y.Timezone)
	}
	if !reflect.DeepEqual(j, y) {
		t.Fatalf("Beispiele verschieden:\njson: %+v\nyaml: %+v", j, y)
	}
}

// Fängt: neutrale Defaults fehlen oder ein leerer example_name erreicht den Prompt.
func TestRoleAndExampleNameDefaults(t *testing.T) {
	d := Default()
	if d.MyRole != "" || d.ExampleName != "Alex" {
		t.Fatalf("Defaults: role=%q name=%q", d.MyRole, d.ExampleName)
	}
	dir := t.TempDir()
	write := func(body string) *Config {
		t.Helper()
		body += "llm:\n  model: m\nnotion:\n  notes_data_source_id: n\n"
		p := filepath.Join(dir, "c.yaml")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, _, err := Load(p)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	if c := write("watch_dir: /x\nmy_role: Teamlead\nexample_name: Kim\n"); c.MyRole != "Teamlead" || c.ExampleName != "Kim" {
		t.Errorf("gesetzte Werte: %q %q", c.MyRole, c.ExampleName)
	}
	if c := write("watch_dir: /x\nexample_name: \"\"\n"); c.ExampleName != "Alex" {
		t.Errorf("leerer example_name: %q", c.ExampleName)
	}
	if c := write("watch_dir: /x\n"); c.MyRole != "" || c.ExampleName != "Alex" {
		t.Errorf("ohne Angabe: %q %q", c.MyRole, c.ExampleName)
	}
}

// Fängt: notes_properties.source wird nicht gelesen, als unbekannt gemeldet,
// verdrängt andere Defaults oder ist ohne Angabe gesetzt (R-M3-17).
func TestNotesSourceProperty(t *testing.T) {
	if Default().Notion.Notes.Source != "" {
		t.Fatalf("Standard muss leer sein (aus): %q", Default().Notion.Notes.Source)
	}
	dir := t.TempDir()
	for name, body := range map[string]string{
		"c.yaml": "watch_dir: /x\nllm:\n  model: m\nnotion:\n  notes_data_source_id: n\n  notes_properties:\n    source: Quelle\n",
		"c.json": `{"watch_dir":"/x","llm":{"model":"m"},"notion":{"notes_data_source_id":"n","notes_properties":{"source":"Quelle"}}}`,
	} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, warnings, err := Load(p)
		if err != nil || len(warnings) != 0 {
			t.Fatalf("%s: %v, Warnungen %v", name, err, warnings)
		}
		if cfg.Notion.Notes.Source != "Quelle" || cfg.Notion.Notes.Title != "Name" || cfg.Notion.Notes.Review != "Prüfen" {
			t.Fatalf("%s: %+v", name, cfg.Notion.Notes)
		}
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
	_, _, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "ungültiges YAML") {
		t.Fatalf("erwartet YAML-Fehler, bekam %v", err)
	}
}

func TestYAMLUnknownFieldIgnored(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte("watch_dir: /x\nfoo: 1\nllm:\n  model: m\nnotion:\n  notes_data_source_id: n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(p)
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
			_, _, err := Load(p)
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
	_, warnings, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	return warnings
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
	cfg, _, err := Load(p)
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

func TestConfigNormalNoWarnings(t *testing.T) {
	for _, f := range examples {
		_, warnings, err := Load(f)
		if err != nil {
			t.Fatal(err)
		}
		if len(warnings) != 0 {
			t.Fatalf("%s: warnings = %q, want keine", f, warnings)
		}
	}
}

// Fängt: ungültige Werte landen mit %q im Fehlertext (S-17). Der Fehler nennt
// den Schlüssel und die erlaubten Werte, aber nicht den konfigurierten Wert.
func TestInvalidEnumErrorsHaveNoValues(t *testing.T) {
	const secret = "GEHEIM-xyz"
	cases := []struct {
		name, body, key string
	}{
		{"orphan_summary_action", "watch_dir: /w\norphan_summary_action: " + secret + "\n", "orphan_summary_action"},
		{"create_tasks_for", "watch_dir: /w\nnotion:\n  create_tasks_for: " + secret + "\n", "create_tasks_for"},
	}
	for _, tc := range cases {
		p := filepath.Join(t.TempDir(), "x.yaml")
		writeCfg(t, p, tc.body)
		_, _, err := Load(p)
		if err == nil {
			t.Fatalf("%s: Fehler erwartet", tc.name)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%s: Wert im Fehler: %v", tc.name, err)
		}
		if !strings.Contains(err.Error(), tc.key) {
			t.Errorf("%s: Schlüssel fehlt im Fehler: %v", tc.name, err)
		}
	}
}

// Fängt: Transkript geht an einen fremden Host (S-02, D3) / URL-Wert im
// Fehlertext (S-17).
func TestLLMBaseURLLoopbackOnly(t *testing.T) {
	cases := []struct {
		url string
		ok  bool
	}{
		{"http://localhost:1234/v1", true},
		{"http://LOCALHOST:1234/v1", false},
		{"http://Localhost:1234/v1", false},
		{"http://localhoſt:1234", false},
		{"http://127.0.0.1:1234/v1", true},
		{"http://[::1]:1234", true},
		{"https://localhost:1234/v1", true},
		{"http://192.168.1.5:1234", false},
		{"http://localhost.evil.com", false},
		{"http://evil.com#@localhost", false},
		{"http://user:pw@localhost", false},
		{"http://@localhost:1234", false},
		{"ftp://localhost", false},
		{"localhost:1234", false},
		{"http://", false},
		{"http:///v1", false},
		{"", false},
		{"http://127.0.0.2", false},
		{"http://0.0.0.0", false},
		{"http://[::1%25en0]:1234", false},
		{"http://localhost:1234\x7f", false},
		{"http://localhost:1234/v1?key=geheim", false},
		{"http://localhost:1234/v1#x", false},
		{"http://localhost:1234/v1?", false},
	}
	const want = "llm.base_url muss auf diesen Mac zeigen (localhost, 127.0.0.1 oder ::1)"
	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			c := Default()
			c.WatchDir, c.LLM.Model, c.Notion.NotesDataSourceID = "/w", "m", "n"
			c.LLM.BaseURL = tc.url
			_, err := c.finalize()
			if tc.ok {
				if err != nil {
					t.Fatalf("Fehler = %v, want keiner", err)
				}
				return
			}
			if err == nil || err.Error() != want {
				t.Fatalf("Fehler = %v, want %q", err, want)
			}
			if tc.url != "" && strings.Contains(err.Error(), tc.url) {
				t.Fatalf("URL-Wert im Fehler: %v", err)
			}
		})
	}
}

// Die Standard-URL muss die Prüfung bestehen.
func TestLLMBaseURLDefaultPasses(t *testing.T) {
	if !isLoopbackURL(Default().LLM.BaseURL) {
		t.Fatal("Standard-llm.base_url wird abgelehnt")
	}
}

// Fängt: Notion-Token im Klartext oder Zugangsdaten in notion.base_url (S-03) /
// URL-Wert im Fehlertext (S-17).
func TestNotionBaseURLHTTPSOnly(t *testing.T) {
	const (
		schema = "notion.base_url muss https verwenden (http nur für localhost)"
		user   = "notion.base_url darf keinen Benutzernamen und kein Passwort enthalten"
	)
	cases := []struct {
		url  string
		want string // "" = ok
	}{
		{"https://api.notion.com", ""},
		{"https://api.notion.com/", ""},
		{"http://localhost:8080", ""},
		{"http://127.0.0.1:8080/", ""},
		{"http://[::1]:8080", ""},
		{"https://127.0.0.1:8443", ""},
		{"http://api.notion.com", schema},
		{"http://LOCALHOST:8080", schema},
		{"http://127.0.0.2", schema},
		{"HTTP://api.notion.com", schema},
		{"https://user:pw@api.notion.com", user},
		{"https://user@api.notion.com", user},
		{"http://user:pw@localhost", user},
		{"https://@api.notion.com", user},
		{"ftp://api.notion.com", schema},
		{"api.notion.com", schema},
		{"https:api.notion.com", schema},
		{"https://", schema},
		{"https:///v1", schema},
		{"", schema},
		{"https://api.notion.com\x7f", schema},
		{"https://api.notion.com?x=1", schema},
		{"https://api.notion.com/#frag", schema},
		{"https://api.notion.com/?", schema},
		{"http://localhost:8080/?x=1", schema},
	}
	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			c := Default()
			c.WatchDir, c.LLM.Model, c.Notion.NotesDataSourceID = "/w", "m", "n"
			c.Notion.BaseURL = tc.url
			_, err := c.finalize()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Fehler = %v, want keiner", err)
				}
				return
			}
			if err == nil || err.Error() != tc.want {
				t.Fatalf("Fehler = %v, want %q", err, tc.want)
			}
			if tc.url != "" && strings.Contains(err.Error(), tc.url) {
				t.Fatalf("URL-Wert im Fehler: %v", err)
			}
			for _, geheim := range []string{"pw", "user", "api.notion.com"} {
				if strings.Contains(err.Error(), geheim) {
					t.Fatalf("Teil der URL (%q) im Fehler: %v", geheim, err)
				}
			}
		})
	}
}

// Die Standard-URL muss die Prüfung bestehen.
func TestNotionBaseURLDefaultPasses(t *testing.T) {
	if err := checkNotionURL(Default().Notion.BaseURL); err != nil {
		t.Fatalf("Standard-notion.base_url wird abgelehnt: %v", err)
	}
}
