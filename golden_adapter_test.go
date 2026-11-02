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

	"meetingsync/internal/cli"
	"meetingsync/internal/config"
	"meetingsync/internal/ledger"
	"meetingsync/internal/pipeline"
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

// testApp ist die Sicht der Root-Tests (Golden, e2e, Ablauf) auf die
// Verdrahtung nach Init: dieselben Schritte wie main (cli.Init, cli.NewPipeline).
// Env wird vor dem Start angepasst, danach nicht mehr.
type testApp struct {
	env    cli.Env
	cfg    *config.Config // = env.Cfg
	loc    *time.Location // = env.Loc
	log    *log.Logger    // = env.Log, derselbe Logger wie in der Pipeline
	ledger *ledger.Ledger // Protokoll der Pipeline
	p      *pipeline.Pipeline
}

// newTestApp baut die Pipeline aus env und dem Ergebnis von Init.
func newTestApp(env cli.Env, s cli.Setup) *testApp {
	return &testApp{env: env, cfg: env.Cfg, loc: env.Loc, log: env.Log, ledger: s.Ledger, p: cli.NewPipeline(env, s)}
}

// startTestApp ruft Init auf und baut die Pipeline.
func startTestApp(ctx context.Context, env cli.Env) (*testApp, error) {
	s, err := cli.Init(ctx, env)
	if err != nil {
		return nil, err
	}
	return newTestApp(env, s), nil
}

// Pass nutzt den vorhandenen Seam: now kommt vom Test.
func (a *testApp) Pass(ctx context.Context, now time.Time) { a.p.Pass(ctx, now) }

// states ist der Wiederholungszustand der Pipeline.
func (a *testApp) states() map[string]*pipeline.FileState { return a.p.States() }

func (a *testApp) Run(ctx context.Context) { cli.Run(ctx, a.env, a.p) }

func (a *testApp) ProcessSingleFile(ctx context.Context, path string) error {
	return cli.ProcessSingleFile(ctx, a.env, a.p, path)
}

// goldenStart lädt die Config wie das Binary (loadConfig), verdrahtet wie main
// (newEnv) und ruft Init auf. Der Logger schreibt ohne Zeitstempel.
func goldenStart(ctx context.Context, cfgPath string, logger *log.Logger, dry bool) (*testApp, error) {
	cfg, loc, _, err := loadConfig(cfgPath)
	if err != nil {
		return nil, err
	}
	env, err := newEnv(cfg, loc, logger, dry, nil)
	if err != nil {
		return nil, err
	}
	env.Notifier = &fakeNotifier{} // nie das echte osascript
	return startTestApp(ctx, env)
}

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
