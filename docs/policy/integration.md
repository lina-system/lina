# Integration

Changes are made on short-lived branches from `dev`, and `node scripts/integrate.mjs <branch>` is the one way a change joins `dev`. `main` identifies the released source; only the [release script](releases.md) advances it.

A worker is a coding tool, such as Codex or Claude Code, working on this repository.

## Branches

Use a short-lived branch from the latest `dev` with one coherent change. Keep unrelated refactoring and behavior changes separate. Reviewability and safe rollback matter more than line count. Coordinate edits that overlap work on another branch.

Branch names, commit messages and merge commit messages are in English, regardless of the language used to request the work. Commit titles follow [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/), for example `docs: clarify integration steps` or `fix: handle empty input`. Quoted source text, logs, and code stay unchanged.

## Integrate

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

The repository installs no Git hooks; `integrate.mjs` applies the `foundation` condition to every change that joins `dev`.

## Describe the change

The merge commit message, given with `--message-file`, carries what a reader needs later:

- The problem, the approach, and relevant issue links.
- Tests or manual checks performed, with results and any gaps.
- Compatibility, migration, and recovery implications when applicable.

For runnable changes, show evidence from the affected usage path, not only unit tests. For documentation, reading and link and format checks are appropriate; do not write tests that pin prose.

## Authority

The owner integrates any change. When the owner delegates a scope (a project or a set of issues) to a worker, the worker integrates its changes in that scope into `dev` itself once `foundation` passes; outside a delegated scope, it integrates only with the owner's explicit authorization. Every release and deployment needs the owner's approval.

A review by a worker or another tool is supporting evidence; it grants no integration authority. Record what a review checked and what it could not establish.

## History

`dev` and `main` only move forward. Changes join `dev` as merge commits, never squashed or rebased, and only through `integrate.mjs`. Only the [release script](releases.md) advances `main`. Neither branch is force-pushed or deleted, and the scripts never force-push. The remote's rulesets reject deletion and force pushes of `dev` and `main` and any change to a release tag.

`integrate.mjs` pushes `dev` and `refs/notes/foundation`; the release script pushes release tags and `main`. Integrating a change does not publish a [release](releases.md) or deploy the product.

## Branch ownership

Check the owner and linked worktrees before deleting an integrated branch. Preserve unintegrated work until its disposition is agreed.

Policy changes use this same integration process. The scripts and the remote's rulesets define enforcement; update this documentation alongside any authorized change to them. Gate behavior is documented in the [foundation policy](foundation.md).
