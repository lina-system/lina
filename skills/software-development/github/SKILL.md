---
name: github
description: "GitHub via gh CLI: PRs, issues, reviews, repos, auth."
version: 1.0.0
author: Ben Barclay (benbarclay), Hermes Agent
license: MIT
platforms: [linux, macos, windows]
metadata:
  hermes:
    tags: [github, gh, git, pull-requests, issues, code-review, repos, auth, ci]
    category: software-development
    related_skills: [code-review, codex, claude-code]
---

# GitHub

Work GitHub end to end with the `gh` CLI (REST fallback where noted): auth,
issues, the PR lifecycle, issue-to-PR delivery, code review, and repo
management. This skill consolidates six former skills; each workflow lives
complete in its reference file — ALWAYS read the matching reference before
starting that workflow, the body below only routes.

## Routing

| Task | Read first |
|---|---|
| Auth broken / new machine / token or SSH setup / gh login | `references/auth.md` |
| Create, triage, label, assign, close issues | `references/issues.md` |
| Branch, commit, open PR, watch CI, merge | `references/pr-workflow.md` |
| Carry an ISSUE to a verified PR (full delivery loop) | `references/issue-to-pr.md` |
| Review someone's PR: diffs, inline comments, verdict | `references/code-review.md` |
| Clone/create/fork repos, remotes, releases | `references/repo-management.md` |
| Organization transfers, visibility changes, exposure checks | `references/repo-management.md` |
| Move open work from another tracker | `references/tracker-migration.md` |

Supporting assets: `scripts/gh-env.sh` + `scripts/git-credential-token.py`
(auth helpers), `templates/` (PR bodies, bug report, feature request),
`references/ci-troubleshooting.md`, `references/conventional-commits.md`,
`references/github-api-cheatsheet.md`, `references/review-output-template.md`.

## Core discipline (applies to every workflow)

- Preflight once per session: `gh auth status` — if it fails, go to
  `references/auth.md` before anything else.
- Prefer `gh` over raw REST; drop to `gh api` only for endpoints the
  porcelain lacks (the cheatsheet lists them).
- Never report CI green without checking `gh pr checks` yourself; never
  claim merged without verifying `state,mergedAt`.
- Read full context before writing: `gh issue view --comments` /
  `gh pr view --comments` — decisions live in threads, not titles.
- Sweep for duplicates before creating anything:
  `gh pr list --search` / `gh issue list --search`.
- Resolve forwarded issue numbers with `gh issue view` and match the title.
  Check all PR states and remote branches before starting duplicate work.
- Before merging, fetch and recheck the issue, default-branch log, and competing
  PRs. Do not overwrite another contributor's completed fix.
- Read test exit codes and full logs; a missing summary match is not a pass.
- Check contributor trailers separately from repository collaborators: attribution
  does not grant access. Do not rewrite shared history to remove attribution.

## LINA issue and PR rules

- Issues use `## Summary`, `## Done when`, `## Context`, in that order.
  Make acceptance observable; put dependencies, size and routing context in Context.
- PRs use `## Summary`, `## Done when`, `## Verification`, then end with
  `Closes #N` for the issue they fully resolve. Record commands, results and any
  unverified behavior in Verification. Use the templates in `templates/`.
- Merge with a merge commit: `gh pr merge --merge`, never squash.
- Read `gh label list -R lina-system/lina` and the target repository's labels
  before applying the organization label set. Do not invent labels.
  Kind: `bug`, `enhancement`, `documentation`, `maintenance`, or `research`.
  Size: `size/S`, `size/M`, `size/L`. Use `blocked` for open dependencies and
  `ready` only when all dependencies are closed. Use `needs-routing` until
  routing is settled; runner labels are `omo`, `dsh`, `grok-cursor`, `agy`.
  `cloud` is paused, not a routing choice. Apply `no-changelog` only when users
  receive no change, and `dev-red-fix` only for a fix to a red dev check.
  Other triage labels follow their live descriptions.

## Verification

- The workflow's own reference file defines done for that task.
- Cross-cutting: every claim about remote state (CI, merge, release,
  issue state) is backed by a fresh `gh` read, never memory.
