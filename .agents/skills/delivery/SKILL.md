---
name: delivery
description: "Take an issue to a merged pull request under the checks."
version: 1.0.0
author: Jun (thisisjun786), Hermes Agent
license: MIT
platforms: [linux, macos, windows]
metadata:
  hermes:
    tags: [delivery, issues, pull-requests, merge, ci, review]
    category: software-development
    related_skills: [github]
---

# Delivery Skill

The command path from an issue to a merged pull request in a lina-system repository. The
rules themselves are in `docs/delivery.md`; this skill does not restate them.

## When to Use

- Before you start an issue, open a pull request or merge one in a lina-system repository.

## Procedure

Run each command with `terminal`.

1. Check the issue can start (every `Depends on` issue closed, current sprint):
   `gh issue view <n> --json body,labels,milestone`
2. Branch from the latest `dev`: `git switch -c <type>/<n>-<slug> origin/dev`
3. Open one pull request for the issue, into `dev`:
   `gh pr create --base dev --title "<type>(<scope>): <what>" --body-file <file>`.
   The body has `Closes #<n>`, the issue's `## Done when` with each box checked (or
   `N/A: <reason>`), and `## Verification`.
4. Wait for the required checks on the head, `Codex review`, `Linked issue` and `dev green` included:
   `gh pr checks <pr> --required --watch`
5. On a P0/P1 finding, fix it on the branch; the new head gets its own review.
6. Merge: `lina-merge <owner/repo> <pr> --wait 30` (from #63: `lina work merge`).
7. File each P2/P3 finding as a follow-up issue (`Follow-up to #<pr>`, same milestone).

## Pitfalls

- Never merge with `--admin`, a ruleset bypass, squash or rebase.
- Never merge while the latest `dev` push CI is red, unless the pull request is the fix
  (`dev-red-fix`) or Jun approved it after seeing why (`dev-red-approved`).
- One issue per pull request. No pull request without an issue, except a typo or one-line
  CI fix with the `no-issue` label.
- Never disable, skip or weaken a test to turn a check green.
