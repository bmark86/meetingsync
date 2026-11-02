# 0015 Package split and dependency direction

Status: accepted (Mark approved M2 at the gate, 2026-11-02), R-M2-01. Number 0015 chosen by Mark at the same gate (0005 is taken by `golang.org/x/sys`).

## Context
- Everything lives in `package main`: domain types, clients, flow and file system code (A-03).
- `App` is a god object for scan, pairing, retry, LLM orchestration, Notion mapping, archive, ledger and notify (A-04).
- No seams for LLM, Notion, clock, file system or `osascript`; tests need one combined fake server (A-05).
- `transient()`/`isTransient()` live in the Notion client but are used by the LLM client and `App` (A-06).
- There are two parallel HTTP error types (A-15).
- Source: `docs/reviews/2026-11-01-architektur-und-security-review.md`, section 4; plan section M2.

## Decision
- Split step by step (strangler). Every task leaves the program runnable; golden tests stay byte-identical over the whole milestone.
- Target packages under `internal/`, one task each:

| Task | Package |
| --- | --- |
| R-M2-01 | `domain` (types, error class transient) |
| R-M2-02 | `transcript` |
| R-M2-03 | `ledger` |
| R-M2-04 | `notion` |
| R-M2-05 | `llm` |
| R-M2-06 | `notify` |
| R-M2-07 | `config` |
| R-M2-08 | `watch` |
| R-M2-09 | `pipeline` |
| R-M2-10 | entry point stays `main.go` in package `main` |

- Dependency rules, enforced by `arch_test.go` (hermetic `go list -deps -json`, extendable list `archRules`):
  1. `internal/domain` imports only stdlib and itself.
  2. No infra package (`internal/*` except `pipeline`) imports `internal/pipeline`, test imports included.
- Delivered in R-M2-01: `internal/domain` with `MeetingNote`, `Thema`, `Action`, `Naechstes`, `Timing`, `NoteMeta`, `Kind`, `Transient`, `IsTransient`. All callers switched, no aliases. Coverage 100 %.
- Logging stays `*log.Logger` with identical texts.

## Alternatives considered
| Option | Rejected because |
| --- | --- |
| Flat `package main` with one file per topic | Keeps all identifiers in one namespace; the compiler cannot enforce a dependency direction; no seams |
| Big-bang rewrite into the target layout | One huge diff, golden tests cannot localize a deviation; the program is not runnable between steps |

## Consequences
- Each of the ten tasks changes callers in `app.go`, so they run in sequence, not in parallel.
- `internal/` stops imports from outside the module.
- Dependency direction is a test, not a convention; new rules are added to `archRules`.
- Golden unchanged over M2 is the safety net; a deviation ends the task.
- More packages mean more exported names and some boilerplate for options structs and interfaces.
- Interfaces and injection are decided separately (ADR 0006, R-M2-09).

## OPEN
- None.
