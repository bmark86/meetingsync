# 0014 `llm.api_key` from the Keychain

Status: accepted (D9, OD-20; lookup order decided by Mark, 2026-11-04), R-M3-15. Finding S-18.

## Context
- `llm.api_key` could only be set in clear text in the config file.
- The Notion token already has a lookup chain (env, Keychain, config) in `internal/secret`, with the env name and error texts hard-coded to Notion.
- LM Studio usually needs no key, so "no key" must stay valid and leave the request unchanged.

## Decision
- Lookup order, like the Notion token: env `LLM_API_KEY` -> Keychain, only if `llm.api_key_keychain_service` is set (value = service name, e.g. `meetingsync-llm`) -> `llm.api_key`.
- An error reading the Keychain aborts start, also in `-dry-run` (it calls the LLM) and `-check`. `-check` then prints no ✓/✗ line (fail closed, accepted by the coordinator).
- Error text: `LLM-API-Key aus dem Schlüsselbund (<dienst>) nicht lesbar: <exit status>`.
- Nothing set -> no `Authorization` header, request unchanged, golden unchanged.
- `internal/secret`: shared `source` type used by `NewNotion` (behaviour and texts byte-identical) and new `NewLLM` (an empty result is allowed).
- Keychain access via `/usr/bin/security` with the minimal environment from R-M3-07. `main.go` resolves the key in `newEnv`.
- The header is sent only to the loopback base URL (R-M3-01, ADR 0007).
- The key never appears in logs or errors (tested). `config.example.yaml` and `.json` document the key without a value.

## Alternatives considered
| Option | Why not |
| --- | --- |
| Keep the key in the config only | Clear text on disk (S-18) |
| Keychain service name fixed in code | Less flexible than the Notion pattern; the config key matches `token_keychain_service` |
| Ignore Keychain errors and send no header | The LLM would answer 401 later with a less clear cause; fail closed is preferred |

## Consequences
- A Keychain error stops `-check` before any check line is printed.
- Setup (optional): `security add-generic-password -a "$USER" -s meetingsync-llm -w` (prompts for the key, nothing in shell history), then set `llm.api_key_keychain_service: meetingsync-llm`.
- Plain `llm.api_key` keeps working.
