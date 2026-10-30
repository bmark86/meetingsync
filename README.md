# meetingsync

MacWhisper-Export → lokales LLM (LM Studio) → Meeting-Notiz im Second Brain.

```
MacWhisper ──► Exportordner ──► meetingsync ──► LM Studio (Gemma, lokal)
                                     │
                                     ├──► Notion › Notes   (Template "Meeting Notes", Status Inbox)
                                     ├──► Notion › Tasks   (deine To-dos, verknüpft mit der Notiz)
                                     └──► _archiv/JJJJ-MM  (verarbeitete Dateien)
```

**Was passiert pro Meeting**

1. Das Tool schaut alle 10 s in den Exportordner und wartet, bis eine Datei 20 s lang nicht mehr verändert wurde.
2. Es erkennt am **Inhalt**, was Transkript und was Zusammenfassung ist (Zeitstempel und Sprecher-Labels = Transkript). Die Dateinamen spielen keine Rolle.
3. Das Transkript geht an dein lokales Modell in LM Studio. Nichts davon verlässt den Mac.
4. In Notion entsteht eine Seite in **Notes** mit genau den Abschnitten deines Templates, Status **Inbox**, und – falls eindeutig – verknüpft mit einem aktiven **Projekt**.
5. Aufgaben, die *du* übernommen hast, landen als To-dos in **Tasks** (mit Deadline, verknüpft mit Notiz und Projekt).
6. Transkript und Zusammenfassung wandern nach `_archiv/JJJJ-MM/`. Ein Protokoll verhindert, dass eine Datei je zweimal verarbeitet wird.

Wenn das Modell unsicher ist (Sprecherzuordnung, schlechte Tonqualität, keine Zeitmarken), steht oben auf der Seite eine gelbe **Bitte prüfen**-Box, die Checkbox **Prüfen** wird gesetzt und du bekommst eine Mitteilung auf dem Mac.

---

## Einrichtung (ca. 15 Minuten)

### 1. Bauen

```bash
cd meetingsync
go test ./...                       # optional, dauert 1 Sekunde
go build -o ~/bin/meetingsync .
```

### 2. LM Studio vorbereiten

- Developer-Tab → **Start Server** (Port 1234).
- Gemma laden und die **Kontextlänge auf mindestens 32768** stellen. Der Standardwert (4096) ist für Transkripte viel zu klein.
- Den exakten Modell-Identifier kopieren (im Developer-Tab oder per `lms ls`). Den brauchst du gleich für `llm.model`.

Läuft LM Studio mal nicht, ist das kein Problem: Die Dateien bleiben liegen und werden automatisch erneut versucht (nach 1, 2, 4, 8, 16 Minuten).

### 3. Notion-Integration anlegen

1. [notion.so/profile/integrations](https://www.notion.so/profile/integrations) → **Neue Integration** → Typ *Intern* → Name z. B. „meetingsync". Berechtigungen: Inhalte lesen, aktualisieren, einfügen.
2. Den Token kopieren.
3. In Notion die Seite **Second Brain** öffnen → `•••` → **Verbindungen** → „meetingsync" hinzufügen. Damit hat die Integration Zugriff auf Notes, Tasks und Projects darunter.

Token sicher im Schlüsselbund ablegen (du wirst nach dem Token gefragt):

```bash
security add-generic-password -a "$USER" -s meetingsync-notion -w
```

### 4. Konfiguration

```bash
mkdir -p ~/.config/meetingsync
cp config.example.json ~/.config/meetingsync/config.json
```

Anpassen musst du nur zwei Dinge:

- `watch_dir` – dein MacWhisper-Exportordner
- `llm.model` – der Identifier aus LM Studio

Die IDs deiner Notes-, Tasks- und Projects-Datenbank sind schon eingetragen.

> **macOS-Hinweis:** Liegt der Exportordner unter *Dokumente*, *Schreibtisch* oder *Downloads*, blockiert macOS den Zugriff im Hintergrund. Entweder einen Ordner wie `~/Meetings/MacWhisper` nehmen oder `~/bin/meetingsync` unter *Systemeinstellungen → Datenschutz & Sicherheit → Festplattenvollzugriff* freigeben.

### 5. Prüfen und testen

```bash
meetingsync -check
```

zeigt, ob Notion und LM Studio erreichbar sind und ob das Modell gefunden wird.

Dann mit einem echten Transkript testen, **ohne** etwas nach Notion zu schreiben:

```bash
meetingsync -dry-run -file ~/Meetings/MacWhisper/irgendein-transkript.txt
```

Du siehst das JSON, das das Modell liefert. Passt das, legt derselbe Befehl ohne `-dry-run` eine echte Notiz an (die Datei bleibt dabei liegen).

### 6. Dauerbetrieb

`de.borukhov.meetingsync.plist` öffnen, `DEINNAME` ersetzen, dann:

```bash
cp de.borukhov.meetingsync.plist ~/Library/LaunchAgents/
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/de.borukhov.meetingsync.plist
tail -f ~/Library/Logs/meetingsync.log
```

Stoppen: `launchctl bootout gui/$(id -u)/de.borukhov.meetingsync`

---

## Notion: zwei Ansichten für den Feierabend

Damit die Inbox nicht zum Archiv wird:

1. In **Notes** eine Checkbox-Property **Prüfen** anlegen (optional – ohne sie gibt es nur die gelbe Box auf der Seite).
2. Auf der Second-Brain-Startseite zwei verknüpfte Ansichten:
   - **Zu prüfen** – Notes, Filter: *Prüfen ist angehakt* und *Status = Inbox*
   - **Meine offenen To-dos** – Tasks, Filter: *Completed ist nicht angehakt*, sortiert nach *Due Date*

Abends gehst du nur noch die markierten Notizen durch. Alle anderen sind fertig vorbereitet und brauchen nur einen Blick.

---

## MacWhisper-Export

Am besten exportierst du das Transkript als **SRT** oder als **TXT mit Zeitstempeln und Sprechern**. Die Zusammenfassung darf jedes Textformat haben.

Wenn Transkript und Zusammenfassung unterschiedliche Endungen haben, kannst du die Erkennung festnageln, z. B.:

```json
"transcript_extensions": ["srt"],
"summary_extensions": ["md"]
```

Die MacWhisper-Zusammenfassung wird standardmäßig **nicht** benutzt, das Modell arbeitet direkt mit dem Transkript. Sie wird nur mit archiviert. Mit `"use_summary_as_context": true` bekommt das Modell sie als Zusatzinfo.

**Datum und Uhrzeit** nimmt das Tool aus dem Dateinamen, falls drin (`2026-10-28 14-00`, `28.10.2026`). Sonst schätzt es: Exportzeit minus Länge des Transkripts.

---

## Konfiguration im Detail

| Schlüssel | Standard | Bedeutung |
|---|---|---|
| `watch_dir` | – | Exportordner von MacWhisper |
| `archive_dir` / `failed_dir` | `watch_dir/_archiv`, `watch_dir/_fehler` | Ziel für verarbeitete bzw. gescheiterte Dateien |
| `my_name` | `Mark` | Dein Name, damit deine To-dos erkannt werden |
| `my_speaker_label` | leer | Falls deine Mikrofonspur immer gleich heißt, z. B. `"Speaker 1"` |
| `include_transcript` | `false` | Volles Transkript als aufklappbarer Block in die Notion-Seite |
| `use_summary_as_context` | `false` | MacWhisper-Zusammenfassung zusätzlich ans Modell geben |
| `orphan_summary_action` | `note` | Datei ohne Zeitmarken und ohne Partner: `note` (Notiz mit Prüf-Hinweis), `archive` oder `keep` |
| `orphan_summary_minutes` | `10` | So lange wartet eine solche Datei auf ihr Transkript |
| `pair_window_seconds` | `180` | Zeitfenster, in dem Transkript und Zusammenfassung als Paar gelten |
| `stable_seconds` | `20` | Datei muss so lange unverändert sein |
| `max_attempts` | `3` | Danach wandert eine Datei nach `_fehler` (mit `.fehler.txt`) |
| `title_format` | `{date} – {title}` | Platzhalter: `{date}`, `{date_de}`, `{title}`, `{typ}` |
| `notify` | `true` | Mac-Mitteilung bei To-dos für dich oder Prüfbedarf |
| `llm.max_chunk_chars` | `60000` | Längere Transkripte werden erst abschnittsweise verdichtet |
| `llm.structured_output` | `true` | JSON-Schema an LM Studio; fällt automatisch zurück, falls nicht unterstützt |
| `notion.create_tasks_for` | `me` | `me` = nur deine To-dos, `all` = alle, `none` = keine |

Der Notion-Token wird in dieser Reihenfolge gesucht: Umgebungsvariable `NOTION_TOKEN` → Schlüsselbund (`token_keychain_service`) → `notion.token`.

---

## Datenschutz

- Transkription (MacWhisper) und Zusammenfassung (LM Studio) laufen komplett lokal.
- Nach Notion geht nur die fertige Notiz. Das Rohtranskript nur, wenn du `include_transcript` einschaltest.
- Die Originaldateien bleiben auf dem Mac im Archivordner.

## Wenn etwas hakt

| Symptom im Log | Ursache |
|---|---|
| `LM Studio nicht erreichbar` | Server in LM Studio nicht gestartet – Dateien warten einfach |
| `Modell … nicht geladen` | Modell laden oder Identifier in `llm.model` prüfen (`meetingsync -check` listet alle) |
| `Antwort … abgeschnitten` | Kontextlänge in LM Studio erhöhen oder `max_chunk_chars` verkleinern |
| `Notion HTTP 404 … Integration verbunden?` | Integration ist nicht mit der Second-Brain-Seite verbunden. Klappt es trotz Verbindung nicht, statt der Data-Source-ID die Datenbank-ID aus der Notion-URL eintragen – beides wird akzeptiert. |
| `operation not permitted` | macOS-Schutz, siehe Hinweis bei Schritt 4 |
