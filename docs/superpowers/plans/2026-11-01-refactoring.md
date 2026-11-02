# Refactoring plan for meetingsync

Date: 2026-11-01 (decisions of 2026-11-01 and 2026-11-02 included). Source of truth for the coordinator. Base: `v1.0.0` + `6ab0703`.
Finding IDs (A-xx, S-xx) and decisions (D1-D13): [review report](../../reviews/2026-11-01-architektur-und-security-review.md).

## 1. Goal, guardrails, non-goals

**Goal:** Close the critical error paths, harden security, cut the code into packages with seams, and add tests and CI as a lasting safety net.

### Plan overview

| Milestone | Tasks | Count |
| --- | --- | --- |
| M0 Safety net and tooling | R-M0-01 to R-M0-11 | 11 |
| M1 Correct error paths | R-M1-01 to R-M1-07 | 7 |
| M2 Package split | R-M2-01 to R-M2-10 | 10 |
| M3 Security hardening | R-M3-01 to R-M3-19 | 19 |
| M4 Repo hygiene | R-M4-01 to R-M4-03 | 3 |
| M5 Performance and robustness | R-M5-01 to R-M5-03 | 3 |
| M6 Documentation | R-M6-01 to R-M6-03 | 3 |
| **Total** | | **56** |

| Model | Tasks |
| --- | --- |
| opus | 27 |
| sonnet | 22 |
| haiku | 7 |

New in this version: R-M0-11 (raise the `go` directive, see ADR 0002), R-M3-13, R-M3-14, R-M3-15 (formerly optional R-OPT-02, R-OPT-03, R-OPT-04, now mandatory), R-M3-16 (to-do count, decided 2026-11-02), R-M3-17 (no duplicate page after an ambiguous `CreatePage` failure, decided 2026-11-02). R-M1-07 (follow-up of R-M1-04: archive backoff, regular files only), R-M3-18 and R-M3-19 (follow-ups of R-M1-07, 2026-11-02). R-OPT-01 and R-OPT-05 are rejected, no task.

### Top guardrail: same output and outcome on the normal path

| Area | Stays the same |
| --- | --- |
| Notion | Method, path, headers (without token), body, and the order of requests |
| LLM | Request body including prompt texts (byte-identical) |
| Files | Which file goes where (`_archiv/JJJJ-MM`, `_fehler`), file names, `.fehler.txt` content |
| Ledger | Content of the JSONL lines |
| CLI | stdout of `-check`, `-dry-run`, `-file`; exit codes |
| Log | Texts of the log lines (timestamps excluded) |

Exceptions only with Mark's decision. They are marked in the task as **⚠ behavior change, decided by Mark on 2026-11-01** and get an ADR.

| ⚠ task | Change | Decision | ADR |
| --- | --- | --- | --- |
| R-M1-01, R-M1-02 | Ledger lines with status; "started without URL" → `_fehler` | D1 | 0004 |
| R-M1-04 | New dependency `golang.org/x/sys` for the atomic rename; `go` 1.26 → 1.26.0; deleting own fresh/partial copy | Mark, 2026-11-02 | 0005 |
| R-M3-01 | Abort at start if `llm.base_url` is not loopback | D3 | 0007 |
| R-M3-12 | Note with to-dos for Mark → `Prüfen` flag, notice box, notification | D5 | 0010 |
| R-M0-11 | `go` directive 1.22 → 1.26: stricter TLS/x509/url/http defaults | Mark, 2026-11-01 (OD-01 revisited) | 0002 (extended) |
| R-M3-13 | `-check` exits with a code other than 0 when a check shows ✗ (G10 changes) | D7 (OD-17) | 0012 |
| R-M3-14 | `-file` without `-dry-run` writes a ledger entry | D8 (OD-18) | 0013 |
| R-M3-15 | `llm.api_key` can come from the Keychain | D9 (OD-20) | 0014 |
| R-M3-16 | To-do count in log and notification only for `fuer_mich` (G16b) | Mark, 2026-11-02 (OD-23) | open: no ADR assigned yet |
| R-M3-17 | Look up an existing page before retrying after an ambiguous `CreatePage` failure | Mark, 2026-11-02 (OD-05 residual risk) | 0004 (update) |
| R-M4-02 | Git history rewritten, tag `v1.0.0` recreated | D2 | 0011 |

Error paths (M1, M3) may get new log lines and error messages. This goes into the changelog. Golden tests for the normal path stay unchanged.

### Guardrail: Language

- All documentation is in simple, plain English: short sentences, no unneeded technical words.
- This covers `docs/`, ADRs, README, CLAUDE.md, release notes, changelog, and reports from agents.
- Code comments, log texts and Notion content stay German.
- German log texts and Notion terms that docs quote stay word for word in backticks, with a short English explanation where needed. Examples: `Konfiguration: <path>`, `_fehler` (error folder), `Prüfen` (check flag).
- The docs-writer checks this in every task (see DoD point 4).

**Non-goals:**
- No new features. No new CLI flags. The behavior changes in the ⚠ table are the only exceptions.
- No new prod dependencies. Stays: stdlib + `go.yaml.in/yaml/v3` + `golang.org/x/sys` (approved by Mark, 2026-11-02, only for the atomic rename in R-M1-04, ADR 0005). Tools for CI/lint that do not go into the binary are allowed (golangci-lint, govulncheck), with no entry in `go.mod`.
- No switch to `log/slog` (rejected, OD-21).
- No log rotation and no reduced log content (rejected, OD-19).
- No change to Notion databases, to `~/.config/meetingsync/`, to the watch folder, or to `~/bin/meetingsync`.
- No optimization of `Ledger.mtimes` (linear, fine at today's size; A-19 covers only the maps).

## 2. Safety strategy (before any rebuild)

### 2.1 Golden harness

| Part | Definition |
| --- | --- |
| Fake LM Studio | `httptest` server on 127.0.0.1; scriptable answers per scenario (success, 503, 4xx, `finish_reason=length`, broken JSON) |
| Fake Notion | Own `httptest` server; data source schema, query, `POST /v1/pages`, `PATCH /v1/blocks/{id}/children`; scriptable errors |
| Recorder | Per request: server, method, path + query, headers, body. `Authorization` is recorded as `Bearer <token>` if it is the test token, else `<present>`. Notion bodies: JSON with sorted keys, indented. LLM bodies: key order as sent (schema order matters), indented. Nothing else changes. Binding rule: ADR 0003 |
| File tree | After the run: relative paths and SHA-256 of every file under the temp watch folder (including `_archiv`, `_fehler`, `.fehler.txt`, ledger). No file permissions |
| Ledger | Content line by line |
| stdout/log | Complete; logger without timestamp, or timestamp prefix removed by regex |
| Exit code | For CLI scenarios |
| Storage | `testdata/golden/<scenario>/{requests.json, tree.txt, ledger.jsonl, stdout.txt, log.txt, exit.txt}` |
| Update | `go test -run Golden ./... -update` |

**Normalization**, final list (everything else stays raw):

| Field | Replaced by |
| --- | --- |
| Temp paths | `<tmp>` |
| Host:port of the fakes | `<llm>`, `<notion>` |
| Ledger field `at`, first line of `.fehler.txt` | `<now>` |
| Duration in "LLM fertig nach …" (LLM done after …) | `<dur>` |
| Log timestamps | removed |

**Determinism:**
- Fixed file mtimes with `os.Chtimes` (date in the past, month different from the current month).
- `cfg.Timezone = "Europe/Berlin"`. `TestMain` sets `time.Local = time.UTC` and the proxy variables before `m.Run` (`HTTP_PROXY`/`HTTPS_PROXY` = `http://127.0.0.1:1`, `NO_PROXY` = `127.0.0.1,localhost`; no real network). CLI children get `TZ=UTC`. Shows that only the config zone counts. See ADR 0003.
- `now` is passed to `Pass(ctx, now)` (existing seam). No clock interface until M2. `time.Now()` calls are covered by the normalization.
- `cfg.Notify = false`, `NOTION_TOKEN` set with `t.Setenv`. Tests call neither `security` nor `osascript`.
- CLI scenarios run by re-exec of the test binary (`TestMain`, an environment variable starts `main()`), no `go build`.

**Adapter:** Scenarios call the code only through `golden_adapter_test.go`. M2 tasks may change the adapter. They may not change scenario definitions or golden files.

### 2.2 Scenarios

| ID | Scenario | Checks, among others |
| --- | --- | --- |
| G01 | `.srt` transcript alone | Prompt, Notion payload, archive, ledger, log lines |
| G02 | `.md` summary alone, `orphan_summary_action=note` | Orphan note, reason for `Prüfen` |
| G03 | like G02 with `archive` | Archive only, no request |
| G04 | like G02 with `keep` | File stays, warning once |
| G05 | Pair transcript + summary (same name stem), `use_summary_as_context` on/off | Pairing, both files archived |
| G06 | Pair within time window `pair_window_seconds` | Pairing without same stem |
| G07 | `.json` with segments | Parser, duration |
| G08 | Long transcript > `max_chunk_chars` | Chunk prompts, order |
| G09 | `-dry-run -file` | stdout, no Notion writes, no file move |
| G10 | `-check`: all ✓; LM Studio off; model missing; Notes DB 404 | stdout, exit code (today 0 with ✗ LM Studio) |
| G11 | LLM 503 up to `max_attempts` → `_fehler` (several `Pass` calls with advancing `now`) | Backoff log lines, `.fehler.txt` |
| G12 | Permanent error (LLM 400 without schema fallback, empty file) → `_fehler` | 2 min interval, counter |
| G13 | Ledger hit (same content, other name); summary close to an already processed transcript | "↺"/"📎" path |
| G14 | Notion limits: > 100 blocks, text > 2000 characters, `include_transcript` | POST + PATCH batches, splits |
| G15 | Project matching: hit (upper/lower case), no hit, Projects DB not readable | Relation in note and task |
| G16 | Tasks: `create_tasks_for` me/all/none, with/without deadline | Task payloads |

**Rules:**
- During M2 no file under `testdata/golden/` changes. Check: `git diff --exit-code -- testdata/golden` after `make check`.
- Golden files change only in ⚠ tasks. The code-reviewer and spec-reviewer explain each diff.
- New golden files (for example notification texts from M2 on, via the notifier seam) are allowed if existing ones stay unchanged.
- Mutation check (R-M0-05): change one character in the prompt or one property name → golden test turns red.

### 2.3 Coverage target

| When | Target |
| --- | --- |
| End of M0 | total ≥ 80 %; no function from the review's 0 % list is still at 0 % (exception `notify` until M2) |
| End of M2 | `internal/ledger`, `internal/pipeline`, `internal/notion`, `internal/watch` each ≥ 85 % |

Confirmed by Mark (OD-16: yes). Measure with: `go test -coverprofile=cover.out ./... && go tool cover -func=cover.out`.

## 3. Definition of Done for every task

1. `make check` is green (from R-M0-02: vet + lint + test -race + vuln).
2. `git diff --exit-code -- testdata/golden` is empty (exception: ⚠ tasks, diff explained).
3. `go.mod`/`go.sum`: no new prod dependency (`go list -m all` unchanged, except R-M0-01 and R-M1-04: `golang.org/x/sys`).
4. Docs and ADR in the same task (docs-writer), in plain English (Language guardrail): changelog line, `docs/progress.md`, ADR as in table 7.
5. Reviews: code-reviewer always; security-reviewer for secrets, network, files, processes or supply chain (column "SR"); qa against the acceptance criteria.
6. No build to `~/bin`, no access to `~/.config/meetingsync/`, the watch folder or `~/Library/LaunchAgents`.
7. Report in the format from `CLAUDE.md`, with the acceptance commands in EVIDENCE.

## 4. Running operation

- Mark tests by hand for one week from 2026-11-01 (`docs/launchd.md`). The service is not set up.
- The binary in `~/bin/meetingsync` is rebuilt only when Mark asks for it. `make build` writes to `./bin/meetingsync` (gitignored).
- Tests run only in `t.TempDir()`.
- Before the first rebuild after M1: tell Mark that the ledger gets new lines (R-M1-01) and that `v1.0.0` must ignore them.
- Before the first rebuild after R-M3-13: tell Mark that `-check` now has a non-zero exit code on ✗ (matters for scripts).

## 5. Milestones

Model choice: haiku = mechanical; sonnet = regular work; opus = security, parsing, concurrency, data model/persistence, file/ledger atomicity, or errors with high follow-up cost. Reviews: CR = code-reviewer, SR = security-reviewer, QA = qa.

### M0 Safety net and tooling

Goal: behavior frozen, tooling and CI active, before any production code is rebuilt.
Exit: all G01-G16 green and checked in; `make check` (vet, lint, race, vuln) green locally and in CI; coverage ≥ 80 %.
In M0 production code changes only through lint fixes (R-M0-03), the version variable (R-M0-02) and minimal test seams with no behavior change (R-M0-09).

#### R-M0-01 Pin the toolchain
- Goal: build with Go ≥ 1.26.10, CVEs from S-01 gone.
- Acceptance: `go.mod` contains `toolchain go1.26.10`; the `go` directive stays `go 1.22` (OD-01, decided: proposal; raising it is a separate task, because the GODEBUG defaults follow the `go` directive of the main module); `go version -m ./bin/meetingsync` shows go1.26.10; `go run golang.org/x/vuln/cmd/govulncheck@<pinned> ./...` → 0 reachable findings; `go mod verify` ok; golden tests (once they exist) unchanged.
- Scope: `go.mod`, `CLAUDE.md` (line "go 1.22"), `README.md` (requirements).
- Out of scope: code changes, CI.
- Depends on: none. Findings: S-01, A-09.
- Model: opus, supply-chain decision with effect on TLS/HTTP defaults.
- Reviews: CR, SR, QA. ADR 0002 (toolchain part).

#### R-M0-02 Makefile targets
- Goal: the same commands locally and in CI.
- Acceptance: targets `build` (to `./bin/meetingsync`, version via `-ldflags "-X main.version=$(git describe --tags --always --dirty)"`), `lint`, `test` (`go test -race ./...`), `vuln` (govulncheck, pinned version), `check` = vet + lint + test + vuln; `make check` green; `bin/` in `.gitignore`; the version is visible only through `go version -m` (OD-02, decided: proposal). Only the variable is added, no new output.
- Scope: `Makefile`, `.gitignore`, `main.go` (only `var version`).
- Out of scope: new flags, log lines.
- Depends on: R-M0-01. Findings: A-09.
- Model: haiku, boilerplate.
- Reviews: CR, QA.

#### R-M0-03 golangci-lint
- Goal: lint config and 0 findings.
- Acceptance: `.golangci.yml` (linters: govet, errcheck, staticcheck, unused, ineffassign, gosec with justified exceptions); version pinned (Makefile + CI); `make lint` → 0 findings; the 12 known findings fixed. errcheck fixes change no behavior (explicit ignore with a comment pointing to the M1 task); real error handling only in M1; golden unchanged.
- Scope: `.golangci.yml`, `*.go` (lint fixes only), `Makefile`.
- Out of scope: behavior changes, refactoring.
- Depends on: R-M0-02. Findings: A-09, A-11 (marker only).
- Model: sonnet, regular; check every spot for neutrality.
- Reviews: CR, QA.

#### R-M0-04 CI, Dependabot, SECURITY.md
- Goal: every push/PR runs `make check` on macOS.
- Acceptance: `.github/workflows/ci.yml` on `macos-latest` (or a pinned version), `permissions: contents: read`, actions pinned by commit SHA, `actions/setup-go` with `go-version-file: go.mod`, steps vet/lint/test -race/vuln; CI calls `make` targets, tool versions are defined only in the Makefile (no duplicate pins); PATH stubs for `security` and `osascript` that write a marker and exit with 1, the job fails if a marker exists; CI checks that `go version` is go1.26.10 and prints `go env GOSUMDB GOFLAGS GOTOOLCHAIN` (fails if the checksum database is off, `-insecure` is set, or the toolchain is `local`); govulncheck also runs on a schedule (`schedule:` trigger), because the vulnerability database changes daily; `.github/dependabot.yml` for `gomod` and `github-actions` (weekly); `SECURITY.md` (how to report, supported version); CI run green (link in the report).
- Scope: `.github/`, `SECURITY.md`.
- Out of scope: GitHub settings (Dependabot alerts, branch protection, signed tags): a task for Mark, OD-15.
- Depends on: R-M0-03. Findings: S-07, A-09.
- Model: opus, supply chain (pins, token permissions).
- Reviews: CR, SR, QA. Extend ADR 0002 (CI/supply chain).

#### R-M0-05 Golden harness
- Goal: the infrastructure from section 2.1, first scenario G01.
- Acceptance: fakes, recorder, normalizer, `-update` flag, adapter, re-exec for CLI; G01 green; mutation check documented (prompt character and property name changed → red, then reverted); `go test -count=2 -run Golden ./...` is stable twice in a row; run with `TZ=UTC` and `TZ=Pacific/Auckland` gives the same result.
- Scope: `golden_test.go`, `golden_adapter_test.go`, `testdata/golden/`.
- Out of scope: prod code.
- Depends on: R-M0-01. Findings: A-05, A-08.
- Model: opus, the normalization decides whether all later golden tests detect changes at all.
- Reviews: CR, QA. ADR 0003.

#### R-M0-06 Golden scenarios: flow
- Goal: G02-G08, G13-G16.
- Acceptance: each scenario has its own directory under `testdata/golden/`; `go test -run Golden -count=2 ./...` green; no prod change.
- Scope: `golden_test.go`, `testdata/golden/`.
- Depends on: R-M0-05. Findings: A-08.
- Model: sonnet, scenarios follow a fixed pattern.
- Reviews: CR, QA.

#### R-M0-07 Golden scenarios: CLI and errors
- Goal: G09-G12.
- Acceptance: as R-M0-06; G10 records today's exit code 0 with ✗ LM Studio (characterization, not an opinion); G11 reaches `_fehler` with advancing `now`, without real waiting.
- Scope: as R-M0-06.
- Depends on: R-M0-05. Findings: A-08, A-14.
- Model: sonnet.
- Reviews: CR, QA.

#### R-M0-08 Unit tests: flow
- Goal: close the gaps in ledger reload (including broken lines, line > 64 KB), pass gating `nextTry`, `near` wait window, orphan archive/keep, `Run` with a cancelled context.
- Acceptance: `LoadLedger` ≥ 90 %, `Run` > 0 %; `go test -race ./...` green.
- Scope: new `*_test.go`.
- Depends on: R-M0-05 (shared fakes). Findings: A-08.
- Model: sonnet.
- Reviews: CR, QA.

#### R-M0-09 Unit tests: clients
- Goal: Notion retry/backoff/`Retry-After`/`sleepCtx`, non-idempotent requests without retry; LLM parse retry, `finish_reason=length`, `chunkSystemPrompt`, schema fallback, `Models`.
- Acceptance: `Notion.do` ≥ 85 %, `backoff`/`retryAfter`/`sleepCtx`/`Models`/`chunkSystemPrompt` > 0 %; total test time < 10 s; sleep seam (if needed) as a package variable, default = today's function.
- Scope: new `*_test.go`; `notion.go` only the seam.
- Depends on: R-M0-03. Findings: A-08, A-15.
- Model: sonnet.
- Reviews: CR, QA.

#### R-M0-10 Flake and hermetic build
- Goal: e2e archive month from `ModTime` instead of `time.Now()`; acceptance test without `go build`.
- Acceptance: e2e_test.go uses `f.ModTime.In(loc)`; `config_yaml_acceptance_test.go` starts `main()` by re-exec; test with `GOFLAGS=-mod=readonly GOPROXY=off` green; test with a simulated month change (mtime on the 31st of the previous month) green.
- Scope: `e2e_test.go`, `config_yaml_acceptance_test.go`.
- Depends on: R-M0-05 (re-exec helper). Findings: A-16.
- Status note (2026-11-01): the e2e archive-month flake is already fixed in R-M0-05. The expected month comes from the file mtime in the config time zone.
- Model: sonnet.
- Reviews: CR, QA.

#### R-M0-11 Raise the go directive to 1.26
⚠ Behavior change (only crypto and protocol defaults), decided by Mark on 2026-11-01 (OD-01 revisited).
- Goal: stricter TLS, x509, url and http defaults (see ADR 0002, "Residual risk").
- Acceptance:
  - `go.mod` has `go 1.26`; the `toolchain` line stays at go1.26.10 or newer.
  - `go version -m` of the built binary shows a DefaultGODEBUG without the legacy values (`tls3des=1`, `tlssha1=1`, `tlsmlkem=0`, `rsa1024min=0`, `x509negativeserial=1`, `x509usepolicies=0`, `urlmaxqueryparams=0`, `httpcookiemaxnum=0`).
  - All golden tests are green and unchanged (`git diff --exit-code -- testdata/golden`).
  - `make check` is green.
  - Manual `-check` run against Notion and LM Studio, by Mark (or the coordinator with Mark's config) ✓.
  - Changed language semantics checked and noted in the report (loopvar of Go 1.22+ is already active).
- Scope: `go.mod`, ADR 0002 (close the residual risk), version line in `CLAUDE.md` and `README.md`.
- Out of scope: code changes.
- Depends on: R-M0-05, R-M0-06, R-M0-07. Findings: S-01 (residual).
- Model: opus, supply chain and TLS behavior.
- Reviews: CR, SR, QA. Extend ADR 0002.

### M1 Correct error paths

Goal: no duplicates, no silent losses, no overwritten files.
Exit: A-01, A-02, A-11, A-12, S-13, S-14, S-15 closed; golden unchanged except `ledger.jsonl` (⚠ D1); crash tests (abort after each step) green; errcheck `check-blank: true` enabled in `.golangci.yml`; every remaining `_ =` has a reason comment.

#### R-M1-01 Ledger format with status ⚠
⚠ Behavior change, decided by Mark on 2026-11-01 (D1).
- Goal: states in the ledger, fsync, compatible loading.
- Acceptance:
  - Lines get a status field. State model per OD-03 (decided: proposal): `begonnen` (started), `fertig` (done), `verworfen` (discarded); the last line per hash wins.
  - Existing lines without status count as `fertig` (test with a v1.0.0 ledger from `testdata/`).
  - `Has` and `HasNear` count only `fertig`.
  - "begonnen" lines are not counted as processed by the v1.0.0 load logic (test against a copied v1.0.0 `LoadLedger`, for example hash in its own field).
  - `Add` calls `f.Sync()`; errors from Write/Sync/Close are returned.
  - Golden: only `ledger.jsonl` changes, diff explained.
- Scope: ledger code (app.go:685-752), tests, `testdata/`.
- Out of scope: callers in `handle` (R-M1-02).
- Note: Ledger characterization tests in `app_flow_test.go` are marked (block "Einordnung für R-M1-01"); adapt them deliberately.
- Depends on: M0. Findings: A-01, A-11, S-13.
- Model: opus, persistence and backward compatibility.
- Reviews: CR, SR, QA. ADR 0004.

#### R-M1-02 Crash-safe flow ⚠
⚠ Behavior change, decided by Mark on 2026-11-01 (D1).
- Goal: `begonnen` before `CreatePage`, `fertig` with URL after it; restart rule.
- Acceptance:
  - If `Add(begonnen)` fails → no Notion request, the error counts as an attempt.
  - If `Add(fertig)` fails → error logged, hash kept in memory as done, no second page in the same process.
  - A `begonnen` found at load time without `fertig`: the file with this hash (and its partner) → `_fehler`, `.fehler.txt` with a note "Notion page may exist – please check in Notion" (final German text in the task). Then set the status per OD-03, so a file that Mark moves back is processed again.
  - In-process error from `CreatePage`: per OD-03/OD-05. Per OD-05 (decided: proposal) a Notion 5xx on `POST /v1/pages` stays transient until data exists; so this behavior stays unchanged.
  - Crash tests: abort after `Add(begonnen)`, after `CreatePage`, after `createTasks`, after `Add(fertig)` → in each case no second page after restart.
  - Golden normal path: only `ledger.jsonl`.
- Scope: `app.go` (`handle`, `process`, `fail`), tests.
- Out of scope: Notion DB changes, task details (R-M1-03).
- Depends on: R-M1-01. Findings: A-01, A-11, S-13.
- Model: opus, ledger atomicity.
- Reviews: CR, SR, QA. Extend ADR 0004.

#### R-M1-03 Do not lose tasks and remaining blocks silently
- Goal: a context cancel in `createTasks` stops the loop; missing tasks and blocks that were not appended are recorded.
- Acceptance: `ctx.Err() != nil` → no more task requests; the number of missing tasks and remaining-block errors goes to the log and to the `fertig` entry (field name in the task); the note is archived as today; whether the missing parts are made up later is decided by OD-04 (decided: proposal: only record, do not make up).
- Scope: `app.go` (`createTasks`, `process`), `notion.go` (`CreatePage` returns remaining-block errors), tests.
- Depends on: R-M1-02. Findings: A-02, A-11.
- Model: opus, error path with persistence.
- Reviews: CR, QA.

#### R-M1-04 moveFile without overwriting ⚠
⚠ Delivered 2026-11-02 with a different technique than first planned (`RenamexNp` instead of `os.Link` + `os.Remove`), see ADR 0005. The acceptance text below is the original plan.
- Goal: exclusive target, sync in the copy path, errors from `.fehler.txt` logged.
- Acceptance: the target is never overwritten (same volume: `os.Link` + `os.Remove`; otherwise copy with `O_CREATE|O_EXCL`, `Sync`, then `Remove`); on `EEXIST` the next name `stem (n).ext` as today; test with a target created beforehand and a target created in parallel; test for the fallback without hardlink support (seam); normal-path golden (`tree.txt`) unchanged.
  - No accumulating copies (deferred from R-M1-02 review): if the copy fallback copies successfully but `Remove(src)` fails, the restart rule (ADR 0004, no backoff) must not leave one more copy in `_fehler` on every pass, and the log line `letzter Lauf wurde beim Anlegen in Notion unterbrochen …` must not repeat every poll. Either backoff for the restart rule or no copy fallback when the source cannot be removed; test with a failing `Remove`.
  - OPEN for the gate: deleting meetingsync's own fresh copy in `_fehler` would be a deletion and needs Mark's approval (Global Constraint). Ask before choosing an option that deletes it.
- Scope: `moveFile`, `fail` in `app.go`, tests.
- Depends on: M0. Findings: S-14, A-11.
- Model: opus, file atomicity.
- Reviews: CR, SR, QA.

#### R-M1-05 TOCTOU recheck
- Goal: archived file = processed file.
- Acceptance: before `archive` after processing, stat again (size, mtime); if different → do not archive, warn, the file stays (the next pass sees the new hash); test: file changed during the LLM run.
- Scope: `app.go` (`handle`, `archive`), tests.
- Depends on: R-M1-04. Findings: S-15.
- Model: opus, file atomicity.
- Reviews: CR, SR, QA.

#### R-M1-07 Archive backoff and regular files only
- Goal: failures in `archive()` do not repeat every poll; only regular files are picked up.
- Acceptance:
  - (a) `archive()` failures (`Remove(src)` fails, any `moveFile` error) set a 2-min `nextTry` backoff like the restart rule on all archive paths (`Has`, processed, `HasNear`, orphan `archive`); not counted against `max_attempts`; state cleared on success. In `Pass` the retry gate for summaries now runs before `HasNear`.
  - (b) `listEntries` skips anything that is not a regular file (symlinks, FIFOs, devices) before reading, logged once per path: `<name>: keine reguläre Datei (z. B. Symlink) → übersprungen`. Also fixes a hang: a FIFO with a supported extension blocked `ReadFile`.
  - (c) decided by Mark, 2026-11-02: a failed folder fsync on the target volume after a synced copy warns and the move continues; `fail()` uses the same path (`.fehler.txt` still written).
  - Pass-level tests over 3 passes count files and log lines; golden unchanged.
- Scope: `app.go` (`archive`, `listEntries`), tests.
- Depends on: R-M1-05 (both change `app.go`, sequential). Findings: follow-up of R-M1-04.
- Status: done 2026-11-02. ADR 0004, ADR 0005. Follow-ups: R-M3-18, R-M3-19, and the shared retry-state helper in R-M2-09.
- Model: opus, file atomicity and retry state.
- Reviews: CR, SR, QA.

#### R-M1-06 Config validation
- Goal: clamps are visible, unknown keys are reported, no run without a timeout.
- Acceptance: `LoadConfig` returns warnings, `main` logs them; clamps (`poll_interval_seconds`, `max_attempts`, `max_chunk_chars`, `timeout_minutes`) each give one warning; `timeout_minutes` < 1 is clamped to 1 with a warning (OD-05, decided: proposal); unknown keys in JSON and YAML → warning with the key name, without the value (rule from ADR 0001), no abort; `config.example.yaml` and `config.example.json` give 0 warnings; golden unchanged.
- Scope: `config.go`, `main.go`, `config_test.go`.
- Depends on: M0. Findings: A-12, S-19.
- Model: sonnet, validation without persistence.
- Reviews: CR, SR, QA.

### M2 Package split (pure rebuild)

Goal: target architecture from the review, step by step (strangler). Every task leaves the program runnable.
Exit: `package main` contains only the entry point; dependency direction checked by a test (`go list -deps`, no import of `pipeline` from infra packages, `domain` without project imports); `git diff --exit-code -- testdata/golden` empty over the whole milestone; coverage targets at end of M2.
Logging stays `*log.Logger` with identical texts. Named constants (A-17) and readability (A-18) are done in the package task they affect, values unchanged.

#### R-M2-01 domain and error classes
- Goal: `internal/domain` with `MeetingNote`, `Thema`, `Action`, `Naechstes`, `Timing`, `NoteMeta`, `Kind`, error class transient (`Transient`, `IsTransient`).
- Acceptance: `domain` imports only stdlib; all callers switched; JSON tags unchanged (golden); a test for the dependency direction (the M2 exit criterion) is created.
- Scope: new `internal/domain/`, changes in `*.go`.
- Depends on: M1. Findings: A-03, A-06, A-15.
- Model: opus, data model and error classes decide the retry semantics.
- Reviews: CR, QA. ADR 0005.

#### R-M2-02 transcript
- Goal: `classify.go` → `internal/transcript` (classify, parser, `meetingTiming`), pure functions; `*Config` replaced by an options struct.
- Acceptance: package without I/O; `classify_test.go` moved along; golden unchanged.
- Scope: `classify.go`, `classify_test.go`, callers.
- Depends on: R-M2-01. Findings: A-03, A-06.
- Model: sonnet, a move without logic change, golden protects it.
- Reviews: CR, QA.

#### R-M2-03 ledger
- Goal: ledger from `app.go` to `internal/ledger`.
- Acceptance: API `Load`, `Begin`, `Finish`, `Has`, `HasNear` (final names in the task); tests from R-M0-08/R-M1-01 moved along; golden unchanged.
- Scope: `app.go` (ledger part), new `internal/ledger/`.
- Depends on: R-M2-01. Findings: A-04.
- Model: sonnet, a move; the persistence logic comes from R-M1-01.
- Reviews: CR, QA.

#### R-M2-04 notion with PropertyKind
- Goal: `notion.go`, `blocks.go`, `noteProps`, the property part of `createTasks` and the schema check from `Init` → `internal/notion`; typed `PropertyKind` instead of string literals; `PageWriter` interface.
- Acceptance: 0 string-literal comparisons of property types outside the mapping function; payload byte-identical (golden G01-G16); no `url.PathEscape` yet (R-M3-08).
- Scope: `notion.go`, `blocks.go`, `app.go` (Init, noteProps, createTasks), new `internal/notion/`.
- Depends on: R-M2-01. Findings: A-07, A-04, A-15.
- Model: opus, data model of the Notion payload, every deviation ends up in Notion.
- Reviews: CR, SR, QA.

#### R-M2-05 llm
- Goal: `llm.go` → `internal/llm` behind `Summarizer`; prompts, schema, chunking in the package.
- Acceptance: prompt texts byte-identical (golden); the HTTP error type stays in the package, classification through `domain`.
- Scope: `llm.go`, new `internal/llm/`.
- Depends on: R-M2-01. Findings: A-03, A-15.
- Model: sonnet, a move; golden checks prompt and parsing.
- Reviews: CR, QA.

#### R-M2-06 notify
- Goal: `notify` → `internal/notify` behind `Notifier`.
- Acceptance: call arguments unchanged (test with a fake runner); a new golden `notify.txt` per scenario is allowed.
- Scope: `app.go` (`notify`), new `internal/notify/`.
- Depends on: R-M2-01. Findings: A-05.
- Model: haiku, small mechanical extraction.
- Reviews: CR, SR, QA.

#### R-M2-07 config and TokenSource
- Goal: `internal/config` without `exec` and without the `loc` field; Keychain access as a `TokenSource` outside the config layer.
- Acceptance: order NOTION_TOKEN → Keychain → `notion.token` unchanged (tests per branch with a fake runner); error messages unchanged; the time zone is resolved in `main` and injected; `LoadConfig` returns warnings explicitly (e.g. `(cfg, warnings, err)` or exported `Warnings()`); the unexported `Config.warnings` field from R-M1-06 is removed; `collectUnknown` must handle pointer-to-struct fields if any are introduced.
- Scope: `config.go`, `config_test.go`, `config_yaml_acceptance_test.go`, `main.go`.
- Depends on: R-M2-01. Findings: A-12, A-03.
- Model: opus, secrets.
- Reviews: CR, SR, QA.

#### R-M2-08 watch with FS interface
- Goal: `listEntries`, `isTempName`, `pairSummary`, `near`, `moveFile` → `internal/watch`; FS interface (ReadDir, Lstat, ReadFile, Link/Rename, exclusive Create).
- Acceptance: unit tests with an in-memory FS for stability, pairing, temp names; the real FS implementation keeps the behavior from R-M1-04; golden unchanged.
- Scope: `app.go` (files part), new `internal/watch/`.
- Depends on: R-M2-01, R-M1-04. Findings: A-04, A-05.
- Model: opus, file atomicity behind a new abstraction.
- Reviews: CR, SR, QA.

#### R-M2-09 pipeline
- Goal: `handle`, `handleError`, `process`, `archive`, `fail`, `activeProjects` → `internal/pipeline`; Clock, FS, Summarizer, PageWriter, Notifier, Ledger injected; retry policy as its own type.
- Acceptance: no `time.Now()` and no `os.*` in `pipeline`; the three copies of "get or create the file state, `nextTry` = now + 2 min" (`interrupted`, `archiveAt`, `handleError`) are merged into one helper (follow-up of R-M1-07); retry tests use the constructor (no struct literal like app_retry_test.go:26); golden unchanged; coverage of `pipeline` ≥ 85 %.
  - Replace the temporary package variable `sleepFn` (notion.go, from R-M0-09) by dependency injection.
  - Switch the test helper `newFlowApp` (app_flow_test.go, builds App with notion=nil) to the new interface fakes.
- Scope: `app.go`, `app_retry_test.go`, new `internal/pipeline/`.
- Depends on: R-M2-02 to R-M2-08. Findings: A-04, A-05.
- Model: opus, state, retry and ledger order.
- Reviews: CR, SR, QA. ADR 0006.

#### R-M2-10 Entry point and wiring
- Goal: `App` dissolved; `Pass`/`Run`, `Check`, dry-run output, `ProcessSingleFile` moved to their place; entry point per OD-06.
- Acceptance: OD-06 option A (decided: proposal): `main.go` in the module root stays the only `package main`; `go build -o ~/bin/meetingsync .` works unchanged; README/CLAUDE.md/plist unchanged. (Option B `cmd/meetingsync` is not chosen.) stdout/exit codes of `-check`, `-dry-run`, `-file` unchanged (G09, G10); `e2e_test.go` runs through the adapter.
- Scope: `main.go`, `app.go` (rest, then deleted), tests.
- Depends on: R-M2-09. Findings: A-03, A-04, A-13.
- Model: sonnet, wiring.
- Reviews: CR, QA.

### M3 Security hardening

Goal: enforce trust boundaries; close the decided CLI and secrets changes.
Exit: S-02, S-03, S-04, S-06, S-08, S-09, S-10, S-12 (permissions), S-16, S-17, S-18, S-20, S-21, A-14 closed; golden unchanged except R-M3-12 (scenarios with `fuer_mich`), R-M3-13 (`exit.txt` of G10 with ✗), R-M3-14 (new scenario) and R-M3-16 (count line of G16b); R-M3-17, R-M3-18 and R-M3-19 leave the golden outputs unchanged.

#### R-M3-01 llm.base_url loopback only ⚠
⚠ Behavior change, decided by Mark on 2026-11-01 (D3).
- Goal: the transcript does not leave the Mac.
- Acceptance: the host from `url.Parse` must be `localhost`, `127.0.0.1` or `::1`. Otherwise abort at start with a message without the URL value (text in the task, for example "llm.base_url must point to this Mac (localhost, 127.0.0.1 or ::1)", in German as a log text); tests: `http://localhost:1234/v1` ok, `http://[::1]:1234` ok, `http://192.168.1.5`, `http://localhost.evil.com`, `http://user:pw@localhost`, `ftp://localhost` → error; golden unchanged (fakes on 127.0.0.1). Precondition met: Mark's `llm.base_url` is `http://localhost:1234/v1` (OD-08, answered). Not blocked.
- Scope: `internal/config`, tests, README (table in "When something goes wrong").
- Depends on: M2. Findings: S-02.
- Model: opus, security.
- Reviews: CR, SR, QA. ADR 0007.

#### R-M3-02 notion.base_url HTTPS only
- Goal: the token is never sent in clear text.
- Acceptance: scheme `https` required; `http` only for loopback (tests); userinfo in the URL → error; message without the value; the log line llm.go:185 no longer contains userinfo; abort at start (OD-07, decided: proposal).
- Scope: `internal/config`, `internal/llm` (error message), tests.
- Depends on: R-M3-01. Findings: S-03.
- Model: opus, secrets.
- Reviews: CR, SR, QA. ADR 0008.

#### R-M3-03 Skip symlinks
- Goal: process only regular files in the watch folder.
- Acceptance: symlink to a file and to a folder → skipped, one warning (text in the task), no read; test with a symlink to a file outside the watch folder; golden unchanged; gosec G304 exclusion for app.go removed from `.golangci.yml`. OD-09 (decided: proposal): no symlinks are in use, so skip them.
- Scope: `internal/watch`, tests.
- Depends on: M2. Findings: S-04.
- Model: opus, file security.
- Reviews: CR, SR, QA.

#### R-M3-04 File permissions
- Goal: new ledger and `.fehler.txt` files 0600; newly created archive, error and month folders 0700; the copy path creates 0600.
- Acceptance: tests check the mode (independent of umask, by `Chmod` after creation); existing files/folders are left as they are (OD-10, decided: proposal: new ones only); golden unchanged (permissions are not part of `tree.txt`); gosec G301/G302/G306 exclusions removed (prod files).
- Scope: `internal/ledger`, `internal/watch`, `internal/pipeline`, tests.
- Depends on: R-M3-03. Findings: S-12.
- Model: opus, permissions.
- Reviews: CR, SR, QA.

#### R-M3-05 Limit response sizes
- Goal: `io.LimitReader` for LLM and Notion responses, `Models()` checks the status code.
- Acceptance: limit per client as a constant (value in the task, well above real responses); exceeding it → own error; `Models()` at ≥ 300 → error with status; tests with an oversized response; read errors of response bodies are no longer discarded (notion.go `data, _ := io.ReadAll`, llm.go): a truncated 2xx response is treated safely (security review: a truncated 2xx answer to POST /v1/pages risks a duplicate); golden unchanged.
- Scope: `internal/llm`, `internal/notion`, tests.
- Depends on: M2. Findings: S-08, A-15.
- Model: sonnet, clearly bounded limit.
- Reviews: CR, SR, QA.

#### R-M3-06 Total block cap
- Goal: limit the number of blocks per page.
- Acceptance: cap value and behavior per OD-11 (still open: value after measuring real notes, behavior for example "shorten + reason for `Prüfen`"; ask Mark right before this task); test with an LLM response far above the cap; golden G14 unchanged (it is below the cap).
- Scope: `internal/notion` (blocks), tests.
- Depends on: M2, answer to OD-11. Findings: S-09.
- Model: sonnet, counting.
- Reviews: CR, SR, QA.

#### R-M3-07 Absolute paths and minimal environment
- Goal: `/usr/bin/security`, `/usr/bin/osascript` with a minimal environment.
- Acceptance: the runner gets an absolute path; environment only `HOME`, `USER`, `LANG`, `PATH=/usr/bin:/bin` (final list in the task); escaping for osascript unchanged (test with `"` and `\`); tests with a fake runner check path and environment; gosec G204 exclusions for app.go/config.go removed.
- Scope: `internal/notify`, TokenSource from R-M2-07, tests.
- Depends on: M2. Findings: S-10.
- Model: opus, secrets and process start.
- Reviews: CR, SR, QA.

#### R-M3-08 IDs in the URL path
- Goal: config IDs go into paths with `url.PathEscape`.
- Acceptance: all path segments from config or responses are escaped; for valid UUIDs the path is identical (golden); test with `../` and `?` in the ID; no format check at start (OD-12, decided: proposal: PathEscape only).
- Scope: `internal/notion`, tests.
- Depends on: M2. Findings: S-16.
- Model: sonnet, locally bounded.
- Reviews: CR, SR, QA.

#### R-M3-09 Config values out of error messages
- Goal: `orphan_summary_action`, `create_tasks_for`, `timezone`: message without the value (progress #1).
- Acceptance: config.go:210, 215, 234 without the `%q` value; a test checks that a test value is not in the message; `docs/progress.md` #1 done.
- Scope: `internal/config`, tests.
- Depends on: M2. Findings: S-17.
- Model: haiku, three format strings.
- Reviews: CR, SR, QA.

#### R-M3-10 plist ThrottleInterval
- Goal: no fast restart loop.
- Acceptance: `ThrottleInterval` of 60 seconds in `de.borukhov.meetingsync.plist` (OD-13, decided: 60 s); `plutil -lint de.borukhov.meetingsync.plist` ok; table in `docs/launchd.md` extended; the installed plist untouched.
- Scope: plist, `docs/launchd.md`.
- Depends on: none. Findings: S-21.
- Model: haiku, config.
- Reviews: CR, SR, QA.

#### R-M3-11 Prompt personalization (D4)
- Goal: role ("Head of Engineering") and example name ("Sascha") come from the config.
- Acceptance: new keys (names in the task, for example `my_role`, `example_name`); the code defaults are neutral (OD-14, decided: proposal); Mark's config gets the values before the rebuild, so with his values the prompt is byte-identical (golden G01, G08 unchanged); `config.example.yaml` documents the keys with neutral examples; test: another value shows up in the prompt, nothing else changes.
- Scope: `internal/config`, `internal/llm`, `config.example.yaml`, `config.example.json`, tests.
- Depends on: R-M3-09 (same package). Findings: S-20, A-20.
- Model: sonnet, config + prompt, golden protects it.
- Reviews: CR, SR, QA. ADR 0009.

#### R-M3-12 `Prüfen` flag for to-dos for Mark ⚠
⚠ Behavior change, decided by Mark on 2026-11-01 (D5).
- Goal: every note with at least one action `fuer_mich=true` gets a reason for the check flag.
- Acceptance: it applies always, also with `create_tasks_for=none` (OD-22, decided: proposal: D5 is tied to `fuer_mich`, not to task creation). Final wording of the reason is set in the task. Effects: checkbox `Prüfen` = true, notice box, log `, ⚠️ prüfen`, notification ` · bitte prüfen`, dry-run `Prüfen: true`; golden diff only in scenarios with `fuer_mich=true`, listed in the report; scenarios without `fuer_mich` unchanged.
- Scope: `internal/pipeline`, golden files of the affected scenarios, README (section "Bitte prüfen", the check-flag section).
- Depends on: M2. Findings: S-06.
- Model: sonnet, small logic, effect visible through golden.
- Reviews: CR, SR, QA. ADR 0010.

#### R-M3-13 `-check` exit code on ✗ ⚠
⚠ Behavior change, decided by Mark on 2026-11-01 (D7, OD-17: yes). Replaces R-OPT-02.
- Goal: scripts and humans see a failed check from the exit code (progress #5).
- Acceptance: `-check` exits with a code other than 0 (value final in the task, for example 1) if at least one check shows ✗; with all ✓ the exit code is 0; stdout unchanged; golden G10: only `exit.txt` of the ✗ variants (LM Studio off, model missing, Notes DB 404) changes, the all-✓ variant is unchanged, the diff is explained; `docs/progress.md` #5 done; README and `docs/` mention the new exit code (together with R-M6-03).
- Scope: entry point/`Check` (after R-M2-10 in `main.go`), `testdata/golden/` (G10), tests.
- Out of scope: changes to the check texts.
- Depends on: M2 (R-M2-10). Findings: A-14.
- Model: sonnet, small change; the golden diff is the proof.
- Reviews: CR, QA. ADR 0012.

#### R-M3-14 `-file` writes a ledger entry ⚠
⚠ Behavior change, decided by Mark on 2026-11-01 (D8, OD-18: yes). Replaces R-OPT-03.
- Goal: a file processed with `-file` is not processed a second time by a later run or the service (progress #6).
- Acceptance: `-file` without `-dry-run` writes the ledger entries like a normal run (states from R-M1-01/R-M1-02, `begonnen` before `CreatePage`, `fertig` after it); `-dry-run -file` still writes and moves nothing (G09 unchanged); a later `Pass` on the same file creates no second page (test); new golden scenario for `-file` (new files only). Whether the file is also moved to `_archiv/JJJJ-MM` or stays in place is decided in the task together with Mark. Moving files of Mark needs his consent (Global Constraint). `docs/progress.md` #6 done; `docs/launchd.md` (section "Hinweis für den manuellen Test", the manual-test note) updated.
- Scope: `internal/pipeline` (single-file path), entry point, tests, new golden scenario.
- Out of scope: deleting any file.
- Depends on: M2, R-M1-02, R-M3-04. Findings: A-14.
- Model: opus, ledger atomicity and file handling.
- Reviews: CR, SR, QA. ADR 0013.

#### R-M3-15 `llm.api_key` from the Keychain ⚠
⚠ Behavior change, decided by Mark on 2026-11-01 (D9, OD-20: yes). Replaces R-OPT-04.
- Goal: the LLM API key does not have to be in the config file in clear text (S-18).
- Acceptance: the key can be read from the Keychain through the `TokenSource` from R-M2-07; lookup order and Keychain service name are set in the task (same pattern as the Notion token); a plain `llm.api_key` in the config keeps working; the key never appears in logs or error messages; `/usr/bin/security` and minimal environment from R-M3-07; tests per branch with a fake runner; `config.example.yaml` and `config.example.json` document it (commented out, without a value); with no key set the LLM request is unchanged (golden unchanged).
- Scope: `internal/config`, TokenSource, `internal/llm` (header), `config.example.*`, README (setup), tests.
- Out of scope: other secrets.
- Depends on: R-M3-07, R-M3-11 (same package, sequence). Findings: S-18.
- Model: opus, secrets.
- Reviews: CR, SR, QA. ADR 0014.

#### R-M3-16 Count only Mark's to-dos ⚠
⚠ Behavior change, decided by Mark on 2026-11-02 (OD-23). Source: progress follow-up #10.
- Goal: the message "N To-dos für dich" (log and Mac notification) counts only actions with `fuer_mich=true`, not all created tasks.
- Acceptance:
  - With `create_tasks_for=all` the count shows only the `fuer_mich=true` actions; a unit test covers this.
  - Golden G16b is changed on purpose, and only its count line changes; the diff is explained.
  - All other golden files stay unchanged (`git diff --exit-code -- testdata/golden` shows G16b only).
- Scope: `internal/pipeline` (after R-M2-09), or `app.go` if M2 is not done yet; tests; `testdata/golden/G16b`.
- Out of scope: the wording of the message; task creation.
- Depends on: R-M3-12 (same log and notification code, same golden files); M2 pipeline split preferred.
- Model: sonnet, small counting change; the golden diff is the proof.
- Reviews: CR, QA. ADR: none assigned yet (open, see section 9).

#### R-M3-17 No duplicate page after an ambiguous CreatePage failure ⚠
⚠ Behavior change, decided by Mark on 2026-11-02 (OD-05 residual risk, ADR 0004).
- Goal: after a `CreatePage` failure where the page may exist, do not create a second page.
- Ambiguous failures: Notion 5xx, network error, 60 s client timeout, 2xx with an unreadable or unparsable body. Today the ledger gets `verworfen` and the retry can create up to `max_attempts` pages (2xx with unreadable body: a certain second page).
- Acceptance:
  - Before retrying after an ambiguous failure, look up whether the page already exists (for example query the Notes data source by a unique marker such as the file hash, or title + date) and reuse it instead of creating a new one. The marker and lookup are set in the task.
  - Tests: each ambiguous failure (5xx, network error, timeout, unreadable 2xx body) produces exactly one page.
  - Golden outputs unchanged for the non-failure scenarios.
  - ADR 0004 updated (residual risk OD-05 removed or narrowed).
- Scope: `app.go` (`handle`, `process`) or the pipeline package after M2, `notion.go` (lookup), tests.
- Out of scope: Notion database changes unless the task shows the marker needs one (then ask Mark first, see ADR 0004 alternatives).
- Depends on: R-M1-02; M2 pipeline split preferred.
- Model: opus, crash and retry semantics.
- Reviews: CR, SR, QA. Update ADR 0004.

#### R-M3-18 Keep the attempt count when the move to _fehler fails
- Goal: a failed move to `_fehler` does not reset `max_attempts`.
- Problem: `handleError` deletes the retry state even when `fail()` returns false, so the file is reprocessed on the next poll and the count restarts from 0 (pre-existing since v1.0.0). Found in R-M1-07.
- Acceptance: use the 2-min `nextTry` pattern (as in R-M1-04/R-M1-07); attempts preserved; Pass-level test (failing move, several passes); golden unchanged.
- Scope: `app.go` (`handleError`) or the pipeline package after M2, tests.
- Depends on: R-M1-07.
- Model: opus, retry state.
- Reviews: CR, SR, QA.

#### R-M3-19 Open export files with O_NOFOLLOW
- Goal: a regular file swapped for a symlink between `ReadDir` and `ReadFile` is not followed.
- Acceptance: open with `O_NOFOLLOW` and check `fstat` `IsRegular`; test with a swap between listing and reading; golden unchanged. Risk low, local only.
- Scope: `app.go` (`listEntries`), tests.
- Depends on: R-M1-07.
- Model: opus, file atomicity.
- Reviews: CR, SR, QA.

### M4 Repo hygiene

Goal: no real IDs or personal data in the public repo.
Exit: `git grep` and `git log -p --all` without the three real IDs; tag `v1.0.0` on the cleaned commit; backup bundle exists.

#### R-M4-01 Placeholders
- Goal: replace real Notion IDs with placeholders.
- Acceptance: `config.example.yaml:124-128`, `config.example.json:24-26` with placeholders (format in the task, for example `00000000-0000-0000-0000-000000000000` with a comment); README.md:76 says that users enter their own IDs (where to find them in Notion); `config_test.go` adapted; `git grep -n "276258fa\|26e258fa"` is empty; Mark's config untouched.
- Scope: `config.example.yaml`, `config.example.json`, `README.md`, `config_test.go`.
- Depends on: none (pull forward, see section 6). Findings: S-05, A-20.
- Model: haiku, text replacement.
- Reviews: CR, SR, QA.

#### R-M4-02 Clean the git history ⚠ destructive
⚠ Behavior change, decided by Mark on 2026-11-01 (D2). Its own human approval right before it runs (AskUserQuestion by the coordinator), even if the plan is approved.
- Goal: real IDs gone from all commits and tags.
- Steps:
  1. Preparation (no approval needed): ADR 0011, replacement list, script in the scratchpad, tool choice (OD-15, still open: `git filter-repo --replace-text` needs installing; ask Mark about tool and time right before this task).
  2. Check: no open branches/worktrees/PRs; tell Mark about forks/clones.
  3. Backup: `git bundle create <outside repo>/meetingsync-vor-rewrite.bundle --all`, `git bundle verify`.
  4. Approval by Mark (right before step 5).
  5. Rewrite in a fresh clone, tag `v1.0.0` on the matching new commit, `git push --force` for `main` and tags.
  6. Follow-up: check release notes `docs/releases/v1.0.0.md`; re-link the GitHub release; note that old commits may stay reachable through forks/caches.
- Acceptance: `git log -p --all | grep -c "276258fa\|26e258fa"` = 0; `make check` green on the new `main`; bundle verified; approval documented.
- Scope: git history, tags.
- Out of scope: deleting local archive/error files, Notion.
- Depends on: R-M4-01; no open branches; answer to OD-15. Findings: S-05.
- Model: opus, irreversible operation.
- Reviews: CR, SR, QA. ADR 0011.

#### R-M4-03 Personal defaults
- Goal: change the `MyName` default "Mark" (config.go:104) to a neutral value.
- Acceptance: `my_name: Mark` is in Mark's config (OD-14b, answered), so the neutral default is allowed; `config.example.yaml` with an example; golden unchanged (scenarios set `my_name` explicitly).
- Scope: `internal/config`, `config.example.*`, README table.
- Depends on: R-M3-11. Findings: A-20.
- Model: sonnet, default with an effect on the prompt.
- Reviews: CR, QA.

### M5 Performance and robustness

Goal: no needless I/O, no unbounded growth.
Exit: A-10, A-19 closed; golden unchanged.

#### R-M5-01 Read content only when processing
- Goal: `ReadFile` only on a cache miss (classification) and during processing.
- Acceptance: test with a counting fake FS: file in backoff → 0 reads per pass; unchanged known file → 0 reads; recheck from R-M1-05 stays; log line "Neu: …" (new file) unchanged; golden unchanged.
- Scope: `internal/watch`, `internal/pipeline`, tests.
- Depends on: M3. Findings: A-10.
- Model: opus, interaction with hash and TOCTOU.
- Reviews: CR, SR, QA.

#### R-M5-02 Bound the maps
- Goal: `warned`, `cache`, `state` hold only paths that exist in the current pass; state deleted after a ledger hit.
- Acceptance: test: file disappears → entries gone after the next pass; the warning shows again only if the file reappears (record the behavior in the task); golden unchanged.
- Scope: `internal/pipeline`, `internal/watch`, tests.
- Depends on: R-M5-01. Findings: A-19.
- Model: sonnet.
- Reviews: CR, QA.

#### R-M5-03 Own timeout for Models()
- Goal: `-check` does not hang for 30 minutes.
- Acceptance: `Models()` with its own context timeout (constant, value in the task); test with a hanging fake; G10 unchanged.
- Scope: `internal/llm`, tests.
- Depends on: R-M3-05. Findings: A-15.
- Model: haiku, one timeout.
- Reviews: CR, QA.

### M6 Documentation

Goal: the current state is fully described. ADRs are written in their tasks (table 7), not here. All docs follow the Language guardrail.
Exit: files below exist, links checked, `docs/progress.md` current.

#### R-M6-01 docs/architecture.md
- Content: context, packages and dependency direction (Mermaid), runtime view of the flow (sequence), ledger states (state diagram), error and retry model, security model/trust boundaries (Mac, LM Studio, Notion, Keychain), launchd deployment. Max. ~2 pages.
- Depends on: M5. Findings: A-21.
- Model: sonnet (docs-writer). Reviews: CR, SR, QA.

#### R-M6-02 docs/testing.md
- Content: pyramid, golden tests and fakes, normalization, coverage targets, updating golden files on purpose (`-update`, review duty, ⚠ tasks only).
- Depends on: M5. Findings: A-08, A-21.
- Model: sonnet (docs-writer). Reviews: CR, QA.

#### R-M6-03 README, CLAUDE.md, changelog, progress
- Content: commands (`make build/lint/vuln/check`), toolchain (with a note: `GOTOOLCHAIN=local` or a blocked proxy.golang.org makes the first build fail, because go1.26.10 must be downloaded once), package structure in CLAUDE.md section "Ablauf" (flow), `-check` exit code and `-file` ledger behavior, changelog section for the release, progress.md cleaned up.
- Depends on: R-M6-01, R-M6-02.
- Model: haiku, bringing docs up to date.
- Reviews: CR, QA.

### Optional tasks

Decided on 2026-11-01. No open optional tasks remain.

| Former ID | Content | Decision | Now |
| --- | --- | --- | --- |
| R-OPT-01 | Switch to `log/slog`, texts unchanged | OD-21: no | rejected |
| R-OPT-02 | `-check` exit ≠ 0 on ✗ (⚠) | OD-17: yes | mandatory: R-M3-13 |
| R-OPT-03 | `-file` with ledger entry (⚠) | OD-18: yes | mandatory: R-M3-14 |
| R-OPT-04 | `llm.api_key` from the Keychain (⚠) | OD-20: yes | mandatory: R-M3-15 |
| R-OPT-05 | Log rotation / reduce log content | OD-19: no | rejected |

## 6. Order and parallel work

Parallel only with disjoint files and `isolation: "worktree"`.

| Phase | Sequence | In parallel | Reason |
| --- | --- | --- | --- |
| P1 | R-M0-01 → R-M0-02 → R-M0-03 → R-M0-04 (R-M0-11 follows in P3 once R-M0-05 to R-M0-07 are done) | after R-M0-01: R-M0-05 (new test files only); right away: R-M4-01 (example configs, README, config_test.go); R-M3-10 (plist, launchd.md) | `go.mod`/Makefile/CI chain vs. new files |
| P2 | R-M0-05 → R-M0-06 / R-M0-07 | R-M0-06 ∥ R-M0-07 only if scenarios are in separate files (`golden_flow_test.go`, `golden_cli_test.go`) | Shared golden directories are disjoint |
| P3 | R-M0-08, R-M0-09, R-M0-10, R-M0-11 | in parallel with each other (separate test files); R-M0-11 touches only `go.mod` and docs | R-M0-09 only after R-M0-03; R-M0-11 only after R-M0-05 to R-M0-07 |
| P4 | R-M1-01 → R-M1-02 → R-M1-03; R-M1-04 → R-M1-05 → R-M1-07 | R-M1-06 (config.go, main.go) | All other M1 tasks change `app.go` → one after another (order: 01, 02, 03, 04, 05, 07) |
| P5 | R-M2-01 → 02 → 03 → 04 → 05 → 06 → 07 → 08 → 09 → 10 | none | Every cut changes callers in `app.go` |
| P6 | Lane Config: R-M3-09 → R-M3-01 → R-M3-02 → R-M3-11 → R-M3-15 (R-M3-15 also after R-M3-07) | Lane Files: R-M3-03 → R-M3-04 → R-M3-14; Lane HTTP: R-M3-05 → R-M3-06 (after OD-11) → R-M3-08; Lane Processes: R-M3-07; Lane CLI: R-M3-13; R-M3-12 → R-M3-16 (sequential: same notification/log code and golden G16b) | Packages are disjoint after M2 |
| P7 | R-M4-02 | nothing (no open branches) | The rewrite needs a quiet state; earliest after R-M4-01, latest before the next release; ask OD-15 first |
| P8 | R-M5-01 → R-M5-02 | R-M5-03, R-M4-03 | |
| P9 | R-M6-01 ∥ R-M6-02 → R-M6-03 | | |

## 7. ADRs

Style like `docs/adr/0001-yaml-dependency.md` (Context, Decision, Alternatives considered, Consequences; max. one page). Written in plain English by the docs-writer in the delivering task.

| ADR | Title | Delivered by | Main alternative |
| --- | --- | --- | --- |
| 0002 | Toolchain pin, CI and supply chain | R-M0-01, extended by R-M0-04 | only install a newer Go locally |
| 0003 | Golden test strategy | R-M0-05 | unit tests only / snapshots without normalization |
| 0004 | Ledger states and crash behavior | R-M1-01, extended by R-M1-02 and R-M3-17 | idempotency marker in Notion (rejected by Mark: no DB change) |
| 0005 | `golang.org/x/sys` for the atomic rename (delivered) | R-M1-04 | stdlib `os.Link` + `os.Remove`, status quo |
| tbd | Package split and dependency direction (0005 is taken; use the next free number) | R-M2-01 | flat `package main` with one file per topic |
| 0006 | Interfaces and dependency injection | R-M2-09 | package variables/monkeypatching for tests |
| 0007 | LLM over loopback only | R-M3-01 | warning instead of abort |
| 0008 | Notion over HTTPS only | R-M3-02 | warning instead of abort |
| 0009 | Prompt personalization via config | R-M3-11 | freely configurable prompt file |
| 0010 | Always check to-dos for Mark | R-M3-12 | only when the model sets `pruefen=true` |
| 0011 | Clean the git history | R-M4-02 (before it runs) | placeholders only, history stays |
| 0012 | `-check` exit code | R-M3-13 | keep exit 0, read the output |
| 0013 | `-file` writes a ledger entry | R-M3-14 | keep as is, move the file by hand |
| 0014 | `llm.api_key` from the Keychain | R-M3-15 | keep the key in the config file only |

## 8. Risks and countermeasures

| Risk | Countermeasure |
| --- | --- |
| Normalization too coarse, golden tests miss changes | Final normalization list; mutation check in R-M0-05 |
| New ledger format; Mark starts `v1.0.0` afterwards: "begonnen" lines count as processed there → file archived without a note | Acceptance criterion in R-M1-01 (v1.0.0 ignores these lines); tell Mark before the rebuild |
| Mark's running test is disturbed | DoD point 6; `make build` to `./bin`; rebuild only on request |
| The `toolchain` line triggers a download of go1.26.10 with `GOTOOLCHAIN=auto` | Report it to Mark in the R-M0-01 report; CI uses `go-version-file` |
| D3 would break the start if Mark's `llm.base_url` were not loopback | Closed: it is `http://localhost:1234/v1` (OD-08). Not blocked |
| `-check` exit code ≠ 0 (R-M3-13) breaks a script that expects 0 | No such script is known; tell Mark before the rebuild; ADR 0012 |
| `-file` ledger entry (R-M3-14) moves or hides a file Mark did not expect | Moving only with Mark's consent, decided in the task; no deletion |
| `llm.api_key` handling (R-M3-15) leaks the key into a log | SR review; tests check logs and error messages for the key |
| M2 as a big bang | One package per task, empty golden diff as the gate |
| Merge conflicts in `app.go` with parallel work | P4/P5 strictly sequential |
| History rewrite loses commits or breaks clones | Bundle first, approval right before, fresh clone, no open branches |
| Lint fixes change behavior | errcheck in M0 only explicit ignore; real handling in M1 |
| CI runner calls `security`/`osascript` | PATH stubs with a marker (R-M0-04), notifier/runner seams from M2 |
| New warnings (clamps, keys, symlinks) show in Mark's log | Only on deviation; list of new texts in the changelog |
| README section titles change while README is translated | Quote section names as they are today; check the links in R-M6-03 |

## 9. Decisions for Mark

All proposals are accepted ("decided: proposal") on 2026-11-01. Two points stay open and are asked right before their task.

| # | Question | Decision | Blocks |
| --- | --- | --- | --- |
| OD-01 | `go` directive: keep `go 1.22` or raise to `go 1.26` (GODEBUG defaults for TLS/HTTP follow the directive) | decided: proposal: keep 1.22, raising is its own task; revisited: raise in R-M0-11 | – |
| OD-02 | How is the version shown: new flag `-version`, startup log line, or only `go version -m` | decided: proposal: only `go version -m` (no output change) | – |
| OD-03 | Ledger state model: third status (`verworfen`) for "moved to `_fehler`" and clear `CreatePage` errors, so files moved back run again | decided: proposal: yes | – |
| OD-04 | Make up missing tasks after an abort, or only record them | decided: proposal: only record | – |
| OD-05 | `timeout_minutes` < 1: today "no timeout"; clamp to 1 with a warning? Notion 5xx on `POST /v1/pages`: stay transient, or treat like "started without URL" | decided: proposal: clamp; leave 5xx as is until data exists | – |
| OD-06 | Entry point: `main.go` in the root (option A) or `cmd/meetingsync` (option B) | decided: proposal: A | – |
| OD-07 | `notion.base_url` without HTTPS: abort at start (not in D1-D6) | decided: proposal: abort | – |
| OD-08 | Is `llm.base_url` in your config loopback? | answered: yes, `http://localhost:1234/v1` | R-M3-01 not blocked |
| OD-09 | Are there symlinks in the watch folder that should be processed? | decided: proposal: no → skip | – |
| OD-10 | Set the existing ledger file and folders to 0600/0700 afterwards, or only new ones | decided: proposal: new ones only | – |
| OD-11 | Total block cap: value and behavior (shorten + reason for `Prüfen`?) | **open**: value after measuring real notes; ask Mark right before the task | R-M3-06 |
| OD-12 | ID format check at start (abort on an invalid ID) | decided: proposal: PathEscape only, no check | – |
| OD-13 | `ThrottleInterval` value | decided: 60 s | – |
| OD-14 | D4: code default = today's text (stays in the repo) or neutral + values in your config. b) Is `my_name` in your config? | decided: proposal: neutral + config entry before the rebuild. b) answered: yes, `my_name: Mark` is in the config | R-M4-03 not blocked |
| OD-15 | History rewrite: tool (install `git filter-repo`?), time, release notes v1.0.0. GitHub settings (Dependabot alerts, branch protection, signed tags) you do yourself | **open**: ask Mark right before the task | R-M4-02, R-M0-04 (settings part) |
| OD-16 | Confirm the coverage targets from 2.3 | decided: proposal: yes | – |
| OD-17 | `-check` exit ≠ 0 on ✗ (progress #5) | decided: yes → mandatory task R-M3-13 (⚠, ADR 0012) | – |
| OD-18 | `-file` with ledger entry (progress #6) | decided: yes → mandatory task R-M3-14 (⚠, ADR 0013) | – |
| OD-19 | Log rotation and log content | decided: no → R-OPT-05 rejected | – |
| OD-20 | `llm.api_key` through the Keychain | decided: yes → mandatory task R-M3-15 (⚠, ADR 0014) | – |
| OD-21 | Switch to `log/slog` | decided: proposal: no → R-OPT-01 rejected | – |
| OD-22 | D5: wording of the reason; does D5 also apply with `create_tasks_for=none`? | decided: proposal: it always applies (D5 is tied to `fuer_mich`, not to task creation); wording set in R-M3-12 | – |
| OD-23 | To-do count "N To-dos für dich" (log, notification): all created tasks or only `fuer_mich` (progress #10) | decided: Mark, 2026-11-02: only `fuer_mich` → R-M3-16 (⚠, G16b) | – |
| OD-24 | Squash the red commit `b6af06d` and `c48e5f4` before the merge to `main` | decided: Mark, 2026-11-02: yes, branch only (section 10) | merge |

Details left to the tasks (not separate questions): exit code value in R-M3-13; whether `-file` also moves the file in R-M3-14 (needs Mark's consent); Keychain lookup order and service name in R-M3-15.

## 10. Merge to main

Decided by Mark on 2026-11-02 (OD-24). Applies before `refactor/architektur` is merged into `main`.

| Step | Action | Check |
| --- | --- | --- |
| 1 | Squash the red commit `b6af06d` (R-M0-05 golden harness) and `c48e5f4` (R-M0-05 review fixes) into one commit. The later commits follow on top | `git log` shows one R-M0-05 commit; `make check` green on it |
| 2 | Force-push the branch `refactor/architektur` only (`git push --force-with-lease`). Never force-push `main` | Remote branch equals the local branch |
| 3 | Merge the branch into `main` | `make check` green on `main` |
