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
| R-M1-05 ⚠ | TOCTOU recheck before archive (primary file only, size + mtime); ADR 0004. Decisions: coordinator: recheck only the primary (ledger key); a kept summary partner would become an orphan, lose its `HasNear` match and with `orphan_summary_action=note` create an extra summary-only note (reproduced by the reviewer, even for a plain touch); a changed partner is archived unprocessed as before. Intended behavior change: a transcript changed during processing gets its own note next pass (no summary context); first note stays | done | 2026-11-02 |
| R-M1-07 ⚠ | Archive backoff and regular files only: `archive()` failures retried after 2 min (not counted against `max_attempts`) on all archive paths; `listEntries` skips non-regular files (log `keine reguläre Datei … übersprungen`, fixes a FIFO hang); golden unchanged; ADR 0004, ADR 0005. Decisions: Mark (2026-11-02): failed folder fsync after a synced copy warns, move continues (also for `_fehler`); coordinator: summary retry gate before `HasNear` (rare error case: archived at `nextTry`, at most 16 min later, never a second note); retry-state helper merged in R-M2-09 | done | 2026-11-02 |
| R-M1-EXIT | M1 exit: errcheck `check-blank: true`; test files excluded for errcheck (path-scoped); 28 blank-ignored errors in production code carry `//nolint:errcheck` with a German reason; no code change (identical after stripping comments); body read errors handled in R-M3-05 | done | 2026-11-02 |
| R-M1-06 | Config validation: clamps and unknown keys as startup warnings; `llm.timeout_minutes` min 1 | done | 2026-11-02 |
| R-M2-01 | `internal/domain` (types, `Transient`/`IsTransient`), `arch_test.go` dependency-direction test; domain coverage 100 %; golden byte-identical; ADR 0015. Decision: Mark (2026-11-02) ADR number 0015 | done | 2026-11-02 |

## M1 summary (complete 2026-11-02: R-M1-01 to R-M1-07 + R-M1-EXIT)

| Finding | Closed by |
| --- | --- |
| A-01 | R-M1-01, R-M1-02 |
| A-02 | R-M1-03 |
| A-11 | R-M1-01, R-M1-02, R-M1-03, R-M1-04 |
| A-12 | R-M1-06 |
| S-13 | R-M1-01, R-M1-02 |
| S-14 | R-M1-04 |
| S-15 | R-M1-05 |

Exit criteria:
- Golden unchanged except `ledger.jsonl` (`begonnen` lines, D1).
- Crash matrix tests green.
- errcheck `check-blank` on.

Decisions taken in M1:

| By | Decision | Task |
| --- | --- | --- |
| Mark | OD-05 residual risk (duplicate page after ambiguous `CreatePage` failure) becomes R-M3-17 | R-M1-02 |
| Mark | Deleting meetingsync's own fresh/partial copy is allowed | R-M1-04 |
| Mark | New dependency `golang.org/x/sys` | R-M1-04 |
| Mark | Failed folder fsync warns and the move continues (supersedes the coordinator rule "no deletion after syncDir error" for syncDir) | R-M1-07 |
| Coordinator | Identical-copy hash set per pass | R-M1-02 |
| Coordinator | `LoadLedger` does not read the gap fields | R-M1-03 |
| Coordinator | TOCTOU recheck on the primary file only | R-M1-05 |
| Coordinator | `EINVAL` is not a fallback trigger | R-M1-04 |
| Coordinator | `go 1.26.0` accepted | R-M1-04 |
| Coordinator | No deletion after an identity mismatch (and after a syncDir error, see above) | R-M1-04 |

Next: M2 package split, R-M2-02 transcript.

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
| 17 | `archive()` failures (`Remove(src)`, folder fsync on the target volume) repeat every poll; `listEntries` accepts non-regular files | open | done (R-M1-07) |
| 18 | Plan ADR numbering for the package split (was 0005, now taken by `golang.org/x/sys`) | open | done (ADR 0015, Mark at the M2 gate, 2026-11-02) |
| 19 | `handleError` deletes the retry state even when the move to `_fehler` fails: the file is reprocessed and `max_attempts` restarts from 0 (pre-existing since v1.0.0) | open | planned (R-M3-18) |
| 20 | A regular file swapped for a symlink between `ReadDir` and `ReadFile` is still followed (low risk, local only) | open | planned (R-M3-19) |
| 21 | Three copies of "get or create state, `nextTry` = now + 2 min" (`interrupted`, `archiveAt`, `handleError`) | open | planned (R-M2-09) |
