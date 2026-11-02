# 0007 llm.base_url must be loopback

Status: accepted (decided by Mark, 2026-11-01, D3), R-M3-01. Finding S-02.

## Context
- The raw transcript is sent to `llm.base_url`. The privacy rule says it never leaves the Mac.
- Before this change any URL was accepted, including LAN or internet hosts.
- Review fix round 1 found two more ways to leak it, both reproduced by the reviewer:
  - `LOCALHOST` with `HTTP(S)_PROXY` set: Go skips a proxy only for exact lowercase `localhost`, so the transcript would go to the proxy.
  - A 307 redirect from the LM Studio port: the default client re-sends the body to any host.

## Decision
- `config.Finalize` rejects `llm.base_url` unless scheme is `http` or `https`, there is no userinfo, and the host is exactly `localhost` (lowercase), `127.0.0.1` or `::1`.
- Start aborts with `llm.base_url muss auf diesen Mac zeigen (localhost, 127.0.0.1 oder ::1)`. The message contains no URL value.
- The LLM `http.Client` never uses a proxy (transport = clone of `http.DefaultTransport` with `Proxy=nil`).
- The LLM client does not follow redirects (`CheckRedirect` returns `http.ErrUseLastResponse`). A 3xx is a non-transient HTTP error.
- Scope was extended from `internal/config` to `internal/llm` because both are bypasses of the same rule.

## Alternatives considered
| Option | Why not |
| --- | --- |
| Allow LAN hosts (private ranges) | The transcript would leave the Mac; contradicts the privacy rule |
| Resolve DNS and check the IP | Resolution can change between check and request (rebinding); more code for no gain on a fixed local setup |
| `strings.EqualFold` for `localhost` | Passes `LOCALHOST`, which bypasses Go's proxy exemption (reproduced) |
| Config check only, default client | A proxy or a 3xx redirect still sends the transcript elsewhere |

## Consequences
- Behavior change: a non-local `llm.base_url` stops the start. Mark's config (`http://localhost:1234/v1`) is unaffected; golden tests unchanged (fakes on 127.0.0.1).
- Residual risk: `/etc/hosts` could point `localhost` elsewhere. Not covered by the check.
- A LAN-hosted LLM is no longer possible without a new decision.
- README troubleshooting table has a row for the error.
- Follow-up in R-M3-02: same redirect protection for the Notion client (a 307/308 re-sends the note body; Go drops only `Authorization` across hosts).
