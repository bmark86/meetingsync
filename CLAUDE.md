# meetingsync

Go-Tool (macOS, nur stdlib, `go 1.22`): MacWhisper-Export → lokales LLM (LM Studio, Gemma) → Meeting-Notiz in Notion (Notes, Tasks, Projects). Läuft dauerhaft per launchd. Details und Konfiguration: `README.md`.

## Ablauf

Exportordner pollen (10 s), Datei muss `stable_seconds` unverändert sein → Transkript/Zusammenfassung am Inhalt erkennen (`classify.go`) → LLM-Extraktion, lange Transkripte in Chunks (`llm.go`) → Notion-Seite und Tasks (`blocks.go`, `notion.go`) → Archiv `_archiv/JJJJ-MM`, Ledger gegen Doppelverarbeitung (`app.go`). Bei Fehlern Retry mit Backoff, nach `max_attempts` nach `_fehler`.

## Befehle

- `make test` (`go test ./...`, inkl. `e2e_test.go`)
- `make vet` (`go vet ./...`)
- `make check` (vet + test, Definition of Done)
- `go build -o ~/bin/meetingsync .`
- `meetingsync -check` (Notion/LM Studio prüfen), `-dry-run -file <datei>` (nichts schreiben), `-once`

## Definition of Done

`make check` läuft grün. Die Befehle gehören in EVIDENCE des Berichts.

## Global Constraints (unverhandelbar)

- Ohne ausdrückliche Zustimmung von Mark dürfen keine existierenden Notizen oder Verknüpfungen (Relationen zu Projekten/Tasks) gelöscht werden, weder in Notion noch lokal. Das gilt auch für Archiv- und Fehlerdateien.

## Regeln

- Datenschutz: Rohtranskript verlässt den Mac nicht, nur die fertige Notiz geht nach Notion (Transkript nur mit `include_transcript`).
- Notion-Token nie ins Repo: `NOTION_TOKEN` → Keychain (`meetingsync-notion`) → `notion.token`. Echte `config.json` liegt unter `~/.config/meetingsync/`.
- Nur stdlib, keine neuen Dependencies ohne Rückfrage.
- Code-Kommentare, Logs und Notion-Inhalte sind deutsch.

## Agenten-Team

Hauptsession läuft als `coordinator` (gesetzt über `agent` in `.claude/settings.local.json`).
Alle Subagents laden diese Datei. Rollendetails stehen in `.claude/agents/<name>.md`.

| Agent | Rolle | Schreibt |
| --- | --- | --- |
| `coordinator` | Plant, verteilt, wählt Modell pro Implementierer-Task, verifiziert, entscheidet | nichts außer Task-Status |
| `developer` | Setzt genau einen Task um, inkl. Unit-Tests | Code im Task-Scope |
| `spec-reviewer` | Prüft Diff auf Task- und Constraint-Treue | nichts |
| `code-reviewer` | Prüft Korrektheit, Design, Testqualität | nichts |
| `security-reviewer` | Prüft sicherheitsrelevante Diffs | nichts |
| `qa` | Prüft gegen Akzeptanzkriterien, schreibt Integrationstests | nur Tests |
| `docs-writer` | Pflegt Changelog, Architektur, ADRs, Prozesse | nur `docs/` |

## Berichtsformat (jeder Unteragent endet genau damit)

```text
STATUS: done | partial | blocked
SCOPE: <task id + one line>
CHANGES: <file paths, one per line, or "none">
EVIDENCE: <command> -> <exit code / result summary>, one per line
FINDINGS: <numbered, file:line, or "none">
ASSUMPTIONS: <what you assumed, or "none">
OPEN: <what is unresolved / needs a human decision, or "none">
```

Berichte sollten nicht länger als 40 Zeilen sein. Außerhalb dieses Blocks darf kein Fließtext stehen.
