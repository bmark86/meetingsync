# 0011 Clean the git history

Status: accepted (Mark, D2 2026-11-01; tool, timing and method 2026-11-05, OD-15). Prepared, not yet executed. R-M4-02.

## Context
- S-05: the 3 real data source IDs (Notes, Tasks, Projects) are in the public history, 15 occurrences, only in the old `config.example.yaml`/`.json`. None in commit messages or the tag message.
- R-M4-01 replaced them with placeholders in the current tree; history still holds them.
- The test fixture ID in `internal/notion/client_test.go` is synthetic and stays.
- Author email in commit metadata is out of scope of S-05.
- Origin: public, 0 forks, no PRs. No other clones (only this Mac).

## Decision
| Item | Choice |
| --- | --- |
| Tool | `git filter-repo --replace-text` (Homebrew, version a40bce548d2c). Dev tool only, no project dependency |
| Timing | Once, at the end: after `refactor/architektur` is merged into `main`, before the next release. IDs stay public until then (low risk without a token, accepted by Mark) |
| Method | In place in the existing repo, no fresh clone: `git filter-repo --force --replace-text <file>` rewrites all branches and tags (incl. `v1.0.0`), removes the `origin` remote, expires reflogs, runs gc |
| Preconditions | Clean working tree, no stashes, no extra worktrees |
| Backup | `git bundle create ~/Backups/meetingsync-vor-rewrite.bundle --all` and `git bundle verify` before the rewrite |
| Verify | `git log -p --all \| grep -c -F -f <ID list>` = 0 (ID list kept outside the repo); `make check` green on all branches |
| Publish | Re-add `origin`; Mark does the force pushes himself (`main`, `refactor/architektur` if it still exists, tags) |
| Approval | The coordinator asks Mark again right before the rewrite runs |
| Follow-up | One docs commit after the rewrite maps old to new hashes (filter-repo commit-map) in `docs/progress.md`, `docs/reviews/2026-11-01-architektur-und-security-review.md` and the plan (11 references, 6 distinct hashes) |

## Alternatives considered
| Option | Rejected because |
| --- | --- |
| Rewrite in a fresh clone (filter-repo default, the original plan) | Mark does not want to re-clone; `--force` in place plus the bundle backup covers the same risk |
| Rewrite now, before the merge | Every later hash would change again and open branches would need rebasing; one rewrite at the end is enough |
| Keep the history, placeholders only | IDs would stay public permanently; D2 decided to remove them |

## Consequences
- All commit hashes change, tag `v1.0.0` moves to the matching new commit; the GitHub release must be re-linked and `docs/releases/v1.0.0.md` checked.
- In-place rewrite has no untouched original: recovery relies on the bundle only.
- Until the rewrite, the IDs remain in public history.
- Docs carry stale hashes between the rewrite and the follow-up docs commit.

## Residual risks
- GitHub may keep old SHAs reachable in caches; removal only via GitHub Support.
- The IDs are not secrets on their own (no token), so the leak is low impact.

## OPEN
- Release notes for v1.0.0 after the rewrite (part of OD-15, not yet answered).
