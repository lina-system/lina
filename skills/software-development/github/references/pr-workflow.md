# GitHub Pull Request Workflow

## Prerequisites

Use `terminal` for Git and GitHub CLI calls. Read `references/auth.md`
if `gh auth status` fails. Work in the intended repository and read its
instructions before changing files.

## Procedure

1. Read the issue and PR threads with `--comments`. Check existing PRs and
   remote branches. Fetch the default branch and use a dedicated branch or worktree.
2. Implement the acceptance criteria, review the diff, and run the repository's
   formatter, lint, typecheck and relevant tests. Check exit codes and full logs;
   no matching summary line does not mean success.
3. Stage only intended files. Commit and push only within the task's authorization.
   Use Conventional Commits; see `references/conventional-commits.md`.
4. Open the PR with a body file and organization labels:

```bash
git push -u origin HEAD
gh label list -R lina-system/lina
gh pr create --title "fix: preserve login redirect" --body-file pr-body.md --label bug
```

Use `templates/pr-body-bugfix.md` or `templates/pr-body-feature.md`.
The body has exactly these main sections, and the closing reference comes last:

```markdown
## Summary
Preserve the requested destination after login.

## Done when
- [x] A successful login returns to the requested destination.

## Verification
- Record the actual commands, exit results, regression proof and manual checks.
- State anything not run and why.

Closes #42
```

Apply labels from the live organization set as described in SKILL.md; do not
invent labels. Use the same body and label contract with REST fallbacks.

5. Read the PR back with `gh pr view` and `gh pr diff`. Verify the base,
   head SHA, files, body and labels. Inspect `gh pr checks` yourself. For failed
   checks, read `gh run view <run-id> --log-failed` before changing anything.
   Fix introduced failures; distinguish baseline and infrastructure failures.
6. Before merge, fetch again and check the issue and competing PRs. If another
   change already delivered the behavior, resolve the duplicate rather than
   overwrite it. Obtain any required reviews and authorization, then merge with
   a merge commit, never squash:

```bash
gh pr merge <number> --merge
```

If auto-merge is appropriate, use `--auto --merge`. REST merge bodies use
`{"merge_method":"merge"}`; GraphQL uses `mergeMethod: MERGE`.
Delete branches only when authorized and no other worker needs them.

7. Verify `gh pr view <number> --json state,mergedAt,mergeCommit`, the issue
   closure, and the delivered behavior. A PR existing or merging does not prove
   the running service picked up the change.

## Review fallback

If a hosted review stalls, run an authorized local read-only review of the same
head, such as `codex review --base origin/main`. Record that substitution; it
does not waive required checks or branch protection.

## Verification

Done when the intended diff, live CI and review results, merge state, and actual
behavior agree. Report remaining blockers explicitly instead of calling them green.
