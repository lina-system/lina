---
name: claude-code
description: "Run delegated coding and multi-turn review sessions."
version: 1.0.0
author: Teknium, LINA
license: MIT
platforms: [linux, macos, windows]
metadata:
  hermes:
    tags: [coding, runner, claude, review, automation]
    category: autonomous-ai-agents
    related_skills: [codex, github, code-review]
---

# Claude Code Skill

Claude Code is a runner that LINA dispatches scoped coding and review work to.
LINA owns routing, the work record, follow-ups and acceptance; the runner returns
artifacts and evidence for the orchestrator to verify.

## When to Use

Use print mode for bounded one-shot work and interactive mode for work that needs
follow-ups in the same session. Run independent writers in separate worktrees.

## Prerequisites

- Install Claude Code; check `claude --version`, `claude --help`, and `claude doctor`.
- Use the configured OCX routing for model access. Check the runner's environment
  separately from LINA's provider config; never print credentials or substitute
  direct vendor access to mask routing failures.
- Read repository instructions and confirm the remote, base and archive state.
- Use `terminal` and `process` for launch, logs, input and cancellation. Interactive
  sessions need a PTY; keep long sessions in the background.

## How to Run

```text
terminal(command="claude -p 'Read BRIEF.md, complete its task and report checks and blockers.' --output-format json --max-turns 20", workdir="~/project", background=true)
```

Use `write_file` for a complete brief: goal, source paths, allowed edit paths,
read-only inputs, non-goals, confidentiality rules, checks and report path.
Confidentiality applies to scratch notes and drafts too. The runner does not
inherit the chat. Follow the start/follow-up/state/result/stop contract described
in LINA issue #35 and the acceptance record described in #36; these are planned
interfaces, not commands to invoke today.

## Quick Reference

| Option | Purpose |
|---|---|
| `-p '<task>'` | One-shot print mode |
| `--output-format json` | Structured result, session ID and usage |
| `--output-format stream-json --verbose` | Streaming events |
| `--resume <id>` | Continue the recorded session |
| `--continue` | Continue the most recent session in the same directory |
| `--max-turns <n>` | Bound print-mode work |
| `--max-budget-usd <n>` | Bound spend in print mode |
| `--allowedTools 'Read,Edit'` | Narrow available capabilities |
| `--permission-mode acceptEdits` | Accept edits while other tools may prompt |
| `--add-dir <paths...>` | Add source directories; not a read-only boundary |

## Procedure

1. Confirm the canonical repository and existing work, then write the brief before
   launch. For a large rewrite, bound the initial exploration and save reusable
   findings instead of repeating broad reads. Done when scope and acceptance are finite.
2. Choose print or interactive mode. Print mode has no interactive approval UI;
   use appropriate permissions, not an assumption that all actions are approved.
   For interactive work launch `claude` in a background PTY, then inspect the pane
   before submitting the brief. Done when the instruction is visibly running.
3. Read trust dialogs before answering: versions differ in whether "No, exit" or
   "Yes, I trust this folder" is selected. Trust only the intended checkout. Never
   send Enter on a timer or approve an unread permission prompt.
   Done when trust and permissions match the task.
4. Observe completion through the session's notifications/logs. Record the JSON
   `session_id` and inspect result subtype, errors and exit status. A fast exit can
   be an argument error, not completed analysis. Done when there is a real result.
5. Send a follow-up to the same interactive session or resume its recorded ID in
   the same directory. Batch related corrections; interrupt immediately only when
   the current work would build on a reversed decision. Confirm pasted input was
   submitted rather than left in an editor or trust dialog.
   Done when the session follows the latest contract.
6. Read the report and inspect every claimed artifact yourself. Verify the diff,
   tests, and any remote state before accepting or landing. Clean up owned sessions
   when finished. Done when the work record reflects verified results and blockers.

## Configuration and Project Context

Keep shared instructions in `CLAUDE.md`, `.claude/rules/` and project agents in
`.claude/agents/`. Project settings live in `.claude/settings.json`; local overrides
belong in `.claude/settings.local.json`. Do not change global settings for a trial.
Use installed-version help for model and effort options. Preserve OCX routing and
check real model/provider usage and tool execution, not a self-reported model name.

For larger delegated work, name the subagent roles, concurrency budget and checks
in the brief. Do not impose a personal model preference or an unverified effort
toggle as a universal requirement. Keep one writer responsible for each edit area.

## Pitfalls

- `--add-dir` is variadic: a trailing prompt may be consumed as another path.
  Put the prompt before flags or supply it on stdin. Added directories are not
  made read-only by this flag; enforce that boundary through the brief and sandbox.
- Print mode ending a turn does not prove all background subagents finished.
  Require the report and expected artifacts, and use an interactive session for
  long work that needs continued steering. Preserve the session ID when resuming.
- Slash commands such as `/compact` operate in interactive mode. Describe the
  task in natural language in print mode.
- `--bare` skips normal project setup and OAuth; do not use it if the run depends
  on configured routing, hooks, project instructions or MCP discovery.
- Do not default to permission-bypass flags. Approve only the specific action
  actually requested; avoid blind keypresses and unbounded automatic loops.
- Do not infer completion or a dead process from slowness alone. Inspect the actual
  session state and output before cancelling, relaunching or starting duplicate work.

## Verification

Check the runner's result status, report, changed files and actual test exits.
Confirm follow-ups reached the intended session and no unexpected files or remote
changes were made. Use the GitHub skill's body, label and merge-commit rules when
the requested outcome includes a PR.
