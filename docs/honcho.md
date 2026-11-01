# Honcho server

LINA builds, installs and upgrades the [Honcho](https://github.com/plastic-labs/honcho) server itself (AGPL-3.0,
Plastic Labs), so its source lives in this repository at `services/honcho/`, with a tracked upstream.

| Path or branch | What it is |
| --- | --- |
| `services/honcho/` | A squashed `git subtree` of Honcho. Upstream's files, plus the LINA changes listed in the ledger. |
| `services/honcho-intake.md` | The intake ledger: one row per subtree add or pull, and every change LINA carries in `services/honcho/`. |
| `honcho-upstream` branch | Honcho `main` exactly as upstream has it, with its full history. We never commit to it. |

The subtree is squashed: `dev` gets one squash commit per intake (Honcho's tree at that upstream commit) instead of
Honcho's whole history, which lives on `honcho-upstream`. Squash commits carry `git-subtree-dir: services/honcho`
and `git-subtree-split: <upstream commit>`; `git subtree pull` and the CI check rely on them.

## Remote

```sh
git remote add honcho https://github.com/plastic-labs/honcho.git
git fetch honcho main --no-tags
```

## The mirror

`honcho-upstream` only fast-forwards to a newer Honcho `main`. If it is not a fast-forward, stop and ask: upstream
rewrote history.

```sh
git fetch honcho main --no-tags
git push origin honcho/main:refs/heads/honcho-upstream   # rejected unless it is a fast-forward
```

Like `upstream`, the branch refuses force push and deletion and has no required checks.

## Taking a Honcho release

1. Advance `honcho-upstream` (above) and pick the upstream commit to take, normally a release tag on it
   (`git ls-remote --tags honcho 'v*'`).
2. On a branch from `dev`:

   ```sh
   git subtree pull --prefix=services/honcho --squash honcho <tag-or-commit>
   ```

   Resolve conflicts with LINA changes listed in the ledger; never resolve one by editing a LICENSE file or a
   copyright or license header.
3. In the same branch:
   - add a row to `## Intakes` in `services/honcho-intake.md` with the full upstream commit sha;
   - update the modification notice at the top of `services/honcho/README.md` to that commit and date;
   - add any new file over 500 KiB that upstream ships to `.github/blob-size-allowlist.txt`;
   - run `python scripts/ci/check_honcho_subtree.py --mirror honcho/main`.
4. Open a pull request into `dev`, add one `CHANGELOG.md` line under `## [Unreleased]` (for example "Took Honcho
   v3.6.0 (<hash>)"), and merge it with a merge commit once the checks are green. A squash or rebase merge would
   drop the subtree squash commit the check reads.

## LINA changes

A change LINA makes inside `services/honcho/` is an ordinary commit on `dev` and gets a row under `## LINA changes`
in the ledger. AGPL-3.0 section 5(a) requires the modified work to carry a prominent notice that it was modified and
when: that is the notice at the top of `services/honcho/README.md`. The LICENSE files and the copyright and license
headers of upstream files are never changed. When upstream fixes the same problem, prefer upstream's fix and remove
ours, and its ledger row, in the intake pull request.

## CI

`.github/workflows/honcho.yml` runs on pull requests and `dev` pushes that touch `services/`, the check script or
the workflow ([ci.md](ci.md)):

- **Honcho subtree check**: `scripts/ci/check_honcho_subtree.py --mirror origin/honcho-upstream`. The ledger's last
  upstream commit is the newest squash's `git-subtree-split`, is on `honcho-upstream` and has the squash's tree;
  every upstream LICENSE, COPYING and NOTICE file is byte for byte unchanged; every copyright or SPDX header line of
  an upstream file is still at the top of that file; `services/honcho/README.md` starts with the modification notice
  naming the ledger's upstream commit.
- **Honcho tests**: upstream's Python suite (`uv run pytest -x --fuzz`) against `pgvector/pgvector:pg15`, as in
  upstream's `unittest.yml`, without its LLM API secrets.
- **Honcho image build**: `docker build services/honcho` (built, not pushed).

The workflows under `services/honcho/.github/` are upstream's and do not run. LINA's own checks skip
`services/honcho/`: ruff (`ruff.toml`), the code-health ratchet, the `/tmp` literal scan and the lazy-deps import
scan. Honcho is held to its upstream's lint config instead.
