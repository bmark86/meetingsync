# Progress

| ID | Title | Status | Date |
| --- | --- | --- | --- |
| T1 | YAML config | done (bf32c03) | 2026-10-31 |
| T2 | Commented example config | done | 2026-10-31 |
| T6 | Retry limit for transient errors | done | 2026-11-01 |
| R-PLAN | Architecture/security review + refactoring plan (2026-11-01) | done (report and plan written; 52 tasks planned) | 2026-11-01 |
| R-M0-01 | Pin the toolchain (go1.26.10, govulncheck 0 findings) | done | 2026-11-01 |
| R-M0-02 | Makefile: build, lint, test -race, vuln, check | done | 2026-11-01 |
| R-M0-03 | golangci-lint config, 12 findings fixed | done | 2026-11-01 |
| R-M0-04 | GitHub Actions CI (macOS), Dependabot, SECURITY.md. CI run (green): https://github.com/bmark86/meetingsync/actions/runs/41356860373 | done | 2026-11-01 |
| R-M0-05 | Golden harness (fakes, recorder, normalization, `-update`), scenario G01; ADR 0003 | done | 2026-11-01 |
| R-M0-06 | Golden flow scenarios G02–G16c (20 scenarios, `golden_flow_test.go`) | done | 2026-11-02 |
| R-M0-07 | Golden CLI/error scenarios G09–G12c (9 scenarios, `golden_cli_test.go`); coverage ca. 76 % | done | 2026-11-02 |
| R-M0-08 | Unit tests flow: ledger reload, pass gating, wait window, orphan actions, `Run`; shared golden builders | done | 2026-11-02 |
| R-M0-09 | Unit tests Notion/LLM clients; test seam `sleepFn` (notion.go); total coverage ca. 86 % | done | 2026-11-02 |
| R-M0-10 | Hermetic acceptance test (re-exec, no `go build`); gosec G204 exception for tests removed | done | 2026-11-02 |
| R-M0-11 | go directive 1.22 → 1.26 (toolchain go1.26.10); legacy GODEBUG defaults gone; golden tests unchanged, live `-check` green | done | 2026-11-02 |
| R-M1-01 | Ledger with status (`begonnen`/`fertig`/`verworfen`), fsync in `Add`; v1.0.0 compatible; ADR 0004 | done | 2026-11-02 |
| R-M1-02 ⚠ | Crash-safe flow: `begonnen` before `CreatePage`, restart rule to `_fehler`, crash matrix tests; ADR 0004. Decisions: identical-copy hash set per pass (coordinator); OD-05 residual risk becomes R-M3-17 (Mark, 2026-11-02); moveFile copy-fallback accumulation deferred to R-M1-04 | done | 2026-11-02 |
| R-M1-03 | Do not lose tasks and remaining blocks silently: gaps counted in the `fertig` line (`todos_fehlen`, `bloecke_fehler`) and logged; cancel stops task requests; `CreatePage` returns `errUnvollstaendig`; golden unchanged; ADR 0004. Decisions: `LoadLedger` does not read the gap fields back (coordinator); per-task log line `To-do %q nicht angelegt` still contains the task text, stays per D10 (coordinator); OD-04: missing parts only recorded | done | 2026-11-02 |
| R-M1-04 ⚠ | moveFile without overwriting: atomic `RenamexNp` (RENAME_EXCL), safe copy fallback, `.fehler.txt` with O_EXCL, failed move retried after 2 min; ADR 0005, ADR 0004. Decisions: Mark (2026-11-02) approved dependency `golang.org/x/sys` and deleting own fresh/partial copy; coordinator: no other deletions, EINVAL no fallback trigger, `go 1.26.0` accepted, Linux unsupported, TOCTOU between `Lstat` and `Remove` accepted | done | 2026-11-02 |
| R-M1-05 | TOCTOU recheck before archive (next) | next | – |
| R-M1-07 | Archive backoff and regular files only (follow-up of R-M1-04, after R-M1-05) | planned | – |
| R-M1-06 | Config validation: clamps and unknown keys as startup warnings; `llm.timeout_minutes` min 1 | done | 2026-11-02 |

## Open follow-ups

| # | Item | Owner | Status |
| --- | --- | --- | --- |
| 1 | Error messages for `orphan_summary_action`, `create_tasks_for` and `timezone` print the value with `%q` (pre-existing, affects JSON and YAML) | open | planned (R-M3-09) |
| 2 | Local toolchain go1.25.5 has stdlib vulnerabilities (security review). Recommendation: rebuild with Go 1.26.10 or newer | open | done (R-M0-01) |
| 3 | Migrate the real config to `config.yaml` and update the installed plist | Mark | open |
| 4 | One week of manual testing from 2026-11-01, then set up the launchd service (see `docs/launchd.md`) | Mark | open |
| 5 | `meetingsync -check` exits 0 although a check fails (✗ seen for LM Studio; `Check` returns nil in `app.go`) | open | planned (R-M3-13) |
| 6 | `-file` without `-dry-run` writes no ledger entry and leaves the file in place; a later service run would process it a second time | open | planned (R-M3-14) |
| 7 | An LLM request that always hits `llm.timeout_minutes` counts as transient (llm.go ~185): retried every 16 min forever, never moved to `_fehler`. Decide whether that is intended (decision: Mark) | open | done (T6) |
| 8 | `go` directive still 1.22: legacy GODEBUG defaults stay active (see ADR 0002) | Mark | done (R-M0-11) |
| 9 | GitHub settings: allow Actions (read-only token, approve fork PRs), enable Private vulnerability reporting, Dependabot alerts and security updates, branch protection for `main` with required check `check`, no force push | Mark | open |
| 10 | With `create_tasks_for=all` the log/notification says "N To-dos für dich" counting all created tasks, not only Mark's (frozen in golden G16b). Decided by Mark (2026-11-02): count only `fuer_mich` | Mark | planned (R-M3-16) |
| 11 | Golden test helpers duplicated between `golden_flow_test.go` and `golden_cli_test.go`. Consolidate in R-M0-08 | Mark | done (R-M0-08) |
| 12 | Transcript toggle truncation at 99 paragraphs not covered by golden tests. Add unit test in R-M0-08 | Mark | done (R-M0-08) |
| 13 | G10b freezes OS/Go-specific "connection refused" text. Watched in R-M0-11 | Mark | done (unchanged with go 1.26) |
| 14 | Before rebuilding `~/bin` (R-M1-02): new `begonnen` lines appear in the ledger (v1.0.0 skips them). A crash during page creation leaves the file in `_fehler` with a note to check Notion; if the page is missing, move the file(s) back into the export folder | Mark | open |
| 15 | After an ambiguous `CreatePage` failure (5xx, network error, timeout, unreadable 2xx body) a retry can create duplicate pages (ADR 0004). Decided by Mark (2026-11-02) | Mark | planned (R-M3-17) |
| 16 | R-M1-04 must not leave a growing number of copies in `_fehler` when `Remove(src)` fails; deleting meetingsync's own copy needs Mark's approval | Mark | done (R-M1-04, approved 2026-11-02) |
| 17 | `archive()` failures (`Remove(src)`, folder fsync on the target volume) repeat every poll; `listEntries` accepts non-regular files | open | planned (R-M1-07) |
