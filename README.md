# LINA

*Lifelong Intelligent Navigator & Ally.*

**Your world. One connected mind.**

Agents to think aloud with. LINA is the first of them.

We’re building agents that know you and that you can count on. Create the agents you want from presets; LINA is the first. A conversation is worth having even when it produces no deliverable.

Talk about your day. Think aloud. Bring a problem. Your agents carry your shared history across conversations and devices, notice what needs doing, turn it into goals and to-dos, and follow through to verified outcomes. What every agent should know is shared; what only one of them knows stays with it. Whichever one you talk to, you are never the messenger or the manager.

[Read our manifesto](MANIFESTO.md). The roadmap is [issue #1](https://github.com/lina-system/lina/issues/1).

> Alpha `0.0.1-alpha`. Not ready for use yet. Expect rough edges and frequent changes.

## Built to finish

LINA takes responsibility for the work and proves the result, without making you manage every step.

LINA's engine comes from [Hermes Agent](https://github.com/NousResearch/hermes-agent) by Nous Research, and its way of working is inspired by the ultrawork spirit of [omo](https://omo.dev).

**Humanity has spent enough time implementing the clipboard interface.**

## Repository

- `upstream` mirrors Hermes Agent unchanged. `dev` is where LINA is developed and the default branch. `main` holds LINA releases. See [docs/forking.md](docs/forking.md).
- Versions follow `0.0.1-alpha.YYYYMMDD.N` during alpha. See [CHANGELOG.md](CHANGELOG.md).

## The web app

`hermes dashboard` also serves the [lina-app](https://github.com/lina-system/lina-app) build under `/app/`, on the same address as the client API (`/api/lina/*`, `/api/plugins/kanban/work/*`). Point `dashboard.app_dist` in `config.yaml` (or `LINA_APP_DIST`, which wins) at the app's build folder (`dist/`). The page receives the same credential the dashboard's own page gets, so it needs no connection setting, and when the dashboard requires sign-in, `/app/` asks for the same sign-in. With no folder configured, or a missing one, `/app/` answers 404 and the rest of the dashboard is unchanged; `hermes serve` never serves it. `dashboard.app_label` names the install in one short line the app shows in its head (for example "시험 사본" on a test copy; trimmed, cut at 20 characters, none when empty), and `dashboard.app_label_kind` marks it `real` or `test` (any other value is dropped); both are read per request.

## License and contributions

LINA is licensed under AGPL-3.0 ([LICENSE](LICENSE)). It is a fork of [Hermes Agent](https://github.com/NousResearch/hermes-agent), whose original code is MIT-licensed ([LICENSES/Hermes-MIT.txt](LICENSES/Hermes-MIT.txt)). The `code-review` and `verification-gate` skills are adapted from [oh-my-hermes](https://github.com/rlaope/oh-my-hermes) (commit `59270ab4`), MIT License, Copyright (c) 2026 oh-my-hermes contributors; each skill carries the notice.

The LINA app is developed in [lina-app](https://github.com/lina-system/lina-app). LINA OS is a separate, optional project in [lina-os](https://github.com/lina-system/lina-os).

Report a problem or an idea in this repository's Issues.
