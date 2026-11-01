# 0002 Toolchain pin, CI and supply chain

Status: accepted (Mark), 2026-11-01. Toolchain part delivered by R-M0-01. R-M0-04 adds the CI part. R-M0-11 closes the residual risk.

## Context
- `go.mod` had only `go 1.22` and no `toolchain` line. The binary was built with whatever Go was installed.
- Built with go1.25.5, govulncheck found 26 reachable stdlib vulnerabilities (crypto/tls, x509, net/http, net/url, os). Fixed from go1.26.10 (finding S-01).
- The `go` directive also sets the GODEBUG defaults (TLS, x509, URL, HTTP). Changing it can change behavior.

## Decision
- `go.mod` gets `toolchain go1.26.10`. The `go` directive stays `go 1.22` for now.
- Check with `go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...` (exact version). The tool does not go into `go.mod`. Result after the pin: 0 findings (before: 26).
- Raise the `go` directive to `go 1.26` in its own task R-M0-11, after the golden tests exist (decided by Mark, 2026-11-01).
- Raise the `toolchain` line by hand at every Go security release. Dependabot does not reliably update it.

## Alternatives considered
| Option | Rejected because |
| --- | --- |
| Only install a newer Go locally | Not visible in the repo; CI and other machines can still build with an old Go |
| Raise the `go` directive to `go 1.26` now | Changes GODEBUG defaults (TLS/HTTP) before golden tests can catch a change; done later in R-M0-11 |
| No pin, rely on the installed Go | That is how S-01 happened |

## Consequences
- Download: with `GOTOOLCHAIN=auto` (default), Go downloads go1.26.10 (`golang.org/toolchain` via proxy.golang.org) on the first build. This needs network access once.
- Download check: the module is verified against sum.golang.org. The check is off with `GOSUMDB=off`, `GONOSUMDB` or `GOFLAGS=-insecure`. CI must verify these settings (R-M0-04).
- `GOTOOLCHAIN=local` with an older Go: the build stops with a clear message (fail closed).
- `go version -m <binary>` shows the toolchain used.
- govulncheck loads the vulnerability database (vuln.go.dev) at run time. A green result is valid for that day only. CI also runs it on a schedule (R-M0-04). govulncheck sends only module and package names, no code.

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
