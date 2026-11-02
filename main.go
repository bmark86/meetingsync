package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"meetingsync/internal/config"
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
	cfg, warnings, err := config.Load(*cfgPath)
	if err != nil {
		logger.Fatalf("Konfiguration: %v", err)
	}
	loc, err := loadLocation(cfg.Timezone)
	if err != nil {
		logger.Fatalf("Konfiguration: %v", err)
	}
	logger.Printf("Konfiguration: %s", *cfgPath)
	for _, w := range warnings {
		logger.Print(w)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	app, err := NewApp(cfg, loc, notionTokens(cfg), logger, *dryRun)
	if err != nil {
		logger.Fatalf("%v", err)
	}
	if *check {
		if err := app.Check(ctx); err != nil {
			os.Exit(1)
		}
		return
	}
	if err := app.Init(ctx); err != nil {
		logger.Fatalf("Start fehlgeschlagen: %v", err)
	}
	switch {
	case *file != "":
		if err := app.ProcessSingleFile(ctx, *file); err != nil {
			logger.Fatalf("%v", err)
		}
	case *once:
		app.Pass(ctx, time.Now())
	default:
		app.Run(ctx)
	}
}

// loadLocation löst die Zeitzone aus der Config auf. Fehlertext wie früher in
// der Config-Prüfung; main meldet ihn wie jeden Config-Fehler.
func loadLocation(tz string) (*time.Location, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("unbekannte Zeitzone %q: %w", tz, err)
	}
	return loc, nil
}

// notionTokens baut die Token-Quelle aus den Notion-Einstellungen der Config.
func notionTokens(cfg *config.Config) secret.TokenSource {
	return secret.NewNotion(cfg.Notion.TokenKeychainService, cfg.Notion.Token)
}
