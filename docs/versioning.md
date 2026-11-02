<!-- SPDX-License-Identifier: AGPL-3.0-only -->
# Versioning

LINA restarts its version at `0.0.1`. Hermes upstream versions and tags are not carried over.

## Alpha versions

During the alpha every release is `0.0.1-alpha.YYYYMMDD.N`:

- `0.0.1` stays fixed for the whole alpha.
- `YYYYMMDD` is the release date.
- `N` counts that day's releases, starting at 1.

The first release is `0.0.1-alpha.20261102.1`.

PEP 440 does not accept the SemVer string, and `0.0.1a20261102.1` is not valid PEP 440 either (no release segment may follow the pre-release number), so the Python package writes `N` as a post-release of the day's pre-release: `0.0.1aYYYYMMDD.postN`. This keeps the order right: `0.0.1a20261102.post1` < `0.0.1a20261102.post2` < `0.0.1a20261103.post1` < `0.0.1`. Jun decides when the alpha ends and what version follows it.

## Where the version lives

| Place | Form | Example |
| --- | --- | --- |
| `hermes_cli/lina_version.py` `LINA_VERSION` | SemVer, shown to users | `0.0.1-alpha.20261102.1` |
| `pyproject.toml` `[project] version` (and the `hermes-agent` entry in `uv.lock`) | PEP 440: `0.0.1aYYYYMMDD.postN` | `0.0.1a20261102.post1` |
| Git tag | `v` + SemVer | `v0.0.1-alpha.20261102.1` |
| `CHANGELOG.md` heading | SemVer | `## [0.0.1-alpha.20261102.1] - 2026-11-02` |

`tests/hermes_cli/test_lina_version.py` fails when `LINA_VERSION` and `pyproject.toml` disagree.
The `package.json` files under `apps/` keep their upstream versions.

`hermes --version` prints `LINA <LINA_VERSION> (Hermes upstream <LINA_UPSTREAM_COMMIT>)` as its first line, followed by the upstream Hermes lines.
`LINA_UPSTREAM_COMMIT` is the `upstream` commit that `dev` last took in; update it in the same pull request that merges upstream.

## Making a release

1. On `dev`, in one pull request: set `LINA_VERSION`, the PEP 440 version in `pyproject.toml` and `uv.lock`, and move the `## [Unreleased]` lines in `CHANGELOG.md` under `## [<version>] - <date>`.
2. After that pull request is merged with both required checks green, fast-forward `main` to that `dev` commit.
3. Tag the commit `v<version>` and push the tag. Tags are never moved or deleted.

Every release needs Jun's approval.
