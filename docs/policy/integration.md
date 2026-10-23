# Integration

Changes are made on short-lived branches from `dev`, with one pull request per issue into `dev`. After one bot review and a green `foundation` check, the pull request joins `dev` with a merge commit. `node scripts/integrate.mjs <branch>` remains the owner's local integration path. `main` identifies the released source; only the [release script](releases.md) advances it.

A worker is a coding tool, such as Codex or Claude Code, working on this repository.

## Branches

Use a short-lived branch from the latest `dev` with one coherent change. Keep unrelated refactoring and behavior changes separate. Reviewability and safe rollback matter more than line count. Coordinate edits that overlap work on another branch.

Branch names, commit messages and merge commit messages are in English, regardless of the language used to request the work. Commit titles follow [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/), for example `docs: clarify integration steps` or `fix: handle empty input`. Quoted source text, logs, and code stay unchanged.

## Pull requests

1. Branch from the latest `dev` and implement the issue's scope.
2. Run `node scripts/foundation.mjs` locally, then push the branch and open a pull request targeting `dev`. Link the issue with `Closes #<number>` and describe the change and its verification.
3. Obtain one review pass from the GitHub review bots and address blocking findings. Do not start repeated bot review loops.
4. Bring the branch up to date with `dev` by merging `dev` into it when needed, resolve conflicts on the branch, and rerun verification. The pull request's `foundation` check must be green for the current head before merging.
5. An authorized merger merges the pull request with a merge commit. Do not squash or rebase.

The [CI workflow](../../.github/workflows/ci.yml) runs `foundation` for pull requests into `dev` and pushes to `dev`. It also checks pull requests targeting `codex/ln-369-*` branches so the owner-requested persona-engine stack verifies each behavior against its preceding branch before landing into `dev`. It checks the pull request event's merge commit (`github.sha`, from `refs/pull/<number>/merge`) over the range from the pull request's base SHA to that merge commit, so the proposed result is verified even when the branch is behind `dev`. Pushes to `dev` and the nightly schedule run the full suite. Selection publishes one candidate plan for parallel build, unit and scenario checks; the final `foundation` job requires every selected result for that exact head and tree. CI has read-only repository permissions and does not push branches or evidence notes.

## Owner's local integration path

The owner can integrate a branch locally with:

```sh
node scripts/integrate.mjs <branch> [--message-file <file>]
```

The script:

1. Takes the integration lock in the local Git common directory, so one integration runs at a time; another integration waits for the lock.
2. Fetches `dev` from the remote.
3. Creates a merge commit of the branch onto `dev` in a clean worktree.
4. Runs [`foundation`](foundation.md) on that merge commit, over the range from the `dev` head to the merge commit.
5. When `foundation` passes, advances `dev` to the merge commit and pushes it, then fetches the remote `refs/notes/foundation`, merges it into the local notes with `git notes --ref=foundation merge -s cat_sort_uniq`, and pushes the notes.
6. Removes the worktree and releases the lock.

When the merge conflicts or `foundation` fails, `dev` stays where it was and the script reports why. Resolve a conflict on the branch, for example by merging `dev` into it, and integrate again.

When the remote rejects the push of `dev` as non-fast-forward, another integration has advanced it. The script fetches the new `dev`, creates the merge commit again on top of it, runs `foundation` again, and pushes again. It never force-pushes.

The repository installs no Git hooks. Pull requests use the CI `foundation` condition; `integrate.mjs` verifies the merge candidate before the owner's local integration advances `dev`.

## Describe the change

The pull request description and merge commit message carry what a reader needs later. For local integration, supply the message with `--message-file`:

- The problem, the approach, and relevant issue links.
- Tests or manual checks performed, with results and any gaps.
- Compatibility, migration, and recovery implications when applicable.

For runnable changes, show evidence from the affected usage path, not only unit tests. For documentation, reading and link and format checks are appropriate; do not write tests that pin prose.

## Authority

The owner can merge any change and may reserve the merge of delegated work. Otherwise, when the owner delegates a scope (a project or a set of issues) to a worker, the worker may merge its pull requests in that scope once the bot review and `foundation` conditions pass. Outside a delegated scope, merging needs the owner's explicit authorization. `integrate.mjs` is the owner's local path. Every release and deployment needs the owner's approval.

A review by a worker or another tool is supporting evidence; it grants no integration authority. Record what a review checked and what it could not establish.

## History

`dev` and `main` only move forward. Changes join `dev` as merge commits through pull requests or the owner's local `integrate.mjs` path, never squashed or rebased. Only the [release script](releases.md) advances `main`. Neither branch is force-pushed or deleted, and the scripts never force-push. The remote's rulesets reject deletion and force pushes of `dev` and `main` and any change to a release tag.

`integrate.mjs` pushes `dev` and `refs/notes/foundation`; the release script pushes release tags and `main`. Integrating a change does not publish a [release](releases.md) or deploy the product.

## Branch ownership

Check the owner and linked worktrees before deleting an integrated branch. Preserve unintegrated work until its disposition is agreed.

Policy changes use this same integration process. The workflow, scripts and the remote's rulesets define enforcement; update this documentation alongside any authorized change to them. Gate behavior is documented in the [foundation policy](foundation.md).
