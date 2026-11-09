# Testing

How tests are layered, how golden tests work and how to run them. Plan reference: `docs/superpowers/plans/2026-11-01-refactoring.md` sections 2.1 to 2.3.

## Test pyramid

| Layer | Where | Fakes | What it proves |
| --- | --- | --- | --- |
| Unit | `internal/<pkg>/*_test.go` | in-memory or `httptest` per package | one package in isolation (parser, ledger, config, Notion blocks, LLM client) |
| Pipeline | `internal/pipeline/*_test.go` | `fakeLLM`, `fakeNotion`, `fakeNotifier` (`helpers_test.go`) on real temp folders | pairing, retry/backoff, archive, ledger, orphan handling |
| Integration / e2e | root `e2e_test.go`, `app_*_test.go`, `llm_key_wiring_test.go`, `notify_pass_test.go`, `config_cli_test.go`, `cli_exit_qa_test.go` | `fakeServer` (LM Studio and Notion in one `httptest` server), `fakeNotifier` | wiring through `cli.Init` / `cli.NewPipeline` as `main` does it, or re-exec of `main()`, with advancing `now` |
| Golden | root `golden_*_test.go`, `testdata/golden/` | `goldenFake` (separate LM Studio and Notion servers) | complete observable behaviour: requests, file tree, ledger, log, exit code |
| Architecture | root `arch_test.go` | none (`go list`) | package dependency direction |

Golden scenarios and most root tests use `golden_adapter_test.go` (`testApp`, `goldenStart`, `goldenRunCLI`). M2 tasks may change the adapter; golden files change only in ⚠ tasks.

Other fakes: `fakeRun` (`internal/notify`, `internal/secret`: no real `osascript`/`security`), `fakeNotion`/`fakeLLM` in `internal/cli/cli_test.go`, `fakeNotionHTTP` in `internal/pipeline/ambiguous_test.go`, `memFS` in `internal/watch/memfs_test.go` (read-side `watch.FS`).

## Golden tests

A scenario (`goldenScenario` in `golden_test.go`) contains:

| Field | Meaning |
| --- | --- |
| `name` | ID, also the folder `testdata/golden/<name>/` (G01 to G16c, see plan 2.2) |
| `config` | YAML with placeholders `{{watch}}`, `{{llm}}`, `{{notion}}` |
| `files` | input files with fixed mtime (`os.Chtimes`, in the past) |
| `llm`, `notion` | scripted replies per `"METHOD /path"`: in order, the last one repeats; no script means 404 |
| `passes` | one `Pass(ctx, now)` per entry (simulated clock, no real waiting), or |
| `args` | CLI arguments, run by re-exec of the test binary (`TestMain` + `MEETINGSYNC_GOLDEN_MAIN=1`, no `go build`) |

Exactly one of `passes` and `args` is set. Scenarios register themselves with `registerGolden` in `init()` (`golden_test.go`, `golden_flow_test.go`, `golden_cli_test.go`, `golden_file_test.go`).

Files per scenario folder:

| File | Content | When |
| --- | --- | --- |
| `requests.json` | every request to both fakes (server, method, path+query, headers, body) | always |
| `tree.txt` | every folder (`name/`) and every file (relative path and SHA-256) under the watch folder (no permissions) | always |
| `ledger.jsonl` | ledger lines | if a ledger exists |
| `log.txt` | pipeline log | pass scenarios |
| `stdout.txt`, `stderr.txt`, `exit.txt` | CLI output and exit code | CLI scenarios |

### Normalisation

Everything not listed stays raw (code: `goldenNormalizer`, `goldenBody`, `goldenTree`, `goldenFake.ServeHTTP` in `golden_test.go`).

| What | Result |
| --- | --- |
| Temp root (also with symlinks resolved) | `<tmp>` |
| Host:port of the fakes | `<llm>`, `<notion>` |
| Ledger field `"at"` and first line of `*.fehler.txt` | `<now>`, only if RFC3339 and inside the time window of the run; otherwise raw |
| Duration in `LLM fertig nach …` | `<dur>` |
| Log timestamp prefix `YYYY/MM/DD hh:mm:ss ` | removed |
| `Authorization` header | Notion: `Bearer <token>` for the test token, else `<present>`; LLM: always `<present>` |
| Notion bodies | JSON decoded and re-encoded (sorted keys), indented |
| LLM bodies | key order as sent (schema order matters), indented |
| Non-JSON body | JSON string |
| `tree.txt` | every folder (`name/`) and every file; ledger and `.fehler.txt` hashed after normalisation (`(normalized)`, no mtime); all other files raw, with mtime in UTC |

Determinism: `TestMain` sets `time.Local = time.UTC` and the proxy variables (`HTTP_PROXY`/`HTTPS_PROXY` = `http://127.0.0.1:1`, `NO_PROXY=127.0.0.1,localhost`), so nothing reaches the real network. Config uses `timezone: Europe/Berlin`. `goldenEnv` sets `HOME`, `PATH` (only stub `security`/`osascript` that fail the test if called), `NOTION_TOKEN`, `TZ=UTC` and the proxy variables for CLI children; `LLM_API_KEY` is unset there (empty in the test process). Notifications are off or faked.

## Coverage

Targets (plan 2.3, confirmed by Mark as OD-16):

| When | Target |
| --- | --- |
| End of M0 | total at least 80 %, no function from the review's 0 % list left at 0 % (exception `notify` until M2) |
| End of M2 | `internal/ledger`, `internal/pipeline`, `internal/notion`, `internal/watch` each at least 85 % |

Measured on 2026-11-06 (`go test -cover ./...`):

| Package | Coverage |
| --- | --- |
| total (`go tool cover -func`) | 96.7 % |
| `meetingsync` (root) | 94.8 % |
| `internal/cli` | 94.2 % |
| `internal/config` | 95.7 % |
| `internal/domain` | 100.0 % |
| `internal/ledger` | 92.2 % |
| `internal/llm` | 96.4 % |
| `internal/notify` | 100.0 % |
| `internal/notion` | 98.8 % |
| `internal/pipeline` | 97.3 % |
| `internal/secret` | 100.0 % |
| `internal/transcript` | 100.0 % |
| `internal/watch` | 95.0 % |

Measure:

```sh
go test -coverprofile="$TMPDIR/cover.out" ./... && go tool cover -func="$TMPDIR/cover.out"
```

## Running

| Goal | Command |
| --- | --- |
| All tests with race detector | `make test` (= `go test -race ./...`) |
| Definition of Done | `make check` (vet, lint, test, vuln) |
| Golden tests only | `go test . -run 'TestGolden'` |
| One golden scenario | `go test . -run 'TestGolden$/^G05a$' -v` |
| Architecture tests | `go test . -run 'TestArch' -v` |
| One package | `go test -race ./internal/pipeline` |

Note: `-run 'Golden/G05a'` also runs the `TestGoldenHarness*` self-tests, because the first part matches every test with "Golden" in the name.

After `make check`, `git diff --exit-code -- testdata/golden` must be empty (except ⚠ tasks, every diff explained).

## Updating golden files on purpose

```sh
go test . -run Golden -update
```

Rules:

- Only in tasks marked ⚠ in the plan, i.e. a behaviour change decided by Mark.
- Never to make a failing test pass. A red golden test is first a regression to understand.
- Review duty: the task report lists and explains every diff under `testdata/golden/`; code-reviewer and spec-reviewer check it.
- New golden files for new scenarios are fine if existing ones stay unchanged.
- `-update` writes files but never deletes. Stale files in a scenario folder are reported as errors and must be removed by hand after review. Nothing is deleted without Mark's approval.
- Mutation check: changing one character of the prompt or one Notion property name must turn the golden test red.

## Architecture tests (`arch_test.go`)

Based on `go list` (offline, `GOPROXY=off`) over all module packages, checking transitive dependencies and direct test imports (`TestImports`/`XTestImports`). Rules in `archRules`:

| Package | May import (besides std) |
| --- | --- |
| `internal/domain` | nothing |
| `internal/ledger`, `internal/notify`, `internal/secret` | nothing |
| `internal/transcript`, `internal/notion`, `internal/llm` | `internal/domain` |
| `internal/config` | `go.yaml.in/yaml/v3` |
| `internal/watch` | `golang.org/x/sys/unix`, `internal/domain`, `internal/transcript` |
| `internal/pipeline` | `golang.org/x/sys/unix`, `internal/{domain,transcript,ledger,watch,llm,notion}` |
| `internal/cli` | `golang.org/x/sys/unix`, `go.yaml.in/yaml/v3`, `internal/{config,domain,transcript,ledger,watch,llm,notion,pipeline}` (not `notify`, `secret`) |
| all `internal/*` except `pipeline`, `cli` | must not import `internal/pipeline` or `internal/cli` |
| all `internal/*` except `notify`, `secret` | must not use `os/exec` |

Further tests: `TestArchRulesDetectViolations` (rules do trigger), `TestArchRootPackageOnlyMain` (root package contains only `main.go`).
