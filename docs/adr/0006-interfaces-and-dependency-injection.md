# 0006 Interfaces and dependency injection

Status: accepted (coordinator, R-M2-09a, 2026-11-02).

## Context
- `App` was a god object for flow, retry, archive, ledger, Notion mapping and notify (A-04).
- No seams for clock, file system, LLM, Notion or notify; tests mutated package variables or built `App` with `notion=nil` (A-05).
- Three copies of "get or create file state, `nextTry` = now + 2 min" existed (`interrupted`, `archiveAt`, `handleError`; follow-up 21).
- `internal/notify` uses `os/exec`; `arch_test.go` allows `os/exec` only in `internal/notify` and `internal/secret`, so `internal/pipeline` may not import it.

## Decision
- Flow code moved from `app.go` to `internal/pipeline` (`pipeline.go`, `files.go`, `retry.go`): `Pass`, `handle`, `interrupted`, `handleError`, `process`, `createTasks`, `archive`/`archiveAt`/`archiveJob`, `fail`, `listEntries`, `changedSinceRead` (via `FS.Lstat`), `ActiveProjects`, per-file retry state.
- Everything the pipeline needs from outside comes in through `Deps`:

| Dep | Type |
| --- | --- |
| Clock | `Now` func |
| File system | `watch.FS`, `watch.Mover` |
| LLM | `llm.Summarizer` |
| Notion | own port in `pipeline`: `PageWriter` + `ActiveProjects` |
| Notify | own `Notifier` port in `pipeline` (see Context) |
| Ledger | `*ledger.Ledger` (concrete) |
| Log, dry-run output | `*log.Logger`, printer func |

- Retry policy is its own type; values unchanged: transient errors 1/2/4/8/16 min, then 16 min; other errors 2 min; `max_attempts`; context cancel is not counted.
- The three state copies are merged into `state()`; `retryLater()` (now + 2 min) serves `interrupted` and archive failures. `handleError` uses `state()` and `retryPolicy.record`, which computes its own backoff by error class.
- `writeFehlerTxt` moved to `internal/watch` as `WriteFehlerTxt` (needs `os.OpenFile`), so `pipeline` has no `os.*` and no `time.Now()`.
- `Init`, `Check`, `Run` (ticker, `time.Now`), `ProcessSingleFile`, `printDryRun` and `mustJSON` moved to `internal/cli` in R-M2-10.

## Alternatives considered
| Alternative | Why not |
| --- | --- |
| `pipeline` imports `internal/notify` directly | Breaks the `os/exec` arch rule; tests would call osascript |
| Keep package variables (`sleepFn`, `renameFn` style) for seams | Global mutation, no parallel tests, hides dependencies |
| Ledger behind an interface | The ledger has exactly one implementation and is small. An interface would be speculative (CLAUDE.md: no abstractions for single-use code). Pipeline tests use a real ledger in `t.TempDir()`, which also exercises the real persistence (fsync, file format). If a second implementation is ever needed, the port can be extracted then. |

## Consequences
- Pipeline tests inject fakes; coverage `pipeline` 93.5 % own / 98.8 % combined; golden byte-identical.
- Trade-off: `pipeline` is coupled to `*ledger.Ledger`; a fake ledger is not possible, tests use a real one in a temp dir.
- Trade-off: `Notifier` is declared twice (`internal/notify` and `pipeline`); `main` wires one into the other.
- `FileState`, `States()` and `ErrUnterbrochen` are exported only for root tests (still so after R-M2-10; follow-up R-M4-04).
- R-M2-09b (coordinator, 2026-11-02): `sleepFn` became the field `Client.sleep` in `internal/notion`; tests inject the Notion transport via the unexported `appSeams`. Rejected: a shared `mustJSON` (needs an artificial common package, so two copies stay in `internal/llm` and `main`) and an exported in-memory `watchtest` FS (pipeline tests use real temp dirs at 93.5 % coverage, so it would be speculative). `llm.Normalize` stays a separate call after `Summarize`; `FormatTitle`/`deadlineLabel` stay in `internal/notion`.
- R-M2-10 (coordinator, 2026-11-02): lazy `App.pipe()` and `appSeams` are gone. `internal/cli` builds the pipeline directly in `NewPipeline` (`time.Now` injected there); `cli.Notion` is an interface, so `Init`/`Check` are tested with fakes. `main.go` constructs notify, secret, LLM and Notion in `newEnv` (the `os/exec` arch rule stays unchanged). Two wiring helpers (`loadConfig`, `newEnv`) instead of one, because a combined helper would move the `Konfiguration:` line and change stdout.
