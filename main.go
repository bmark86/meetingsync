package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"meetingsync/internal/cli"
	"meetingsync/internal/config"
	"meetingsync/internal/llm"
	"meetingsync/internal/notify"
	"meetingsync/internal/notion"
	"meetingsync/internal/secret"
)

// version wird beim Build per -ldflags gesetzt (siehe Makefile, Target build).
// Der Linker entfernt die ungenutzte Variable; sichtbar ist der Wert nur in den
// Build-Infos ("go version -m", ldflags-Eintrag). Lint-Ausnahme in .golangci.yml.
var version = "dev"

// meetingsync: MacWhisper-Export → lokales LLM (LM Studio) → Notion Second Brain.
func main() {
	cfgPath := flag.String("config", config.DefaultPath(), "Pfad zur Konfigurationsdatei")
	once := flag.Bool("once", false, "nur einen Durchlauf machen und beenden")
	dryRun := flag.Bool("dry-run", false, "nichts in Notion schreiben, keine Dateien verschieben, Ergebnis nur ausgeben")
	file := flag.String("file", "", "eine einzelne Datei verarbeiten (zum Testen, bleibt liegen)")
	check := flag.Bool("check", false, "Konfiguration, Notion-Zugriff und LM Studio prüfen")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "meetingsync – Meeting-Transkripte aus MacWhisper als Notizen nach Notion\n\nAufruf: meetingsync [Optionen]\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	logger := log.New(os.Stdout, "", log.LstdFlags)
	cfg, loc, warnings, err := loadConfig(*cfgPath)
	if err != nil {
		logger.Fatalf("Konfiguration: %v", err)
	}
	logger.Printf("Konfiguration: %s", *cfgPath)
	for _, w := range warnings {
		logger.Print(w)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	env, err := newEnv(cfg, loc, logger, *dryRun, nil)
	if err != nil {
		logger.Fatalf("%v", err)
	}
	if *check {
		if err := cli.Check(ctx, env); err != nil {
			os.Exit(1)
		}
		return
	}
	s, err := cli.Init(ctx, env)
	if err != nil {
		logger.Fatalf("Start fehlgeschlagen: %v", err)
	}
	p := cli.NewPipeline(env, s)
	switch {
	case *file != "":
		if err := cli.ProcessSingleFile(ctx, env, p, *file); err != nil {
			logger.Fatalf("%v", err)
		}
	case *once:
		p.Pass(ctx, time.Now())
	default:
		cli.Run(ctx, env, p)
	}
}

// loadConfig lädt die Config und löst ihre Zeitzone auf. Beide Fehler meldet
// main als Config-Fehler.
func loadConfig(path string) (*config.Config, *time.Location, []string, error) {
	cfg, warnings, err := config.Load(path)
	if err != nil {
		return nil, nil, nil, err
	}
	loc, err := loadLocation(cfg.Timezone)
	if err != nil {
		return nil, nil, nil, err
	}
	return cfg, loc, warnings, nil
}

// loadLocation löst die Zeitzone aus der Config auf. Fehlertext wie früher in
// der Config-Prüfung; main meldet ihn wie jeden Config-Fehler.
func loadLocation(tz string) (*time.Location, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		// Kein %w: der Fehler von time.LoadLocation nennt den Namen ("unknown time zone …").
		return nil, errors.New("unbekannte Zeitzone in timezone (IANA-Name erwartet, z. B. Europe/Berlin)")
	}
	return loc, nil
}

// notionTokens baut die Token-Quelle aus den Notion-Einstellungen der Config.
func notionTokens(cfg *config.Config) secret.TokenSource {
	return secret.NewNotion(cfg.Notion.TokenKeychainService, cfg.Notion.Token)
}

// newLLMClient baut den LM-Studio-Client aus der Konfiguration. transport nil
// heißt eine Kopie von http.DefaultTransport ohne Proxy, ohne Weiterleitungen zu folgen.
func newLLMClient(cfg *config.Config, logger *log.Logger, transport http.RoundTripper) *llm.Client {
	return llm.NewClient(llm.Options{
		BaseURL:          cfg.LLM.BaseURL,
		Model:            cfg.LLM.Model,
		APIKey:           cfg.LLM.APIKey,
		Temperature:      cfg.LLM.Temperature,
		Timeout:          time.Duration(cfg.LLM.TimeoutMinutes) * time.Minute,
		MaxChunkChars:    cfg.LLM.MaxChunkChars,
		StructuredOutput: cfg.LLM.StructuredOutput,
	}, logger, transport)
}

// newEnv verdrahtet die Clients für internal/cli: LM Studio, Mac-Mitteilung,
// Notion-Token und Notion. Ohne Token läuft nur der Dry-Run (ohne Notion).
// notionTransport nil heißt http.DefaultTransport; Tests setzen eigene Hooks.
func newEnv(cfg *config.Config, loc *time.Location, logger *log.Logger, dry bool, notionTransport http.RoundTripper) (cli.Env, error) {
	e := cli.Env{
		Cfg:      cfg,
		Loc:      loc,
		Dry:      dry,
		Log:      logger,
		LLM:      newLLMClient(cfg, logger, nil),
		Notifier: notify.New(),
	}
	token, err := notionTokens(cfg).Token()
	switch {
	case err == nil:
		e.Notion = notion.NewClient(cfg.Notion.BaseURL, cfg.Notion.APIVersion, token, notionTransport)
	case dry:
		logger.Printf("Hinweis: %v – Dry-Run läuft ohne Notion (keine Projektliste).", err)
	default:
		return cli.Env{}, err
	}
	return e, nil
}
