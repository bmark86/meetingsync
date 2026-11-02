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
- R-M0-03: 12 lint findings fixed, no behavior change.
- R-M1-01 ⚠: ledger lines carry a status (`begonnen`, `fertig`, `verworfen`); last line per hash wins; `Has`/`HasNear` count only `fertig`. `fertig` lines stay byte-identical to v1.0.0; other lines use field `sha256`, so v1.0.0 skips them. `Add` fsyncs and returns write/sync/close errors. See ADR 0004.
- R-M1-06 ⚠: `llm.timeout_minutes` below 1 (e.g. 0) is now clamped to 1 (was: no timeout).
- R-M1-02 ⚠: ledger gets a `begonnen` line (fsync) after the LLM run, right before Notion `CreatePage`. After success `fertig` as before (byte-identical to v1.0.0), on a non-cancel error `verworfen`, on context cancel `begonnen` stays. v1.0.0 skips `begonnen` lines. Golden diff: only `ledger.jsonl` and its hash in `tree.txt`. See ADR 0004.
- Docs: review report, refactoring plan; documentation switched to plain English.

### Fixed
- R-M1-02: restart rule: a file whose last ledger status is `begonnen` goes to `_fehler` (partner first, primary last) with a `.fehler.txt` note to check Notion, log line `letzter Lauf wurde beim Anlegen in Notion unterbrochen`, then `verworfen`; identical copies in the same pass follow. No second page at the tested crash points (after `begonnen`, `CreatePage`, tasks, `fertig`, archive; failed partner move; identical copy).
- e2e test archive month no longer depends on the local time zone

### Security
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
