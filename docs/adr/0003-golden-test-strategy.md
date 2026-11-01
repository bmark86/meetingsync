# 0003 Golden test strategy

Status: draft (R-M0-05), 2026-11-01. Needs review and Mark's acceptance.

## Context
- The refactoring (M1 to M3) must keep the normal path the same: Notion requests, LLM prompts, file moves, ledger, CLI output and log texts.
- Today only a few end-to-end tests exist. They check single strings, so many changes would pass unseen.
- Outputs contain values that change on every run: temp paths, ports of the test servers, the clock, the run time of the LLM call.

## Decision
- Golden tests record the full outcome of a scenario and compare it with files under `testdata/golden/<scenario>/`: `requests.json`, `tree.txt`, `ledger.jsonl`, and `log.txt` for pass scenarios. CLI scenarios (field `args`) write `stdout.txt`, `stderr.txt` and `exit.txt` instead of `log.txt`.
- Scenarios are registered with `registerGolden` in an `init()` function. One runner (`TestGolden`) runs all of them, sorted by name. So R-M0-06 (`golden_flow_test.go`) and R-M0-07 (`golden_cli_test.go`) can add scenarios in separate files at the same time. A duplicate name stops the test binary.
- Two local fake servers (`httptest` on 127.0.0.1) play LM Studio and Notion. Answers are scripted per scenario. Every request is recorded in order: server, method, path with query, headers, body.
- Recorder bodies: Notion bodies are stored as JSON with sorted keys. LLM bodies keep keys and values exactly as sent, in send order. Both are only indented, nothing else changes. The plan text in section 2.1 says "sorted keys" for all bodies; this ADR is the binding rule.
- Scenarios reach the code only through `golden_adapter_test.go`. The M2 package split may change the adapter, not the scenarios or golden files.
- CLI scenarios start `main()` by running the test binary again (`TestMain` and the variable `MEETINGSYNC_GOLDEN_MAIN`). No `go build`. The child gets its own flag set, whose usage calls `flag.Usage` like the real binary. So `-h` and unknown flags print exactly the same text as the built binary. The child also gets `GOCOVERDIR` (outer value, else a temp folder), so a coverage build does not print a warning on stderr.
- Process time zone: `TestMain` sets `time.Local = time.UTC` before `m.Run`, so before any test goroutine starts (no data race). The config time zone is `Europe/Berlin`, which shows that only the config zone counts. CLI children get `TZ=UTC`.
  - Why: the earlier child process with `TZ=UTC` lost the `-run` filter and the coverage of the golden tests.
  - Side effect: this applies to all tests in the package, not only golden tests. Checked on 2026-11-01: without the override, `TestGolden` (ledger `mtime` in local time) and the e2e archive month depended on `TZ`; the e2e test now computes the month with the config zone like the app. All tests are green with `TZ=Europe/Berlin`, `TZ=UTC` and `TZ=Pacific/Auckland`.
- Isolation: `HOME`, watch folder and config are in `t.TempDir()`. `PATH` contains only stubs for `security` and `osascript`. A stub writes a marker, and the test fails if a marker exists. `NOTION_TOKEN` is set with `t.Setenv`.
- No real network: `HTTP_PROXY` and `HTTPS_PROXY` point to `http://127.0.0.1:1` (nothing listens there), `NO_PROXY` is `127.0.0.1,localhost`. A request to any other host fails instead of leaving the Mac. In the test process `TestMain` sets these variables before `m.Run`, because `http.DefaultTransport` reads them only once, at the first request. CLI children get them from `goldenEnv`. Go never sends loopback requests through a proxy, so the fakes still work. These variables apply to all child processes of the package tests, also `go build` in `TestAC8`. If `go` must download modules or a toolchain there, the download fails at the dead proxy. This is no problem today: the module cache has only stdlib and yaml, and go1.26.10 is installed locally.
- Update: `go test -run Golden ./... -update`. Old files are reported, never deleted.
- Normalization, final list (everything else stays raw):

| Field | Replaced by |
| --- | --- |
| Temp paths (also the `/private/...` form) | `<tmp>` |
| Host:port of the fakes | `<llm>`, `<notion>` |
| Ledger field `at`, first line of `.fehler.txt` (error file) | `<now>`, only if the value is an RFC3339 time between start and end of the run; else raw |
| Duration in `LLM fertig nach …` (LLM done after …) | `<dur>` |
| Log timestamps | removed |
| `Authorization` header | `Bearer <token>` if it is the test token, else `<present>` |
| Notion body | JSON with sorted keys, indented |
| LLM body | key order as sent (schema order matters), indented |
| Hash in `tree.txt` | ledger and `.fehler.txt` hashed after the rules above, without mtime; other files raw, with mtime (UTC, RFC3339; inputs get a fixed mtime with `os.Chtimes`) |

- Mutation check: one character in the system prompt (`llm.go`) and one property name (`Description` in `config.go`) were changed. G01 turned red both times, and green again after the revert. Also `At: p.ModTime` instead of `At: time.Now()` in the ledger entry (`app.go`) turns G01 red, because the value is outside the run.
- Clock: `app.go` uses the real clock for the project cache (`time.Since(a.projectsAt) < 15*time.Minute`), not the `now` of `Pass`. Scenarios with several passes must keep their `now` values less than 15 minutes apart, or the result depends on the real run time. The clock seam in M2 must cover this cache too.

## Alternatives considered
| Option | Rejected because |
| --- | --- |
| Unit tests only | Do not show changes in the order of requests or in full payloads |
| Snapshots without normalization | Fail on every run (ports, paths, clock) |
| Sorted keys for the LLM body too | Hides a change in the schema field order, which changes model output |
| A clock interface in production code now | Production code stays unchanged in M0; normalization covers `time.Now()` |

## Consequences
- Any change on the normal path shows up as a diff in a golden file. ⚠ tasks must explain each diff.
- A golden diff during M2 means a behavior change. Check: `git diff --exit-code -- testdata/golden`.
- `time.Local` is UTC for every test in the package. A new test that needs another local zone must set it itself and must not run in parallel.
- Notion bodies are compared by content, not by key order. LLM bodies are compared including key order.

## OPEN
- None.
