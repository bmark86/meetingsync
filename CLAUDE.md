# meetingsync

Go tool (macOS, stdlib plus `go.yaml.in/yaml/v3`, `go 1.22`): MacWhisper export → local LLM (LM Studio, Gemma) → meeting note in Notion (Notes, Tasks, Projects). Runs all the time via launchd. Details and configuration: `README.md`.

## Flow

Poll the export folder (10 s). A file must stay unchanged for `stable_seconds` → detect transcript or summary by extension and content (`classify.go`) → pair transcript and summary (`pair_window_seconds`); a summary without a partner is handled by `orphan_summary_action` → LLM extraction, long transcripts are split into chunks (`llm.go`) → Notion page and tasks (`blocks.go`, `notion.go`) → archive in `_archiv/JJJJ-MM` (JJJJ-MM = YYYY-MM), ledger against double processing (`app.go`).

Retry: temporary errors (LM Studio or Notion not reachable (including timeout), HTTP 429/5xx, LM Studio model not loaded, Notion 401/403/404) are retried with backoff (1/2/4/8 min, then every 16 min). All errors count together against `max_attempts` (default 10; other errors are retried every 2 min). After that the file goes to `_fehler`.

## Files

- `main.go` flags, `config.go` config (YAML/JSON, defaults), `app.go` flow/ledger/archive
- `classify.go`, `llm.go`, `blocks.go`, `notion.go` as described above
- `config.example.yaml` commented reference of all options
- Docs in `docs/`: `progress.md` (open items), `adr/`, `launchd.md`, `changelog.md`
- Active plan: `docs/superpowers/plans/2026-11-01-refactoring.md`

## Commands

- `make test` (`go test ./...`, including `e2e_test.go`)
- `make vet` (`go vet ./...`)
- `make check` (vet + test, Definition of Done)
- `go build -o ~/bin/meetingsync .`
- `meetingsync -check` (checks Notion/LM Studio; exit code is 0 even with ✗ for folder or LM Studio, so read the output), `-dry-run -file <file>` (writes nothing), `-once`, `-config <path>`
- `-file` without `-dry-run` creates a real note, but without a ledger entry and without moving the file.

## Definition of Done

`make check` passes. Put the commands in EVIDENCE of the report.

## Global Constraints (non-negotiable)

- Without explicit approval from Mark, no existing notes or links (relations to projects/tasks) may be deleted, neither in Notion nor locally. This also applies to archive and failed files.

## Rules

- Privacy: the raw transcript never leaves the Mac. Only the finished note goes to Notion (transcript only with `include_transcript`).
- Never put the Notion token in the repo: `NOTION_TOKEN` → Keychain (`meetingsync-notion`, only if `token_keychain_service` is set; an error there aborts) → `notion.token`. The real `config.yaml` (or `config.json`) is in `~/.config/meetingsync/`.
- Stdlib only, the single exception is `go.yaml.in/yaml/v3`; no other dependencies without asking first.
- Code comments, log messages and Notion content are German; all documentation (README, CLAUDE.md, docs/) is plain English.

## Agent team

The main session runs as `coordinator` (set via `agent` in `.claude/settings.local.json`).
All subagents load this file. Role details are in `.claude/agents/<name>.md`.

| Agent | Role | Writes |
| --- | --- | --- |
| `coordinator` | Plans, assigns, picks the model for each implementer task, verifies, decides | nothing except task status |
| `developer` | Implements exactly one task, including unit tests | code in the task scope |
| `spec-reviewer` | Checks the diff against the task and the constraints | nothing |
| `code-reviewer` | Checks correctness, design, test quality | nothing |
| `security-reviewer` | Checks security-relevant diffs | nothing |
| `qa` | Checks against the acceptance criteria, writes integration tests | tests only |
| `docs-writer` | Maintains changelog, architecture, ADRs, processes | `docs/` only |

## Report format (every subagent ends with exactly this)

```text
STATUS: done | partial | blocked
SCOPE: <task id + one line>
CHANGES: <file paths, one per line, or "none">
EVIDENCE: <command> -> <exit code / result summary>, one per line
FINDINGS: <numbered, file:line, or "none">
ASSUMPTIONS: <what you assumed, or "none">
OPEN: <what is unresolved / needs a human decision, or "none">
```

Reports should not be longer than 40 lines. No prose is allowed outside this block.
