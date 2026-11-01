# CI

LINA runs a small CI of its own. The upstream Hermes workflows are kept, unchanged and inactive, for comparison when upstream is taken in ([forking.md](forking.md)).

## What runs

GitHub Actions reads only `.github/workflows/`. It holds one workflow, `ci.yml` ("CI"), which runs on pull requests into `dev` or `main`, on pushes to `dev`, and by manual dispatch, on GitHub-hosted runners. None of its jobs uses an upstream organisation secret, runner or app. A pull request starts one workflow run.

Like upstream `ci.yaml`, the first job (`Detect affected areas`) classifies the diff once with `scripts/ci/classify_changes.py`, and a lane the change cannot affect starts no runner. A pull request that touches only prose (`*.md` outside `skills/`, `docs/`) runs no test slices. A push to `dev` or a manual run has no diff, so every lane runs. Any `.github/` change also runs every lane.

| Job (check name) | Runs when | What it does | Comes from upstream |
| --- | --- | --- | --- |
| Detect affected areas | always | Picks the lanes below | `ci.yaml`, job `detect` |
| **Lint** (required) | always | `ruff check .` with the ruff version pinned by `scripts/code_health` | `lint.yml`, job "ruff enforcement (blocking)" |
| Python tests (Linux n/8) | `python` lane | `scripts/run_tests.sh` on Ubuntu, in 8 slices, with the per-file duration cache (`test_durations.json`; pull requests read it, `dev` pushes write it) | `tests.yml`, job "Run tests" |
| **Python tests (Linux)** (required) | always | Green when every slice passed, or when detect skipped the slices because the change cannot reach Python | `ci.yaml`, job `all-checks-pass` |
| Python tests (macOS) | pushes to `dev`, manual runs | macOS-marked tests | `tests-os.yml`, macOS jobs |
| Diff checks | always | Blob size check (a binary over 500 KiB fails; exceptions: `.github/blob-size-allowlist.txt`), and on the `scan`/`deps` lanes the supply-chain scan (added lines that decode base64 into `exec`/`eval` or run an encoded subprocess command) | `blob-size-check.yml`, `supply-chain-audit.yml` job "scan" |
| uv.lock check | `uv_lock` lane | `uv.lock` agrees with `pyproject.toml` | `uv-lockfile-check.yml` |
| Prune check | always | Fails while a tracked file matches `prune-list.txt`, the upstream paths LINA does not carry ([forking.md](forking.md)) | LINA only |

Changes from upstream in the kept checks: `ubuntu-24.04`/`macos-latest` instead of Blacksmith runners, `actions/checkout` instead of `useblacksmith/checkout`, one workflow instead of `ci.yaml` calling reusable workflows, and no review-comment or timing artifacts. The advisory lint diff, the code-health ratchet, the Windows-footgun checks, the dependency-bound check, the e2e lanes and the Windows tests are not run.

Local equivalents: `ruff check .`, `scripts/run_tests.sh`, `python -m pm.build_env --source . --check-lock`, `python scripts/ci/check_blob_size.py --base origin/dev`, `python3 scripts/ci/check_prune_list.py`.

## Required checks

`dev` and `main` require two checks: **Lint** and **Python tests (Linux)**. Both are job names in `ci.yml` and always report, so a pull request never waits on a check that did not start. The repository ruleset also forbids force push and deletion on `upstream`, `dev` and `main`. `upstream` has no required checks: it only fast-forwards to Hermes `main`.

## What was moved

- All 57 upstream workflows are in `.github/workflows-upstream/`, byte for byte, including the original versions of the checks above and of `ci.yaml`. Actions does not read that folder.
- `dependabot.yml`, `CODEOWNERS`, `ISSUE_TEMPLATE/` and `PULL_REQUEST_TEMPLATE.md` are in `.github/upstream/`, so they are inactive. Issue and pull request forms come from [lina-system/.github](https://github.com/lina-system/.github).
- `.github/actions/`, `.github/scripts/`, `actionlint.yaml` and `blob-size-allowlist.txt` stay in place: the kept workflows and the tests use them.
- Tests that read upstream workflow files read them from `.github/workflows-upstream/`.

When upstream is taken in, compare upstream's `.github/` with `.github/workflows-upstream/` and `.github/upstream/`, copy the new versions there, and port a change into `.github/workflows/` only when it fits LINA.

## Private repositories

Only public repositories use GitHub CI. Private repositories (for example hermes-ops and its dashboard) run no GitHub Actions: their tests are run locally before a change is merged, and the pull request body records the command and its result.

## Moved workflows

- `archive-inputs.yml`
- `blob-size-check.yml` (LINA version: a job in `.github/workflows/ci.yml`)
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
- `lint.yml` (LINA version: a job in `.github/workflows/ci.yml`)
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
- `supply-chain-audit.yml` (LINA version: a job in `.github/workflows/ci.yml`)
- `termux-verify.yml`
- `tests-os.yml`
- `tests.yml` (LINA version: a job in `.github/workflows/ci.yml`)
- `uv-lockfile-check.yml` (LINA version: a job in `.github/workflows/ci.yml`)
- `windows-bundle-sdk.yml`
- `windows-desktop-attach-e2e.yml`
- `windows-install-update-e2e.yml`
- `windows-venv-e2e.yml`
