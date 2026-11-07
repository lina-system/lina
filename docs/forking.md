# Forking and upstream

LINA is a full fork of [Hermes Agent](https://github.com/NousResearch/hermes-agent) (MIT, Nous Research).
The repository keeps the whole upstream `main` history. Upstream tags and other upstream branches are not kept.

## Branches

| Branch | What it is | Who moves it |
| --- | --- | --- |
| `upstream` | Hermes `main` exactly as upstream has it. We never commit to it. | Fast-forward to a newer upstream `main` only |
| `dev` | LINA development. The default branch; every pull request targets it. | Merged pull requests |
| `main` | LINA releases. | Fast-forward to a released `dev` commit, then tag |

All three refuse force push and deletion. `dev` and `main` also require the lint and Python test checks.

## Remotes

```sh
git clone https://github.com/lina-system/lina.git
cd lina
git remote add upstream https://github.com/NousResearch/hermes-agent.git
git fetch upstream main --no-tags
```

## Taking upstream changes

1. Advance the `upstream` branch. It must be a fast-forward; if it is not, stop and ask: upstream rewrote history.

   ```sh
   git fetch upstream main --no-tags
   git push origin upstream/main:refs/heads/upstream   # rejected unless it is a fast-forward
   ```

2. Choose what to take. Upstream is taken selectively, so `dev` does not have to follow every upstream commit.

   ```sh
   git log --oneline origin/dev..origin/upstream
   ```

3. On a branch from `dev`, bring the chosen changes in:
   - everything up to an upstream commit: `scripts/sync-upstream <upstream-commit>` (default `origin/upstream`). It merges with `--no-ff --no-commit`, runs `git rm` on every path matching `prune-list.txt`, reports new upstream top-level entries and plugin directories the list does not classify (add each to the list or keep it), and lists the conflicts left in kept files;
   - single fixes: `git cherry-pick -x <upstream-commit>` (`-x` records the upstream hash in the message), then `git rm` what `python3 scripts/ci/check_prune_list.py` reports.
4. In the same branch, set `LINA_UPSTREAM_COMMIT` in `hermes_cli/lina_version.py` to the upstream commit you merged up to (a merge only; a cherry-pick does not move it), and compare `.github/workflows-upstream/` and `.github/upstream/` with upstream's `.github/` (see [ci.md](ci.md)).
5. Open a pull request into `dev`, add one `CHANGELOG.md` line under `## [Unreleased]` (for example "Took Hermes upstream up to <hash>"), and merge it with a merge commit once the checks are green.

Never rebase `dev` onto upstream and never force-push any of the three branches.

When taking upstream changes, do not change upstream LICENSE files or copyright headers.

## What LINA does not carry

`prune-list.txt` at the repository root lists the upstream paths LINA does not carry; its header explains the format. The CI "Prune check" fails while a tracked file matches it.

After deleting upstream paths, and on every intake before the merge is committed, run `python3 scripts/ci/check_pruned_imports.py --upstream <ref>`: it fails while kept code imports a module that only deleted upstream files provided (its exceptions, each with a reason, are listed in the script).

## Our patches on top of upstream

Changes we carry that upstream does not have are ordinary commits on `dev`. When upstream fixes the same problem, prefer upstream's fix and remove ours in the merge pull request.
Our production checkout carried three uncommitted patches. At the fork:

- launchd `MaterializeDatalessFiles` (`hermes_cli/gateway_launchd.py`): carried as a commit on `dev`.
- 1Password service-account vault: not carried; upstream fixed it (b45971380c, d7c4d49db9: `op item get` names the item's vault).
- Bot Chat turn cap: not carried; upstream removed the local `hermes chat` turn the patch capped (ee09dad420); local delivery now waits on the live owner with a 30-minute bound.

## Ref names

The branch `upstream` and the remote `upstream` share a name, so a bare `upstream` is ambiguous to git (it resolves to the branch and warns). Write `upstream/main` for the Hermes remote, and `origin/upstream` or `refs/heads/upstream` for the branch.
