# CI

LINA runs a small CI of its own. The upstream Hermes workflows are kept, unchanged and inactive, for comparison when upstream is taken in ([forking.md](forking.md)).

## What runs

GitHub Actions reads only `.github/workflows/`. It holds `ci.yml` ("CI"), which runs on pull requests into `dev` or `main`, on pushes to `dev`, and by manual dispatch, on GitHub-hosted runners. None of its jobs uses an upstream organisation secret, runner or app. A pull request starts one workflow run. `honcho.yml` checks, tests and builds the vendored Honcho server in `services/honcho/` and runs only when those paths change ([honcho.md](honcho.md)). `codex-review-gate.yml` ("Merge gates") sets the `Codex review`, `Linked issue` and `dev green` commit statuses on a pull request's head ([Required checks](#required-checks)).

Like upstream `ci.yaml`, the first job (`Detect affected areas`) classifies the diff once with `scripts/ci/classify_changes.py`, and a lane the change cannot affect starts no runner. A pull request that touches only prose (`*.md` outside `skills/`, `docs/`) runs no test slices. A push to `dev` or a manual run has no diff, so every lane runs. Any `.github/` change also runs every lane.

| Job (check name) | Runs when | What it does | Comes from upstream |
| --- | --- | --- | --- |
| Detect affected areas | always | Picks the lanes below | `ci.yaml`, job `detect` |
| **Lint** (required) | always | `ruff check .` with the ruff version pinned by `scripts/code_health` | `lint.yml`, job "ruff enforcement (blocking)" |
| Python tests (Linux n/8) | `python` lane | `scripts/run_tests.sh` on Ubuntu, in 8 slices, with the per-file duration cache (`test_durations.json`; pull requests read it, `dev` pushes write it) | `tests.yml`, job "Run tests" |
| **Python tests (Linux)** (required) | always | Green when every slice passed, or when detect skipped the slices because the change cannot reach Python | `ci.yaml`, job `all-checks-pass` |
| **Python tests (macOS)** (required) | pushes to `dev`, manual runs; a pull request on the `macos` lane | macOS-marked tests. A pull request runs them when it changes a test file `scripts/ci/list_os_marked_tests.py macos` lists or a module such a file imports, or carries the `dev-red-fix` label (added after the last push, it counts on a re-run); other pull requests skip them (runner cost; a skip passes the check). Upstream runs them on every pull request with a Python change | `tests-os.yml`, macOS jobs |
| LINA Helper (macOS) | always | On `macos-latest`: Swift tests, the unlock package's native checks and an ad-hoc build of `helper/macos` (LINA Helper). Its sign-and-notarise job is a placeholder that does not run | LINA's own (from the LINA Helper repository's `check.yml`) |
| **LINA Helper (macOS)** (required) | `helper` lane | On `macos-latest`: Swift tests, the unlock package's native checks and an ad-hoc build of `helper/macos` (LINA Helper). Its sign-and-notarise job is a placeholder that does not run | LINA's own (from the LINA Helper repository's `check.yml`) |
| Diff checks | always | Blob size check (a binary over 500 KiB fails; exceptions: `.github/blob-size-allowlist.txt`), and on the `scan`/`deps` lanes the supply-chain scan (added lines that decode base64 into `exec`/`eval` or run an encoded subprocess command) | `blob-size-check.yml`, `supply-chain-audit.yml` job "scan" |
| uv.lock check | `uv_lock` lane | `uv.lock` agrees with `pyproject.toml` | `uv-lockfile-check.yml` |
| Prune check | always | Fails while a tracked file matches `prune-list.txt`, the upstream paths LINA does not carry ([forking.md](forking.md)) | LINA only |
| Honcho subtree check, Honcho tests, Honcho image build (`honcho.yml`) | `services/`, its check script or the workflow change | The Honcho server in `services/honcho/`: subtree, ledger, LICENSE files, headers and modification notice; its pytest suite against pgvector; its Docker image. Only when `services/`, the check script or the workflow change ([honcho.md](honcho.md)) | Honcho's `unittest.yml` and `docker-build.yml` |

Changes from upstream in the kept checks: `ubuntu-24.04`/`macos-latest` instead of Blacksmith runners, `actions/checkout` instead of `useblacksmith/checkout`, one workflow instead of `ci.yaml` calling reusable workflows, and no review-comment or timing artifacts. The advisory lint diff, the code-health ratchet, the Windows-footgun checks, the dependency-bound check, the e2e lanes and the Windows tests are not run.

Local equivalents: `ruff check .`, `scripts/run_tests.sh`, `python -m pm.build_env --source . --check-lock`, `python scripts/ci/check_blob_size.py --base origin/dev`, `python3 scripts/ci/check_prune_list.py`.

The `macos` lane is on when a changed path names macOS (`macos`, `darwin`, `launchd`, `keychain`, `osascript`), is under `helper/` or `hermes_platform/`, is the test harness or the dependency set (`pyproject.toml`, `uv.lock`, `scripts/run_tests*.`, `tests/conftest.py`, the platform-gating fixture, `scripts/ci/list_os_marked_tests.py`), or is a `.py` file whose checkout text has a darwin branch or a `platforms(...)` gate that reaches the macOS lane (the files `scripts/ci/list_os_marked_tests.py macos` lists). A pull request labelled `dev-red-fix` also turns it on, and CI runs on `labeled` so adding the label starts it. The `helper` lane is on for `helper/`. Like every lane, both are on for pushes, manual runs and `.github/` changes. Upstream ran its macOS tests on every Python change; LINA narrows that to macOS paths, so `detect` checks out the whole tree to read the changed files.

## Required checks

`dev` and `main` require these checks on the head commit. A skipped job counts as passed, so each always reports and a pull request never waits on a check that did not start.

| Check | Source | Green when |
| --- | --- | --- |
| Lint | `ci.yml` job | `ruff check .` passes |
| Python tests (Linux) | `ci.yml` job | every slice passed, or `detect` found no change the suite can see; fails when `detect` fails |
| Python tests (macOS) | `ci.yml` job | the macOS tests passed, or skipped: a pull request that changes no macOS path and has no `dev-red-fix` label |
| LINA Helper (macOS) | `ci.yml` job | Swift tests, native checks and the ad-hoc build passed, or skipped: `helper/` did not change. The Linux half of its `make test` runs inside Python tests (Linux) (`tests/helper/`) |
| Codex review | `codex-review-gate.yml` status | `chatgpt-codex-connector[bot]` reviewed this head (a review on it, its summary comment marking a finished review of it without findings, or a "no major issues" comment newer than it) and none of its review comments made on this head carries a P0 or P1 badge. Red with the count and the first finding; pending until the review of this head arrives. A review of an earlier head never counts |
| Linked issue | `codex-review-gate.yml` status | the body closes (`Closes`/`Fixes #n`, GitHub `closingIssuesReferences`) an open issue of this repository, and a branch named `<type>/<n>-<slug>` closes #n; or the pull request carries the `no-issue` label (a typo, a one-line CI fix) |
| dev green | `codex-review-gate.yml` status | the latest completed push CI on `dev` passed. While `dev` is red it is pending (wait) for every pull request except one labelled `dev-red-fix` on whose head at least one of `dev`'s red jobs passes (skipped is not passing) and no other check is red (red when it adds a red check or makes none of `dev`'s pass), or one the owner labelled `dev-red-approved` (a repository admin added the label; [delivery.md](delivery.md), step 7) |

The three statuses are judged by `scripts/ci/codex_review_gate.py` (tests: `tests/ci/test_codex_review_gate.py`). The workflow runs on `pull_request_target` (opened, synchronize, reopened, edited, labeled, unlabeled), `pull_request_review` (submitted), `issue_comment` (created, edited: Codex edits one summary comment per pull request) and `workflow_run` of CI (completed: a finished `dev` push or pull request run changes `dev green`, so every open pull request into `dev` is judged again). It checks out the gate script from `dev` and runs nothing from the pull request, with `GITHUB_TOKEN` only (`statuses: write`; `actions`, `checks`, `contents`, `issues`, `pull-requests`: read). Local run: `python3 scripts/ci/codex_review_gate.py --repo lina-system/lina --pr <n> --dry-run`.

Known limits: if Codex is down, `Codex review` stays pending and merging stops; wait, do not bypass. Commit statuses come from GitHub Actions, so a workflow added by a pull request could post one; the gate stops mistakes, not a hostile author. A pull request from a fork gets a read-only token on `pull_request_review`, so its status updates on the next comment or push. The repository ruleset also forbids force push and deletion on `upstream`, `dev` and `main`. `upstream` has no required checks: it only fast-forwards to Hermes `main`.

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
