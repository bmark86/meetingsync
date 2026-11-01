# Running as a service with launchd

## What is a plist?

- "Property list": the configuration format of macOS (XML).
- `de.borukhov.meetingsync.plist` (in the repo) describes a background service:

| Key | Value | Meaning |
| --- | --- | --- |
| `Label` | `de.borukhov.meetingsync` | Name of the service |
| `ProgramArguments` | `~/bin/meetingsync -config ~/.config/meetingsync/config.yaml` | What is started (the plist has the full path `/Users/DEINNAME/...`, where `DEINNAME` is a placeholder for your user name) |
| `RunAtLoad` | `true` | Start at login |
| `KeepAlive` | `true` | Restart after a crash or exit |
| `ProcessType` | `Background` | Low priority |
| `StandardOutPath`, `StandardErrorPath` | `~/Library/Logs/meetingsync.log` | Where the log goes (stdout and stderr in the same file) |

## Why use it?

- meetingsync should run all the time and check the export folder every 10 s (`poll_interval_seconds`, default 10). No terminal should need to stay open.
- macOS manages background services with **launchd** (the system's start process, like systemd).
- launchd learns about services from plists.

## Why `~/Library/LaunchAgents`?

| Place | Runs as | When | Admin rights |
| --- | --- | --- | --- |
| `~/Library/LaunchAgents` | you | while you are logged in | no |
| `/Library/LaunchAgents` | every logged-in user | when that user logs in | yes |
| `/Library/LaunchDaemons` | root | without a login | yes |

The first one fits meetingsync. The service needs your user account for the Keychain (Notion token, service `meetingsync-notion`) and for Mac notifications (`osascript`). Admin rights are not needed.

## plist and launchctl are two different things

- `~/Library/LaunchAgents` holds only the **plist** (the description).
- `launchctl` is the **command** that talks to launchd.

Commands as in the README (section "6. Running all the time", the service section) and the plist comment:

| Purpose | Command |
| --- | --- |
| Set up and start now | `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/de.borukhov.meetingsync.plist` |
| Stop and remove | `launchctl bootout gui/$(id -u)/de.borukhov.meetingsync` |
| Is it running? | `launchctl list \| grep meetingsync` |
| Follow the log | `tail -f ~/Library/Logs/meetingsync.log` |

Note: `launchctl load` / `unload` are the older way to write this. This repo uses `bootstrap` / `bootout`.

## Alternative: by hand in the terminal

Start `meetingsync` directly. It runs until you close the terminal or restart the Mac. After that nothing starts by itself.

## Current plan

- From 2026-11-01 Mark tests by hand for one week. The service is **not** set up.
- If this works without problems, the service is set up:
  1. Build the binary: `go build -o ~/bin/meetingsync .`
  2. Copy the plist to `~/Library/LaunchAgents/` and replace `DEINNAME`. `-config` points to `~/.config/meetingsync/config.yaml` (the template already says this).
  3. `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/de.borukhov.meetingsync.plist`
  4. Check the log: `tail -f ~/Library/Logs/meetingsync.log`

## Note for the manual test

| Call | Behavior |
| --- | --- |
| no options | One run right away, then every 10 s. Processes **everything** in the watch folder (now 52 meetings, about 5-8 hours of LLM time, 52 notes). Finished files move to `_archiv/JJJJ-MM` (archive folder by year and month), the hash goes into the ledger. |
| `-once` | Exactly one run, then exit. Also processes everything that is there (archive and ledger as above), just without running continuously. Unstable files (younger than `stable_seconds`) and files waiting for a retry are skipped and not tried again. |
| `-file <file>` | Creates one note. The file **stays in place** and does **not** go into the ledger. |
| `-dry-run` | Nothing is written or moved. The result is only printed. |

Result of `-file`: the file must be taken out of the watch folder afterwards. Otherwise a later run or the service creates a second note (see `docs/progress.md`, follow-up 6). Delete or move actions on Mark's files only with his consent.
