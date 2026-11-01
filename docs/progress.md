# Progress

| ID | Title | Status | Date |
| --- | --- | --- | --- |
| T1 | YAML config | done (bf32c03) | 2026-10-31 |
| T2 | Commented example config | done | 2026-10-31 |
| T6 | Retry limit for transient errors | done | 2026-11-01 |
| R-PLAN | Architecture/security review + refactoring plan (2026-11-01) | done (report and plan written; 50 tasks planned) | 2026-11-01 |

## Open follow-ups

| # | Item | Owner | Status |
| --- | --- | --- | --- |
| 1 | Error messages for `orphan_summary_action`, `create_tasks_for` and `timezone` print the value with `%q` (pre-existing, affects JSON and YAML) | open | planned (R-M3-09) |
| 2 | Local toolchain go1.25.5 has stdlib vulnerabilities (security review). Recommendation: rebuild with Go 1.26.10 or newer | open | planned (R-M0-01) |
| 3 | Migrate the real config to `config.yaml` and update the installed plist | Mark | open |
| 4 | One week of manual testing from 2026-11-01, then set up the launchd service (see `docs/launchd.md`) | Mark | open |
| 5 | `meetingsync -check` exits 0 although a check fails (✗ seen for LM Studio; `Check` returns nil in `app.go`) | open | planned (R-M3-13) |
| 6 | `-file` without `-dry-run` writes no ledger entry and leaves the file in place; a later service run would process it a second time | open | planned (R-M3-14) |
| 7 | An LLM request that always hits `llm.timeout_minutes` counts as transient (llm.go ~185): retried every 16 min forever, never moved to `_fehler`. Decide whether that is intended (decision: Mark) | open | done (T6) |
