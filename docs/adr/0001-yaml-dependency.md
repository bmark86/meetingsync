# 0001 YAML library instead of "stdlib only"

Status: accepted (Mark), 2026-10-31

## Context
- Config should be readable as YAML in addition to JSON (T1).
- Previous rule: stdlib only, no new dependencies without asking.
- The Go stdlib has no YAML parser.

## Decision
- Use `go.yaml.in/yaml/v3` v3.0.6, the only external dependency. CLAUDE.md lists it as the exception.
- Initially `gopkg.in/yaml.v3`; switched on security review advice: upstream gopkg.in is archived, `go.yaml.in` is the maintained successor under the YAML organisation. Origin and checksums verified by the security review.
- Accompanying rule: YAML error texts never contain file content (the library writes values such as tokens into error messages, which end up in the log).

## Alternatives considered
| Option | Rejected because |
| --- | --- |
| Own YAML parser (stdlib only) | Error-prone |
| `gopkg.in/yaml.v3` | Upstream archived |
| Keep JSON only | Misses the goal of T1 |

## Consequences
- "Stdlib only" no longer holds without exception; supply-chain surface grows by one dependency (`go.sum` in repo).
- Worse diagnostics for broken YAML: line number only, no context.
- JSON stays supported, `config.yaml` wins; `config.yml` is not searched in the default folder, only via `-config`.

## OPEN
- No update process for the dependency defined (not in the brief).
