# 0005 golang.org/x/sys for the atomic no-overwrite rename

Status: accepted (Mark, 2026-11-02), R-M1-04

## Context
- `moveFile` checked whether the target exists and then renamed. Between check and rename another process could create the target, and `os.Rename` would overwrite it (S-14, A-11).
- CLAUDE.md allows no dependency except `go.yaml.in/yaml/v3` without asking first (ADR 0001).
- The stdlib has no rename that fails when the target exists.
- Mark approved the new dependency on 2026-11-02, only for this rename.

## Decision
- Use `golang.org/x/sys` v0.49.0, only `unix.RenamexNp(src, dst, RENAME_EXCL)`. On `EEXIST` the next name `stem (n).ext` is tried, as before.
- Copy fallback only on `EXDEV` or `ENOTSUP`. `EINVAL` is not a trigger. Any other rename error is returned.
- Fallback steps: source `Lstat` (regular file, same identity as the opened file, symlinks refused), copy with `O_CREATE|O_EXCL`, `Sync`, `Close`, fsync of the target folder, identity check (`Lstat` + `SameFile`) before `Remove(src)`.
- Deletions (approved by Mark, 2026-11-02): only meetingsync's own fresh copy, identity-checked: when `Remove(src)` fails (original stays, warning), and a partial copy after an `io.Copy`, `Sync` or `Close` error.
- No other deletion: after an identity mismatch both files stay (the copy is then the only copy of the old version).
- Folder fsync error (R-M1-07, decided by Mark on 2026-11-02): the copy is already written and synced, so the move goes on as normal (identity check, then `Remove(src)`). A German warning is logged (`<name>: Zielordner nicht synchronisiert, trotzdem verschoben nach <dst>: …`, target folder not synced, moved anyway). The file counts as moved, both for the archive and for `_fehler`. Nothing else is deleted. Residual risk: after a crash right then, the folder entry of the copy may be lost while the original is gone.
- A failed move into the archive is retried after 2 minutes, not every poll (R-M1-07, see ADR 0004).
- Supply chain: `go.sum` hashes checked against sum.golang.org by the security review, `govulncheck` clean, Dependabot already covers `gomod`.

## Alternatives considered
| Option | Rejected because |
| --- | --- |
| Stdlib `os.Link` + `os.Remove` with identity check | Link is atomic and exclusive, but needs hardlink support on the target; a window remains between link and remove, and the stdlib path needs more code for the same guarantee |
| Status quo (check, then `os.Rename`) | A residual window stays in which the target can be overwritten |

## Consequences
- Second external dependency. CLAUDE.md and ADR 0001 reference this ADR.
- `go.mod` directive `go 1.26` became `go 1.26.0`, forced by x/sys v0.49.0. No behavior change, toolchain go1.26.10 unchanged.
- `RenamexNp` exists only on darwin: the Linux build is unsupported. meetingsync is macOS-only and CI runs on macOS.
- Residual risk accepted: between `Lstat` and `Remove(src)` the path can change (path-based unlink, macOS has no unlink by identity).
- Golden tests unchanged.

## OPEN
- None.
