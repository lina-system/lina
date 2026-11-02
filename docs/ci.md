<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# CI

LINA runs a small CI of its own. The upstream Hermes workflows are kept, unchanged and inactive, for comparison when upstream is taken in ([forking.md](forking.md)).

## What runs

GitHub Actions reads only `.github/workflows/`. Every workflow there runs on pull requests into `dev` or `main`, on pushes to `dev`, and by manual dispatch, on GitHub-hosted runners. None uses an upstream organisation secret, runner or app.

| Workflow | Job (check name) | What it does | Comes from upstream |
| --- | --- | --- | --- |
| `lint.yml` | **Lint** (required) | `ruff check .` with the ruff version pinned by `scripts/code_health` | `lint.yml`, job "ruff enforcement (blocking)" |
| `tests.yml` | **Python tests (Linux)** (required) | `scripts/run_tests.sh` on Ubuntu, in 8 slices (`Python tests (Linux n/8)`); the required job is green only when every slice passed | `tests.yml`, job "Run tests" |
| `tests.yml` | Python tests (macOS) | macOS-marked tests; pushes to `dev` and manual runs only | `tests-os.yml`, macOS jobs |
| `uv-lockfile-check.yml` | uv.lock check | `uv.lock` agrees with `pyproject.toml` | `uv-lockfile-check.yml` |
| `supply-chain-audit.yml` | Supply chain audit | Fails when the added lines decode base64 into `exec`/`eval` or run an encoded subprocess command | `supply-chain-audit.yml`, job "scan" |
| `blob-size-check.yml` | Blob size check | Fails when a change adds or modifies a binary over 500 KiB (exceptions: `.github/blob-size-allowlist.txt`) | `blob-size-check.yml` |

Changes from upstream in the kept checks: `ubuntu-24.04`/`macos-latest` instead of Blacksmith runners, `actions/checkout` instead of `useblacksmith/checkout`, standalone triggers instead of the `ci.yaml` orchestrator, and no review-comment or timing artifacts. The advisory lint diff, the code-health ratchet, the Windows-footgun checks, the dependency-bound check, the e2e lanes and the Windows tests are not run.

Local equivalents: `ruff check .`, `scripts/run_tests.sh`, `python -m pm.build_env --source . --check-lock`, `python scripts/ci/check_blob_size.py --base origin/dev`.

## Required checks

`dev` and `main` require two checks: **Lint** and **Python tests (Linux)**. The repository ruleset also forbids force push and deletion on `upstream`, `dev` and `main`. `upstream` has no required checks: it only fast-forwards to Hermes `main`.

## What was moved

- All 57 upstream workflows are in `.github/workflows-upstream/`, byte for byte, including the original versions of the five above. Actions does not read that folder.
- `dependabot.yml`, `CODEOWNERS`, `ISSUE_TEMPLATE/` and `PULL_REQUEST_TEMPLATE.md` are in `.github/upstream/`, so they are inactive. Issue and pull request forms come from [lina-system/.github](https://github.com/lina-system/.github).
- `.github/actions/`, `.github/scripts/`, `actionlint.yaml` and `blob-size-allowlist.txt` stay in place: the kept workflows and the tests use them.
- Tests that read upstream workflow files read them from `.github/workflows-upstream/`.

When upstream is taken in, compare upstream's `.github/` with `.github/workflows-upstream/` and `.github/upstream/`, copy the new versions there, and port a change into `.github/workflows/` only when it fits LINA.

## Private repositories

Only public repositories use GitHub CI. Private repositories (for example hermes-ops and its dashboard) run no GitHub Actions: their tests are run locally before a change is merged, and the pull request body records the command and its result.

## Moved workflows

- `archive-inputs.yml`
- `blob-size-check.yml` (LINA version in `.github/workflows/`)
- `bootstrap-installer-build.yml`
- `bootstrap-installer.yml`
- `canary-release.yml`
- `case-collision-check.yml`
- `catalog-tests.yml`
- `ci-review-comment.yml`
- `ci.yaml`
- `contributor-check.yml`
- `deploy-site.yml`
- `desktop-bundle-smoke.yml`
- `desktop-bundled-release.yml`
- `docker-lint.yml`
- `docker.yml`
- `docs-search-monitor.yml`
- `docs-site-checks.yml`
- `e2e-desktop-core.yml`
- `e2e-desktop-update.yml`
- `e2e-desktop.yml`
- `history-check.yml`
- `icons-freshness-check.yml`
- `infographic-check.yml`
- `install-e2e-macos-run.yml`
- `install-e2e-red.yml`
- `install-e2e-run.yml`
- `install-e2e-windows-run.yml`
- `install-e2e.yml`
- `js-autofix.yml`
- `js-tests.yml`
- `label-rerun.yml`
- `lazy-deps-guard.yml`
- `lint.yml` (LINA version in `.github/workflows/`)
- `live-providers.yml`
- `lockfile-diff.yml`
- `nix.yml`
- `osv-scanner.yml`
- `plugin-catalog-ci.yml`
- `pm-bundle.yml`
- `profile-artifact-check.yml`
- `prompt-surface-diff.yml`
- `remote-backends.yml`
- `rust-tests.yml`
- `sandbox-image.yml`
- `skills-index-freshness.yml`
- `skills-index.yml`
- `stable-release-publication.yml`
- `stable-release.yml`
- `supply-chain-audit.yml` (LINA version in `.github/workflows/`)
- `termux-verify.yml`
- `tests-os.yml`
- `tests.yml` (LINA version in `.github/workflows/`)
- `uv-lockfile-check.yml` (LINA version in `.github/workflows/`)
- `windows-bundle-sdk.yml`
- `windows-desktop-attach-e2e.yml`
- `windows-install-update-e2e.yml`
- `windows-venv-e2e.yml`

## Next

- Dependency license check that flags AGPL, GPL and LGPL dependencies.
