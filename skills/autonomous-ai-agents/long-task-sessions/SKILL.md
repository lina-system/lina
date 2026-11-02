---
name: long-task-sessions
description: "Run long tasks with checkpoints and restart recovery."
version: 1.0.0
author: LINA
license: AGPL-3.0
platforms: [linux, macos, windows]
metadata:
  hermes:
    tags: [background, sessions, recovery, prompt-cache]
    category: autonomous-ai-agents
    related_skills: [hermes-agent]
---

# Long Task Sessions Skill

Keep conversation responsive while long work runs with durable progress records.
Background execution alone does not guarantee survival across a restart.

## When to Use

Use for multi-step work, long commands, or waits; keep short lookups and owner decisions in chat.

## Prerequisites

Use `terminal` for commands, `delegate_task` for bounded independent reasoning,
and `write_file` or `patch` for a durable work record. Inspect available tool arguments first.
Restart survival requires an installed supervisor or scheduler outside the restarting process.

## How to Run

Record the brief before launching. For a long command, use
`terminal(background=true, notify=true)` when supported; keep its returned handle.
For restart-safe work, use the host's configured supervisor and retain its job identifier.

## Quick Reference

- One command: background terminal plus completion notification.
- Independent reasoning: `delegate_task` with explicit inputs and acceptance criteria.
- Multi-day work: durable task ledger, supervised execution, checkpoints, and recovery.
- Remote work: supervise it on the destination, not inside a connection from another host.

## Procedure

1. Write the goal, allowed paths, forbidden changes, completion checks, result location,
   owner, dependencies, and resume instructions into the work record. Exclude secrets.
2. Verify the chosen execution mechanism's restart behavior. Ordinary child processes and
   delegated tasks may stop with their parent; a reboot requires configured recovery.
3. Launch once and record the handle. Subscribe to completion; do independent work or end
   the turn instead of repeatedly checking status. Serialize writers to the same task.
4. Checkpoint completed steps, pending work, partial changes, and verification evidence.
   Add follow-up instructions to the existing task through its supported control channel.
5. If a correction invalidates current work, stop that exact job, inspect partial outputs,
   and restart from the last safe checkpoint with the correction first.
6. Before shutdown, request a safe checkpoint and confirm affected jobs have stopped.
   After restart, inspect durable results and execution state before resuming unfinished steps.
7. Compare result files and exit status against acceptance checks. Process disappearance
   alone is not success. Report the result, verification, or one concrete blocker.

## Pitfalls

- Do not invent task-runner commands or assume background means restart-safe.
- Do not kill jobs by broad command-line patterns or interfere with another owner's job.
- Do not create a new task for every follow-up or a new agent session for deterministic events.
- Do not treat an intentionally offline dependency as a failure without checking intent.
- Do not let unattended work exceed the original permission boundary.

## Verification

Confirm a durable checkpoint exists, completion is observable, and resume skips finished work.
For reused conversations, keep the system prompt, toolset, and startup skill set byte-stable;
load extra skills with `skill_view` in the current turn, not by changing startup injection.
Defer persona and memory reloads to a new session; compression is the permitted cache break.
Batch recurring events and rotate sessions with bounded history. Diagnose cache misses using
request differences and cache usage, separating expiry or account changes from prefix changes.
