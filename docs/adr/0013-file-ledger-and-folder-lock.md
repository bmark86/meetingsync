# 0013 `-file` writes ledger entries, folder lock against parallel runs

Status: accepted (D8, OD-18 decided by Mark, 2026-11-01; no-move decision by Mark, 2026-11-04; lock decided by the coordinator), R-M3-14. Finding A-14, progress follow-up 6.

## Context
- `-file` without `-dry-run` created a real note but no ledger line and did not move the file. A file in the export folder was processed again by the next run (second page).
- With ledger lines (ADR 0004) `-file` and the running service can race: both read the ledger before either writes `begonnen`, and both create a page.
- The ledger was loaded once at start, so a running service did not see lines written by `-file`.
- The export and archive folders must not receive new files (a lock file would show up there).

## Decision
- `-file` writes `begonnen` before `CreatePage`, `fertig` after, `verworfen` on a failed `CreatePage` (same states as ADR 0004).
- `-file` does not move the file (Mark, 2026-11-04). A file in the export folder is archived by the service on its next pass as already processed. Files outside stay.
- `-file` always processes the given file, also if its hash is known (coordinator). `-dry-run -file` writes nothing (G09).
- Mutual exclusion: advisory `flock` on the export folder itself (`internal/ledger/lock.go`), no lock file.
  - Each service pass (not dry-run) tries the lock. If held: pass skipped, one log line per lock period.
  - Lock impossible (e.g. unsupported filesystem): one warning, the pass runs unprotected.
  - `-file` waits for the lock before reading the file, so a file archived meanwhile fails with not found instead of a second page.
- The service re-reads ledger lines appended by other processes at the start of each pass (`Ledger.Refresh`: offset based, a torn last line waits, a truncated file is re-read). `-file` refreshes after taking the lock.

## Alternatives considered
| Option | Why not |
| --- | --- |
| Lock file in the export or archive folder | A new file appears in folders Mark and the service scan; stale file after a kill |
| No lock, rely on the ledger only | Race between service and `-file` can create two pages; the service never saw foreign lines |
| `-file` moves the file like a normal run | Mark decided against it (2026-11-04) |
| Keep `-file` without ledger | Second page for files in the export folder (follow-up 6) |

## Consequences
- Trade-off: the service skips passes while `-file` runs (LLM time), and `-file` can wait for a running pass.
- `-file` killed between `begonnen` and `fertig`: a file in the export folder goes to `_fehler` on the next pass (as for a normal run), no second page.
- Without `watch_dir`, `-file` runs without the lock; `Add` may create `watch_dir/_archiv` (0700).
- The lock only works between processes with the same `watch_dir`.
- Rollout: restart the service after installing the new binary (follow-up 30). An old service has no lock or refresh.
- Unchanged: `-file` on a summary while its transcript is still in the export folder; the service still creates a page from the pair, only the primary hash is checked (follow-up 31).
