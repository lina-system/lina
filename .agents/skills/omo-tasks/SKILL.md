---
name: omo-tasks
description: "Delegate scoped work and inspect runner results."
version: 1.0.0
author: LINA
license: MIT
platforms: [linux, macos, windows]
metadata:
  hermes:
    tags: [delegation, runners, execution]
    category: software-development
    related_skills: [work-ledger, verification-gate, code-review, long-task-sessions]
---

# Omo Tasks Skill

Use omo as the default coding runner. Keep assignment, execution, result delivery, acceptance, and integration as separate observed states.

## When to Use

Delegate implementation, research, document work, or independent units of a larger goal. Tiny read-only checks and trivial local edits can stay in the current session.

## Prerequisites

Inspect the installed runner's supported interface and repository instructions before using it. Issue #35 describes the intended runner contract, not a shipped worker command. Use the available runner tools; do not invent a dispatcher or CLI.

## How to Run

Prepare a scoped brief, link it to the work record, start the supported runner, and retain its task/session identifier. Use `read_file` for returned artifacts and `terminal` for verification through the project's own commands.

## Quick Reference

| Mode | Use |
|---|---|
| `ulw` | A defined deliverable: implement, execute, and verify. |
| `research` | An open question needing sourced investigation. |
| `mass` | Many independent units with explicit dependencies. |
| `mass-research` | Broad research followed by evidence-based narrowing. |

Use only modes the installed interface exposes. Select the mode once through its supported control; do not duplicate prefixes if the runner adds them.

## Procedure

1. Read the work record and check for an existing run or already-integrated result. Reuse the same session for follow-ups when supported; create a replacement only when its context is genuinely unavailable.
2. Write the brief with: target outcome, requirement/issue links, allowed edit paths, forbidden paths and effects, input locations, measurable stop condition, output locations, and exact verification requirements. Treat input copies as read-only.
3. Confirm the repository named in the brief matches the assigned checkout. Check concurrent work; serialize shared-contract changes or pass explicit ownership boundaries rather than allowing conflicting edits.
4. Route to omo by default. Use another configured runner only under an explicit repository routing rule, not an improvised failure chain. Record requested model/profile settings; inspect run evidence before claiming which model actually executed.
5. Start the run and attach its identifier to the task. Subscribe to completion through the available notification surface. Do not repeatedly poll; inspect status only when it changes a decision. A launched command is not a completed deliverable.
6. Steer running work through its existing session. After completion, send a follow-up for missing work through the supported continuation interface. A follow-up may execute immediately: do not send future notes as executable instructions.
7. Require a report naming created artifacts, sources and commands checked, assumptions, unverified areas, acceptance-condition results, remaining blockers, and what the next session must know. Long checks must finish and their results be collected before completion is claimed.
8. On notification, verify the latest run identifier and whether another session already accepted or integrated it. Read the report and actual artifacts, not just the summary. Inspect code revisions and changed paths; check source references for research claims.
9. Compare every end condition with observed evidence using `verification-gate`. Missing outputs, stale results, unfinished commands, or permission-limited no-ops are not success. Request a correction in the same session and record why the result was rejected.
10. Accept only after inspection; record what was checked and link the evidence. Before integration, check whether it already landed. Preserve unrelated changes, verify against the current base, and require the repository's review and quality gates. Acceptance does not authorize publishing, pushing, or merging.
11. Move reusable findings into the appropriate domain record, update the work record, and report the result promptly. On cancellation, stop the owning run and its supervisors/watchers, confirm the stopped state, and retain artifacts.

## Pitfalls

- Never equate "done", delivered, accepted, merged, and deployed.
- A stalled or disconnected status may hide finished artifacts. Inspect the latest report and history before duplicating work; do not automatically restart on another runner.
- Do not put secrets in briefs or send private source to another provider without authorization.
- Do not delegate messages, credential changes, deletion, spending, or external publication without the owner's authorization for that effect.
- Do not prescribe machine names, private launchers, or fixed model settings as portable runner requirements.

## Verification

The work record identifies the real run, the latest artifacts satisfy the brief, required checks have completed, and the acceptance record names inspected evidence. Any remaining blocker or integration step is explicit rather than hidden behind the runner's completion label.
