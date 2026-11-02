# Changelog

Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added
- R-M0-02: Makefile targets `build` (to `./bin`, version via ldflags), `lint`, `test` (with `-race`), `vuln`; `check` = vet + lint + test + vuln. Tools pinned: golangci-lint v2.11.4, govulncheck v1.9.0.
- R-M0-03: `.golangci.yml` (govet, errcheck, staticcheck all except ST1005, unused, ineffassign, gosec). gosec exclusions limited to app.go, config.go and tests until R-M3-03/04/07.
- R-M0-04: CI workflow (GitHub Actions, macOS): vet, lint, test, vuln; daily run; toolchain and env checks. See ADR 0002.
- R-M0-04: Dependabot for gomod and github-actions (weekly).
- R-M0-04: `SECURITY.md` (private reporting, answer within 14 days).
- R-M0-05: golden test harness (fake Notion and LM Studio, request/file/ledger/log recording, normalization, -update), first scenario G01; ADR 0003.
- R-M0-06: 20 golden flow scenarios G02–G16c in `golden_flow_test.go` (summary modes, pairs, JSON, chunking, Notion limits with multibyte text, project matching, `create_tasks_for`).
- R-M0-07: 9 golden CLI/error scenarios G09–G12c in `golden_cli_test.go` (`-dry-run`, `-check`, backoff to `_fehler`, LLM errors incl. schema fallback, empty file). Total coverage ca. 76 %.
- R-M0-08: unit tests for the flow (ledger reload, pass gating `nextTry`/`near`, orphan actions, `Run` with cancelled context, transcript toggle truncation at 99 paragraphs); shared golden test builders in `golden_test.go`.
- R-M0-09: unit tests for the Notion and LLM clients (retry, backoff, `Retry-After`, no retry for non-idempotent requests, LLM parse retry, schema fallback, `Models`); test seam `sleepFn` in `notion.go`. Total coverage ca. 86 %.
- R-M0-10: hermetic acceptance test (re-exec instead of `go build`); gosec G204 exception for test files removed from `.golangci.yml`; month change covered by golden G01.
- R-M1-06: config warnings at startup (log): clamps as `Konfiguration: <key>=<n> ist zu klein, verwende <min>` (`poll_interval_seconds`, `max_attempts`, `llm.max_chunk_chars`, `llm.timeout_minutes`); unknown keys (YAML/JSON, nested) as `Konfiguration: unbekannter Schlüssel "<pfad>" wird ignoriert`, key path only, never the value.

### Changed
- R-M2-02: `classify.go` moved to `internal/transcript` (pure functions, no I/O; stdlib + `internal/domain` only, enforced by `arch_test.go`). Exported: `Options` (`TranscriptExtensions`, `SummaryExtensions`), `Classify`, `IsSupportedExt`, `Text`, `MeetingTiming`, `NormalizeNewlines`. `contains()` replaced by `slices.Contains`; `weekdaysLong` moved to `llm.go`. Tests moved, package coverage 100 %, new config test for normalized extensions. No behavior change, golden unchanged.
- R-M2-01: new package `internal/domain` (`MeetingNote`, `Thema`, `Action`, `Naechstes`, `Timing`, `NoteMeta`, `Kind`, error class `Transient`/`IsTransient`, stdlib only); all callers switched, no aliases. `arch_test.go` checks the dependency direction (`go list -deps`). No behavior change, golden unchanged. See ADR 0015.
- R-M1-04 ⚠: new dependency `golang.org/x/sys` v0.49.0 (approved by Mark, 2026-11-02), only for the atomic no-overwrite rename. `go` directive `1.26` → `1.26.0` (forced by x/sys); toolchain go1.26.10 unchanged. Linux build unsupported (`RenamexNp` is darwin-only). See ADR 0005.
- R-M1-04: a failed move (restart rule) is retried after 2 min via the retry state, not on every poll and not counted against `max_attempts`. See ADR 0004.
- R-M1-EXIT: errcheck `check-blank: true` in `.golangci.yml`; test files excluded for errcheck (path-scoped). The 28 blank-ignored errors in production code carry `//nolint:errcheck // <reason>`. Lint only, no behavior change.
- R-M0-03: 12 lint findings fixed, no behavior change.
- R-M1-01 ⚠: ledger lines carry a status (`begonnen`, `fertig`, `verworfen`); last line per hash wins; `Has`/`HasNear` count only `fertig`. `fertig` lines stay byte-identical to v1.0.0; other lines use field `sha256`, so v1.0.0 skips them. `Add` fsyncs and returns write/sync/close errors. See ADR 0004.
- R-M1-06 ⚠: `llm.timeout_minutes` below 1 (e.g. 0) is now clamped to 1 (was: no timeout).
- R-M1-02 ⚠: ledger gets a `begonnen` line (fsync) after the LLM run, right before Notion `CreatePage`. After success `fertig` as before (byte-identical to v1.0.0), on a non-cancel error `verworfen`, on context cancel `begonnen` stays. v1.0.0 skips `begonnen` lines. Golden diff: only `ledger.jsonl` and its hash in `tree.txt`. See ADR 0004.
- R-M1-03: `CreatePage` returns ID and URL with an error wrapping `errUnvollstaendig` when appending the remaining blocks fails; the caller treats it as success with a gap (no retry, request bodies unchanged). Unused `Notion.log` field and `NewNotion` logger parameter removed.
- Docs: review report, refactoring plan; documentation switched to plain English.

### Fixed
- R-M1-07 ⚠: a failed archive move (`moveFile` error, e.g. `Remove(src)`) is retried after 2 min via the retry state, on all archive paths; the warning `… konnte nicht archiviert werden …` and the copy no longer repeat every poll. Not counted against `max_attempts`, never sends the file to `_fehler`; state cleared on success. In `Pass` the summary retry gate now runs before `HasNear`. Golden unchanged. See ADR 0004.
- R-M1-07 ⚠: `listEntries` skips non-regular files (symlinks, FIFOs, devices), logged once per path: `<name>: keine reguläre Datei (z. B. Symlink) → übersprungen`. Fixes a hang: a FIFO with a supported extension blocked `ReadFile`. Visible change: symlinks in the export folder are no longer processed.
- R-M1-07: a failed folder fsync on the target volume after a synced copy is a warning (`Zielordner nicht synchronisiert, trotzdem verschoben nach …`), the move continues; also for `_fehler` (`.fehler.txt` still written). Decided by Mark, 2026-11-02. See ADR 0005.
- R-M1-05 ⚠: TOCTOU recheck before archiving (S-15). `archiveJob` compares size and mtime of the job's primary file (`Lstat`) with the values from `listEntries`; if changed, the file stays in the export folder with the warning `<Datei> wurde seit dem Einlesen geändert → nicht archiviert, bleibt im Exportordner (nächster Durchlauf prüft sie erneut)`. Partners are archived as before; `fertig` is still written. Applies to the processed and the already-processed (`Has`) path; no recheck in `-dry-run`, `HasNear` and orphan paths. Behavior change: a transcript changed during processing gets its own note in the next pass (without the summary as context); the first note stays. Before, the changed file was archived and its new content never processed. Golden unchanged. See ADR 0004.
- R-M1-04: `moveFile` never overwrites an existing target: same volume uses `unix.RenamexNp` with `RENAME_EXCL` (on `EEXIST` the next name `stem (n).ext`). On `EXDEV`/`ENOTSUP` the copy fallback checks the source (regular file, same identity, symlinks refused), copies with `O_CREATE|O_EXCL`, syncs file and target folder, and checks identity before `Remove(src)`. Other rename errors are returned.
- R-M1-04: no accumulating copies. If `Remove(src)` fails, only meetingsync's own fresh copy is deleted (identity-checked), the original stays, with a warning. A partial copy after an `io.Copy`/`Sync`/`Close` error is deleted too (own copy only). Both deletions approved by Mark. After a folder fsync error or an identity mismatch both files stay.
- R-M1-04: `.fehler.txt` is created with `O_EXCL` (next name on conflict); write errors are logged (`Fehlerhinweis zu <name> nicht geschrieben: …`).
- R-M1-02: restart rule: a file whose last ledger status is `begonnen` goes to `_fehler` (partner first, primary last) with a `.fehler.txt` note to check Notion, log line `letzter Lauf wurde beim Anlegen in Notion unterbrochen`, then `verworfen`; identical copies in the same pass follow. No second page at the tested crash points (after `begonnen`, `CreatePage`, tasks, `fertig`, archive; failed partner move; identical copy).
- R-M1-03: missing tasks and blocks are no longer silent. The `fertig` ledger line gets optional fields `todos_fehlen` and `bloecke_fehler` (only when something is missing; normal path byte-identical to v1.0.0, v1.0.0 reads such lines as done). New log line `⚠️  <Datei>: Notiz unvollständig – N To-do(s) nicht angelegt, M Fehler beim Anhängen von Blöcken (nicht nachgeholt)` (file name and counts only). A context cancel in `createTasks` stops further task requests; a cancel after the page exists ends as `fertig` with URL (archived, no `_fehler` after restart, no second page). Missing parts are only recorded, not made up later (OD-04). See ADR 0004.
- e2e test archive month no longer depends on the local time zone

### Security
- R-M1-04: closes the overwrite window in `moveFile` (S-14). New dependency `golang.org/x/sys`: `go.sum` hashes verified against sum.golang.org, govulncheck clean, Dependabot covers `gomod`. Accepted residual risk: path-based `Remove` between `Lstat` and unlink. See ADR 0005.
- Toolchain pinned to go1.26.10 (fixes 26 reachable stdlib vulnerabilities, govulncheck clean). See ADR 0002.
- R-M0-11: go directive raised to 1.26 – stricter TLS/x509/URL defaults, post-quantum key exchange; no change in output (golden tests unchanged, live -check green)

## [1.0.0] - 2026-11-01

### Added
- T1: Config can be read as YAML (`.yaml`/`.yml`, extension case-insensitive); `config.example.yaml`.
- T1: Without `-config`, `~/.config/meetingsync/config.yaml` takes precedence over `config.json` (`configPathIn`).
- T1: Startup logs "Konfiguration: <path>".
- T1: Dependency `go.yaml.in/yaml/v3` v3.0.6 (see ADR 0001).
- T2: `config.example.yaml` is a full option reference: sections, German comment per option (purpose, values, default); unused options commented out with default; `token` and `api_key` commented out without value.

### Changed
- T1: README and plist template point to `config.yaml`; `config.json` keeps working.
- T1: CLAUDE.md: "stdlib only" now excepts `go.yaml.in/yaml/v3`; real config path updated.
- T2: Defaults documented per option, not in a header; active values unchanged, loaded config identical (test).
- T6: Transient errors (LM Studio/Notion unreachable incl. timeout, HTTP 429/5xx, model not loaded, Notion 401/403/404) count toward `max_attempts`, together with other errors. Wait times unchanged (transient 1/2/4/8/16 min, then 16 min; other 2 min).
- T6: After `max_attempts` the file moves to `_fehler` with `.fehler.txt`. Context cancellation does not count as an attempt.
- T6: Default `max_attempts` 3 → 10 (breaking: also applies to non-transient errors).
- T6: Consequence: if LM Studio is down for about 95 min, or the Notion access is missing, all affected files end up in `_fehler` and must be moved back to the watch folder by hand.

### Fixed
- T2: Keychain hint in `config.example.yaml`: `security add-generic-password ... -w`. Without `-w` an empty password is stored and Notion answers 401 permanently.

### Security
- T1: YAML errors never contain file content, only path and line number (keeps tokens out of the log).
