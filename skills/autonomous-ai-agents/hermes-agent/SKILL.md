---
name: hermes-agent
description: "Configure and operate the agent and its shared workspace."
version: 1.0.0
author: Hermes Agent + Teknium
license: MIT
platforms: [linux, macos, windows]
metadata:
  hermes:
    tags: [lina, cli, configuration, delegation, skills, gateway]
    category: autonomous-ai-agents
    related_skills: [claude-code, codex]
---

# LINA Skill

Operate LINA through its CLI, messaging gateway, and shared Desk. This is an operating guide; inspect the installed command's help and repository source before relying on a flag or setting.

## When to Use

- Configure models, tools, skills, profiles, or messaging.
- Coordinate independent agent runs or diagnose missing capabilities.
- Work with the Desk, project context, or CLI skins.

## Prerequisites

- A working LINA installation. The command is `lina`; `hermes` is an alias with the same arguments and exit codes.
- Model access through the magpie provider in `plugins/model-providers/magpie/`. magpie owns routing to upstream models.
- Resolve the active profile's state through `$HERMES_HOME`; use `get_hermes_home()` in code. Do not assume another profile shares credentials or memory.
- Use `terminal` for CLI commands and `read_file` / `search_files` to inspect local instructions and source.

## How to Run

```bash
lina --help
lina setup
lina model
lina doctor
lina
```

For a bounded query that exits even in a terminal:

```bash
lina chat --oneshot -q "Summarize the project instructions before changing files."
```

Use the repository's `docs/` for LINA-specific guidance, especially `docs/desk.md`. For additional commands, inspect `hermes_cli/subcommands/` and their wiring in `hermes_cli/main.py`; chat flags live in `hermes_cli/_parser.py`. Do not infer availability from an old guide.

## Quick Reference

| Task | Read first |
| --- | --- |
| Command syntax and source evidence | [CLI reference](references/cli-reference.md) |
| magpie and model selection | [Providers and models](references/providers-and-models.md) |
| Settings, toolsets, voice | [Configuration](references/configuration.md) |
| Project instructions and the Desk | [Project context](references/project-context-files.md) |
| Secret handling and approvals | [Security and privacy](references/security-privacy.md) |
| Delegation, schedules, skill maintenance | [Background systems](references/background-systems.md) |
| Delegation capacity diagnosis | [Concurrency diagnosis](references/delegate-task-concurrency-diagnosis.md) |
| External MCP tools | [Native MCP](references/native-mcp.md) |
| Event-triggered runs | [Webhooks](references/webhooks.md) |
| CLI palette | [Themes](references/themes.md), [skin template](templates/skin.yaml) |
| In-session commands | [Slash commands](references/slash-commands.md) |
| Terminal mascots | [Petdex](references/petdex.md) |
| Operational failures | [Troubleshooting](references/troubleshooting.md) |
| Windows behavior | [Windows notes](references/windows-quirks.md) |
| Code contributions | [Contributor guide](references/contributor-guide.md) |

## Procedure

1. Identify the active profile, working directory, and requested outcome. Read the matching reference above before giving detailed instructions.
2. Inspect configuration using `lina config path` and `lina config get <key>`. Keep settings in `config.yaml` and secrets in `.env`. Use `lina config set <key> <value>` rather than hand-editing YAML.
3. Check the shared Desk with `lina desk show`. Create it with `lina desk init`, or adopt an existing folder with `lina desk init --path ~/project --adopt`. Adoption preserves existing files and numbered PARA folders.
4. Keep owner files in Inbox, Projects, Areas, Resources, or Archive. Records hold task state and point to files as `desk:<relative path>` instead of duplicating their contents. Archive completed material; do not delete owner files without approval. Re-read before editing and leave files alone while another agent edits them.
5. Use `delegate_task` for bounded subtasks. For a separate process, use `lina chat --oneshot -q <prompt>` with an explicit working directory, output artifact, and timeout. Parallel editors need separate worktrees; `lina -w` provides worktree mode. Observe completion and inspect the artifact before reporting success.
6. For interactive subprocesses, use a real PTY and wait for the visible input prompt before sending text; startup can discard early input. Do not substitute fixed startup sleeps for readiness. Use `process_manage` to inspect or steer the process. A background handle alone does not prove persistence or success.
7. Start a fresh session after changes to prompt-affecting state. Verify the requested behavior on the affected surface.

## Pitfalls

- Preserve the cached conversation prefix: do not mutate past context, swap toolsets, or rebuild the system prompt mid-conversation. Context compression is the exception.
- Preserve user/assistant role alternation; repeated tool-result messages are allowed.
- The Desk is deliberately shared: `desk.path` belongs to the default profile's config, not the active profile. Profile state remains isolated.
- Do not invent commands for work records or workers. Use the task ledger as a concept until source proves a CLI exists.
- LINA Helper is separate from the CLI: inspect `helper/` and `hermes_cli/helper_install.py` for macOS installation and permissions. Do not treat CLI readiness as proof that OS permissions are granted.
- A missing topic in this skill is not evidence that a feature is absent. Check `lina --help` and source.

## Verification

- Confirm the selected magpie model with a real bounded query, without printing credentials.
- Confirm the Desk path and layout with `lina desk show --json`.
- Confirm configuration by reading it back; confirm tool or skill changes in a fresh session.
- Report the actual result, pending work, and any failed checks, not just the command issued.
