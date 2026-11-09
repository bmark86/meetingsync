# 0010 Review flag for notes with to-dos for Mark

Status: accepted (Mark, D5, 2026-11-01; reason wording 2026-11-04), R-M3-12.

## Context
- Tasks without a clear owner or due date need a human look (S-06).
- Review reasons so far came only from the LLM and from the summary-only case.
- OD-22: should the flag depend on task creation (`create_tasks_for`) or on the content of the note?

## Decision
- Every note with at least one next action `fuer_mich=true` gets the reason "Enthält To-dos für dich – bitte Zuordnung und Fälligkeit prüfen".
- It is appended once, after the LLM reasons and the summary-only reason.
- It applies always, also with `create_tasks_for=none`: the trigger is `fuer_mich`, not task creation.
- Effects: `Prüfen` checkbox true; yellow notice box "Bitte prüfen: …" at the top (appended with " · " if a box already exists); log `, ⚠️ prüfen`; notification ` · bitte prüfen`; dry-run `Prüfen: true`.

## Alternatives considered
| Option | Rejected because |
| --- | --- |
| Flag only when tasks are actually created (`create_tasks_for` not `none`) | Mark decided D5 is tied to `fuer_mich` (OD-22); with `none` the to-do would get no review hint at all |

## Consequences
- In practice almost every note gets `Prüfen=true`, because most meetings have a to-do for Mark. The flag loses selectivity; this is D5 as decided.
- The notice box counts toward the 1000-block cap (R-M3-06). Consistent, because the box is really sent.
- Golden: 21 scenarios changed on purpose (log suffix, checkbox and callout in `requests.json`, G09 stdout). G14a shows a bullet moving across the 100-block chunk boundary (expected). G03, G04, G10*, G11, G12a/b unchanged.

## OPEN
- None.
