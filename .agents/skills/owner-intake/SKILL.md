---
name: owner-intake
description: "Route owner requests without losing the active plan."
version: 1.0.0
author: LINA
license: AGPL-3.0
platforms: [linux, macos, windows]
metadata:
  hermes:
    tags: [intake, planning, owner-report]
    category: productivity
    related_skills: [work-ledger, lina-worker, writing-review]
---

# Owner Intake Skill

Classify incoming owner messages against the active goal. Preserve one authoritative plan while recording new work, decisions, and questions without silently widening scope.

## When to Use

When the owner sends a request, idea, correction, problem report, or decision while work is active.

## Prerequisites

Read the current goal, acceptance checklist, issue history, dependencies, and any pending owner decisions. Use the existing issue or work-record surface; do not assume planned CLI features exist.

## How to Run

Inspect the authoritative record and source evidence with `read_file`, `search_files`, or available issue tools. Use `work-ledger` to track accepted work and `lina-worker` for scoped execution.

## Quick Reference

| Input | Outcome |
|---|---|
| Explicit stop, go, correction, or chosen option | Apply to exactly the named scope. |
| Small improvement within authorized scope | Update the plan and continue. |
| Useful idea outside the current goal | Park it in the plan's later-work list. |
| Proposal changing scope, order, or a shared contract | Show impact and recommendation; obtain the owner's decision. |
| Pure question | Answer from evidence; do not silently create implementation work. |

## Procedure

1. Read the full message and its immediate context. Distinguish an instruction from a thought or question; do not treat every idea as a new execution order. Apply corrections narrowly and retain adjacent work not cancelled by the owner.
2. State how it affects the current goal: no change, detail, priority, or scope. Check the real tool, screen, or artifact named before explaining or redesigning it. If two meanings remain, state the narrower interpretation or ask the one question that changes the outcome.
3. Choose one outcome from the table. Record accepted new work in the same turn with the owner's words, source, owner, end condition, and execution link. Keep parked ideas distinct from commitments.
4. Keep the plan in one issue or work record. Update that source first, then direct running sessions to reread it; do not stack conflicting instructions in scratch notes. Batch related incoming messages before revising the plan.
5. Express real dependencies as blockers. Start unblocked independent work; pause only the streams affected by a pending decision. Do not add arbitrary concurrency limits or turn a merge blocker into a blanket stop on unrelated work.
6. At an agreed checkpoint, stop new work beyond it. Report the original goal, verified outcomes, deviations, unknowns, and next-stage recommendation; resume the next stage only after the owner's decision.

### `owner-report` label flow

Use `owner-report` on the tracked issue when it must be brought to the owner's attention: an unresolved choice, an owner-only action, or a checkpoint awaiting review. Follow any repository-specific label policy; the label itself neither authorizes implementation nor implies an automated notification.

1. Verify the issue's current state and remove already-resolved or superseded questions from the report.
2. Record what the owner must decide or do, why it matters now, the verified evidence, options, and a recommendation. Link affected work and block only dependent items.
3. Present the concise report through the authorized conversation. Number independent decisions so replies can be applied individually.
4. When the owner answers, preserve the actual answer and apply it to the authoritative plan and affected tasks in the same turn. Clear `owner-report` only when no unresolved owner action remains; otherwise update the remaining request.

## Pitfalls

- Do not quote agent checks as owner approval or turn a speculative question into a directive.
- Do not retire neighboring tools, cancel unrelated goals, or broaden a correction without evidence that the owner requested it.
- Do not duplicate policy decisions across a script, skill, and issue; name the authoritative source.
- Do not present worker claims as verified facts. Check artifacts, commands, and live state before reporting progress.
- Do not hide an unresolved decision inside technical logs. Use plain outcome language and `writing-review` for owner-facing text.
- When the owner stops work, stop the affected sessions, runs, commands, supervisors, and watchers; verify they remain stopped. Preserve records and artifacts rather than deleting them.

## Verification

The latest owner intent is reflected in one plan; active work has an owner and evidence trail; parked ideas remain parked. Every `owner-report` issue has a concrete unresolved owner action, and answered decisions are reflected in dependent work.
