# meetingsync

Go tool (macOS, stdlib plus `go.yaml.in/yaml/v3` and `golang.org/x/sys`, `go 1.26.0`, `toolchain go1.26.10`): MacWhisper export → local LLM (LM Studio, Gemma) → meeting note in Notion (Notes, Tasks, Projects). Runs all the time via launchd. Details and configuration: `README.md`.

## Flow

Poll the export folder (10 s ticker in `internal/cli`, one pass in `internal/pipeline`, listing in `internal/watch`). A file must stay unchanged for `stable_seconds` → detect transcript or summary by extension and content (`internal/transcript`) → pair transcript and summary (`pair_window_seconds`); a summary without a partner is handled by `orphan_summary_action` → LLM extraction, long transcripts are split into chunks (`internal/llm`) → Notion page and tasks (`internal/notion`: `client.go`, `blocks.go`, `properties.go`) → archive in `_archiv/JJJJ-MM` (JJJJ-MM = YYYY-MM), ledger against double processing (`internal/ledger`).

Retry: temporary errors (LM Studio or Notion not reachable (including timeout), HTTP 429/5xx, LM Studio model not loaded, Notion 401/403/404) are retried with backoff (1/2/4/8 min, then every 16 min). All errors count together against `max_attempts` (default 10; other errors are retried every 2 min). After that the file goes to `_fehler`. Exception: a note with more than 1000 Notion blocks (transcript not counted) is not retried and goes to `_fehler` at once.

## Files

- `main.go` flags, `internal/config` config (YAML/JSON, defaults)
- `internal/cli` entry points (Init, Check, Run, `-file`, dry-run output), `internal/pipeline` processing flow, `internal/watch` file listing, pairing and moving
- `internal/transcript`, `internal/ledger`, `internal/domain` (data types, error class transient), `internal/llm`, `internal/notion` (`client.go`, `blocks.go`, `properties.go`), `internal/notify` (macOS notification), `internal/secret` (Keychain lookup for Notion token and LLM key) as described above
- `config.example.yaml` commented reference of all options
- Docs in `docs/`: `progress.md` (open items), `adr/`, `launchd.md`, `changelog.md`, `architecture.md` (packages and data flow), `testing.md` (test layers, golden tests)
- Active plan: `docs/superpowers/plans/2026-11-01-refactoring.md`

## Commands

- `make build` (builds `./bin/meetingsync` with the version set; `./bin` is gitignored)
- `make test` (`go test -race ./...`, including `e2e_test.go`)
- `make vet` (`go vet ./...`)
- `make lint` (golangci-lint, pinned version)
- `make vuln` (govulncheck, pinned version)
- `make check` (vet + lint + test + vuln, Definition of Done)
- `go build -o ~/bin/meetingsync .`
- `meetingsync -check` (checks folder, Notion/LM Studio; exits 1 if any check shows ✗ or on a config, token or keychain error, otherwise 0), `-dry-run -file <file>` (writes nothing), `-once`, `-config <path>`
- `-file` without `-dry-run` creates a real note and writes a ledger entry, but does not move the file. It waits while a service pass runs (both lock the export folder).

## Definition of Done

`make check` passes. Put the commands in EVIDENCE of the report.

## Global Constraints (non-negotiable)

- Without explicit approval from Mark, no existing notes or links (relations to projects/tasks) may be deleted, neither in Notion nor locally. This also applies to archive and failed files.

## Rules

- Privacy: the raw transcript never leaves the Mac. Only the finished note goes to Notion (transcript only with `include_transcript`).
- Never put the Notion token in the repo: `NOTION_TOKEN` → Keychain (`meetingsync-notion`, only if `token_keychain_service` is set; an error there aborts) → `notion.token`. The real `config.yaml` (or `config.json`) is in `~/.config/meetingsync/`.
- Stdlib only, the exceptions are `go.yaml.in/yaml/v3` and `golang.org/x/sys` (only for the atomic no-overwrite rename `unix.RenamexNp` with `RENAME_EXCL`, approved 2026-11-02); no other dependencies without asking first.
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
