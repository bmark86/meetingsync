# 0003 Golden test strategy

Status: draft (R-M0-05), 2026-11-01. Needs review and Mark's acceptance.

## Context
- The refactoring (M1 to M3) must keep the normal path the same: Notion requests, LLM prompts, file moves, ledger, CLI output and log texts.
- Today only a few end-to-end tests exist. They check single strings, so many changes would pass unseen.
- Outputs contain values that change on every run: temp paths, ports of the test servers, the clock, the run time of the LLM call.

## Decision
- Golden tests record the full outcome of a scenario and compare it with files under `testdata/golden/<scenario>/`: `requests.json`, `tree.txt`, `ledger.jsonl`, `log.txt` (CLI scenarios later also `stdout.txt`, `exit.txt`).
- Two local fake servers (`httptest` on 127.0.0.1) play LM Studio and Notion. Answers are scripted per scenario. Every request is recorded in order: server, method, path with query, headers, body.
- Scenarios reach the code only through `golden_adapter_test.go`. The M2 package split may change the adapter, not the scenarios or golden files.
- CLI scenarios start `main()` by running the test binary again (`TestMain` and the variable `MEETINGSYNC_GOLDEN_MAIN`). No `go build`.
- Golden tests run with process `TZ=UTC` and config time zone `Europe/Berlin`. With another `TZ`, `TestGolden` runs itself once more as a child process with `TZ=UTC`, because Go reads the local zone only at start.
- Isolation: `HOME`, watch folder and config are in `t.TempDir()`. `PATH` contains only stubs for `security` and `osascript`. A stub writes a marker, and the test fails if a marker exists. `NOTION_TOKEN` is set with `t.Setenv`.
- Update: `go test -run Golden ./... -update`. Old files are reported, never deleted.
- Normalization, final list (everything else stays raw):

| Field | Replaced by |
| --- | --- |
| Temp paths (also the `/private/...` form) | `<tmp>` |
| Host:port of the fakes | `<llm>`, `<notion>` |
| Ledger field `at`, first line of `.fehler.txt` (error file) | `<now>` |
| Duration in `LLM fertig nach …` (LLM done after …) | `<dur>` |
| Log timestamps | removed |
| `Authorization` header | `Bearer <token>` if it is the test token, else `<present>` |
| Notion body | JSON with sorted keys, indented |
| LLM body | key order as sent (schema order matters), indented |
| Hash in `tree.txt` | ledger and `.fehler.txt` hashed after the rules above, other files raw |

- Mutation check: one character in the system prompt (`llm.go`) and one property name (`Description` in `config.go`) were changed. G01 turned red both times, and green again after the revert.

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
- If the outer `TZ` is not `UTC`, coverage from the child process is not counted.
- Notion bodies are compared by content, not by key order. LLM bodies are compared including key order.

## OPEN
- Coverage of golden tests in the child process (for the 80 % target at the end of M0).
