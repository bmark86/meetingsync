# Dauerbetrieb mit launchd

## Was ist eine plist?

- "Property List": das Konfigurationsformat von macOS (XML).
- `de.borukhov.meetingsync.plist` (im Repo) beschreibt einen Hintergrunddienst:

| Key | Wert | Bedeutung |
| --- | --- | --- |
| `Label` | `de.borukhov.meetingsync` | Name des Dienstes |
| `ProgramArguments` | `~/bin/meetingsync -config ~/.config/meetingsync/config.yaml` | Was gestartet wird (in der plist mit ausgeschriebenem Pfad `/Users/DEINNAME/...`) |
| `RunAtLoad` | `true` | Start bei der Anmeldung |
| `KeepAlive` | `true` | Bei Absturz/Beenden neu starten |
| `ProcessType` | `Background` | Niedrige Priorität |
| `StandardOutPath`, `StandardErrorPath` | `~/Library/Logs/meetingsync.log` | Ziel für Log (stdout und stderr in dieselbe Datei) |

## Wofür?

- meetingsync soll dauerhaft laufen und den Exportordner alle 10 s prüfen (`poll_interval_seconds`, Default 10), ohne dass ein Terminal offen ist.
- macOS verwaltet Hintergrunddienste mit **launchd** (Startprozess des Systems, vergleichbar mit systemd).
- Welche Dienste es gibt, erfährt launchd über plists.

## Warum `~/Library/LaunchAgents`?

| Ort | Läuft als | Wann | Admin-Rechte |
| --- | --- | --- | --- |
| `~/Library/LaunchAgents` | du | solange du angemeldet bist | nein |
| `/Library/LaunchAgents` | jeder angemeldete Nutzer | bei Anmeldung des jeweiligen Nutzers | ja |
| `/Library/LaunchDaemons` | root | ohne Anmeldung | ja |

Für meetingsync passt der erste: Der Dienst braucht dein Benutzerkonto für den Schlüsselbund (Notion-Token, Service `meetingsync-notion`) und für Mac-Mitteilungen (`osascript`). Admin-Rechte sind nicht nötig.

## plist und launchctl sind zweierlei

- In `~/Library/LaunchAgents` liegt nur die **plist** (die Beschreibung).
- `launchctl` ist der **Befehl**, um mit launchd zu sprechen.

Befehle laut README (Abschnitt "6. Dauerbetrieb") und plist-Kommentar:

| Zweck | Befehl |
| --- | --- |
| Einrichten und sofort starten | `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/de.borukhov.meetingsync.plist` |
| Stoppen und abmelden | `launchctl bootout gui/$(id -u)/de.borukhov.meetingsync` |
| Läuft er? | `launchctl list \| grep meetingsync` |
| Log verfolgen | `tail -f ~/Library/Logs/meetingsync.log` |

Hinweis: `launchctl load` / `unload` sind die ältere Schreibweise. Dieses Repo nutzt `bootstrap` / `bootout`.

## Alternative: manuell im Terminal

`meetingsync` direkt starten. Läuft, bis das Terminal geschlossen oder der Mac neu gestartet wird. Danach startet nichts von selbst.

## Aktueller Plan

- Ab 2026-11-01 testet Mark eine Woche lang manuell. Der Dienst ist **nicht** eingerichtet.
- Läuft das ohne Probleme, wird der Dienst eingerichtet:
  1. Binary bauen: `go build -o ~/bin/meetingsync .`
  2. plist kopieren nach `~/Library/LaunchAgents/`, dabei `DEINNAME` ersetzen. `-config` zeigt auf `~/.config/meetingsync/config.yaml` (so steht es schon in der Vorlage).
  3. `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/de.borukhov.meetingsync.plist`
  4. Log prüfen: `tail -f ~/Library/Logs/meetingsync.log`

## Hinweis für den manuellen Test

| Aufruf | Verhalten |
| --- | --- |
| ohne Optionen | Sofort ein Durchlauf, danach alle 10 s. Verarbeitet **alles** im Watch-Ordner (aktuell 52 Meetings, grob 5-8 Stunden LLM-Zeit, 52 Notizen). Fertige Dateien wandern nach `_archiv/JJJJ-MM`, der Hash kommt ins Ledger. |
| `-once` | Genau ein Durchlauf, dann Ende. Verarbeitet ebenfalls alles Vorhandene (Archiv und Ledger wie oben), nur ohne Dauerlauf. Instabile Dateien (jünger als `stable_seconds`) und Dateien in Retry-Wartezeit werden übersprungen und nicht erneut versucht. |
| `-file <datei>` | Legt eine Notiz an. Die Datei **bleibt liegen** und kommt **nicht** ins Ledger. |
| `-dry-run` | Nichts wird geschrieben oder verschoben, Ergebnis nur ausgegeben. |

Folge von `-file`: Die Datei muss danach aus dem Watch-Ordner, sonst legt ein späterer Lauf oder der Dienst eine zweite Notiz an (siehe `docs/progress.md`, Follow-up 6). Lösch- oder Verschiebeaktionen an Mark-Dateien nur mit seiner Zustimmung.
