---
name: work-ledger
description: "Track goals, execution, blockers, and completion proof."
version: 1.0.0
author: LINA
license: AGPL-3.0
platforms: [linux, macos, windows]
metadata:
  hermes:
    tags: [work, goals, evidence]
    category: productivity
    related_skills: [omo-tasks, verification-gate, writing-review, owner-intake]
---

# Work Ledger Skill

Keep the work record aligned with the owner's request and the actual execution. This is a record-keeping procedure, not a new CLI.

## When to Use

When accepting a task, making a promise, reporting progress, resolving a blocker, or verifying completion.

## Prerequisites

Identify the repository's existing record surface: an issue, task board, or maintained work document. The planned work-record integration in issue #36 is not an available command; use the existing surface and its documented operations.

## How to Run

Read the current record and linked source artifacts with `read_file` or the available task tools before changing state. Use `omo-tasks` for delegated execution and `verification-gate` for evidence.

## Quick Reference

Record: goal, task, owner, original request and source, measurable end condition, check, execution link, status, blocker, next action, evidence, and any owner-supplied deadline. Link external issues and runs rather than copying their changing status.

## Procedure

1. In the accepting turn, add or update the goal and at least one actionable task or execution link. Reuse an equivalent goal and append its new basis; do not turn questions or casual discussion into tasks.
2. Preserve the owner's exact words with their source. Agent promises are agent statements, not owner instructions. If the original message is unavailable, disclose the source actually used; never reconstruct a quotation.
3. Split work into independently finishable tasks with an owner, benefit, end condition, and check. Give each execution unit its own task instead of hiding unrelated runs under one umbrella item. Confirm the record identifier exists before handing it to a runner.
4. Attach the execution or issue link immediately. For conditional work, record the dependency and who will resolve it. A waiting item names the prerequisite, next action, and next review point; a date is not a substitute for an action.
5. Use a bounded, read-only check that proves the stated outcome. Run it before registering it and record exit status and scope. Checks must not write, restart services, publish, or expose secrets. Use inspected evidence only when the outcome cannot be checked mechanically.
6. Report progress from the current record and source state, not session memory. Record material progress, waiting reasons, and owner decisions promptly. Before reacting to a stale reminder, check whether another session already handled it.
7. Compare actual artifacts with the end condition before closing. A worker's completion status or an attached link alone does not prove the task is done. For multi-stage work, use an end-to-end condition that cannot pass on an intermediate artifact.
8. Attach verified evidence and name who inspected it and what they saw. Keep owner confirmation separate from agent verification; never quote an agent's check as the owner's approval. If an item closed early, reopen it and repair its completion condition together.
9. For multi-stage goals, record measurable checkpoints and hold dependent stages until the agreed owner review. At a checkpoint report the original goal, achieved evidence, deviations, unknowns, and the next-stage recommendation; start nothing beyond the agreed boundary.
10. Apply cancellation only to what the owner cancelled, preserving their words. Reconcile open child tasks explicitly rather than assuming cancellation cascades. Do not silently delete, cancel, or reassign another agent's work.

## Pitfalls

- Do not invent work-record commands or promise integration that has not shipped.
- Do not copy secrets into requests, notes, checks, or evidence; check output may become visible to others.
- Do not infer deadlines, fabricate approval, or mark a goal complete from a summary alone.
- Do not duplicate the writing checker or its word lists. Use `writing-review` for titles, labels, briefs, and owner-facing reports; keep technical identifiers in links and evidence fields.
- If a write is refused, read the error, correct the named field, and verify the saved result before continuing.

## Verification

Each open task has an owner and real execution or a named dependency. Each closed task has evidence matching its end condition. Progress reports distinguish complete, active, waiting, and unverified work and state only the decisions or actions the owner must take.
