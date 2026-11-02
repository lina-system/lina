# GitHub Issue to Pull Request

Turn a GitHub issue into a tested, verified PR. This skill owns the end-to-end discipline — premise validation, duplicate sweeps, class-level fixes, and honest CI reporting; the sibling GitHub and development skills own their own mechanics.

## When to Use

- "Fix issue #123 and open a PR."
- "Implement this GitHub feature request."
- "Take this bug from issue to green CI."

Don't use for: reviewing an existing PR, or answering a code question with no requested change.

## Procedure

### 1. Read the live issue — body AND full thread

Use `terminal` to run `gh issue view <N> --comments`. The body is a snapshot from filing time; the newest comments carry the live state: partial fixes already merged, new root-cause analyses, maintainer decisions, or questions directed at you that change the task. Also read repository instructions (`AGENTS.md`, contribution docs) with `read_file`. Done when the currently requested behavior, non-goals, and any unanswered thread questions are known.

### 2. Sweep for existing and duplicate work

Before writing anything, run `gh pr list --search "#<N>" --state all` plus at least two keyword/synonym variants of the symptom (`gh pr list --search "<subsystem> <symptom>" --state open`). Popular issues attract multiple independent fixes; building a duplicate wastes the work and the credit. Also check whether a recent commit already fixed it: `git log --oneline -20 -- <relevant files>`. Done when you know every open PR and recent commit touching this issue, or that none exist.

### 3. Validate the premise against current code — and against design intent

Reproduce the bug or demonstrate the missing behavior on the current default branch with a failing test or fixture, using `search_files` and `read_file` to trace the reported path. Then check the second question: is the "bug" actually deliberate design? Run `git log -p -S "<symbol>"` on the code the issue wants changed and read the original commit's intent — a missing link or restriction is often the feature. Challenge stale or flawed issue prose instead of implementing it blindly. Done when the root cause or feature gap is demonstrated in current code AND the change doesn't fight an intentional design.

### 4. Define acceptance and risk

List acceptance criteria, interfaces, migrations/state changes, compatibility, security/privacy, rollout, and rollback. Map every criterion to a test or explicit verification. Done when review has a finite contract.

### 5. Implement the smallest complete change — and fix the class

Work on an isolated branch or worktree. Add a regression test that reproduces the bug, then implement. When the fix is in hand, use `search_files` for the same bug shape at sibling call sites and fix the whole class. Every changed line must trace to the issue; no drive-by cleanup. Done when targeted tests pass, the original failure no longer reproduces, and sibling sites are fixed or explicitly ruled out.

### 6. Prove the regression test bites (sabotage run)

Temporarily restore the old behavior of the exact function under test, run the new test, and confirm it FAILS; then restore the fix and confirm it passes. A regression test that passes with and without the fix proves nothing. Done when the test demonstrably fails on pre-fix code.

### 7. Run repository quality gates, then open the PR immediately

Run formatting, lint, typecheck, and the repository's canonical tests on affected areas; review the diff. Follow `references/pr-workflow.md`, then push and open the PR: it dispatches CI. Use Summary / Done when / Verification, finish with `Closes #N`, and apply the live organization labels. Read the PR back and verify head SHA, base, title, and files. Done when the PR exists with the intended diff and CI is running.

### 8. Shepherd CI honestly and close the loop

Inspect live checks and failure logs via `gh pr checks` / `gh run view --log-failed`. Distinguish introduced failures from baseline or infrastructure failures; reproduce on the default branch when unsure. Before merging, fetch and recheck competing work and the issue state. Use a merge commit, not squash. Never say "green," "merged," or "released" without live evidence. Verify the delivered behavior and issue closure after merge. Done when CI state, remaining blockers, and the issue thread all reflect reality.

## Pitfalls

- Coding before reading issue comments, sweeping for duplicate PRs, or reading current code.
- "Fixing" behavior that the original commit shows is intentional design.
- Fixing a symptom at one call site while sibling sites keep the same bug.
- Shipping a regression test that also passes without the fix.
- Opening a PR with unrun tests or unrelated formatting churn.
- Claiming the issue is delivered because a PR exists.

## Verification

- [ ] Full issue thread read; newest comment state reflected in the plan.
- [ ] Duplicate-PR sweep run with issue number + 2 keyword variants.
- [ ] Premise reproduced on current code; design intent checked via git history.
- [ ] Regression test proven to fail without the fix.
- [ ] Sibling call sites fixed or explicitly ruled out.
- [ ] Every changed line traces to the issue.
- [ ] CI state reported from live evidence only; issue commented with the PR link.
