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
2. Es erkennt an **Endung und Inhalt**, was Transkript und was Zusammenfassung ist (`.srt`/`.vtt` sind immer Transkript, bei `.txt`/`.md` entscheiden Zeitstempel und Sprecher-Labels). Der Dateiname liefert nur das Datum und die Paarbildung: Gleicher Namensstamm gehört zusammen, sonst gilt die zeitlich nächste Datei innerhalb von `pair_window_seconds`.
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

Läuft LM Studio mal nicht, ist das kein Problem: Die Dateien bleiben liegen und werden automatisch erneut versucht (nach 1, 2, 4, 8 Minuten, danach alle 16 Minuten, ohne Ende).

Zeitbedarf: ca. 4–5 Minuten LLM-Zeit pro Meeting-Stunde (Erfahrungswert mit `gemma-4-31b-qat`).

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
cp config.example.yaml ~/.config/meetingsync/config.yaml
```

Eine bestehende `config.json` funktioniert weiterhin. Liegen beide Dateien im Ordner, hat `config.yaml` Vorrang.

> **Aktive Config:** `~/.config/meetingsync/config.yaml`. Ohne `-config` wird sie gelesen, beim Start steht der Pfad im Log („Konfiguration: …“).
> Änderungen nur dort machen. Eine `config.yaml` im Repo-Ordner ist per `.gitignore` ausgeschlossen und wird nur mit `-config ./config.yaml` gelesen. Sie ist also keine aktive Kopie und kann bei `git clean -x` verloren gehen.

Anpassen musst du nur drei Dinge:

- `watch_dir` – dein MacWhisper-Exportordner
- `llm.model` – der Identifier aus LM Studio, z. B. `google/gemma-4-31b-qat`
- `my_speaker_label` – der Sprechername deiner eigenen Spur, bei MacWhisper `"Microphone"`

In `config.example.yaml` sind die IDs der Notes-, Tasks- und Projects-Datenbank schon eingetragen. Alle Optionen mit Kommentaren stehen dort.

> **macOS-Hinweis:** Liegt der Exportordner unter *Dokumente*, *Schreibtisch* oder *Downloads*, blockiert macOS den Zugriff im Hintergrund. Entweder einen Ordner wie `~/Meetings/MacWhisper` nehmen oder `~/bin/meetingsync` unter *Systemeinstellungen → Datenschutz & Sicherheit → Festplattenvollzugriff* freigeben.

### 5. Prüfen und testen

```bash
meetingsync -check
```

zeigt, ob Notion und LM Studio erreichbar sind und ob das Modell gefunden wird. Exit 1 gibt es bei Config-Fehler, fehlendem Token oder Schlüsselbund-Fehler und bei Fehlern mit der Notes- oder Tasks-Datenbank. Ein ✗ bei Ordner, LM Studio oder Projects-Datenbank endet trotzdem mit Exit 0, also die Ausgabe lesen.

Liegt `~/bin` nicht im PATH, rufst du `~/bin/meetingsync …` auf oder trägst `export PATH="$HOME/bin:$PATH"` in `~/.zshrc` ein.

Weitere Aufrufe:

```bash
meetingsync -once                         # einen Durchlauf über den Exportordner, dann beenden
meetingsync -config ./config.yaml -check  # andere Konfigurationsdatei verwenden
```

Dann mit einem echten Transkript testen, **ohne** etwas nach Notion zu schreiben:

```bash
meetingsync -dry-run -file ~/Meetings/MacWhisper/irgendein-transkript.srt
```

Du siehst das JSON, das das Modell liefert. Passt das, legt derselbe Befehl ohne `-dry-run` eine echte Notiz an.

> **Achtung `-file` ohne `-dry-run`:** Es entsteht eine echte Notiz, aber kein Ledger-Eintrag, und die Datei wird nicht verschoben. Liegt sie im `watch_dir`, verarbeitet ein späterer Lauf sie erneut (zweite Notiz). Danach die Datei aus dem `watch_dir` nehmen.

### 6. Dauerbetrieb
Was eine plist ist, warum sie unter `~/Library/LaunchAgents` liegt und wie `launchctl` funktioniert: siehe [docs/launchd.md](docs/launchd.md).

`de.borukhov.meetingsync.plist` öffnen, `DEINNAME` ersetzen, dann:

```bash
cp de.borukhov.meetingsync.plist ~/Library/LaunchAgents/
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/de.borukhov.meetingsync.plist
tail -f ~/Library/Logs/meetingsync.log
```

Die plist ruft `-config …/config.yaml` explizit auf, ein Wechsel der Config-Datei braucht also auch eine Anpassung dort.

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

Exportiere nur den **kombinierten SRT-Export** aus MacWhisper (Sprecher + Zeitmarken). Deine eigene Spur heißt darin `Microphone`, das trägst du als `my_speaker_label` ein.

> **Warnung:** `.md`/`.txt` ohne Zeitmarken gelten als Zusammenfassung. Hat sie keinen Partner, wird nach `orphan_summary_minutes` eine eigene Notiz mit Prüf-Box daraus (nur bei `orphan_summary_action: note`, dem Standard). Getrennte Spur-Exporte (`_app-audio` / `_mic-audio`) ergeben je eine eigene Notiz.

- **Unterstützte Endungen:** `.srt`, `.vtt`, `.json`, `.txt`, `.md`, `.markdown`. `.srt`/`.vtt` sind immer Transkript, `.json` wird an der Segmentstruktur erkannt, bei `.txt`/`.md` entscheidet der Inhalt.
- **Ignoriert** werden Unterordner, versteckte und temporäre Dateien (`.tmp`, `.part`, …) sowie Dateien über 20 MB. Nicht unterstützte Endungen werden ebenfalls ignoriert, mit einer Logmeldung.

Wenn die Erkennung nicht passt, kannst du sie in der Config festnageln, z. B.:

```yaml
transcript_extensions: [srt]
summary_extensions: [md]
```

Die MacWhisper-Zusammenfassung wird standardmäßig **nicht** benutzt, das Modell arbeitet direkt mit dem Transkript. Sie wird nur mit archiviert. Mit `use_summary_as_context: true` bekommt das Modell sie als Zusatzinfo.

**Datum und Uhrzeit** nimmt das Tool aus dem Dateinamen, falls drin (MacWhisper-Format `2026-10-30 14_30_49`, außerdem `24.08.2026`). Ohne Datum gilt der Exportzeitpunkt minus Länge des Transkripts. Steht im Namen nur ein Datum ohne Uhrzeit, gilt dieses Datum ohne Uhrzeit. Ist es der Exporttag, wird die Uhrzeit aus Exportzeitpunkt minus Dauer geschätzt.

**Ledger:** Verarbeitete Dateien stehen in `<archive_dir>/.meetingsync-ledger.jsonl`. Es arbeitet per Inhalts-Hash, dieselbe Datei wird also nie zweimal verarbeitet (außer per `-file`, siehe oben), auch nicht unter anderem Namen.

---

## Konfiguration im Detail

Die vollständige, kommentierte Referenz aller Optionen steht in [config.example.yaml](config.example.yaml). Die wichtigsten:

| Schlüssel | Standard | Bedeutung |
|---|---|---|
| `watch_dir` | – | Exportordner von MacWhisper |
| `archive_dir` / `failed_dir` | `watch_dir/_archiv`, `watch_dir/_fehler` | Ziel für verarbeitete bzw. gescheiterte Dateien |
| `my_name` | `Mark` | Dein Name, damit deine To-dos erkannt werden |
| `my_speaker_label` | leer | Sprechername deiner Mikrofonspur, bei MacWhisper `"Microphone"` |
| `include_transcript` | `false` | Volles Transkript als aufklappbarer Block in die Notion-Seite |
| `use_summary_as_context` | `false` | MacWhisper-Zusammenfassung zusätzlich ans Modell geben |
| `orphan_summary_action` | `note` | Datei ohne Zeitmarken und ohne Partner: `note` (Notiz mit Prüf-Hinweis), `archive` oder `keep` |
| `orphan_summary_minutes` | `10` | So lange wartet eine solche Datei auf ihr Transkript |
| `pair_window_seconds` | `180` | Zeitfenster, in dem Transkript und Zusammenfassung als Paar gelten |
| `stable_seconds` | `20` | Datei muss so lange unverändert sein |
| `max_attempts` | `3` | Fehlversuche, danach wandert die Datei nach `_fehler` (mit `.fehler.txt`) |
| `llm.model` | – | Modell-Identifier aus LM Studio |
| `llm.max_chunk_chars` | `60000` | Längere Transkripte werden erst abschnittsweise verdichtet |
| `notion.create_tasks_for` | `me` | `me` = nur deine To-dos, `all` = alle, `none` = keine |

Der Notion-Token wird in dieser Reihenfolge gesucht: Umgebungsvariable `NOTION_TOKEN` → Schlüsselbund, wenn `token_keychain_service` gesetzt ist (ein Fehler dort bricht ab, `notion.token` wird dann nicht gelesen) → `notion.token`.

---

## Datenschutz

- Transkription (MacWhisper) und Zusammenfassung (LM Studio) laufen komplett lokal.
- Nach Notion geht nur die fertige Notiz. Das Rohtranskript nur, wenn du `include_transcript` einschaltest.
- Die Originaldateien bleiben auf dem Mac im Archivordner.

## Wenn etwas hakt

| Symptom im Log | Ursache |
|---|---|
| `ungültiges YAML in … (Zeile N)` | Syntaxfehler in der Config in der genannten Zeile. Der Inhalt steht bewusst nicht in der Meldung (könnte ein Token sein) |
| `LM Studio nicht erreichbar` | Server in LM Studio nicht gestartet – Dateien warten einfach |
| `Modell … nicht geladen` | Modell laden oder Identifier in `llm.model` prüfen (`meetingsync -check` listet alle) |
| `Antwort … abgeschnitten` | Kontextlänge in LM Studio erhöhen oder `llm.max_chunk_chars` verkleinern |
| `Notion HTTP 404 … Integration verbunden?` | Integration ist nicht mit der Second-Brain-Seite verbunden. Klappt es trotz Verbindung nicht, statt der Data-Source-ID die Datenbank-ID aus der Notion-URL eintragen – beides wird akzeptiert. |
| `operation not permitted` | macOS-Schutz, siehe Hinweis bei Schritt 4 |
| `Notion HTTP 401 … – Token gültig? Ist die Integration mit der Datenbank verbunden?` bleibt dauerhaft | Schlüsselbund-Eintrag ohne `-w` angelegt (leeres Token, Eintrag neu anlegen, siehe Schritt 3) oder Token ungültig bzw. widerrufen |

Vorübergehende Fehler (LM Studio oder Notion nicht erreichbar, auch bei Timeout, HTTP 429/5xx, LM-Studio-Modell nicht geladen, Notion 401/403/404) werden ohne Ende wiederholt: nach 1, 2, 4, 8 Minuten, danach alle 16 Minuten. Solche Dateien landen nie in `_fehler`, sie warten, bis die Ursache behoben ist. Nur andere Fehler zählen gegen `max_attempts`.

## Weitere Doku

- [docs/changelog.md](docs/changelog.md) – Änderungen
- [docs/progress.md](docs/progress.md) – Task-Stand und offene Punkte
- [docs/adr/](docs/adr/) – Architekturentscheidungen
- [docs/launchd.md](docs/launchd.md) – plist und `launchctl` erklärt
