# meetingsync

MacWhisper export → local LLM (LM Studio) → meeting note in the Second Brain.

```
MacWhisper ──► export folder ──► meetingsync ──► LM Studio (Gemma, local)
                                     │
                                     ├──► Notion › Notes   (template "Meeting Notes", status Inbox)
                                     ├──► Notion › Tasks   (your to-dos, linked to the note)
                                     └──► _archiv/JJJJ-MM  (processed files; JJJJ-MM = YYYY-MM)
```

**What happens for each meeting**

1. The tool looks at the export folder every 10 s. It waits until a file has not changed for 20 s.
2. It detects by **extension and content** which file is the transcript and which is the summary. `.srt`/`.vtt` are always transcripts. For `.txt`/`.md`, timestamps and speaker labels decide. The file name only gives the date and the pairing: files with the same name stem belong together. Otherwise the closest file in time within `pair_window_seconds` is used.
3. The transcript goes to your local model in LM Studio. None of it leaves the Mac.
4. A page is created in **Notes** in Notion. It has exactly the sections of your template and the status **Inbox**. If there is a clear match, it is linked to an active **Project**.
5. Tasks that *you* took on become to-dos in **Tasks** (with a due date, linked to the note and the project).
6. The transcript and the summary move to `_archiv/JJJJ-MM/`. A ledger makes sure a file is never processed twice.

If the model is unsure (speaker assignment, poor audio quality, no timestamps), a yellow **Bitte prüfen** box ("please check") appears at the top of the page. The checkbox **Prüfen** ("check") is set, and you get a notification on the Mac.

---

## Setup (about 15 minutes)

### 1. Build

The build needs Go 1.26 (`go 1.26` in `go.mod`). `go.mod` pins the toolchain to go1.26.10. An installed Go 1.21 or newer is enough: with the default `GOTOOLCHAIN=auto`, Go downloads go1.26.10 on the first build.

```bash
cd meetingsync
go test ./...                       # optional, takes 1 second
go build -o ~/bin/meetingsync .
```

### 2. Prepare LM Studio

- Developer tab → **Start Server** (port 1234).
- Load Gemma and set the **context length to at least 32768**. The default (4096) is far too small for transcripts.
- Copy the exact model identifier (in the Developer tab or with `lms ls`). You need it soon for `llm.model`.

If LM Studio is not running, that is fine. The files stay in place and are retried automatically (after 1, 2, 4, 8 minutes, then every 16 minutes, until `max_attempts` is reached, then `_fehler`).

Time needed: about 4–5 minutes of LLM time per hour of meeting (from experience with `gemma-4-31b-qat`).

### 3. Create the Notion integration

1. [notion.so/profile/integrations](https://www.notion.so/profile/integrations) → **New integration** (Neue Integration) → type *Internal* (Intern) → name, for example "meetingsync". Permissions: read content, update content, insert content.
2. Copy the token.
3. In Notion, open the **Second Brain** page → `•••` → **Connections** (Verbindungen) → add "meetingsync". This gives the integration access to Notes, Tasks and Projects below it.

Store the token safely in the keychain (you will be asked for the token):

```bash
security add-generic-password -a "$USER" -s meetingsync-notion -w
```

### 4. Configuration

```bash
mkdir -p ~/.config/meetingsync
cp config.example.yaml ~/.config/meetingsync/config.yaml
```

An existing `config.json` still works. If both files are in the folder, `config.yaml` wins.

> **Active config:** `~/.config/meetingsync/config.yaml`. It is read when you do not pass `-config`. At start, the path is written to the log (`Konfiguration: …`, German for "configuration").
> Make changes only there. A `config.yaml` in the repo folder is excluded by `.gitignore`. It is only read with `-config ./config.yaml`. So it is not an active copy, and it can be lost with `git clean -x`.

You only need to change three things:

- `watch_dir` – your MacWhisper export folder
- `llm.model` – the identifier from LM Studio, for example `google/gemma-4-31b-qat`
- `my_speaker_label` – the speaker name of your own track, `"Microphone"` in MacWhisper

In `config.example.yaml`, the IDs of the Notes, Tasks and Projects databases are already filled in. All options with comments are listed there.

> **macOS note:** If the export folder is under *Documents*, *Desktop* or *Downloads*, macOS blocks background access. Either use a folder like `~/Meetings/MacWhisper`, or allow `~/bin/meetingsync` under *System Settings (Systemeinstellungen) → Privacy & Security (Datenschutz & Sicherheit) → Full Disk Access (Festplattenvollzugriff)*.

### 5. Check and test

```bash
meetingsync -check
```

shows whether Notion and LM Studio are reachable and whether the model is found. It exits with code 1 on a config error, a missing token, a keychain error, and errors with the Notes or Tasks database. A ✗ for the folder, LM Studio or the Projects database still ends with exit code 0, so read the output.

If `~/bin` is not in your PATH, call `~/bin/meetingsync …` or add `export PATH="$HOME/bin:$PATH"` to `~/.zshrc`.

More calls:

```bash
meetingsync -once                         # one pass over the export folder, then exit
meetingsync -config ./config.yaml -check  # use a different config file
```

Then test with a real transcript, **without** writing anything to Notion:

```bash
meetingsync -dry-run -file ~/Meetings/MacWhisper/any-transcript.srt
```

You see the JSON that the model returns. If it looks right, the same command without `-dry-run` creates a real note.

> **Warning, `-file` without `-dry-run`:** A real note is created, but there is no ledger entry and the file is not moved. If the file is in `watch_dir`, a later run processes it again (second note). Remove the file from `watch_dir` afterwards.

### 6. Running all the time

What a plist is, why it lives in `~/Library/LaunchAgents` and how `launchctl` works: see [docs/launchd.md](docs/launchd.md).

Open `de.borukhov.meetingsync.plist`, replace `DEINNAME` (German for "YOURNAME"), then:

```bash
cp de.borukhov.meetingsync.plist ~/Library/LaunchAgents/
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/de.borukhov.meetingsync.plist
tail -f ~/Library/Logs/meetingsync.log
```

The plist calls `-config …/config.yaml` explicitly. If you switch to another config file, you must also change it there.

To stop: `launchctl bootout gui/$(id -u)/de.borukhov.meetingsync`

---

## Notion: two views for the end of the day

So that the inbox does not turn into an archive:

1. In **Notes**, create a checkbox property **Prüfen** (optional. Without it, you only get the yellow box on the page).
2. On the Second Brain start page, add two linked views:
   - **Zu prüfen** ("to check") – Notes, filter: *Prüfen is checked* and *Status = Inbox*
   - **Meine offenen To-dos** ("my open to-dos") – Tasks, filter: *Completed is not checked*, sorted by *Due Date*

In the evening, you only go through the marked notes. All others are fully prepared and only need a quick look.

---

## MacWhisper export

Export only the **combined SRT export** from MacWhisper (speakers + timestamps). Your own track is called `Microphone` in it. Enter this as `my_speaker_label`.

> **Warning:** `.md`/`.txt` without timestamps count as a summary. If it has no partner, it becomes its own note with the yellow **Bitte prüfen** hint box after `orphan_summary_minutes` (only with `orphan_summary_action: note`, the default). Separate track exports (`_app-audio` / `_mic-audio`) each give their own note.

- **Supported extensions:** `.srt`, `.vtt`, `.json`, `.txt`, `.md`, `.markdown`. `.srt`/`.vtt` are always transcripts. `.json` is detected by its segment structure. For `.txt`/`.md`, the content decides.
- **Ignored** are subfolders, hidden and temporary files (`.tmp`, `.part`, …) and files over 20 MB. Unsupported extensions are also ignored, with a log message.

If the detection does not fit, you can fix it in the config, for example:

```yaml
transcript_extensions: [srt]
summary_extensions: [md]
```

By default, the MacWhisper summary is **not** used. The model works directly with the transcript. The summary is only archived. With `use_summary_as_context: true`, the model gets it as extra information.

**Date and time** come from the file name if they are in it (MacWhisper format `2026-10-30 14_30_49`, also `24.08.2026`). Without a date, the export time minus the length of the transcript is used. If the name has only a date and no time, that date is used without a time. If it is the export day, the time is estimated from the export time minus the duration.

**Ledger:** Processed files are listed in `<archive_dir>/.meetingsync-ledger.jsonl`. It works with a content hash. So the same file is never processed twice (except with `-file`, see above), even under a different name.

---

## Configuration in detail

The full, commented reference of all options is in [config.example.yaml](config.example.yaml). The most important ones:

| Key | Default | Meaning |
|---|---|---|
| `watch_dir` | – | Export folder of MacWhisper |
| `archive_dir` / `failed_dir` | `watch_dir/_archiv`, `watch_dir/_fehler` | Target for processed and failed files |
| `my_name` | `Mark` | Your name, so that your to-dos are recognized |
| `my_speaker_label` | empty | Speaker name of your microphone track, `"Microphone"` in MacWhisper |
| `include_transcript` | `false` | Put the full transcript in the Notion page as a collapsible block |
| `use_summary_as_context` | `false` | Also give the MacWhisper summary to the model |
| `orphan_summary_action` | `note` | File without timestamps and without a partner: `note` (note with the **Bitte prüfen** hint box), `archive` or `keep` |
| `orphan_summary_minutes` | `10` | How long such a file waits for its transcript |
| `pair_window_seconds` | `180` | Time window in which a transcript and a summary count as a pair |
| `stable_seconds` | `20` | A file must stay unchanged for this long |
| `max_attempts` | `10` | Failed attempts (all errors, also temporary ones). After that, the file moves to `_fehler` (with `.fehler.txt`) |
| `llm.model` | – | Model identifier from LM Studio |
| `llm.max_chunk_chars` | `60000` | Longer transcripts are first condensed in parts |
| `notion.create_tasks_for` | `me` | `me` = only your to-dos, `all` = all, `none` = none |

The Notion token is looked up in this order: environment variable `NOTION_TOKEN` → keychain, if `token_keychain_service` is set (an error there aborts, and `notion.token` is then not read) → `notion.token`.

---

## Privacy

- Transcription (MacWhisper) and summarizing (LM Studio) run fully locally.
- Only the finished note goes to Notion. The raw transcript goes only if you turn on `include_transcript`.
- The original files stay on the Mac in the archive folder.

## When something goes wrong

| Symptom in the log | Cause |
|---|---|
| `ungültiges YAML in … (Zeile N)` (invalid YAML) | Syntax error in the config at the given line. The content is deliberately not in the message (it could be a token) |
| `LM Studio nicht erreichbar` (not reachable) | Server in LM Studio is not started. Files simply wait |
| `Modell … nicht geladen` (model not loaded) | Load the model or check the identifier in `llm.model` (`meetingsync -check` lists all) |
| `Antwort … abgeschnitten` (answer cut off) | Increase the context length in LM Studio or lower `llm.max_chunk_chars` |
| `Notion HTTP 404 … Integration verbunden?` (integration connected?) | The integration is not connected to the Second Brain page. If it still fails although it is connected, enter the database ID from the Notion URL instead of the data source ID. Both are accepted. |
| `operation not permitted` | macOS protection, see the note in step 4 |
| `Notion HTTP 401 … – Token gültig? Ist die Integration mit der Datenbank verbunden?` (token valid? is the integration connected to the database?) stays for good | Keychain entry was created without `-w` (empty token, create the entry again, see step 3) or the token is invalid or revoked |

Temporary errors (LM Studio or Notion not reachable, also on timeout, HTTP 429/5xx, LM Studio model not loaded, Notion 401/403/404) are retried with backoff: after 1, 2, 4, 8 minutes, then every 16 minutes. Like all other errors, they count against `max_attempts` (default 10). After that, the file goes to `_fehler`. With the default values, a file waits at least about 95 minutes for the cause to be fixed. The run time of each attempt comes on top (for LLM timeouts, up to `llm.timeout_minutes` per attempt). Other errors are retried every 2 minutes.

## More docs

- [docs/changelog.md](docs/changelog.md) – changes
- [docs/progress.md](docs/progress.md) – task status and open items
- [docs/adr/](docs/adr/) – architecture decisions
- [docs/launchd.md](docs/launchd.md) – plist and `launchctl` explained
