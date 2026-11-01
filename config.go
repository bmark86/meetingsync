package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Config wird aus einer JSON- oder YAML-Datei geladen. Alles, was dort fehlt,
// behält den Standardwert aus defaultConfig().
type Config struct {
	WatchDir             string   `json:"watch_dir" yaml:"watch_dir"`
	ArchiveDir           string   `json:"archive_dir" yaml:"archive_dir"`
	FailedDir            string   `json:"failed_dir" yaml:"failed_dir"`
	PollIntervalSeconds  int      `json:"poll_interval_seconds" yaml:"poll_interval_seconds"`
	StableSeconds        int      `json:"stable_seconds" yaml:"stable_seconds"`
	PairWindowSeconds    int      `json:"pair_window_seconds" yaml:"pair_window_seconds"`
	OrphanSummaryMinutes int      `json:"orphan_summary_minutes" yaml:"orphan_summary_minutes"`
	OrphanSummaryAction  string   `json:"orphan_summary_action" yaml:"orphan_summary_action"` // note | archive | keep
	MaxAttempts          int      `json:"max_attempts" yaml:"max_attempts"`
	TranscriptExtensions []string `json:"transcript_extensions" yaml:"transcript_extensions"`
	SummaryExtensions    []string `json:"summary_extensions" yaml:"summary_extensions"`
	UseSummaryAsContext  bool     `json:"use_summary_as_context" yaml:"use_summary_as_context"`
	IncludeTranscript    bool     `json:"include_transcript" yaml:"include_transcript"`
	MyName               string   `json:"my_name" yaml:"my_name"`
	MySpeakerLabel       string   `json:"my_speaker_label" yaml:"my_speaker_label"`
	Timezone             string   `json:"timezone" yaml:"timezone"`
	TitleFormat          string   `json:"title_format" yaml:"title_format"`
	Notify               bool     `json:"notify" yaml:"notify"`

	LLM    LLMConfig    `json:"llm" yaml:"llm"`
	Notion NotionConfig `json:"notion" yaml:"notion"`

	loc *time.Location

	// warnings sammelt Hinweise aus dem Laden (unbekannte Schlüssel, geklemmte
	// Werte). Sie enthalten nie Werte aus der Datei; main schreibt sie ins Log.
	warnings []string
}

type LLMConfig struct {
	BaseURL          string  `json:"base_url" yaml:"base_url"`
	Model            string  `json:"model" yaml:"model"`
	APIKey           string  `json:"api_key" yaml:"api_key"`
	Temperature      float64 `json:"temperature" yaml:"temperature"`
	TimeoutMinutes   int     `json:"timeout_minutes" yaml:"timeout_minutes"`
	MaxChunkChars    int     `json:"max_chunk_chars" yaml:"max_chunk_chars"`
	StructuredOutput bool    `json:"structured_output" yaml:"structured_output"`
}

type NotionConfig struct {
	Token                string `json:"token" yaml:"token"`
	TokenKeychainService string `json:"token_keychain_service" yaml:"token_keychain_service"`
	BaseURL              string `json:"base_url" yaml:"base_url"`
	APIVersion           string `json:"api_version" yaml:"api_version"`

	NotesDataSourceID    string `json:"notes_data_source_id" yaml:"notes_data_source_id"`
	TasksDataSourceID    string `json:"tasks_data_source_id" yaml:"tasks_data_source_id"`
	ProjectsDataSourceID string `json:"projects_data_source_id" yaml:"projects_data_source_id"`

	CreateTasksFor string `json:"create_tasks_for" yaml:"create_tasks_for"` // me | all | none

	Notes    NotesProps    `json:"notes_properties" yaml:"notes_properties"`
	Tasks    TasksProps    `json:"tasks_properties" yaml:"tasks_properties"`
	Projects ProjectsProps `json:"projects_properties" yaml:"projects_properties"`
}

type NotesProps struct {
	Title       string `json:"title" yaml:"title"`
	Status      string `json:"status" yaml:"status"`
	StatusValue string `json:"status_value" yaml:"status_value"`
	Projects    string `json:"projects" yaml:"projects"`
	Review      string `json:"review" yaml:"review"`
}

type TasksProps struct {
	Title       string `json:"title" yaml:"title"`
	Description string `json:"description" yaml:"description"`
	DueDate     string `json:"due_date" yaml:"due_date"`
	Notes       string `json:"notes" yaml:"notes"`
	Projects    string `json:"projects" yaml:"projects"`
	Completed   string `json:"completed" yaml:"completed"`
}

type ProjectsProps struct {
	Title     string `json:"title" yaml:"title"`
	Status    string `json:"status" yaml:"status"`
	DoneValue string `json:"done_value" yaml:"done_value"`
	Archive   string `json:"archive" yaml:"archive"`
}

func defaultConfig() *Config {
	return &Config{
		PollIntervalSeconds:  10,
		StableSeconds:        20,
		PairWindowSeconds:    180,
		OrphanSummaryMinutes: 10,
		OrphanSummaryAction:  "note",
		MaxAttempts:          10,
		MyName:               "Mark",
		Timezone:             "Europe/Berlin",
		TitleFormat:          "{date} – {title}",
		Notify:               true,
		LLM: LLMConfig{
			BaseURL:          "http://localhost:1234/v1",
			Temperature:      0.2,
			TimeoutMinutes:   30,
			MaxChunkChars:    60000,
			StructuredOutput: true,
		},
		Notion: NotionConfig{
			BaseURL:        "https://api.notion.com",
			APIVersion:     "2025-09-03",
			CreateTasksFor: "me",
			Notes: NotesProps{
				Title: "Name", Status: "Status", StatusValue: "Inbox",
				Projects: "Projects", Review: "Prüfen",
			},
			Tasks: TasksProps{
				Title: "Name", Description: "Description", DueDate: "Due Date",
				Notes: "Notes", Projects: "Projects", Completed: "Completed",
			},
			Projects: ProjectsProps{
				Title: "Name", Status: "status", DoneValue: "Done", Archive: "Archive",
			},
		},
	}
}

func defaultConfigPath() string {
	return configPathIn(filepath.Join(homeDir(), ".config", "meetingsync"))
}

// configPathIn bevorzugt config.yaml, sonst config.json.
func configPathIn(dir string) string {
	y := filepath.Join(dir, "config.yaml")
	if _, err := os.Stat(y); err == nil {
		return y
	}
	return filepath.Join(dir, "config.json")
}

var yamlLineRe = regexp.MustCompile(`line (\d+)`)

// yamlErrorMessage baut die Meldung nur aus Pfad und Zeilennummern (ohne
// Doppelte, Reihenfolge des ersten Auftretens).
func yamlErrorMessage(path string, err error) string {
	var lines []string
	seen := map[string]bool{}
	for _, m := range yamlLineRe.FindAllStringSubmatch(err.Error(), -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			lines = append(lines, m[1])
		}
	}
	msg := "ungültiges YAML in " + path
	if len(lines) > 0 {
		msg += " (Zeile " + strings.Join(lines, ", ") + ")"
	}
	return msg
}

func LoadConfig(path string) (*Config, error) {
	cfg := defaultConfig()
	data, err := os.ReadFile(expandHome(path))
	if err != nil {
		return nil, fmt.Errorf("kann %s nicht lesen: %w", path, err)
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(data, cfg); err != nil {
			// Originaltext bewusst verworfen: yaml-Fehler können Werte aus der
			// Datei enthalten (etwa Tokens). Nur die Zeilennummern bleiben.
			return nil, errors.New(yamlErrorMessage(path, err))
		}
		var raw map[string]any
		if yaml.Unmarshal(data, &raw) == nil {
			cfg.warnUnknownKeys(raw, false)
		}
	default:
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("ungültiges JSON in %s: %w", path, err)
		}
		var raw map[string]any
		if json.Unmarshal(data, &raw) == nil {
			cfg.warnUnknownKeys(raw, true)
		}
	}
	if err := cfg.finalize(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// warnUnknownKeys meldet Schlüssel, die in der Datei stehen, aber nicht zu
// Config gehören (Tippfehler wirken sonst nicht). Nur der Schlüsselpfad kommt
// in die Meldung, nie ein Wert. JSON ordnet Schlüssel ohne Beachtung der
// Groß-/Kleinschreibung zu, YAML exakt.
func (c *Config) warnUnknownKeys(raw map[string]any, caseInsensitive bool) {
	var found []string
	collectUnknown(raw, reflect.TypeOf(*c), "", caseInsensitive, &found)
	sort.Strings(found)
	for _, k := range found {
		c.warnings = append(c.warnings, fmt.Sprintf("Konfiguration: unbekannter Schlüssel %q wird ignoriert", k))
	}
}

// collectUnknown prüft nur Struct-Felder rekursiv, keine *Struct. Kommen in M2
// Pointer-Felder hinzu, muss die Rekursion angepasst werden.
func collectUnknown(raw map[string]any, t reflect.Type, prefix string, ci bool, out *[]string) {
	tag := "yaml"
	if ci {
		tag = "json"
	}
	fields := map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get(tag), ",")
		if name != "" && name != "-" {
			fields[name] = t.Field(i).Type
		}
	}
	for k, v := range raw {
		var ft reflect.Type
		var ok bool
		if ci {
			for name, typ := range fields {
				if strings.EqualFold(name, k) {
					ft, ok = typ, true
					break
				}
			}
		} else {
			ft, ok = fields[k]
		}
		if !ok {
			*out = append(*out, prefix+k)
			continue
		}
		if sub, isMap := v.(map[string]any); isMap && ft.Kind() == reflect.Struct {
			collectUnknown(sub, ft, prefix+k+".", ci, out)
		}
	}
}

func (c *Config) finalize() error {
	if strings.TrimSpace(c.WatchDir) == "" {
		return errors.New("watch_dir fehlt")
	}
	c.WatchDir = expandHome(c.WatchDir)
	if c.ArchiveDir == "" {
		c.ArchiveDir = filepath.Join(c.WatchDir, "_archiv")
	}
	if c.FailedDir == "" {
		c.FailedDir = filepath.Join(c.WatchDir, "_fehler")
	}
	c.ArchiveDir = expandHome(c.ArchiveDir)
	c.FailedDir = expandHome(c.FailedDir)
	c.TranscriptExtensions = normExts(c.TranscriptExtensions)
	c.SummaryExtensions = normExts(c.SummaryExtensions)

	switch c.OrphanSummaryAction {
	case "note", "archive", "keep":
	default:
		return fmt.Errorf("orphan_summary_action muss note, archive oder keep sein, nicht %q", c.OrphanSummaryAction)
	}
	switch c.Notion.CreateTasksFor {
	case "me", "all", "none":
	default:
		return fmt.Errorf("create_tasks_for muss me, all oder none sein, nicht %q", c.Notion.CreateTasksFor)
	}
	if c.PollIntervalSeconds < 2 {
		c.warnClamp("poll_interval_seconds", c.PollIntervalSeconds, 2)
		c.PollIntervalSeconds = 2
	}
	if c.MaxAttempts < 1 {
		c.warnClamp("max_attempts", c.MaxAttempts, 1)
		c.MaxAttempts = 1
	}
	if c.LLM.MaxChunkChars < 5000 {
		c.warnClamp("llm.max_chunk_chars", c.LLM.MaxChunkChars, 5000)
		c.LLM.MaxChunkChars = 5000
	}
	if c.LLM.TimeoutMinutes < 1 {
		c.warnClamp("llm.timeout_minutes", c.LLM.TimeoutMinutes, 1)
		c.LLM.TimeoutMinutes = 1
	}
	if strings.TrimSpace(c.LLM.Model) == "" {
		return errors.New("llm.model fehlt – trag den Modell-Identifier aus LM Studio ein (siehe README)")
	}
	if strings.TrimSpace(c.Notion.NotesDataSourceID) == "" {
		return errors.New("notion.notes_data_source_id fehlt")
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return fmt.Errorf("unbekannte Zeitzone %q: %w", c.Timezone, err)
	}
	c.loc = loc
	return nil
}

func (c *Config) warnClamp(key string, was, now int) {
	c.warnings = append(c.warnings, fmt.Sprintf("Konfiguration: %s=%d ist zu klein, verwende %d", key, was, now))
}

// NotionToken sucht den Token in dieser Reihenfolge:
// Umgebungsvariable NOTION_TOKEN → macOS-Schlüsselbund → Config-Datei.
func (c *Config) NotionToken() (string, error) {
	if t := strings.TrimSpace(os.Getenv("NOTION_TOKEN")); t != "" {
		return t, nil
	}
	if s := strings.TrimSpace(c.Notion.TokenKeychainService); s != "" {
		out, err := exec.Command("security", "find-generic-password", "-s", s, "-w").Output()
		if err != nil {
			return "", fmt.Errorf("Notion-Token aus dem Schlüsselbund (%s) nicht lesbar: %w", s, err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	if t := strings.TrimSpace(c.Notion.Token); t != "" {
		return t, nil
	}
	return "", errors.New("kein Notion-Token: NOTION_TOKEN setzen oder token_keychain_service bzw. token in der Config angeben")
}

func normExts(in []string) []string {
	out := make([]string, 0, len(in))
	for _, e := range in {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if !strings.HasPrefix(e, ".") {
			e = "." + e
		}
		out = append(out, e)
	}
	return out
}

func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

func expandHome(p string) string {
	if p == "~" {
		return homeDir()
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(homeDir(), p[2:])
	}
	return p
}
