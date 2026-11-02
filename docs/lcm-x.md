# LCM-X context engine

LINA ships [LCM-X](https://github.com/electricsheephq/lcm-x) (MIT, Voltropy / Hermes Community), the
lossless context engine, as its core context engine: one copy, in this repository, for every agent. Its
source lives at `plugins/context_engine/lcm-x/`, with a tracked upstream.

| Path or branch | What it is |
| --- | --- |
| `plugins/context_engine/lcm-x/` | A squashed `git subtree` of LCM-X. Upstream's files, plus the LINA changes listed in the ledger. |
| `plugins/context_engine/lcm-x-intake.md` | The intake ledger: one row per subtree add or pull, and every change LINA carries in the subtree. |
| `lcm-x-upstream` branch | LCM-X `main` exactly as upstream has it, with its full history. We never commit to it. |

The subtree is squashed: `dev` gets one squash commit per intake (LCM-X's tree at that upstream commit)
instead of LCM-X's whole history, which lives on `lcm-x-upstream`. Squash commits carry
`git-subtree-dir: plugins/context_engine/lcm-x` and `git-subtree-split: <upstream commit>`; `git subtree pull`
and the CI check rely on them. The squash holds only upstream's tree: LINA's changes are ordinary commits on
top of it, so the next `git subtree pull` merges them instead of reverting them. Never rebase a branch that
holds a subtree merge (it flattens the merge and re-applies upstream files at the repository root); bring in
a newer `dev` with `git merge origin/dev`.

## How agents use it

`context.engine: lcm-x` in a home's `config.yaml` selects it. `hermes_cli/config_defaults.py` keeps
`compressor` (the built-in summarizer) as the default for a home with no `context` key, so existing homes
and the test suite are unchanged; new homes get `lcm-x` from `cli-config.yaml.example`, which the installers
copy into a new home.

`plugins/context_engine/__init__.py` loads the engine for every agent the home builds, with a real plugin
context: the engine instance is per agent, and its hooks (`post_llm_call` ingest, `pre_llm_call`,
`subagent_start`, `subagent_stop`), its `hermes-lcm-x:hermes-lcm` skill and its `lcm-x` system prompt
section register on the home's plugin manager as one group that each new agent replaces, so a home holds
one set however many agents it builds. LCM-X routes each hook call to the engine of that session through
its own `engine_registry`.

Storage and settings are LCM-X's own: `$HERMES_HOME/lcm.db` and the `lcm:` keys of `config.yaml` (and its
`LCM_*` environment variables), as with the plugin copy. `tiktoken` is a core dependency for its token
counts; without it (Windows on ARM64, Android) LCM-X counts by characters.

A copy installed in the home (`$HERMES_HOME/plugins/hermes-lcm-x`, or the pre-rename `hermes-lcm`) does
not load next to the bundled engine, even when `plugins.enabled` lists it: two copies would both write
every turn into the same `lcm.db`. Plugin discovery skips it with one warning naming the bundled copy, and
`plugin-catalog/removed.yaml` refuses `hermes plugins install hermes-lcm-x` (the live catalog still lists
it). Remove such a copy with `hermes plugins remove hermes-lcm-x`.

## Remote

```sh
git remote add lcm-x https://github.com/electricsheephq/lcm-x.git
git fetch lcm-x main --no-tags
```

## The mirror

`lcm-x-upstream` only fast-forwards to a newer LCM-X `main`. If it is not a fast-forward, stop and ask:
upstream rewrote history.

```sh
git fetch lcm-x main --no-tags
git push origin lcm-x/main:refs/heads/lcm-x-upstream   # rejected unless it is a fast-forward
```

## Taking an LCM-X release

1. Advance `lcm-x-upstream` (above) and pick the upstream commit to take, normally a release tag on it
   (`git ls-remote --tags lcm-x 'v*'`).
2. On a branch from `dev`:

   ```sh
   git subtree pull --prefix=plugins/context_engine/lcm-x --squash lcm-x <tag-or-commit>
   ```

   Resolve conflicts with the LINA changes listed in the ledger; never resolve one by editing the LICENSE
   file or a copyright or license header.
3. In the same branch:
   - add a row to `## Intakes` in `plugins/context_engine/lcm-x-intake.md` with the full upstream commit sha,
     and drop the `## LINA changes` rows upstream now covers;
   - add any new file over 500 KiB that upstream ships to `.github/blob-size-allowlist.txt`;
   - run `python scripts/ci/check_lcm_x_subtree.py --mirror lcm-x/main` and LCM-X's own suite (below);
   - review `.github/lcm-x-known-failures.txt`: drop the entries the release fixed.
4. Open a pull request into `dev`, add one `CHANGELOG.md` line under `## [Unreleased]` (for example "Took
   LCM-X v0.30.0 (<hash>)"), and merge it with a merge commit once the checks are green. A squash or rebase
   merge would drop the subtree squash commit the check reads.

## LINA changes

A change LINA makes inside `plugins/context_engine/lcm-x/` is an ordinary commit on `dev` and gets a row
under `## LINA changes` in the ledger. LCM-X is MIT: its LICENSE file and the copyright and license headers
of upstream files are never changed, and no modification notice is added (that notice exists for Honcho
because Honcho is AGPL-3.0, [honcho.md](honcho.md)). When upstream fixes the same problem, prefer upstream's
fix and remove ours, and its ledger row, in the intake pull request.

## CI

`.github/workflows/lcm-x.yml` runs on pull requests and `dev` pushes that touch the subtree, its ledger, the
context engine loader, the check scripts, the known-failures list or the workflow ([ci.md](ci.md)). It is
not a required check.

- **LCM-X subtree check**: `scripts/ci/check_lcm_x_subtree.py --mirror origin/lcm-x-upstream`. The ledger's
  last upstream commit is the newest squash's `git-subtree-split`, is on `lcm-x-upstream` and has the
  squash's tree; every upstream LICENSE, COPYING and NOTICE file is byte for byte unchanged; every copyright
  or SPDX header line of an upstream file is still at the top of that file. The checks are shared with the
  Honcho subtree (`scripts/ci/subtree_check.py`).
- **LCM-X tests**: LCM-X's own pytest suite, run from inside the subtree with the LINA tree on `PYTHONPATH`
  (the real `agent.context_engine`, not the stub upstream's CI writes), `-m "not latency"`, with xdist like
  upstream's `ci.yml`, without `tiktoken` (upstream's suite is calibrated on LCM-X's character estimate),
  and with `LCM_REAL_HERMES_PYTHON`/`LCM_REAL_HERMES_SRC` pointing LCM-X's real-host probes at the LINA
  tree. `.github/lcm-x-known-failures.txt` lists the tests that fail at the carried fork commit and the
  ones that fail only against LINA, one cause each; they are deselected, and any other failure fails the
  job.

The workflows under `plugins/context_engine/lcm-x/.github/` are upstream's and do not run. LINA's own checks
skip the subtree: ruff (`ruff.toml`), the code-health ratchet, the Windows-footgun, shebang, detached-spawn,
config-reader, temp-path literal (`scripts/check_no_tmp_literals.py`) and lazy-deps scans, and the AGENTS.md size check. LCM-X is held to its
upstream's lint config instead. The LINA-side tests of the bundled engine are
`tests/plugins/context_engine/test_lcm_x_bundled.py`, in the required suite.
