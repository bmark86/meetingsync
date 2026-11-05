# 0004 Ledger states and crash behavior

Status: draft (R-M1-01, extended by R-M1-02, R-M1-03 and R-M3-17), 2026-11-02. Decided by Mark on 2026-11-01 (D1, OD-03, OD-05).

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
- `-file` (`ProcessSingleFile`) writes the same lines (`begonnen`, `fertig`, `verworfen`) since R-M3-14, but does not move the file. See ADR 0013.

### Recheck before archiving (R-M1-05, S-15)
- Since R-M5-01 the content is read lazily (in `handle`, not in `watch.List` on a cache hit). The hash is always computed from content actually read in that pass, never from cached size+mtime.
- Before archiving a job (in `handle`, on the processed path and on the already-processed path `Has`), only the primary file is checked again (`Lstat`, size and mtime) against the values from the pass that read it, because the ledger key is the hash of the primary file. If they differ, the primary file is not archived: a warning with the file name is logged (`… wurde seit dem Einlesen geändert → nicht archiviert …`) and the file stays in the export folder. The next pass reads the new content and gets a new hash. A transcript is processed as a new file (one new page, without the archived summary as context; the first note stays). A summary-only primary whose new mtime is within `pair_window_seconds` of a processed note is archived via `HasNear` without a new note. The `fertig` line for the processed hash is still written, because that note exists; so the restart rule does not apply to it. Decision (coordinator): only the primary file is rechecked. Partner files (the summary of a pair) are archived as before, even if they changed (unprocessed content is not retried): a kept summary would become an orphan in the next pass, miss its `HasNear` match and, with `orphan_summary_action=note`, create a second note. The `HasNear` path and `orphan_summary_action=archive` have no recheck. If the primary file is gone (`Lstat` fails), it counts as unchanged and `watch.Mover.Move` (`internal/watch/move.go`) reports the error as before.

### Archive backoff and regular files only (R-M1-07)
- If archiving a file fails (any `watch.Mover.Move` error, for example `Remove(src)` fails), the file gets a retry time 2 minutes later via the retry state, like the restart rule. This does not count against `max_attempts` and never sends the file to `_fehler`. So the warning `… konnte nicht archiviert werden …` and the copy are not repeated every poll.
- The wait applies on all archive paths: already processed (`Has`), just processed, `HasNear` and `orphan_summary_action=archive`. For summaries the retry time is now checked before `HasNear`. After a successful archive the retry state of the file is removed. On the processed path the old retry state is removed before archiving, so an archive error sets a fresh wait.
- `watch.Lister.List` (`internal/watch/watch.go`) only picks up regular files. Symlinks, devices, sockets and similar are skipped (folders already were). This is logged once per path and process: `<name>: keine reguläre Datei (z. B. Symlink) → übersprungen` (not a regular file, skipped). The link and its target stay untouched.

### Missing tasks and blocks (R-M1-03, OD-04)
- Once the page exists, the `fertig` line also records what is missing in Notion: `todos_fehlen` (tasks not created, because of an error or a context cancel) and `bloecke_fehler` (failed append of the blocks after the first 100; at most 1 per page). Both fields use `omitempty`, so a complete note gives the same bytes as `v1.0.0`. `v1.0.0` ignores the extra fields and counts the line as done.
- The missing parts are only recorded and logged (file name and counts), never made up later. The files are archived as usual.
- A context cancel during the tasks or the append stops further task requests, but still writes `fertig` with the URL and the counts. Keeping `begonnen` would send a note that already exists to `_fehler` after the restart.
- `CreatePage` returns ID, URL and an error that wraps `errUnvollstaendig` when the append fails. The caller treats this as success with a gap, so there is no retry and no second page.
- `todos_fehlen` is an upper bound: a task request cancelled in flight may have reached Notion, so at most 1 task may exist although it is counted.
- `ledger.Load` does not read the gap fields back; they are for humans only.

### Restart rule (D1, OD-03)
- If a pass finds a file whose hash has `begonnen` as its last status, the run before stopped between `begonnen` and `fertig`. The page may exist.
- The file and its partner go to `_fehler` (error folder). The `.fehler.txt` has this text (German, word for word):
  `Notion-Seite existiert evtl. schon – bitte in Notion prüfen. Die Verarbeitung wurde beim Anlegen der Seite unterbrochen (Absturz oder Abbruch). Fehlt die Seite in Notion, die Datei(en) zurück in den Exportordner legen; sie werden dann neu verarbeitet.`
  (The Notion page may already exist, please check in Notion. If the page is missing, move the file(s) back; it is then processed again.)
- Log line: `✗ <file>: letzter Lauf wurde beim Anlegen in Notion unterbrochen → <_fehler> (bitte in Notion prüfen)`.
- Move order: the partner (summary) first, the file with the hash (transcript) last. The first failed move stops the rule. So if anything is left behind, it is always the file with `begonnen`, and the next pass catches it again. A summary is never left alone in the export folder, where it could become an orphan and create a second page (`HasNear` counts only `fertig`). The same holds for a crash between the two moves.
- Only after all moves succeeded, the ledger gets `verworfen`. A file that Mark moves back then runs normally, also without a restart of the process.
- If a file cannot be moved, `begonnen` stays and nothing else happens. The move is retried after 2 minutes via the retry state; this does not count against `max_attempts` (R-M1-04).
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
- Ambiguous `CreatePage` failure (OD-05, task R-M3-17, Mark 2026-11-04): Notion 5xx, network error or timeout (`*url.Error`), and an unreadable or unparsable 2xx body count as `notion.ErrUnklar`; the page may exist. Texts and the transient class are unchanged; 429 and other 4xx are not ambiguous. Protection works only if `notes_properties.source` is set (text column "Quelle" in Notes; empty = off, behavior as before):
  - On create, the marker `<file name> · <modification time 2006-01-02 15:04:05 in config timezone>` is written into the column. No content hash is stored in Notion (privacy: a hash would let someone with the file confirm the link).
  - Before the next create, the Notes data source is queried (`rich_text` equals) for the marker stored in the `verworfen` entry at the failure (survives restart, rename and put-back), and for the current marker if different.
  - Found: reuse the page (append blocks beyond the first 100, create tasks, `fertig`, archive). Several matches: the oldest, with a German warning; nothing is deleted.
  - Not found within 5 minutes of the failure: transient wait, no create (the Notion query index lags). After 5 minutes: create. Lookup error: normal failed attempt, no create.
  - The LLM result is cached in memory (bound to the content hash) and reused on the retry, so the page content stays consistent. After a restart the LLM runs once again.
  - `giveUp` after an ambiguous cause adds "Seite evtl. angelegt – bitte in Notion prüfen" to `.fehler.txt`. `Init` and `-check` print a German hint if `source` is set but the column is missing or not `rich_text`. `-file` does no lookup.
- Remaining residual risks: without `notes_properties.source` the old risk stays (up to `max_attempts` pages). Two ambiguous creates with a rename in between keep only the newer marker. A non-ambiguous `verworfen` (for example HTTP 400) followed by a restart triggers a lookup and up to 5 minutes delay (delay only). A 2xx with valid JSON but an empty id still counts as success. The restart rule with `begonnen` on disk is unchanged (file goes to `_fehler`).
- Why a context cancel keeps `begonnen` but a client timeout writes `verworfen`: `process` checks only `ctx.Err()`. A cancel (Ctrl+C, launchd stop) ends the process, so only the restart rule can handle the file, and it must assume the page exists. A client timeout is an error inside a running process with a live context; it is retried, now with the lookup above when `notes_properties.source` is set.
- Double fault: if writing `fertig` fails (the disk still has `begonnen`) and the summary also cannot be archived, the summary stays in the export folder while the transcript is archived. After a restart `HasNear` does not see the `begonnen` entry, so the summary alone may create a second page as an orphan. This needs two independent errors and is very rare; it is accepted.
- Pruning (R-M5-02, A-19): the per-pass cleanup of in-memory file state never removes states with `ambiguousCreate` or `giveUp`. The in-memory marker can be the only copy of the first mtime marker, so dropping it while the file is briefly absent could allow a second Notion page. Trade-off: such states for files that never return stay until restart (rare failure events only); a returning given-up file goes straight to `_fehler`.
- `fsync` costs a little time per entry. There are only a few entries per day.
- Only the folder that holds the ledger is synced, not its parent, even if `Add` creates the archive folder.

## OPEN
- None. (Folder sync after the first creation of the ledger file: decided and done in R-M1-02.)
