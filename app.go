package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"meetingsync/internal/config"
	"meetingsync/internal/domain"
	"meetingsync/internal/ledger"
	"meetingsync/internal/llm"
	"meetingsync/internal/notify"
	"meetingsync/internal/notion"
	"meetingsync/internal/pipeline"
	"meetingsync/internal/secret"
	"meetingsync/internal/transcript"
	"meetingsync/internal/watch"
)

// App verdrahtet Konfiguration, Clients und internal/pipeline. Hier bleiben
// nur Einstiegspunkte (Init, Check, Run, -file, Dry-Run-Ausgabe); App wird in
// R-M2-10 aufgelöst.
type App struct {
	cfg      *config.Config
	loc      *time.Location // Zeitzone aus cfg.Timezone, von main aufgelöst
	log      *log.Logger
	dry      bool
	llm      *llm.Client
	notion   *notion.Client
	ledger   *ledger.Ledger
	notifier notify.Notifier
	mover    watch.Mover // Nullwert: echtes Verschieben

	notesDS, tasksDS, projectsDS          string
	notesTypes, tasksTypes, projectsTypes notion.Schema

	pl *pipeline.Pipeline // von pipe() beim ersten Gebrauch gebaut
}

// newLLMClient baut den LM-Studio-Client aus der Konfiguration. transport nil
// heißt http.DefaultTransport.
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

// NewApp baut die App. loc ist die aufgelöste Zeitzone der Config, tokens
// liefert den Notion-Token.
func NewApp(cfg *config.Config, loc *time.Location, tokens secret.TokenSource, logger *log.Logger, dry bool) (*App, error) {
	return newApp(cfg, loc, tokens, logger, dry, appSeams{})
}

// appSeams sind Einhängepunkte für Tests; der Nullwert ist die Produktion.
type appSeams struct {
	notionTransport http.RoundTripper // nil: http.DefaultTransport
	notifier        notify.Notifier   // nil: echte Mac-Mitteilung
}

// newApp ist NewApp mit Einhängepunkten.
func newApp(cfg *config.Config, loc *time.Location, tokens secret.TokenSource, logger *log.Logger, dry bool, s appSeams) (*App, error) {
	a := &App{
		cfg:      cfg,
		loc:      loc,
		log:      logger,
		dry:      dry,
		llm:      newLLMClient(cfg, logger, nil),
		notifier: notify.New(),
	}
	if s.notifier != nil {
		a.notifier = s.notifier
	}
	token, err := tokens.Token()
	switch {
	case err == nil:
		a.notion = notion.NewClient(cfg.Notion.BaseURL, cfg.Notion.APIVersion, token, s.notionTransport)
	case dry:
		logger.Printf("Hinweis: %v – Dry-Run läuft ohne Notion (keine Projektliste).", err)
	default:
		return nil, err
	}
	return a, nil
}

// transcriptOpts reicht die konfigurierten Endungen an internal/transcript.
func (a *App) transcriptOpts() transcript.Options {
	return transcript.Options{TranscriptExtensions: a.cfg.TranscriptExtensions, SummaryExtensions: a.cfg.SummaryExtensions}
}

// Init prüft die Notion-Datenbanken und lädt das Verarbeitungsprotokoll.
func (a *App) Init(ctx context.Context) error {
	defer func() { a.pl = nil }() // Pipeline nach jedem Init neu bauen
	l, err := ledger.Load(filepath.Join(a.cfg.ArchiveDir, ".meetingsync-ledger.jsonl"), !a.dry)
	if err != nil {
		return err
	}
	a.ledger = l
	if a.notion == nil {
		return nil
	}

	nc := a.cfg.Notion
	if a.notesDS, a.notesTypes, err = a.notion.ResolveDataSource(ctx, nc.NotesDataSourceID); err != nil {
		return fmt.Errorf("Notes-Datenbank nicht lesbar: %w", err)
	}
	if err := a.notesTypes.CheckNotes(notion.NotesProps(nc.Notes)); err != nil {
		return err
	}
	if !a.notesTypes.Is(nc.Notes.Review, notion.KindCheckbox) {
		a.log.Printf("Hinweis: Notes hat keine Checkbox %q – Prüf-Hinweise stehen nur als Hinweisbox oben auf der Seite.", nc.Notes.Review)
	}

	if nc.CreateTasksFor != "none" && nc.TasksDataSourceID != "" {
		if a.tasksDS, a.tasksTypes, err = a.notion.ResolveDataSource(ctx, nc.TasksDataSourceID); err != nil {
			return fmt.Errorf("Tasks-Datenbank nicht lesbar: %w", err)
		}
		if err := a.tasksTypes.CheckTasks(notion.TasksProps(nc.Tasks)); err != nil {
			return err
		}
	}
	if nc.ProjectsDataSourceID != "" {
		if a.projectsDS, a.projectsTypes, err = a.notion.ResolveDataSource(ctx, nc.ProjectsDataSourceID); err != nil {
			a.log.Printf("Hinweis: Projects-Datenbank nicht lesbar (%v) – Projektvorschläge sind aus.", err)
			a.projectsDS = ""
		}
	}
	return nil
}

// Check prüft Notion und LM Studio und gibt einen kurzen Bericht aus.
func (a *App) Check(ctx context.Context) error {
	fmt.Printf("Ordner:   %s\nArchiv:   %s\nFehler:   %s\n", a.cfg.WatchDir, a.cfg.ArchiveDir, a.cfg.FailedDir)
	if _, err := os.Stat(a.cfg.WatchDir); err != nil {
		fmt.Printf("✗ Ordner nicht gefunden: %v\n", err)
	}
	if a.notion == nil {
		return errors.New("kein Notion-Token gefunden")
	}
	if err := a.Init(ctx); err != nil {
		fmt.Printf("✗ Notion: %v\n", err)
		return err
	}
	fmt.Printf("✓ Notion: Notes erreichbar (%s)\n", a.notesDS)
	if a.tasksDS != "" {
		fmt.Printf("✓ Notion: Tasks erreichbar (%s)\n", a.tasksDS)
	}
	if a.projectsDS != "" {
		p := a.pipe().ActiveProjects(ctx)
		fmt.Printf("✓ Notion: %d aktive Projekte\n", len(p))
	}
	models, err := a.llm.Models(ctx)
	if err != nil {
		fmt.Printf("✗ LM Studio nicht erreichbar unter %s: %v\n", a.cfg.LLM.BaseURL, err)
		return nil
	}
	if slices.Contains(models, a.cfg.LLM.Model) {
		fmt.Printf("✓ LM Studio: Modell %q verfügbar\n", a.cfg.LLM.Model)
	} else {
		fmt.Printf("✗ LM Studio: Modell %q nicht gefunden. Verfügbar:\n", a.cfg.LLM.Model)
		for _, m := range models {
			fmt.Printf("    %s\n", m)
		}
	}
	return nil
}

func (a *App) Run(ctx context.Context) {
	a.log.Printf("Überwache %s (alle %ds). Beenden mit Ctrl+C.", a.cfg.WatchDir, a.cfg.PollIntervalSeconds)
	ticker := time.NewTicker(time.Duration(a.cfg.PollIntervalSeconds) * time.Second)
	defer ticker.Stop()
	a.Pass(ctx, time.Now())
	for {
		select {
		case <-ctx.Done():
			a.log.Printf("Beendet.")
			return
		case <-ticker.C:
			a.Pass(ctx, time.Now())
		}
	}
}

// Pass ist ein Durchlauf über den Exportordner.
func (a *App) Pass(ctx context.Context, now time.Time) { a.pipe().Pass(ctx, now) }

// pipe baut die Pipeline nach Init aus den Feldern der App (Ledger,
// Datenbanken, Anpassungen der Tests) und nach jedem Init neu (vorläufig bis R-M2-10).
func (a *App) pipe() *pipeline.Pipeline {
	if a.pl != nil {
		return a.pl
	}
	var nc pipeline.Notion
	if a.notion != nil { // kein Interface mit nil-Zeiger
		nc = a.notion
	}
	c := a.cfg
	a.pl = pipeline.New(pipeline.Options{
		WatchDir:            c.WatchDir,
		ArchiveDir:          c.ArchiveDir,
		FailedDir:           c.FailedDir,
		Stable:              time.Duration(c.StableSeconds) * time.Second,
		PairWindow:          time.Duration(c.PairWindowSeconds) * time.Second,
		OrphanWait:          time.Duration(c.OrphanSummaryMinutes) * time.Minute,
		OrphanAction:        c.OrphanSummaryAction,
		MaxAttempts:         c.MaxAttempts,
		Transcript:          a.transcriptOpts(),
		Loc:                 a.loc,
		Dry:                 a.dry,
		MyName:              c.MyName,
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
		NotesDS:             a.notesDS,
		TasksDS:             a.tasksDS,
		ProjectsDS:          a.projectsDS,
		NotesTypes:          a.notesTypes,
		TasksTypes:          a.tasksTypes,
		ProjectsTypes:       a.projectsTypes,
	}, pipeline.Deps{
		Now:      time.Now,
		FS:       watch.OS{},
		Mover:    a.mover,
		LLM:      a.llm,
		Notion:   nc,
		Notifier: a.notifier,
		Ledger:   a.ledger,
		Log:      a.log,
		DryRun:   a.printDryRun,
	})
	return a.pl
}

// ProcessSingleFile verarbeitet eine Datei direkt (zum Testen). Die Datei
// bleibt liegen und wird nicht ins Protokoll eingetragen.
func (a *App) ProcessSingleFile(ctx context.Context, path string) error {
	path = config.ExpandHome(path)
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	e := &watch.Entry{Path: path, Name: filepath.Base(path), ModTime: info.ModTime(), Size: info.Size(), Stable: true, Content: content}
	e.Kind = transcript.Classify(path, content, a.transcriptOpts())
	a.log.Printf("%s erkannt als: %s", e.Name, e.Kind)
	if e.Kind == domain.KindUnsupported {
		return fmt.Errorf("Dateityp %s wird nicht unterstützt", filepath.Ext(path))
	}
	url, err := a.pipe().ProcessFile(ctx, e)
	if err != nil {
		return err
	}
	if !a.dry {
		fmt.Println(url)
	}
	return nil
}

func (a *App) printDryRun(title string, project *notion.Page, n *domain.MeetingNote, reasons []string, blocks int) {
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
