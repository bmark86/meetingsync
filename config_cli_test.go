package main

// CLI-Tests zur Config (Re-Exec des Testbinaries); die Lade-Logik selbst
// testet internal/config.

import (
	"os"
	"path/filepath"
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

// AC-8: Binary loggt den Pfad nach erfolgreichem Laden; kein Netz, kein echtes HOME.
func TestAC8_BinaryLogsConfigPath(t *testing.T) {
	home := t.TempDir()
	// ohne NOTION_TOKEN; Proxy-Schutz gegen echtes Netz
	env := append([]string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}, goldenProxyEnv...)
	run := func(args ...string) string {
		// -check ohne Token endet mit Exit 1, ohne Netzzugriff
		stdout, stderr, _ := goldenRunCLI(t, env, args...)
		return stdout + stderr
	}
	body := "watch_dir: " + filepath.Join(home, "w") + "\nllm:\n  model: m\nnotion:\n  token_keychain_service: \"\"\n  notes_data_source_id: n\n"

	// explizit per -config
	p := filepath.Join(t.TempDir(), "x.yaml")
	writeCfg(t, p, body)
	if out := run("-config", p, "-check"); !strings.Contains(out, "Konfiguration: "+p+"\n") {
		t.Fatalf("Pfadlog fehlt:\n%s", out)
	}
	// Default: config.yaml hat Vorrang vor config.json
	dir := filepath.Join(home, ".config", "meetingsync")
	writeCfg(t, filepath.Join(dir, "config.json"), `{"watch_dir":"/x","llm":{"model":"m"},"notion":{"token_keychain_service":"","notes_data_source_id":"n"}}`)
	if out := run("-check"); !strings.Contains(out, "Konfiguration: "+filepath.Join(dir, "config.json")) {
		t.Fatalf("json-Default:\n%s", out)
	}
	writeCfg(t, filepath.Join(dir, "config.yaml"), body)
	if out := run("-check"); !strings.Contains(out, "Konfiguration: "+filepath.Join(dir, "config.yaml")) {
		t.Fatalf("yaml-Vorrang:\n%s", out)
	}
	// Fehlerfall: kein Erfolgs-Log
	writeCfg(t, p, "notify: [x\n")
	out := run("-config", p, "-check")
	if strings.Contains(out, "Konfiguration: "+p+"\n") || !strings.Contains(out, "ungültiges YAML in "+p) {
		t.Fatalf("Fehlerfall:\n%s", out)
	}
}

// Fängt: anderer Fehlertext bei unbekannter Zeitzone, seit main sie auflöst (R-M2-07).
func TestLoadLocation(t *testing.T) {
	loc, err := loadLocation("Europe/Berlin")
	if err != nil || loc.String() != "Europe/Berlin" {
		t.Fatalf("loc = %v, err = %v", loc, err)
	}
	_, err = loadLocation("GEHEIM-xyz")
	if err == nil || !strings.Contains(err.Error(), "timezone") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "GEHEIM-xyz") {
		t.Fatalf("Zeitzonenwert im Fehler: %v", err)
	}
}

// Fängt: Zeitzonenfehler nicht mehr als Config-Fehler vor dem Pfad-Log.
func TestConfigCLIBadTimezone(t *testing.T) {
	home := t.TempDir()
	env := append([]string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}, goldenProxyEnv...)
	p := filepath.Join(t.TempDir(), "x.yaml")
	writeCfg(t, p, "watch_dir: "+filepath.Join(home, "w")+"\ntimezone: Nirgendwo/Stadt\nfoo: 1\nllm:\n  model: m\nnotion:\n  token_keychain_service: \"\"\n  notes_data_source_id: n\n")
	stdout, stderr, _ := goldenRunCLI(t, env, "-config", p, "-check")
	out := stdout + stderr
	if !strings.Contains(out, "Konfiguration: unbekannte Zeitzone in timezone (") ||
		strings.Contains(out, "Nirgendwo/Stadt") ||
		strings.Contains(out, "Konfiguration: "+p+"\n") || strings.Contains(out, "unbekannter Schlüssel") {
		t.Fatalf("Ausgabe:\n%s", out)
	}
}
