# Contributor Guide

Read the repository root and area-specific `AGENTS.md` before editing. LINA-specific documentation is in `docs/`; check current source for inherited internals.

| Area | Source |
| --- | --- |
| CLI and command wiring | `hermes_cli/main.py`, `hermes_cli/subcommands/` |
| Chat parser | `hermes_cli/_parser.py` |
| Turn phases | `agent/turn_*.py`, `run_agent.py` facade |
| Tools | `tools/registry.py`, `model_tools.py`, `toolsets.py` |
| OCX routing | `plugins/model-providers/ocx/` |
| Shared files | `hermes_cli/desk.py`, `docs/desk.md` |
| Messaging and scheduling | `gateway/`, `cron/` |

Extend an existing tool, CLI plus skill, or plugin before adding a core tool. Plugins stay within their directory and the documented extension interfaces; never patch core from a plugin. Register tools through the registry with a reachability check and a toolset. Do not add a hook without a concrete consumer.

Keep behavior in topical siblings rather than growing facades. Resolve paths with `get_hermes_home()`; bind owning profile scope for background callbacks and child processes. Do not read a launch-profile global as though it belongs to the current conversation.

Preserve prompt caching and role alternation. Remote terminal backends must not advertise inaccessible host files; file tools and environment hints must describe the same execution environment.

Run focused behavior tests through `scripts/run_tests.sh` using a disposable `HERMES_HOME`. Never fake the host OS to test OS behavior. Use the repository PM workflow for dependencies, not raw package installs into the application environment. Pure prose needs authoring checks, not tests that pin wording.
