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
| R-M2-02 | `internal/transcript` (classify, text, timing; pure, imports stdlib + `internal/domain` only, arch_test rule); coverage 100 %; golden byte-identical. QA found a pre-existing date bug, not fixed: follow-up 22 | done | 2026-11-02 |
| R-M2-03 | `internal/ledger` (`ledger.Ledger`, `ledger.Entry`, `ledger.Load`, `Add`, `Has`, `HasNear`, `Started`; stdlib only, arch_test rule); fixture moved to `internal/ledger/testdata/`; coverage 97.3 %; golden byte-identical. Decisions: coordinator: no `Begin`/`Finish` redesign, method set stays to avoid API churn; `syncDir`/`absDur` duplicated in `app.go` and `internal/ledger` (accepted, shared helper possible later); gosec G30[126]/G304 exclusions extended to `internal/ledger/ledger.go`, to be removed in R-M3-03/R-M3-04 | done | 2026-11-02 |
| R-M2-04 | `internal/notion` (`client.go`, `blocks.go`, new `properties.go`): typed `PropertyKind` (Notion API names, decoded from the schema JSON; kind-to-payload mapping only in `value`/`filter`), `NoteProperties`/`TaskProperties`, `Schema`, `ActiveProjects` filter, `PageWriter` interface (wiring in R-M2-09); `internal/notion` imports stdlib + domain only (arch_test); coverage 97.8 %; golden byte-identical; security review approved | done | 2026-11-02 |
| R-M2-05 | `internal/llm` (`client.go`: `Options`, `Summarizer`, `Client`, `NewClient`, `NoteInput`, `Normalize`, `Models`); pure move, prompts/schema/splitter/parse retry/schema fallback byte-identical; `httpError` unexported, classification via `domain.Transient`; 18 tests moved; arch_test rule (stdlib + domain only), added in fix round 1 after the first report claimed it without it; golden unchanged. `App` still holds `*llm.Client`; `mustJSON` duplicated in `app.go` and `internal/llm` (merge in R-M2-09) | done | 2026-11-02 |
| R-M2-06 | `internal/notify` (`Notifier`, `New()` osascript notifier, unexported runner seam); command/escaping byte-identical; `App.notifier`; arch_test rule (stdlib only); gosec G204 exclusion extended to `internal/notify/notify.go` (removal in R-M3-07); golden byte-identical; CR and SR approved. QA: fake notifier injected in golden/flow tests (pre-existing gap: scenarios ran with `Notify=true`). Next: R-M2-07 config and TokenSource (opus, security-relevant: secrets; Mark approved M2 incl. R-M2-07 at the gate) | done | 2026-11-02 |
| R-M2-07 | `internal/config` (`Load` returns warnings; `Default`, `DefaultPath`, `Finalize`, `ExpandHome` exported; no `os/exec`, no `loc`) and `internal/secret` (`TokenSource`, Keychain lookup, unexported runner seam); time zone resolved in `main`, `NewApp(cfg, loc, tokens, logger, dry)`; arch rules; gosec G204 path `internal/secret/secret.go`. Coverage config 92.1 %, secret 100 %. Golden byte-identical; CR and SR approved. Next: R-M2-08 | done | 2026-11-02 |
| R-M2-08 | `internal/watch` (`Lister`, `PairSummary`, `Near`, `FS`/`OS` read side, `Mover{Rename, Copy, SyncDir}` atomic move unchanged; nil fields = real functions). Decision: coordinator: FS covers ReadDir/ReadFile/Lstat only, the move is not rebuilt on an FS abstraction (`os.SameFile`/`RENAME_EXCL` risk, ADR 0015); arch rule; gosec G30[126]/G304 paths moved to `internal/watch`. Coverage 96.2 %, tests 216 → 231, golden byte-identical, CR and SR approved. Next: R-M2-09 pipeline (opus) | done | 2026-11-02 |
| R-M2-09a | `internal/pipeline` core: flow, retry policy type, archive, fail moved from `app.go`; injection via `Deps` (clock, FS, Mover, Summarizer, Notion port, Notifier port, `*ledger.Ledger`); state helpers merged (follow-up 21); ADR 0006. Decisions: coordinator: split R-M2-09 into 09a/09b; concrete `*ledger.Ledger` accepted; own `Notifier` port (arch rule); `handleError` computes its own backoff. Coverage 93.5 % own / 98.8 % combined, tests 231 → 251, golden byte-identical, CR and SR approved. Incident: a reviewer ran `git stash` and popped it at once; working tree verified intact. Next: R-M2-09b (opus) | done | 2026-11-02 |
| R-M2-09b | Pipeline leftovers: `Client.sleep` replaces `sleepFn`; `appSeams` + `newApp` (zero value in `NewApp`); `newFlowApp` on fakes, `hookNotion` removed; nil-guards for `Notifier`/`DryRun` in `pipeline.New`. Decisions: coordinator: `mustJSON` stays twice, `Normalize` stays a separate call, `FormatTitle`/`deadlineLabel` stay in `internal/notion`, no `watchtest` FS (real temp dirs). Tests 251 → 253, golden byte-identical, review approved | done | 2026-11-02 |
| R-M2-10 | Entry point and wiring: `app.go` deleted; new `internal/cli` (`Env`, `Init` → `Setup`, `NewPipeline`, `Check`, `Run`, `ProcessSingleFile`, `printDryRun`); `cli.Notion` interface; `main.go` only file of package `main` (flags, `loadConfig`, `newEnv`, exit codes); `appSeams` removed; arch rules incl. `TestArchRootPackageOnlyMain`; G304 exclusion path `internal/cli/cli.go`. Decisions: coordinator: two wiring helpers (one would move the `Konfiguration:` line), notify/secret built only in `main`, Run/ticker in `cli`, Notion transport stays a `newEnv` parameter. Tests 253 → 268, coverage 86–100 % per package, golden byte-identical, review approved | done | 2026-11-02 |
| R-M3-01 ⚠ | `llm.base_url` loopback only (S-02, D3): exact lowercase `localhost`, `127.0.0.1`, `::1`, no userinfo, http/https; LLM client without proxy and without redirects (fix round 1: `LOCALHOST` + proxy and 307 reproduced by the reviewer); README troubleshooting row; ADR 0007. Residual risk: `/etc/hosts`. Golden unchanged. Follow-ups in R-M3-02: redirect protection for Notion, base URL in messages as scheme://host:port | done | 2026-11-02 |
| R-M3-02 ⚠ | `notion.base_url` HTTPS only (S-03): http only for exact loopback, no userinfo, value-free start errors; Notion client without redirects (proxy allowed); `domain.RedactURL` in messages and `-check`. Decisions: Mark (2026-11-02) approved `-check` printing the LM Studio address without path (golden G10b, one line); coordinator: `llm.base_url` and `notion.base_url` also reject `?`/`#` (url.Error prints the request URL). Residual: request path in the local log. README rows; ADR 0008; security review approved | done | 2026-11-02 |
| R-M3-03 | Regular files only, no symlink follow (S-04, merged with R-M3-19): `watch.OS.ReadFile` opens with `O_RDONLY\|O_NOFOLLOW\|O_NONBLOCK`, requires `fstat` regular file, reads via `LimitReader` up to 20 MB (`maxDateiBytes`, unchanged); a symlink swapped in after listing → ELOOP, FIFO/folder → `keine reguläre Datei`, nothing read, no hang. Copy-fallback source in `move.go` opened the same way (identity checks kept). Listing already skipped non-regular entries (R-M1-07). gosec G304 path exclusion removed from `.golangci.yml`; line-level `nolint` with German reasons at the remaining opens (ledger file, `syncDir`, `ReadFile`, `copyExcl`, `-file` path). Golden unchanged, no new log lines. Decisions: coordinator: `-file` (`ProcessSingleFile`) still follows a user-given symlink and has no 20 MB limit (explicit CLI action by Mark); hard links into the watch folder not refused (needs the same user writing into the folder, `Nlink > 1` check is speculative). Mark (2026-11-03): iCloud "Desktop & Documents" sync is not active, so dataless iCloud files with `O_NONBLOCK` are not a concern. Security review approved (a symlinked `watch_dir` or parent folder still works; `O_NONBLOCK` has no effect on regular-file reads). Idea (no task): optional `warnOnce` when `ReadFile` fails during listing (silently skipped, as before) | done | 2026-11-03 |
| R-M3-04 | File permissions (S-12, OD-10 new items only): new ledger, `.fehler.txt`, copy-fallback copies 0600; new archive root, `_fehler`, month folders 0700, set explicitly (umask-independent); existing items and renamed export files keep their mode. Ledger `Add` uses `O_EXCL` to detect creation. `mkdirPrivate` duplicated in `internal/ledger` and `internal/watch` (import rules, accepted). gosec G301/G302/G306 exclusions only for `_test.go`. Golden unchanged; security review approved. Residual: Chmod follows symlinks between Mkdir and Chmod. Optional follow-up 25 | done | 2026-11-03 |
| R-M3-05 | Response size limits: `io.LimitReader` (10 MiB, `maxAntwortBytes`) in `internal/notion` and `internal/llm`; oversize or read error on 2xx is a non-transient error without body; error statuses keep their classification; `Models()` errors at status >= 300; `notion.ErrUnklar` for incomplete 2xx `CreatePage` (used in R-M3-17); truncated PATCH reply = append error (warning). Golden unchanged; code and security review approved. Idea (no task): an LLM body read timeout is non-transient (as before); wrapping it in `domain.Transient` would match the CLAUDE.md retry rule; coordinator decided not to change behavior now | done | 2026-11-02 |
| R-M3-07 | Absolute paths and minimal environment (S-10): `/usr/bin/osascript` (`internal/notify`) and `/usr/bin/security` (`internal/secret`) as constants; environment `HOME`, `USER`, `LANG` (each only if set) + `PATH=/usr/bin:/bin`; arguments, escaping, trimming, error texts unchanged. gosec G204 exclusion removed (line-level `nolint` with reason on both `exec.Command` lines). Decisions: coordinator: `minimalEnv` duplicated in both packages (shared package would need an arch rule change); the PATH stubs for `security`/`osascript` in `ci.yml` and the golden setup now only guard against bare-name calls and are kept (removal is optional cleanup). Tests assert exact path and environment; marker stubs verified that no test starts the real binaries. Security review approved. Manual check: follow-up 24 | done | 2026-11-02 |
| R-M3-09 | Config values out of error messages (S-17): `orphan_summary_action muss note, archive oder keep sein`, `create_tasks_for muss me, all oder none sein`, `unbekannte Zeitzone in timezone (IANA-Name erwartet, z. B. Europe/Berlin)` (no wrapping of the `time.LoadLocation` error, it contains the name). Tests assert value absence; golden unchanged; code and security review approved. Remaining messages echo only the `-config` path and unknown key names (outside S-17). Follow-up #1 done | done | 2026-11-02 |
| R-M3-19 | Open export files with O_NOFOLLOW: merged into R-M3-03 (coordinator decision, 2026-11-02); done with R-M3-03 | done | 2026-11-03 |
| R-M3-10 | plist `ThrottleInterval` 60 s in the repo template (S-21, OD-13); `plutil -lint` ok; `docs/launchd.md` row and note; installed plist untouched. Security review approved | done | 2026-11-03 |
| R-M3-08 | IDs in Notion URL paths escaped with `url.PathEscape` (S-16): databases, data_sources, data_sources query, blocks children; `/v1/pages` has no ID; valid UUIDs unchanged, golden byte-identical; tests `TestIDsAreEscapedInPath`, `TestValidUUIDPathUnchanged`. Review approved. Accepted residual risk (OD-12, no change): `.` and `..` are not escaped, a config value `..` could be normalized by a server or proxy | done | 2026-11-03 |
| R-M3-20 ⚠ | German date with underscore/dash/dot and time in file name (`07.01.2026_08-45`) no longer read as ISO date; German date used when it starts before the ISO match (follow-up 22). Decisions: Mark (2026-11-02) approved the fix (output change); coordinator: `DD.MM.YYYY.HH.MM` gets date only (`reDateDE` does not allow `.` before the time), pattern not widened; accepted limit: only the leftmost ISO and German matches are compared (`07.01.2026_08-45 und 2026-11-01` uses the German date). Golden unchanged; code review approved | done | 2026-11-03 |
| R-M3-06 | Total block cap 1000 per note (S-09, OD-11): above it no Notion page, no ledger entry, no retry; files to `_fehler` with `.fehler.txt`, notification if `notify` is on. Golden unchanged; code review approved, fix round 1 done | done | 2026-11-04 |

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
| Coordinator | `ledger.Load` (formerly `LoadLedger`) does not read the gap fields | R-M1-03 |
| Coordinator | TOCTOU recheck on the primary file only | R-M1-05 |
| Coordinator | `EINVAL` is not a fallback trigger | R-M1-04 |
| Coordinator | `go 1.26.0` accepted | R-M1-04 |
| Coordinator | No deletion after an identity mismatch (and after a syncDir error, see above) | R-M1-04 |

## M2 summary (complete 2026-11-02: R-M2-01 to R-M2-10)

| Finding | Closed by |
| --- | --- |
| A-03 | R-M2-01 to R-M2-10 |
| A-04 | R-M2-09a, R-M2-10 |
| A-05 | R-M2-06, R-M2-09a, R-M2-09b, R-M2-10 |
| A-06 | R-M2-01, R-M2-02 |
| A-07 | R-M2-04, R-M2-05, R-M2-07, R-M2-08 |
| A-12 | R-M2-07 |
| A-13 | R-M2-10 |
| A-15 | R-M2-01, R-M2-05 |

Exit criteria (met):
- Package `main` contains only `main.go` (`TestArchRootPackageOnlyMain`).
- Dependency-direction test in `arch_test.go` (`go list -deps`).
- Golden byte-identical over the whole of M2.
- Coverage per package 86–100 %; tests 253 → 268 Test functions after R-M2-10.

Decisions taken in M2:

| By | Decision | Task |
| --- | --- | --- |
| Mark | ADR number 0015 for the package split | R-M2-01 |
| Mark | Approved M2 incl. R-M2-07 (opus, secrets) at the gate | R-M2-07 |
| Coordinator | Ledger API kept (no `Begin`/`Finish` redesign) | R-M2-03 |
| Coordinator | R-M2-09 split into 09a/09b; concrete `*ledger.Ledger` injected | R-M2-09 |
| Coordinator | `watch.FS` read-only plus `Mover` for the atomic move | R-M2-08 |
| Coordinator | `mustJSON` stays twice; `llm.Normalize` stays a separate call; `FormatTitle`/`deadlineLabel` stay in `internal/notion`; no `watchtest` FS | R-M2-09b |
| Coordinator | R-M2-10 upgraded to opus | R-M2-10 |
| Coordinator | Two wiring helpers (`loadConfig`, `newEnv`) instead of one | R-M2-10 |

Incidents:
- R-M2-05: the first report claimed an arch rule that did not exist; caught in verification, rule added in fix round 1.
- R-M2-09a: a reviewer ran `git stash` and popped it at once; working tree verified intact, harmless.

Next: M3 (gate passed 2026-11-02).

## M3 gate decisions (2026-11-02)

| By | Decision | Task |
| --- | --- | --- |
| Mark | M3 approved as planned (20 tasks, incl. security changes: secrets, permissions, plist); tasks marked ⚠ change visible behavior as described in the plan | M3 |
| Mark | German date with underscore time is fixed | R-M3-20 |
| Mark | No duplicate page after an ambiguous `CreatePage` failure: marker in a new optional text column in the Notes database (e.g. "Quelle", created by Mark, name configurable via `notes_properties`); file identifier written there and queried before a retry; without the column behavior stays as today | R-M3-17 |
| Coordinator | M3 lanes run partly in parallel git worktrees (disjoint packages); results are applied to the branch only after review and a green `make check` | M3 |

Open: Mark must create the text column in the Notes database before R-M3-17 is tested live (the coordinator asks him).

## Open follow-ups

| # | Item | Owner | Status |
| --- | --- | --- | --- |
| 1 | Error messages for `orphan_summary_action`, `create_tasks_for` and `timezone` printed the value with `%q` (pre-existing, affects JSON and YAML); fixed in R-M3-09 | open | done (R-M3-09) |
| 2 | Local toolchain go1.25.5 has stdlib vulnerabilities (security review). Recommendation: rebuild with Go 1.26.10 or newer | open | done (R-M0-01) |
| 3 | Migrate the real config to `config.yaml` and update the installed plist | Mark | open |
| 4 | One week of manual testing from 2026-11-01, then set up the launchd service (see `docs/launchd.md`) | Mark | open |
| 5 | `meetingsync -check` exits 0 although a check fails (✗ seen for LM Studio; `Check` returns nil in `internal/cli/cli.go`) | open | planned (R-M3-13) |
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
| 20 | A regular file swapped for a symlink between `ReadDir` and `ReadFile` is still followed (low risk, local only) | open | done (R-M3-03, merged from R-M3-19) |
| 21 | Three copies of "get or create state, `nextTry` = now + 2 min" (`interrupted`, `archiveAt`, `handleError`) | open | done (R-M2-09a) |
| 22 | A German date followed by an underscore and a time (`Weekly 07.01.2026_08-45.txt`) is misparsed: `reDateISO` matches `2026_08-45`, `dateFromName` never tries `reDateDE`, the meeting date falls back to an estimate (pre-existing since v1.0.0, found by QA in R-M2-02). Mark decided at the M3 gate (2026-11-02): fix it | Mark | done (R-M3-20) |
| 23 | Root tests still need exported `pipeline.FileState`, `States()`, `ErrUnterbrochen` and `config.Finalize` (e2e builds its config in code). Move tests into `internal/pipeline`, load e2e config from a file, then unexport | open | planned (R-M4-04) |
| 24 | Manual check at the next real run (launchd): a notification with ü/ö shows correctly and the Keychain token is still read. Under launchd `LANG` was already unset before R-M3-07; the new loss is `__CF_USER_TEXT_ENCODING`. Expected fine (argv is UTF-8). If umlauts are garbled, add `__CF_USER_TEXT_ENCODING` to the `minimalEnv` allowlist in `internal/notify` | Mark | open (R-M3-07) |
| 25 | Optional, not required: existing `_archiv`/`_fehler` folders and the ledger keep their old permissions (meetingsync never changes them). To make them private: `chmod 700` on the folders, `chmod 600` on the ledger | Mark | open (R-M3-04) |
| 26 | Optional, when switching to launchd: the installed plist does not have `ThrottleInterval` yet. Copy the template again to `~/Library/LaunchAgents/`, then `launchctl bootout` and `launchctl bootstrap` (see `docs/launchd.md`) | Mark | open (R-M3-10) |
