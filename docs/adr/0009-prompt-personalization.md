# 0009 Prompt personalization via config

Status: accepted (D4; OD-14 decided by Mark, 2026-11-01), R-M3-11. Findings S-20, A-20.

## Context
- The LLM system prompt in `internal/llm/client.go` hard-coded the role "Head of Engineering" and the example name "Sascha". Personal data of one user sat in the repo.
- `my_name` already comes from the config. Role and example name did not.
- Mark's output must not change, so the prompt with his values has to stay byte-identical (golden G01, G08).

## Decision
- New config keys `my_role` (default `""`) and `example_name` (default `Alex`). Code defaults are neutral.
- Empty `my_role`: ", <role>" is left out, the prompt reads "für <my_name>. Du bekommst …". `example_name` empty or whitespace is reset to `Alex` in `config.Finalize`. `my_role` is not trimmed (a whitespace-only role is self-inflicted).
- Values go `internal/cli` → `internal/pipeline` `Options` → `llm.NoteInput`.
- Golden configs set `my_role: Head of Engineering` and `example_name: Sascha`, so all golden outputs stay byte-identical.
- `config.example.yaml` and `.json` document both keys with `Teamlead` and `Alex`.

## Alternatives considered
| Option | Why not |
| --- | --- |
| Keep today's text as code default | Personal data stays in the repo (S-20); OD-14 chose neutral defaults |
| Freely configurable prompt file | More surface (path handling, schema drift between prompt and parser) for two values; not needed |

## Consequences
- Mark's `~/.config/meetingsync/config.yaml` must carry `my_role: Head of Engineering` and `example_name: Sascha` before the next rebuild of `~/bin`. Without them his prompt changes (no role, example name "Alex"). Tracked as progress follow-up 27.
- With his values the prompt is byte-identical to before.
- New users get a neutral prompt without role; the example name only affects the prompt's example text.
- `internal/pipeline` and `internal/cli` changed beyond the planned scope (pure pass-through).
