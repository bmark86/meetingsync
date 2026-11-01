# Security policy

## Supported versions

| Version | Supported |
| --- | --- |
| Latest release (now `v1.0.0`) and `main` | yes |
| Older releases | no |

Fixes go into `main` and the next release. Older releases get no fixes.

## How to report a problem

Please do not open a public issue for a security problem.

Report it privately through GitHub:

1. Open <https://github.com/bmark86/meetingsync/security/advisories/new>
   (tab **Security**, button **Report a vulnerability**).
2. Describe the problem, the affected version and the steps to reproduce it.

You get an answer within 14 days. Once the fix is out, the advisory is made public.

Do not include real transcripts, meeting notes or tokens in a report. Use made-up data.

## Scope

In scope:

- The `meetingsync` program in this repository.
- How it handles the Notion token (environment variable, macOS Keychain, config file).
- What it sends to the local LLM (LM Studio) and to Notion.
- The CI workflow and its supply chain (Go toolchain, tools, GitHub Actions).

Out of scope:

- Bugs in MacWhisper, LM Studio, Notion or macOS itself. Report them to the vendor.
- Problems that need an attacker who already controls your Mac user account.

## Privacy

meetingsync is built so that raw transcripts stay on your Mac:

- The transcript goes only to the local model in LM Studio.
- Only the finished meeting note goes to Notion. The transcript goes to Notion only if you turn on `include_transcript`.
- Processed files stay local in `_archiv` (archive folder) and `_fehler` (error folder).

A report that shows a transcript leaving the Mac in any other way is a security problem. Please report it as above.
