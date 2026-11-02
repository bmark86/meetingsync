# 0004 Ledger states and crash behavior

Status: draft (R-M1-01, extended by R-M1-02 and R-M1-03), 2026-11-02. Decided by Mark on 2026-11-01 (D1, OD-03, OD-05).

## Context
- The ledger (`.meetingsync-ledger.jsonl` in the archive folder) stops a file from being processed twice. In `v1.0.0` a line is written only after the Notion page and the tasks exist.
- A crash between `CreatePage` and the ledger line creates a second page after the restart (A-01).
- Errors from writing the ledger were dropped, and the file was not synced to disk (A-11, S-13).
- Mark still runs `v1.0.0` on his real ledger. `v1.0.0` counts every line with a `hash` field as processed, and also uses its `mtime` to match summaries (`HasNear`).

## Decision

### Format (R-M1-01)
- Each ledger entry has a status: `begonnen` (started), `fertig` (done), `verworfen` (discarded, may run again).
- For each hash the last line wins. `Has` and `HasNear` count only entries whose last status is `fertig`.
- Lines without a status (all `v1.0.0` lines) count as `fertig`. `Add` without a status also means `fertig`. A `sha256` line without a status also counts as `fertig`.
- An unknown status counts as "not `fertig`" and also not as `begonnen`: the file runs again.
- File format:
  - `fertig` lines stay exactly as in `v1.0.0`: `{"hash":…,"file":…,"mtime":…,"url":…,"at":…}`, no status field. Since R-M1-03 they may also carry the optional `omitempty` fields `todos_fehlen` and `bloecke_fehler`; on the normal path the line is still byte-identical to `v1.0.0`.
  - `begonnen` and `verworfen` lines put the hash in the field `sha256` and add `status`, for example `{"sha256":…,"status":"begonnen",…}`. They have no `hash` field, so `v1.0.0` skips them.
- `Add` first updates the entry in memory, then appends one line, calls `Sync` and `Close`. The first error of Write, Sync or Close is returned. The entry in memory stays even if writing fails.
- Torn last line: if the file is not empty and does not end with a newline (crash while writing), `Add` first writes a newline. So the new line does not merge with the broken rest. The normal path writes exactly the same bytes as before.
- Folder sync: when `Add` creates the ledger file, it also syncs the folder that holds it, so the new file survives a crash. An error from this sync is returned like a write error. Later entries do not sync the folder.

### Flow in `handle` (R-M1-02)
1. Last status `fertig` → archive as before (`↺`).
2. Last status `begonnen` → restart rule (see below). No LLM run, no Notion request.
3. Otherwise LLM run as before. Right before `CreatePage`, write `begonnen`.
   - `begonnen` is written after the LLM run, not before. A crash or stop during the long LLM run cannot create a page, so the file runs again normally.
   - If writing `begonnen` fails: no Notion request. The error counts as an attempt (`max_attempts`, 2 minutes wait). The entry in memory is set to `verworfen`, so the next attempt runs normally.
4. `CreatePage` returns an error (for example Notion 4xx or 5xx, or no connection): write `verworfen`. Retry and backoff stay as before. Per OD-05 a 5xx stays transient, even though the page may exist; this stays until there is data.
5. `CreatePage` stops because the context was cancelled (Ctrl+C, launchd stop): `begonnen` stays. The request may have reached Notion. After the restart the restart rule handles the file. The cancel does not count as an attempt, as before.
6. Page created → tasks → write `fertig` with the URL → archive.
   - If writing `fertig` fails: the error is logged with the page URL (`Protokoll nicht geschrieben (Notiz existiert: <url>): …`, ledger not written, note exists). The hash counts as done in memory, so this process makes no second page. The files are still archived.
- `-file` (`ProcessSingleFile`) still writes no ledger line. That changes in R-M3-14.

### Missing tasks and blocks (R-M1-03, OD-04)
- Once the page exists, the `fertig` line also records what is missing in Notion: `todos_fehlen` (tasks not created, because of an error or a context cancel) and `bloecke_fehler` (failed append of the blocks after the first 100; at most 1 per page). Both fields use `omitempty`, so a complete note gives the same bytes as `v1.0.0`. `v1.0.0` ignores the extra fields and counts the line as done.
- The missing parts are only recorded and logged (file name and counts), never made up later. The files are archived as usual.
- A context cancel during the tasks or the append stops further task requests, but still writes `fertig` with the URL and the counts. Keeping `begonnen` would send a note that already exists to `_fehler` after the restart.
- `CreatePage` returns ID, URL and an error that wraps `errUnvollstaendig` when the append fails. The caller treats this as success with a gap, so there is no retry and no second page.
- `todos_fehlen` is an upper bound: a task request cancelled in flight may have reached Notion, so at most 1 task may exist although it is counted.
- `LoadLedger` does not read the gap fields back; they are for humans only.

### Restart rule (D1, OD-03)
- If a pass finds a file whose hash has `begonnen` as its last status, the run before stopped between `begonnen` and `fertig`. The page may exist.
- The file and its partner go to `_fehler` (error folder). The `.fehler.txt` has this text (German, word for word):
  `Notion-Seite existiert evtl. schon – bitte in Notion prüfen. Die Verarbeitung wurde beim Anlegen der Seite unterbrochen (Absturz oder Abbruch). Fehlt die Seite in Notion, die Datei(en) zurück in den Exportordner legen; sie werden dann neu verarbeitet.`
  (The Notion page may already exist, please check in Notion. If the page is missing, move the file(s) back; it is then processed again.)
- Log line: `✗ <file>: letzter Lauf wurde beim Anlegen in Notion unterbrochen → <_fehler> (bitte in Notion prüfen)`.
- Move order: the partner (summary) first, the file with the hash (transcript) last. The first failed move stops the rule. So if anything is left behind, it is always the file with `begonnen`, and the next pass catches it again. A summary is never left alone in the export folder, where it could become an orphan and create a second page (`HasNear` counts only `fertig`). The same holds for a crash between the two moves.
- Only after all moves succeeded, the ledger gets `verworfen`. A file that Mark moves back then runs normally, also without a restart of the process.
- If a file cannot be moved, `begonnen` stays and nothing else happens. The next pass tries again.
- Identical copies: the hash is kept in a set for the current pass. Further files with the same hash in the same pass also go to `_fehler` with the same note (no second `verworfen` line). The set is reset at the start of each pass, so a file that Mark moves back later runs normally. A copy that only becomes stable in a later pass is not caught.

## Alternatives considered
| Option | Rejected because |
| --- | --- |
| Idempotency marker in Notion (search for the page before creating it) | Needs a Notion database change; Mark said no |
| `status` field on every line, also `"status":"fertig"` | Changes every golden `ledger.jsonl` without a gain; a missing status already means `fertig` |
| Keep `hash` for all lines, only add `status` | `v1.0.0` would treat a `begonnen` line as processed and archive the file without a note |
| Rewrite the ledger file (replace lines) | Not append-only; a crash during the rewrite can lose old entries |
| Write `begonnen` before the LLM run | A stop during the long LLM run would send the file to `_fehler`, although no page can exist |
| `verworfen` also after a context cancel during `CreatePage` | The request may have reached Notion; a restart could create a second page |

## Consequences
- Crash tests show no second page after the restart for these crash points: abort after `begonnen`, after `CreatePage`, after the tasks, after `fertig`, after the archive. Also tested: a failed move of the summary during the restart rule, and an identical copy in the export folder at the restart. Other crash points and the cases below are not covered by this claim.
- Golden: every scenario that creates a page gets one `begonnen` line before each `fertig` line in `ledger.jsonl`. In `tree.txt` only the hash of the ledger file changes, because it hashes the ledger. Requests, logs and stdout stay the same.
- An old binary (`v1.0.0`) still reads a new ledger safely: it sees only `fertig` lines. A file that is only `begonnen` or `verworfen` is processed again there. After a crash this can give a second page, as in `v1.0.0` today, but never a file archived without a note.
- If `v1.0.0` reads a hash whose last line is `verworfen` after an older `fertig` line, it still counts it as processed. This only avoids a second page, it never creates one.
- A crash after moving to `_fehler` but before `verworfen` leaves `begonnen`. If Mark moves the file back, it goes to `_fehler` again. This is safe (no second page), but needs a manual ledger fix. It is very unlikely.
- Residual risk OD-05 (unchanged against `v1.0.0`, planned as task R-M3-17): on a Notion 5xx, a network error or the 60 s client timeout of the Notion client, `CreatePage` returns an error, the ledger gets `verworfen` and the file is retried. The page may exist anyway. So one file can create up to `max_attempts` pages, each with a new LLM run. A 2xx answer whose body cannot be read or parsed also gives `verworfen`, and the retry then creates a certain second page.
- Why a context cancel keeps `begonnen` but a client timeout writes `verworfen`: `process` checks only `ctx.Err()`. A cancel (Ctrl+C, launchd stop) ends the process, so only the restart rule can handle the file, and it must assume the page exists. A client timeout is an error inside a running process with a live context; it is treated like any other error answer and retried as in `v1.0.0` (OD-05).
- Double fault: if writing `fertig` fails (the disk still has `begonnen`) and the summary also cannot be archived, the summary stays in the export folder while the transcript is archived. After a restart `HasNear` does not see the `begonnen` entry, so the summary alone may create a second page as an orphan. This needs two independent errors and is very rare; it is accepted.
- `fsync` costs a little time per entry. There are only a few entries per day.
- Only the folder that holds the ledger is synced, not its parent, even if `Add` creates the archive folder.

## OPEN
- None. (Folder sync after the first creation of the ledger file: decided and done in R-M1-02.)
