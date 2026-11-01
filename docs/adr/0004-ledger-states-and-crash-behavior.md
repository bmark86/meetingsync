# 0004 Ledger states and crash behavior

Status: draft (R-M1-01), 2026-11-02. Decided by Mark on 2026-11-01 (D1, OD-03). R-M1-02 extends this ADR with the flow and the restart rule.

## Context
- The ledger (`.meetingsync-ledger.jsonl` in the archive folder) stops a file from being processed twice. Today a line is written only after the Notion page and the tasks exist.
- A crash between `CreatePage` and the ledger line creates a second page after the restart (A-01).
- Errors from writing the ledger were dropped, and the file was not synced to disk (A-11, S-13).
- Mark still runs `v1.0.0` on his real ledger. `v1.0.0` counts every line with a `hash` field as processed, and also uses its `mtime` to match summaries (`HasNear`).

## Decision
- Each ledger entry has a status: `begonnen` (started), `fertig` (done), `verworfen` (discarded, file moved to `_fehler`, may run again).
- For each hash the last line wins. `Has` and `HasNear` count only entries whose last status is `fertig`.
- Lines without a status (all `v1.0.0` lines) count as `fertig`. `Add` without a status also means `fertig`.
- File format:
  - `fertig` lines stay exactly as in `v1.0.0`: `{"hash":…,"file":…,"mtime":…,"url":…,"at":…}`, no status field. So the golden `ledger.jsonl` files do not change in R-M1-01.
  - `begonnen` and `verworfen` lines put the hash in the field `sha256` and add `status`, for example `{"sha256":…,"status":"begonnen",…}`. They have no `hash` field, so `v1.0.0` skips them.
- `Add` first updates the entry in memory, then appends one line, calls `Sync` and `Close`. The first error of Write, Sync or Close is returned. The entry in memory stays even if writing fails.
- R-M1-01 changes only the format and the ledger API. The caller in `handle` still writes `fertig` after the page exists. R-M1-02 adds `begonnen` before `CreatePage` and the restart rule.

## Alternatives considered
| Option | Rejected because |
| --- | --- |
| Idempotency marker in Notion (search for the page before creating it) | Needs a Notion database change; Mark said no |
| `status` field on every line, also `"status":"fertig"` | Changes every golden `ledger.jsonl` without a gain; a missing status already means `fertig` |
| Keep `hash` for all lines, only add `status` | `v1.0.0` would treat a `begonnen` line as processed and archive the file without a note |
| Rewrite the ledger file (replace lines) | Not append-only; a crash during the rewrite can lose old entries |

## Consequences
- An old binary (`v1.0.0`) still reads a new ledger safely: it sees only `fertig` lines. A file that is only `begonnen` or `verworfen` is processed again there. After a crash this can give a second page, as in `v1.0.0` today, but never a file archived without a note.
- If `v1.0.0` reads a hash whose last line is `verworfen` after an older `fertig` line, it still counts it as processed. This only avoids a second page, it never creates one.
- A failed ledger write is now visible to the caller. Handling it in `handle` comes with R-M1-02.
- `fsync` costs a little time per entry. There are only a few entries per day.
- The folder is not synced after the ledger file is created for the first time. See OPEN.

## OPEN
- Sync of the archive folder after the first creation of the ledger file (only matters for the very first entry). Decide in R-M1-02.
