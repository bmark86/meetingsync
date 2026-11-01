# Progress

| ID | Title | Status | Date |
| --- | --- | --- | --- |
| T1 | YAML config | done (bf32c03) | 2026-10-31 |
| T2 | Commented example config | done | 2026-10-31 |
| T6 | Retry limit for transient errors | done | 2026-11-01 |
| R-PLAN | Architecture/security review + refactoring plan (2026-11-01) | done (report and plan written; 51 tasks planned) | 2026-11-01 |
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
| 10 | With `create_tasks_for=all` the log/notification says "N To-dos für dich" counting all created tasks, not only Mark's (frozen in golden G16b). Likely a bug, decision Mark | Mark | open |
| 11 | Golden test helpers duplicated between `golden_flow_test.go` and `golden_cli_test.go`. Consolidate in R-M0-08 | Mark | done (R-M0-08) |
| 12 | Transcript toggle truncation at 99 paragraphs not covered by golden tests. Add unit test in R-M0-08 | Mark | done (R-M0-08) |
| 13 | G10b freezes OS/Go-specific "connection refused" text. Watch in R-M0-11 | Mark | open |
