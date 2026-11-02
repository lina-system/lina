# Delivery

How a change reaches `dev`. GitHub enforces the required checks; the rest is the rule.

1. **Issue.** `## Summary`, `## Done when` (observable checks, as `- [ ]` boxes), `## Context` with `Sprint:`, `Depends on: #n` (or `none`), `Size: S|M|L` and `Runner:`. The forms are in [lina-system/.github](https://github.com/lina-system/.github/tree/main/ISSUE_TEMPLATE).
2. **Start** an issue only when its Blocked by list (GitHub issue dependencies) has no open issue and it is in the current sprint. A chained issue starts after the one before it merged into `dev`.
3. **Size and runner** follow the `issue_labels` rules of [lina-system/.github](https://github.com/lina-system/.github/blob/main/scripts/issue_labels.py).
4. **One pull request per issue**, into `dev`, from branch `<type>/<issue>-<slug>` (`ci/61-delivery-procedure`). The body has `Closes #<issue>`, the issue's `## Done when` with each box checked (or `N/A: <reason>`), and `## Verification` (commands run and what you saw). A change users see adds one line under `## [Unreleased]` in `CHANGELOG.md`; any other change carries the `no-changelog` label. A typo or one-line CI fix may skip the issue with the `no-issue` label.
5. **Parallel.** Only two things hold back new work: an open issue in its Blocked by list, or work already running on the same contract (storage or config format, command name, shared module, safety path). Otherwise open as many as the work machines have room for, the issues the most others wait on first. Open pull requests, the CI queue and a red `dev` do not hold back new work; a red `dev` stops merging only (step 7).
6. **Required checks** on the head commit:

   | Check | Green when |
   | --- | --- |
   | Lint | `ruff check .` passes |
   | Python tests (Linux) | every test slice passed, or the change cannot reach Python |
   | Python tests (macOS) | macOS tests passed; skipped (green) when no macOS path changed |
   | LINA Helper (macOS) | Swift and native checks passed; skipped (green) when `helper/` did not change |
   | Codex review | Codex reviewed this head and left no P0/P1 finding on it |
   | Linked issue | the body closes an open issue of this repository (the branch number among them), or `no-issue` |
   | dev green | the latest `dev` push CI passed; or this is a `dev-red-fix` pull request on whose head at least one check red on `dev` passes (skipped is not passing) and nothing new is red; or the owner approved (step 7) |

   Details: [ci.md](ci.md). Codex follows `## Code Review Rules` in [AGENTS.md](../AGENTS.md).

   A pull request that changes a screen (label `ui`) also gets one browser QA pass against the feature map. A defect (it breaks Intended behavior or a `Done when` item) blocks the merge like a P1; improvement ideas go into one issue per sprint. Details: [lina-system/lina-app#16](https://github.com/lina-system/lina-app/issues/16).
7. **Merge only when green**: every check on the head and the latest `dev` push CI. Merge commit, delete the branch, never `--admin` or a bypass. If Codex is down, `Codex review` stays pending: wait.
   - While `dev` is red, only the pull request that fixes it merges (label `dev-red-fix`: at least one check red on `dev` passes on its head, skipped is not passing, and nothing new is red). Every other pull request waits for `dev` to turn green; starting new work does not wait.
   - No intended red: a new check lands in the same pull request as the fix that makes it pass, or is switched on after that fix merged.
   - The one exception, merging while `dev` stays red: before merging, show Jun why the red must stay, when it turns green and what it blocks meanwhile, and get his approval. He records it with the `dev-red-approved` label. Without it there is no merge over a red `dev` except `dev-red-fix`.
8. **After merge.** P2/P3 findings go into follow-up issues: one open issue (label `follow-up`) per area (module) and sprint, each finding a checkbox with its pull request; a finding already listed is not added again. Tool: `review-followups` (hermes-ops). If `dev` turns red, fix the cause or revert the pull request.
9. **Sprint close.** The owner checks the sprint; only the owner opens the next one. Releases need the owner's approval.

| Step | Now (hermes-ops) | From #63 |
| --- | --- | --- |
| Pick what to open | `lina-next` | `lina work next` |
| Small issue | `quick-issue` | |
| Merge | `lina-merge <owner/repo> <pr> [--wait <min>]` | `lina work merge <repo> <pr>` |
| Audit closed issues | | `lina work audit` |
| P2/P3 follow-ups | `review-followups` | |
