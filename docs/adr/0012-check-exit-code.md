# 0012 `-check` exit code on failed checks

Status: accepted (D7; OD-17 decided by Mark, 2026-11-01), R-M3-13. Finding A-14, progress follow-up 5.

## Context
- `meetingsync -check` printed ✗ for a missing export folder, an unreachable LM Studio or a missing model, but exited 0. Only config errors, a missing token, keychain errors and Notion errors on the Notes or Tasks database exited 1.
- A script or a human who does not read the output could not tell a failed check from a good one.
- `docs/launchd.md`: the plist starts `meetingsync -config <path>` without `-check`, so launchd never runs it.

## Decision
- `Check` in `internal/cli` returns `errCheckFailed` if at least one check line shows ✗. `main.go` is unchanged (it already calls `os.Exit(1)` on an error).
- Exit 1 if any check line shows ✗, otherwise 0.
- A Projects database that cannot be read is a `Hinweis` notice, not ✗, so it keeps exit 0.
- Output text unchanged, no new line.
- Golden: `exit.txt` of G10b (LM Studio unreachable) and G10c (model missing) changed 0 → 1. G10a (all ✓) stays 0, G10d (Notes 404) was already 1.

## Alternatives considered
| Option | Why not |
| --- | --- |
| Keep exit 0, read the output | Failure invisible to scripts and `&&` chains; OD-17 chose a non-zero code |
| Distinct codes per failure kind | More contract to maintain; the output already names the failing line |

## Consequences
- Scripts that call `-check` and expect 0 now see 1 on ✗. Tell Mark before the first rebuild after this change.
- launchd is unaffected: the service does not run `-check`.
