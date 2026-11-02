---
name: codex
description: "Run delegated coding tasks and local reviews."
version: 1.0.0
author: Hermes Agent
license: MIT
platforms: [linux, macos, windows]
metadata:
  hermes:
    tags: [coding, runner, codex, review]
    category: autonomous-ai-agents
    related_skills: [claude-code, github, code-review]
---

# Codex Skill

Codex is a coding runner that LINA dispatches bounded work to. LINA owns the
work record, routing, follow-ups and acceptance; a runner's exit is evidence to
review, not automatic permission to commit, push or merge.

## When to Use

- Implement a scoped feature or fix in an isolated checkout.
- Review a diff locally or run independent tasks in separate worktrees.
- Continue work assigned to this runner rather than starting a duplicate run.

## Prerequisites

- Install the Codex CLI and check `codex --version` and `codex exec --help`.
- Use a repository with its instructions available and a dedicated worktree for
  each writing run. Record the initial status and base revision.
- Configure model access through OCX. Never read or copy authentication files.
- Use `terminal` with a PTY for interactive runs and `process` for session input,
  logs and cancellation. Keep long runs in a background session.

## How to Run

```text
terminal(command="codex exec --sandbox workspace-write 'Read BRIEF.md and complete its scoped task; report checks and remaining blockers.'", workdir="~/project", background=true, pty=true)
```

Write the full brief with `write_file`: goal, allowed edit paths, read-only
inputs, non-goals, acceptance checks and report location. The runner does not
inherit the conversation. The dispatch contract in LINA issue #35 covers start,
follow-up, state, result and stop; the work ledger in #36 tracks acceptance.
Those issues describe planned integration, not an available work subcommand.

## Quick Reference

| Command | Purpose |
|---|---|
| `codex exec '<task>'` | Bounded one-shot task |
| `--sandbox workspace-write` | Permit workspace edits within the sandbox |
| `--sandbox read-only` | Read-only investigation or review |
| `codex review --base origin/main` | Review against the merge base |
| `codex review --commit <sha>` | Review one commit |
| `codex review --uncommitted` | Review staged, unstaged and untracked changes |
| `-c review_model='<catalog-model>'` | Choose the reviewer model |

## Procedure

1. Confirm the canonical repository, issue, base and existing work. Put the
   complete contract in the brief before launch, including confidentiality rules
   for scratch files and reports. Done when the runner can work without chat context.
2. Launch in its own worktree with the intended permissions. Inspect the initial
   pane or log: a trust dialog or idle prompt means the task has not started.
   Read the selected trust option before answering; never send blind input.
   Done when the actual task is running.
3. Retain the session handle and observe logs or completion notifications. An
   `exec` run does not accept conversational steering: finish and amend locally,
   or stop and relaunch with a corrected brief when the goal materially changes.
   Done when the report or an explicit blocker arrives.
4. Read the diff, tests and report yourself. Check build/CI changes for sandbox-only
   workarounds. Do not alter tests to conceal environment failures. Run required
   host checks before accepting. Done when the result meets the brief with evidence.
5. LINA's orchestrator handles acceptance and authorized landing outside the
   runner. Check shared history for unexpected commits; never use Git plumbing
   to evade a sandbox's inability to write repository metadata.
   Done when the work record reflects verified results and remaining blockers.

## OCX Configuration

LINA model calls use the `ocx` provider (`opencodex` alias), with `OCX_BASE_URL`
and secret `OCX_API_KEY`; the default local API is `http://127.0.0.1:10100/v1`.
LINA's provider setting does not configure the standalone Codex process.

For Codex, use its existing OCX provider entry in `~/.codex/config.toml`:

```toml
model_provider = "opencodex"
model = "<OCX catalog model id>"

[model_providers.opencodex]
name = "OCX"
base_url = "<OCX endpoint compatible with this Codex version>"
env_key = "OCX_API_KEY"
```

Preserve the installation's transport settings; LINA's chat-completions endpoint
alone does not prove Codex Responses compatibility. Check `ocx inspect catalog
--json` for models and supported reasoning settings. Never replace routing with
direct vendor auth to work around a proxy failure.

Project agent roles belong in `.codex/agents/<name>.toml`; keep reusable brief
instructions in `AGENTS.md` and project skills in `.agents/skills/`. Ask explicitly
for delegation, isolate each writer, and verify installed-version role settings.
Verify the actual model/provider and real tool execution in OCX logs; text that
looks like a tool call is not proof it ran. Keep trial settings project-local.

## Pitfalls

- A sandbox failure is not a product failure. On macOS, repository locks outside
  the worktree, Unix socket binding, nested SwiftPM sandboxes, signature trust
  checks and network access can fail only inside the sandbox. Reproduce on the
  appropriate host before changing product code. Report sandbox limits in the brief.
- Do not automatically switch to unrestricted execution when a sandbox fails.
  Use a host with a working sandbox or report the blocker.
- Inspect structured logs read-only under `~/.codex/` before blaming the model.
  Session transcripts may omit transport errors. A websocket 426 followed by
  successful HTTP fallback is not a failed run.
- For an expired MCP OAuth grant, reauthenticate through `codex mcp login <server>`
  and verify a real server call in a new session. A remote callback needs an SSH
  loopback tunnel; the owner approves in their browser. Do not copy login files.
- Cloud capacity and account pooling are not LINA's active routing path. Do not
  dispatch to a paused runner or change global account configuration for a trial.

## Verification

Check the diff and actual command exit results, not only the runner's summary.
Re-run sandbox-sensitive failures on the intended host and record limitations.
For reviews, confirm the reviewed head matches the proposed change. Use the
GitHub skill's issue, label, review and merge-commit rules when landing is requested.
