// Package cli sind die Einstiegspunkte des Programms: Init (Notion-Datenbanken
// prüfen, Protokoll laden), Bau der Pipeline, Check, Run, -file und die
// Dry-Run-Ausgabe. main baut Token und Mitteilung (os/exec, Regel in
// arch_test.go) sowie die Clients und reicht sie per Env herein.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"meetingsync/internal/config"
	"meetingsync/internal/domain"
	"meetingsync/internal/ledger"
	"meetingsync/internal/llm"
	"meetingsync/internal/notion"
	"meetingsync/internal/pipeline"
	"meetingsync/internal/transcript"
	"meetingsync/internal/watch"
)

// Notion ist der Teil des Notion-Clients, den Init, Check und die Pipeline
// brauchen.
type Notion interface {
	pipeline.Notion
	ResolveDataSource(ctx context.Context, id string) (string, notion.Schema, error)
}

// LLM ist der LM-Studio-Client: Zusammenfassen (Pipeline) und Modellliste (Check).
type LLM interface {
	llm.Summarizer
	Models(ctx context.Context) ([]string, error)
}

// Env sind Konfiguration und die von main gebauten Abhängigkeiten.
type Env struct {
	Cfg      *config.Config
	Loc      *time.Location // Zeitzone aus Cfg.Timezone, von main aufgelöst
	Dry      bool
	Log      *log.Logger
	LLM      LLM
	Notion   Notion // nil: kein Token (nur im Dry-Run, ohne Projektliste)
	Notifier pipeline.Notifier
	Mover    watch.Mover // Nullwert: echtes Verschieben
}

// Setup ist das Ergebnis von Init: Protokoll und aufgelöste Datenbanken.
type Setup struct {
	Ledger                                *ledger.Ledger
	NotesDS, TasksDS, ProjectsDS          string
	NotesTypes, TasksTypes, ProjectsTypes notion.Schema
}

// transcriptOpts reicht die konfigurierten Endungen an internal/transcript.
func transcriptOpts(c *config.Config) transcript.Options {
	return transcript.Options{TranscriptExtensions: c.TranscriptExtensions, SummaryExtensions: c.SummaryExtensions}
}

// Init prüft die Notion-Datenbanken und lädt das Verarbeitungsprotokoll.
func Init(ctx context.Context, e Env) (Setup, error) {
	var s Setup
	l, err := ledger.Load(filepath.Join(e.Cfg.ArchiveDir, ".meetingsync-ledger.jsonl"), !e.Dry)
	if err != nil {
		return Setup{}, err
	}
	s.Ledger = l
	if e.Notion == nil {
		return s, nil
	}

	nc := e.Cfg.Notion
	if s.NotesDS, s.NotesTypes, err = e.Notion.ResolveDataSource(ctx, nc.NotesDataSourceID); err != nil {
		return Setup{}, fmt.Errorf("Notes-Datenbank nicht lesbar: %w", err)
	}
	if err := s.NotesTypes.CheckNotes(notion.NotesProps(nc.Notes)); err != nil {
		return Setup{}, err
	}
	if !s.NotesTypes.Is(nc.Notes.Review, notion.KindCheckbox) {
		e.Log.Printf("Hinweis: Notes hat keine Checkbox %q – Prüf-Hinweise stehen nur als Hinweisbox oben auf der Seite.", nc.Notes.Review)
	}

	if nc.CreateTasksFor != "none" && nc.TasksDataSourceID != "" {
		if s.TasksDS, s.TasksTypes, err = e.Notion.ResolveDataSource(ctx, nc.TasksDataSourceID); err != nil {
			return Setup{}, fmt.Errorf("Tasks-Datenbank nicht lesbar: %w", err)
		}
		if err := s.TasksTypes.CheckTasks(notion.TasksProps(nc.Tasks)); err != nil {
			return Setup{}, err
		}
	}
	if nc.ProjectsDataSourceID != "" {
		if s.ProjectsDS, s.ProjectsTypes, err = e.Notion.ResolveDataSource(ctx, nc.ProjectsDataSourceID); err != nil {
			e.Log.Printf("Hinweis: Projects-Datenbank nicht lesbar (%v) – Projektvorschläge sind aus.", err)
			s.ProjectsDS = ""
		}
	}
	return s, nil
}

// NewPipeline baut die Pipeline aus Env und dem Ergebnis von Init. Die Uhr
// (time.Now) kommt von hier, nicht aus der Pipeline.
func NewPipeline(e Env, s Setup) *pipeline.Pipeline {
	c := e.Cfg
	return pipeline.New(pipeline.Options{
		WatchDir:            c.WatchDir,
		ArchiveDir:          c.ArchiveDir,
		FailedDir:           c.FailedDir,
		Stable:              time.Duration(c.StableSeconds) * time.Second,
		PairWindow:          time.Duration(c.PairWindowSeconds) * time.Second,
		OrphanWait:          time.Duration(c.OrphanSummaryMinutes) * time.Minute,
		OrphanAction:        c.OrphanSummaryAction,
		MaxAttempts:         c.MaxAttempts,
		Transcript:          transcriptOpts(c),
		Loc:                 e.Loc,
		Dry:                 e.Dry,
		MyName:              c.MyName,
		MyRole:              c.MyRole,
		ExampleName:         c.ExampleName,
		MyLabel:             c.MySpeakerLabel,
		UseSummaryAsContext: c.UseSummaryAsContext,
		IncludeTranscript:   c.IncludeTranscript,
		Notify:              c.Notify,
		Model:               c.LLM.Model,
		TitleFormat:         c.TitleFormat,
		CreateTasksFor:      c.Notion.CreateTasksFor,
		Notes:               notion.NotesProps(c.Notion.Notes),
		Tasks:               notion.TasksProps(c.Notion.Tasks),
		Projects:            notion.ProjectsProps(c.Notion.Projects),
		NotesDS:             s.NotesDS,
		TasksDS:             s.TasksDS,
		ProjectsDS:          s.ProjectsDS,
		NotesTypes:          s.NotesTypes,
		TasksTypes:          s.TasksTypes,
		ProjectsTypes:       s.ProjectsTypes,
	}, pipeline.Deps{
		Now:      time.Now,
		FS:       watch.OS{},
		Mover:    e.Mover,
		LLM:      e.LLM,
		Notion:   e.Notion, // nil bleibt nil (Interface-Umwandlung)
		Notifier: e.Notifier,
		Ledger:   s.Ledger,
		Log:      e.Log,
		DryRun:   printDryRun,
	})
}

// errCheckFailed meldet, dass mindestens eine Prüfzeile ✗ zeigt (Exit-Code 1).
var errCheckFailed = errors.New("Prüfung fehlgeschlagen")

// Check prüft Notion und LM Studio und gibt einen kurzen Bericht aus.
func Check(ctx context.Context, e Env) error {
	c := e.Cfg
	fmt.Printf("Ordner:   %s\nArchiv:   %s\nFehler:   %s\n", c.WatchDir, c.ArchiveDir, c.FailedDir)
	failed := false
	if _, err := os.Stat(c.WatchDir); err != nil {
		fmt.Printf("✗ Ordner nicht gefunden: %v\n", err)
		failed = true
	}
	if e.Notion == nil {
		return errors.New("kein Notion-Token gefunden")
	}
	s, err := Init(ctx, e)
	if err != nil {
		fmt.Printf("✗ Notion: %v\n", err)
		return err
	}
	fmt.Printf("✓ Notion: Notes erreichbar (%s)\n", s.NotesDS)
	if s.TasksDS != "" {
		fmt.Printf("✓ Notion: Tasks erreichbar (%s)\n", s.TasksDS)
	}
	if s.ProjectsDS != "" {
		p := NewPipeline(e, s).ActiveProjects(ctx)
		fmt.Printf("✓ Notion: %d aktive Projekte\n", len(p))
	}
	models, err := e.LLM.Models(ctx)
	if err != nil {
		fmt.Printf("✗ LM Studio nicht erreichbar unter %s: %v\n", domain.RedactURL(c.LLM.BaseURL), err)
		return errCheckFailed
	}
	if slices.Contains(models, c.LLM.Model) {
		fmt.Printf("✓ LM Studio: Modell %q verfügbar\n", c.LLM.Model)
	} else {
		fmt.Printf("✗ LM Studio: Modell %q nicht gefunden. Verfügbar:\n", c.LLM.Model)
		for _, m := range models {
			fmt.Printf("    %s\n", m)
		}
		failed = true
	}
	if failed {
		return errCheckFailed
	}
	return nil
}

// Run überwacht den Exportordner: ein Durchlauf sofort, dann im Takt von
// poll_interval_seconds, bis ctx endet.
func Run(ctx context.Context, e Env, p *pipeline.Pipeline) {
	e.Log.Printf("Überwache %s (alle %ds). Beenden mit Ctrl+C.", e.Cfg.WatchDir, e.Cfg.PollIntervalSeconds)
	ticker := time.NewTicker(time.Duration(e.Cfg.PollIntervalSeconds) * time.Second)
	defer ticker.Stop()
	p.Pass(ctx, time.Now())
	for {
		select {
		case <-ctx.Done():
			e.Log.Printf("Beendet.")
			return
		case <-ticker.C:
			p.Pass(ctx, time.Now())
		}
	}
}

// lockPoll: so oft prüft -file, ob der laufende Durchlauf die Sperre freigibt.
var lockPoll = time.Second

// ProcessSingleFile verarbeitet eine Datei direkt (zum Testen). Die Datei
// bleibt liegen. Außer im Dry-Run trägt es sie ins Protokoll ein (R-M3-14) und
// hält dafür die Sperre auf den Exportordner, schon vor dem Lesen der Datei;
// ein laufender Durchlauf des Dienstes wird abgewartet. Liegt die Datei im Exportordner, archiviert sie
// der Dienst danach als schon verarbeitet.
func ProcessSingleFile(ctx context.Context, e Env, p *pipeline.Pipeline, path string) error {
	path = config.ExpandHome(path)
	if !e.Dry {
		lk, err := ledger.WaitLock(ctx, e.Cfg.WatchDir, lockPoll, func() {
			e.Log.Printf("Dienst verarbeitet gerade den Exportordner – warte, bis der Durchlauf fertig ist …")
		})
		switch {
		case err == nil:
			defer func() { _ = lk.Unlock() }() //nolint:errcheck // Schließen gibt den flock frei, Fehler ohne Folgen
		case !errors.Is(err, os.ErrNotExist):
			return err
		}
		// ohne Exportordner läuft kein Durchlauf, die Sperre ist nicht nötig.
		// Erst danach lesen: hat der Dienst die Datei inzwischen archiviert,
		// fehlt sie hier, statt sie ein zweites Mal anzulegen.
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	content, err := os.ReadFile(path) //nolint:gosec // G304: Pfad gibt der Nutzer bewusst mit -file an (Testaufruf, auch außerhalb des Exportordners)
	if err != nil {
		return err
	}
	f := &watch.Entry{Path: path, Name: filepath.Base(path), ModTime: info.ModTime(), Size: info.Size(), Stable: true, Content: content}
	f.Kind = transcript.Classify(path, content, transcriptOpts(e.Cfg))
	e.Log.Printf("%s erkannt als: %s", f.Name, f.Kind)
	if f.Kind == domain.KindUnsupported {
		return fmt.Errorf("Dateityp %s wird nicht unterstützt", filepath.Ext(path))
	}
	url, err := p.ProcessFile(ctx, f)
	if err != nil {
		return err
	}
	if !e.Dry {
		fmt.Println(url)
	}
	return nil
}

func printDryRun(title string, project *notion.Page, n *domain.MeetingNote, reasons []string, blocks int) {
	fmt.Println("────────── DRY-RUN ──────────")
	fmt.Println("Titel:   ", title)
	if project != nil {
		fmt.Println("Projekt: ", project.Name)
	}
	fmt.Println("Prüfen:  ", len(reasons) > 0, strings.Join(reasons, " · "))
	fmt.Println("Blöcke:  ", blocks)
	fmt.Println(mustJSON(n))
	fmt.Println("─────────────────────────────")
}

func mustJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ") //nolint:errcheck // Marshal von Notiz/Properties (nur Strings, Zahlen, Listen) scheitert nicht
	return string(b)
}
