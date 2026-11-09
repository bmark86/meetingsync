package main

// QA R-M2-10: Exit-Code-Pfade von main() per Re-Exec des Testbinaries.
// Nur Temp-Verzeichnisse, kein Schlüsselbund (token_keychain_service leer), kein Netz.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func exitQACfg(t *testing.T, home, extra string) string {
	t.Helper()
	w := filepath.Join(home, "w")
	if err := os.MkdirAll(w, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "x.yaml")
	writeCfg(t, p, "watch_dir: "+w+"\nllm:\n  model: m\nnotion:\n  token_keychain_service: \"\"\n  notes_data_source_id: n\n"+extra)
	return p
}

func exitQAEnv(home string, more ...string) []string {
	return append(append([]string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}, goldenProxyEnv...), more...)
}

// Fängt: Config-/Zeitzonenfehler endet nicht mit Exit 1 (ohne -check).
func TestExitQAConfigAndTimezoneErrors(t *testing.T) {
	home := t.TempDir()
	bad := filepath.Join(t.TempDir(), "y.yaml")
	writeCfg(t, bad, "notify: [x\n")
	_, _, exit := goldenRunCLI(t, exitQAEnv(home), "-config", bad, "-once")
	if exit != 1 {
		t.Fatalf("YAML-Fehler: exit = %d", exit)
	}
	tz := exitQACfg(t, home, "timezone: Nirgendwo/Stadt\n")
	stdout, _, exit := goldenRunCLI(t, exitQAEnv(home), "-config", tz, "-once")
	if exit != 1 || !strings.Contains(stdout, "Konfiguration: unbekannte Zeitzone in timezone (") || strings.Contains(stdout, "Nirgendwo/Stadt") {
		t.Fatalf("Zeitzone: exit = %d\n%s", exit, stdout)
	}
}

// Fängt: fehlendes Token im Normalmodus läuft weiter / zeigt den Dry-Run-Hinweis;
// im Dry-Run fehlt der Hinweis oder der Lauf bricht ab.
func TestExitQATokenMissingNormalVsDryRun(t *testing.T) {
	home := t.TempDir()
	cfg := exitQACfg(t, home, "")
	const hint = "Dry-Run läuft ohne Notion"

	stdout, _, exit := goldenRunCLI(t, exitQAEnv(home), "-config", cfg, "-once")
	if exit != 1 || strings.Contains(stdout, hint) || !strings.Contains(stdout, "Konfiguration: "+cfg) {
		t.Fatalf("normal: exit = %d\n%s", exit, stdout)
	}
	stdout, _, exit = goldenRunCLI(t, exitQAEnv(home), "-config", cfg, "-dry-run", "-once")
	if exit != 0 || !strings.Contains(stdout, hint) {
		t.Fatalf("dry-run: exit = %d\n%s", exit, stdout)
	}
}

// -check mit Init-Fehler (404) und ✗ LM Studio decken golden G10d/G10b ab.
// Fängt: -check ohne Token liefert Exit 0 statt 1 (normal: Abbruch in newEnv;
// mit -dry-run: Abbruch in Check nach dem Ordnerbericht).
func TestExitQACheckNoToken(t *testing.T) {
	home := t.TempDir()
	cfg := exitQACfg(t, home, "")
	stdout, _, exit := goldenRunCLI(t, exitQAEnv(home), "-config", cfg, "-check")
	if exit != 1 || !strings.Contains(stdout, "kein Notion-Token") || strings.Contains(stdout, "Ordner:   ") {
		t.Fatalf("normal: exit = %d\n%s", exit, stdout)
	}
	stdout, _, exit = goldenRunCLI(t, exitQAEnv(home), "-config", cfg, "-dry-run", "-check")
	if exit != 1 || !strings.Contains(stdout, "Ordner:   ") || !strings.Contains(stdout, "Dry-Run läuft ohne Notion") {
		t.Fatalf("dry-run: exit = %d\n%s", exit, stdout)
	}
}

// Fängt: Init-Fehler ohne -check (Ledger-Pfad ist eine Datei) endet nicht mit Exit 1.
func TestExitQAStartFailed(t *testing.T) {
	home := t.TempDir()
	blocker := filepath.Join(home, "arch")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := exitQACfg(t, home, "archive_dir: "+blocker+"\n")
	stdout, _, exit := goldenRunCLI(t, exitQAEnv(home), "-config", cfg, "-dry-run", "-once")
	if exit != 1 || !strings.Contains(stdout, "Start fehlgeschlagen: ") {
		t.Fatalf("exit = %d\n%s", exit, stdout)
	}
}
