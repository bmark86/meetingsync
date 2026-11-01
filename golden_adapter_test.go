package main

// Adapter für die Golden-Tests: Szenarien greifen nur über diese Datei auf den
// Produktionscode zu. M2-Tasks dürfen den Adapter anpassen, nicht aber die
// Szenarien oder die Golden-Dateien.

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// goldenMainEnv startet im Kindprozess main() statt der Tests (Re-Exec für CLI-Szenarien).
const goldenMainEnv = "MEETINGSYNC_GOLDEN_MAIN"

// goldenProxy ist ein Proxy, der nie antwortet: Requests an Hosts außer
// 127.0.0.1/localhost scheitern, statt ins echte Netz zu gehen.
const goldenProxy = "http://127.0.0.1:1"

// goldenProxyEnv gilt im Testprozess (TestMain) und in CLI-Kindprozessen (goldenEnv).
var goldenProxyEnv = []string{"HTTP_PROXY=" + goldenProxy, "HTTPS_PROXY=" + goldenProxy, "NO_PROXY=127.0.0.1,localhost"}

func TestMain(m *testing.M) {
	if os.Getenv(goldenMainEnv) == "1" {
		// Kindprozess: os.Args enthält Programm + CLI-Argumente. Eigenes FlagSet,
		// damit die test.*-Flags nicht in -h auftauchen und -update nicht stört.
		// Usage ruft wie bei flag.CommandLine flag.Usage auf (main setzt den Text).
		fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
		fs.Usage = func() { flag.Usage() }
		flag.CommandLine = fs
		main()
		os.Exit(0)
	}
	// Feste Prozess-Zeitzone für alle Tests im Paket, vor m.Run und damit vor
	// allen Goroutinen der Tests (race-frei). Nur die Config-Zeitzone soll zählen.
	time.Local = time.UTC
	// Netzschutz: http.DefaultTransport liest die Proxy-Variablen nur einmal,
	// beim ersten Request. Darum hier setzen, vor jedem Test.
	for _, kv := range goldenProxyEnv {
		k, v, _ := strings.Cut(kv, "=")
		if err := os.Setenv(k, v); err != nil {
			panic(err)
		}
	}
	os.Exit(m.Run())
}

// goldenApp ist die einzige Sicht der Szenarien auf die App.
type goldenApp struct{ app *App }

// goldenStart lädt die Config wie das Binary (LoadConfig), baut die App und
// ruft Init auf. Der Logger schreibt ohne Zeitstempel.
func goldenStart(ctx context.Context, cfgPath string, logger *log.Logger, dry bool) (*goldenApp, error) {
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		return nil, err
	}
	app, err := NewApp(cfg, logger, dry)
	if err != nil {
		return nil, err
	}
	if err := app.Init(ctx); err != nil {
		return nil, err
	}
	return &goldenApp{app: app}, nil
}

// Pass nutzt den vorhandenen Seam: now kommt vom Szenario.
func (g *goldenApp) Pass(ctx context.Context, now time.Time) { g.app.Pass(ctx, now) }

// goldenRunCLI startet main() per Re-Exec des Testbinaries (kein go build).
// env ersetzt die Umgebung vollständig. GOCOVERDIR kommt dazu (äußerer Wert,
// sonst ein Temp-Ordner), sonst warnt ein Coverage-Binary auf stderr.
func goldenRunCLI(t *testing.T, env []string, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	coverDir := os.Getenv("GOCOVERDIR")
	if coverDir == "" {
		coverDir = t.TempDir()
	}
	cmd := exec.Command(os.Args[0], args...) //nolint:gosec // G702: eigenes Testbinary, Argumente aus dem Szenario, keine Shell
	cmd.Env = append(append([]string{}, env...), goldenMainEnv+"=1", "GOCOVERDIR="+coverDir)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		exit = ee.ExitCode()
	default:
		t.Fatalf("Re-Exec fehlgeschlagen: %v", err)
	}
	return out.String(), errOut.String(), exit
}
