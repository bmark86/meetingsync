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
)

// meetingsync: MacWhisper-Export → lokales LLM (LM Studio) → Notion Second Brain.
func main() {
	cfgPath := flag.String("config", defaultConfigPath(), "Pfad zur Konfigurationsdatei")
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
	cfg, err := LoadConfig(*cfgPath)
	if err != nil {
		logger.Fatalf("Konfiguration: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	app, err := NewApp(cfg, logger, *dryRun)
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
