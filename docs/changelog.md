# Changelog

Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

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

### Fixed
- T2: Keychain hint in `config.example.yaml`: `security add-generic-password ... -w`. Without `-w` an empty password is stored and Notion answers 401 permanently.

### Security
- T1: YAML errors never contain file content, only path and line number (keeps tokens out of the log).
