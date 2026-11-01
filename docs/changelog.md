# Changelog

Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added
- R-M0-02: Makefile targets `build` (to `./bin`, version via ldflags), `lint`, `test` (with `-race`), `vuln`; `check` = vet + lint + test + vuln. Tools pinned: golangci-lint v2.11.4, govulncheck v1.9.0.
- R-M0-03: `.golangci.yml` (govet, errcheck, staticcheck all except ST1005, unused, ineffassign, gosec). gosec exclusions limited to app.go, config.go and tests until R-M3-03/04/07.

### Changed
- R-M0-03: 12 lint findings fixed, no behavior change.
- Docs: review report, refactoring plan; documentation switched to plain English.

### Security
- Toolchain pinned to go1.26.10 (fixes 26 reachable stdlib vulnerabilities, govulncheck clean). See ADR 0002.

## [1.0.0] - 2026-11-01

### Added
- T1: Config can be read as YAML (`.yaml`/`.yml`, extension case-insensitive); `config.example.yaml`.
- T1: Without `-config`, `~/.config/meetingsync/config.yaml` takes precedence over `config.json` (`configPathIn`).
- T1: Startup logs "Konfiguration: <path>".
- T1: Dependency `go.yaml.in/yaml/v3` v3.0.6 (see ADR 0001).
- T2: `config.example.yaml` is a full option reference: sections, German comment per option (purpose, values, default); unused options commented out with default; `token` and `api_key` commented out without value.

### Changed
- T1: README and plist template point to `config.yaml`; `config.json` keeps working.
- T1: CLAUDE.md: "stdlib only" now excepts `go.yaml.in/yaml/v3`; real config path updated.
- T2: Defaults documented per option, not in a header; active values unchanged, loaded config identical (test).
- T6: Transient errors (LM Studio/Notion unreachable incl. timeout, HTTP 429/5xx, model not loaded, Notion 401/403/404) count toward `max_attempts`, together with other errors. Wait times unchanged (transient 1/2/4/8/16 min, then 16 min; other 2 min).
- T6: After `max_attempts` the file moves to `_fehler` with `.fehler.txt`. Context cancellation does not count as an attempt.
- T6: Default `max_attempts` 3 → 10 (breaking: also applies to non-transient errors).
- T6: Consequence: if LM Studio is down for about 95 min, or the Notion access is missing, all affected files end up in `_fehler` and must be moved back to the watch folder by hand.

### Fixed
- T2: Keychain hint in `config.example.yaml`: `security add-generic-password ... -w`. Without `-w` an empty password is stored and Notion answers 401 permanently.

### Security
- T1: YAML errors never contain file content, only path and line number (keeps tokens out of the log).
