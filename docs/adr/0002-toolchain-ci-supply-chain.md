# 0002 Toolchain pin, CI and supply chain

Status: accepted (Mark), 2026-11-01. Toolchain part delivered by R-M0-01. CI part delivered by R-M0-04. R-M0-11 closes the residual risk.

## Context
- `go.mod` had only `go 1.22` and no `toolchain` line. The binary was built with whatever Go was installed.
- Built with go1.25.5, govulncheck found 26 reachable stdlib vulnerabilities (crypto/tls, x509, net/http, net/url, os). Fixed from go1.26.10 (finding S-01).
- The `go` directive also sets the GODEBUG defaults (TLS, x509, URL, HTTP). Changing it can change behavior.

## Decision
- `go.mod` gets `toolchain go1.26.10`. The `go` directive stays `go 1.22` for now.
- Check with `go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...` (exact version). The tool does not go into `go.mod`. Result after the pin: 0 findings (before: 26).
- Tool pins (R-M0-02): golangci-lint (`v2.11.4`) and govulncheck (`v1.9.0`) run via `go run module@version` from the Makefile. The module download is verified by sumdb; the tools are not in `go.mod`. Dependabot does not see these pins: check and raise them by hand at every release check. The tools run with Mark's rights, so only exact versions, never `@latest`.
- Raise the `go` directive to `go 1.26` in its own task R-M0-11, after the golden tests exist (decided by Mark, 2026-11-01).
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

## Residual risk (until R-M0-11)
- GODEBUG defaults follow the `go` directive (1.22), not the toolchain. These legacy defaults stay active:

| Setting | Value | Effect |
| --- | --- | --- |
| `tls3des` | 1 | 3DES cipher suites allowed |
| `tlssha1` | 1 | SHA-1 signatures in TLS 1.2 allowed |
| `tlsmlkem` | 0 | no post-quantum key exchange |
| `rsa1024min` | 0 | RSA keys below 1024 bits accepted |
| `x509negativeserial` | 1 | negative certificate serial numbers accepted |
| `x509usepolicies` | 0 | certificate policies not checked |
| `urlmaxqueryparams` | 0 | no limit on query parameters |
| `httpcookiemaxnum` | 0 | no limit on cookies |

- Practical risk is low: the program only makes outgoing client calls and never sets `InsecureSkipVerify`.

## OPEN
- None.
