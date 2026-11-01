# 0002 Toolchain pin, CI and supply chain

Status: accepted (Mark), 2026-11-01. Toolchain part delivered by R-M0-01. CI part delivered by R-M0-04. `go` directive raised to `go 1.26` by R-M0-11 (residual risk resolved).

## Context
- `go.mod` had only `go 1.22` and no `toolchain` line. The binary was built with whatever Go was installed.
- Built with go1.25.5, govulncheck found 26 reachable stdlib vulnerabilities (crypto/tls, x509, net/http, net/url, os). Fixed from go1.26.10 (finding S-01).
- The `go` directive also sets the GODEBUG defaults (TLS, x509, URL, HTTP). Changing it can change behavior.

## Decision
- `go.mod` gets `toolchain go1.26.10`. The `go` directive stayed `go 1.22` at first (R-M0-01).
- Check with `go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...` (exact version). The tool does not go into `go.mod`. Result after the pin: 0 findings (before: 26).
- Tool pins (R-M0-02): golangci-lint (`v2.11.4`) and govulncheck (`v1.9.0`) run via `go run module@version` from the Makefile. The module download is verified by sumdb; the tools are not in `go.mod`. Dependabot does not see these pins: check and raise them by hand at every release check. The tools run with Mark's rights, so only exact versions, never `@latest`.
- Raise the `go` directive to `go 1.26` in its own task R-M0-11, after the golden tests exist (decided by Mark, 2026-11-01). Done: `go.mod` now has `go 1.26`.
- Raise the `toolchain` line by hand at every Go security release. Dependabot does not reliably update it.

## Decision: CI (R-M0-04)
- `.github/workflows/ci.yml` runs on `macos-latest` on every push, every pull request, once a day (`schedule`, for govulncheck) and by hand.
- The steps call only `make vet`, `make lint`, `make test`, `make vuln`. Tool versions stay in the Makefile only.
- `actions/setup-go` reads the `toolchain` line from `go.mod`. setup-go sets `GOTOOLCHAIN=local`; the workflow sets it back to `auto`, the same as on Mark's Mac.
- A check step fails if `go version` is not go1.26.10, `GOSUMDB` is not `sum.golang.org`, `GOFLAGS` contains `-insecure`, `GOTOOLCHAIN` is not `auto`, or `GONOSUMDB`, `GOPRIVATE` or `GOINSECURE` is set. It also runs `go mod verify`.
- Token rights: `permissions: contents: read`. No secrets. `actions/checkout` does not keep the token (`persist-credentials: false`).
- Actions are pinned by full commit SHA, with the version as a comment. Dependabot raises the pins and `go.mod` weekly. A Dependabot gomod PR can raise the `go` directive if a dependency needs it; check the go.mod diff of such PRs against the decision in R-M0-11.
- Cache: the setup-go cache for modules and builds. The key includes `go.sum` and the Makefile. Runs for pull requests save only to their own cache scope, so a fork cannot change the cache of `main`. No `pull_request_target`.
- `security` and `osascript` are replaced by stubs at the front of `PATH`. A stub writes a marker and exits with 1. The last step fails if a marker exists.

## Alternatives considered
| Option | Rejected because |
| --- | --- |
| Only install a newer Go locally | Not visible in the repo; CI and other machines can still build with an old Go |
| Keep `GOTOOLCHAIN=local` from setup-go | CI would not use the `toolchain` line the same way as Mark's Mac; the plan asks to fail on `local` |
| Actions pinned by tag (`@v7`) | A tag can be moved to other code; a commit SHA cannot |
| A separate workflow only for the scheduled govulncheck | One workflow is simpler; the daily run also finds other drift |
| Raise the `go` directive to `go 1.26` now | Changes GODEBUG defaults (TLS/HTTP) before golden tests can catch a change; done later in R-M0-11 |
| No pin, rely on the installed Go | That is how S-01 happened |

## Consequences
- Download: with `GOTOOLCHAIN=auto` (default), Go downloads go1.26.10 (`golang.org/toolchain` via proxy.golang.org) on the first build. This needs network access once.
- Download check: the module is verified against sum.golang.org. The check is off with `GOSUMDB=off`, `GONOSUMDB` or `GOFLAGS=-insecure`. CI must verify these settings (R-M0-04).
- `GOTOOLCHAIN=local` with an older Go: the build stops with a clear message (fail closed).
- `go version -m <binary>` shows the toolchain used.
- govulncheck loads the vulnerability database (vuln.go.dev) at run time. A green result is valid for that day only. CI also runs it on a schedule (R-M0-04). govulncheck sends only module and package names, no code.
- CI: GitHub turns off scheduled runs after 60 days without activity in the repo. Then the daily govulncheck stops until someone turns it on again.
- CI: the stubs only catch calls through `PATH`. Once R-M3-07 calls `/usr/bin/security` and `/usr/bin/osascript` by absolute path, the fake runner from M2 must keep tests away from the real programs.
- CI: `go mod verify` checks only modules from `go.mod`. Tools from `go run module@version` in the cache are checked once, when they are downloaded.

## Residual risk (resolved by R-M0-11)
- Before R-M0-11, the GODEBUG defaults followed the `go` directive (1.22), not the toolchain. Old, weaker defaults were active.
- R-M0-11 set `go 1.26`. Now `go version -m ./bin/meetingsync` shows no `DefaultGODEBUG` line at all: every setting uses the go1.26 default.

## Decision: `go 1.26` (R-M0-11)
- `go.mod` has `go 1.26`. The `toolchain` line stays at go1.26.10.
- Language: loop variables per iteration were already active since `go 1.22`. Go 1.23 to 1.26 add new features (range over functions, generic type aliases, `new(expr)`), but do not change how existing code behaves.
- These defaults changed (old value before, go1.26 default now):

| Setting | Before | Effect now | Effect on this program |
| --- | --- | --- | --- |
| `tls3des` | 1 | 3DES cipher suites off | None. api.notion.com uses TLS 1.3 / modern TLS 1.2 ciphers |
| `tlssha1` | 1 | SHA-1 signatures in TLS 1.2 refused | None, same reason |
| `tlsmlkem` | 0 | Post-quantum key exchange (X25519MLKEM768) offered | More secure; falls back to X25519 if the server does not support it |
| `tlssecpmlkem` | 0 | Also offers SecP256r1MLKEM768 | Same as above |
| `rsa1024min` | 0 | RSA keys below 1024 bits refused | None, Notion certificates use larger keys |
| `x509negativeserial` | 1 | Certificates with a negative serial number refused | None for public CAs |
| `x509usepolicies` | 0 | Certificate policies are checked | None for public CAs |
| `x509keypairleaf` | 0 | `tls.X509KeyPair` fills `Leaf` | Not used (no client certificates) |
| `x509rsacrt` | 0 | RSA keys use stored CRT values | Not used (no private keys) |
| `x509sha256skid` | 0 | New certificates get a SHA-256 key ID | Not used (no certificates created) |
| `urlmaxqueryparams` | 0 | Limit on query parameters | None, requests have few or no query parameters |
| `urlstrictcolons` | 0 | Host parts with extra colons refused | None for normal `base_url` values like `http://127.0.0.1:1234/v1` |
| `httpcookiemaxnum` | 0 | Limit on cookies | None, no cookie jar is used |
| `httpservecontentkeepheaders` | 1 | `ServeContent` error responses drop some headers | Not used (no HTTP server) |
| `httpservecontentmaxranges` | 1 | Limit on ranges in `ServeContent` | Not used |
| `asynctimerchan` | 1 | Timer channels are unbuffered; `Stop`/`Reset` never leave an old value | None. `Run` uses `time.NewTicker` with `Stop`, `sleepCtx` uses `time.NewTimer` with `Stop`; no `Reset`, no `len`/`cap` on timer channels |
| `containermaxprocs` | 0 | GOMAXPROCS follows cgroup CPU limits | None on macOS |
| `updatemaxprocs` | 0 | GOMAXPROCS updates when the CPU limit changes | None in practice on macOS |
| `cryptocustomrand` | 1 | Crypto functions ignore a custom random source | Not used |
| `randseednop` | 0 | `math/rand.Seed` does nothing | Not used |
| `multipathtcp` | 0 | Multipath TCP default for listeners | Not used (no listener in the program) |
| `decoratemappings` | 0 | Memory mappings get names (Linux only) | None on macOS |
| `gotypesalias` | 0 | `go/types` alias type | Not used |
| `gotestjsonbuildtext` | 1 | `go test -json` build output | Only tooling, not in the binary |
| `winreadlinkvolume`, `winsymlink` | 0 | Windows only | None on macOS |

- Check: all golden tests stay unchanged, including G10b with the stdlib error text `connection refused`.
- Notion (TLS) and LM Studio (HTTP on localhost) are checked by hand with `meetingsync -check` and Mark's config.

## OPEN
- None.
