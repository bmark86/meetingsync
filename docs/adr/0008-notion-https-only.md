# 0008 Notion over HTTPS only

Status: accepted (OD-07 proposal; `-check` output change approved by Mark, 2026-11-02), R-M3-02. Finding S-03.

## Context
- The Notion token and the note body go to `notion.base_url`. Before this change any URL was accepted, including `http://` (token in clear text) and userinfo.
- Go's wrapped `*url.Error` prints the full request URL, so userinfo, query or fragment in a base URL would reach the log.
- A 307/308 redirect re-sends the note body to another host (follow-up from ADR 0007).

## Decision
- `config.Finalize` requires `notion.base_url` with scheme `https`, or `http` only with an exact loopback host (`localhost`, `127.0.0.1`, `::1`; for test fakes). Userinfo, empty host and missing scheme are rejected.
- Messages contain no value: `notion.base_url muss https verwenden (http nur für localhost)`, `notion.base_url darf keinen Benutzernamen und kein Passwort enthalten`. Start aborts.
- `llm.base_url` and `notion.base_url` also reject a query or fragment (`?`, `#`), because of the `*url.Error` behavior above (fix round, coordinator decision).
- The Notion `http.Client` does not follow redirects. A 3xx is a non-transient error "Weiterleitung wird nicht verfolgt" (no retry).
- The proxy stays allowed for Notion (corporate proxy). HTTPS goes through a CONNECT tunnel, the token stays inside TLS. Go never proxies loopback.
- New `domain.RedactURL` prints only `scheme://host[:port]` (`(ungültige URL)` on parse errors). Used in `LM Studio nicht erreichbar unter …` and in `-check`.

## Alternatives considered
| Option | Why not |
| --- | --- |
| Allow `http` with a warning | Token in clear text; a warning is ignored in a launchd run (the plan table named this option) |
| Pin the TLS certificate or CA | Breaks with Notion certificate rotation and corporate TLS inspection; HTTPS already protects the token |
| Disable the proxy for Notion | Breaks setups behind a corporate proxy; the CONNECT tunnel keeps the token inside TLS |

## Consequences
- Behavior change: a non-https, non-loopback Notion URL stops the start. The default Notion URL is unaffected.
- `-check` prints the LM Studio address without path (`http://localhost:1234` instead of `http://localhost:1234/v1`); golden G10b changes by one line.
- Residual risk (accepted): the wrapped `url.Error` still contains the request path (Notion data source/page IDs, no credentials) in the local log.
- README troubleshooting has rows for the new errors.
- Depends on: ADR 0007 (same pattern for `llm.base_url`).
