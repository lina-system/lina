# Changelog

All notable changes to LINA are documented in this file.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versions follow
[docs/versioning.md](docs/versioning.md): `0.0.1-alpha.YYYYMMDD.N` during the alpha.

Hermes Agent upstream changes are not listed here; see the upstream history on the `upstream` branch.

## [Unreleased]

### Added

- The shared Desk: `hermes desk init` (run by the installer) creates `~/Desk` with Inbox, Projects, Areas, Resources and Archive, or adopts an existing folder; every agent starts there, and the app reads it from `GET /api/desk` (see [docs/desk.md](docs/desk.md)).

## [0.0.1-alpha.20261102.1] - 2026-11-02

### Added

- Forked from Hermes upstream c7c7e0fa92 (`NousResearch/hermes-agent` `main`, 2026-11-01).
- LINA is licensed under AGPL-3.0-only. Hermes Agent code keeps its MIT License; the notice is in `LICENSES/Hermes-MIT.txt`.
- `hermes --version` reports the LINA version and the Hermes upstream commit.
- The macOS gateway launchd job materializes dataless (on-demand cloud) files when opened.

### Changed

- CI runs only lint, Python tests, lockfile, supply-chain and blob-size checks; the other upstream workflows are kept under `.github/workflows-upstream/` (see [docs/ci.md](docs/ci.md)).
