# Changelog

All notable changes to LINA are documented in this file.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versions follow
[docs/versioning.md](docs/versioning.md): `0.0.1-alpha.YYYYMMDD.N` during the alpha.

Hermes Agent upstream changes are not listed here; see the upstream history on the `upstream` branch.

## [Unreleased]

### Fixed

- A kanban goal-mode card whose judge says WAIT is parked (`scheduled`) on the event it waits for, recorded on the card, and the dispatcher wakes it once when that card is done, that review has its result, that PR's required checks finish or that time passes (`kanban schedule|block --wait-for` record the same by hand); a spent turn budget, runtime limit or agent iteration budget hands off and returns the card to ready without counting toward `max_retries`, and `kanban.planned_stop_limit` (default 5) such stops in a row block the card for a person.

### Added

- Worker contacts negotiate protocol ranges and capabilities before verbs, preserve legacy run recovery, and guard service controls with per-start instance identities (lina#218).
- `hermes kanban gate ui-qa` passes a card that changes a screen only when its PR head has a QA result comment (the aside-qa answer with `ui-qa-result v1 head=<sha> instance=disposable` on top: sections Defects, Verified, Suggestions and Map drift) taken on a disposable instance with zero defects and no `FAIL:` line under Verified; it fails with the defect list, fails on a result for an older head, and waits (exit 75) while a `ui-qa-run v1 head=<sha> state=running` QA run is in progress; the kanban-work-plan skill documents the record and lists `ui-qa` in screen cards' gates (lina#162).
- Work settings: devices and caps, runners and their models, model defaults and fallback and routing rules in the LINA root's `config.yaml` are validated and versioned (`work-settings/revisions`, an audit line per change, one-step rollback, outside edits recorded), edited by `lina worker settings show|set|edit|validate|history|diff|rollback|route|models` and `GET/PATCH /api/lina/work/settings` (writes only from the owner's devices), with OCX's enabled models, their quota left and reset, and a `settings` source on `/api/lina/events` (see [docs/lina-work-settings.md](docs/lina-work-settings.md), lina#256).
- `ocx.external: {cli, data_dir}` in the LINA root's `config.yaml` makes `lina models` and the client API read an OCX run outside LINA, read-only (an argv allowlist, each read cached 60 s, every OCX write verb of `lina models` / `lina services` refused); `GET /api/lina/usage?range=` serves tokens and cost by model, provider, account and machine, `GET /api/lina/models/state` the models on and each machine link's freshness, and `GET /api/lina/quota` adds `left`, account `active`/`state` and the `missing` services (lina#232).
- The shared Desk: `hermes desk init` (run by the installer) creates `~/Desk` with Inbox, Projects, Areas, Resources and Archive, or adopts an existing folder; every agent starts there, and the app reads it from `GET /api/desk` (see [docs/desk.md](docs/desk.md)).
- The memory store: `lina store install` (also run by `lina install`) runs one Postgres with pgvector in Docker, with schemas `memory`, `ctx`, `ltm` and `vec` and row-level security on `agent_id`, and the Honcho api and deriver built from `services/honcho`; `lina store backup` / `restore` back up and restore the whole cluster, and `lina store upgrade` upgrades Honcho by backup, rehearsal migration, one migration job and smoke test (see [docs/store.md](docs/store.md)).
- The delivery procedure of [docs/delivery.md](docs/delivery.md) from the core: `lina work next` lists the issues to open and why the rest waits (`--submit` hands each pick to its runner through `lina worker`), `lina work merge <repo> <pr>` is the only merge path (the gates, a merge commit or the merge queue, then the issue closed and review follow-ups filed; `--branch` lands a worker branch by merging `dev` into it), `lina work status` shows sprint progress per milestone and `lina work sprint current <S>` moves the current sprint.
- The Honcho server v3.5.0 (`plastic-labs/honcho` 0cd37ddbd8) in `services/honcho/`, a squashed subtree tracked by the `honcho-upstream` branch and `services/honcho-intake.md`; CI checks, tests and builds it (see [docs/honcho.md](docs/honcho.md)).
- LCM-X is the core context engine, one copy for every agent: v0.29.1-rc2 (`electricsheephq/lcm-x` 0eb6e8736a) with the `hermes` fork's patches in `plugins/context_engine/lcm-x/`, a squashed subtree tracked by the `lcm-x-upstream` branch; new homes get `context.engine: lcm-x`, its hooks now register, and a `hermes-lcm-x` plugin copy no longer loads beside it (see [docs/lcm-x.md](docs/lcm-x.md)).
- Web Push to the owner's devices: the core pushes a decision to make, an agent's reply to his comment and a work card stopped for him, one notification per item that is replaced on change and closed when the item leaves (`/api/lina/push/*`, see [docs/api/push-client.yaml](docs/api/push-client.yaml)); off unless `LINA_PUSH_VAPID_PRIVATE_KEY` is set in `.env`.
- The hub's own health checks: each check that runs outside the core writes `<LINA root>/status/<name>.json` (`{name, ok, line, at, stale_after}`) and the app reads them from `GET /api/lina/status/checks`, newest first, stale past `stale_after`, a malformed file as one error row (`docs/api/board-client.yaml`).
- `lina worker` takes runs from a Hermes card and reports back on that card (`worker.card_link: {kind: hermes, command: [hermes]}`): the start, the end and `report.md` land on the card through Hermes's own `kanban` command, with no chat notice; `lina worker submit --card <id> --board <slug>` names the card, and `lina worker wait` parks it until the first run end wakes it (see [docs/worker-card-session.md](docs/worker-card-session.md)).

## [0.0.1-alpha.20261102.1] - 2026-11-02

### Added

- Forked from Hermes upstream c7c7e0fa92 (`NousResearch/hermes-agent` `main`, 2026-11-01).
- LINA is licensed under AGPL-3.0-only. Hermes Agent code keeps its MIT License; the notice is in `LICENSES/Hermes-MIT.txt`.
- `hermes --version` reports the LINA version and the Hermes upstream commit.
- The macOS gateway launchd job materializes dataless (on-demand cloud) files when opened.

### Changed

- CI runs only lint, Python tests, lockfile, supply-chain and blob-size checks; the other upstream workflows are kept under `.github/workflows-upstream/` (see [docs/ci.md](docs/ci.md)).
