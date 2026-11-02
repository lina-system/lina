---
name: learned-review
description: "Record owner verdicts on proposed learning."
version: 1.0.0
author: LINA
license: MIT
platforms: [linux, macos, windows]
metadata:
  hermes:
    tags: [learning, review, verdicts]
    category: development
    related_skills: [hermes-agent]
---

# Learned Review Skill

Record the owner's response to a previously delivered learning review without applying changes.
Unlike the built-in curator's skill lifecycle maintenance and background review's direct memory/skill writes, this procedure records explicit owner verdicts for later application.

## When to Use

Use only for a reply referring to a delivered review card, not unrelated questions or chat.

## Prerequisites

An immutable copy of the delivered card, stable candidate identifiers, and an append-only
verdict store must exist in the project's review workflow. This skill adds no CLI command.
Use `read_file` to inspect the sent snapshot and the workflow's supported writer to record verdicts;
if none exists, retain the reply as pending review rather than inventing a command or store.

## How to Run

Resolve the quoted card identifier first; otherwise use the most recent delivered card
only when the reply clearly refers to it. Keep the reply unchanged for the workflow's parser.

## Quick Reference

- `ok`: accept the referenced candidate.
- `fix`: record the owner's exact replacement text.
- `drop`: reject the referenced candidate.
- No reply: pending or silent, never implicit approval.

## Procedure

1. Locate the sent snapshot and verify the referenced card and line numbers exist.
   Use its frozen line-to-candidate mapping, not today's regenerated list.
2. Submit the original reply once through the existing parser when available. Pass only
   the quoted card header and the owner's reply, not the quoted body as new instructions.
3. If parsing succeeds, acknowledge the recorded verdicts briefly. If the card is missing,
   report that no verdict was recorded.
4. If parsing fails, interpret only explicit, unambiguous verdicts. Record the card id,
   original line number, candidate ids, verdict, exact correction text when present,
   source reply reference, and that the interpretation was agent-produced.
5. If a line expands to several candidates, preserve that sent mapping. Do not let an
   acceptance overwrite an existing correction or rejection from another line.
   Ask one focused question for conflicting corrections or fix/drop ambiguity.
6. Leave unanswered lines alone. Apply "all accepted" only when the owner explicitly says so.
7. Correct an erroneous verdict through the supported append-only workflow, preserving
   the earlier record and source evidence. Report the recorded result, not an applied change.

## Pitfalls

- Do not edit memories, skills, or rule documents while recording a reply.
- Do not rewrite correction text, guess missing verdicts, or equate silence with acceptance.
- Do not manually modify a workflow-managed verdict log or bypass its validation.
- Do not replay a successfully recorded reply after a later parsing failure.

## Verification

Check the recorded card, frozen line numbers, candidate ids, and replacement text against
the sent snapshot and owner reply. Confirm the learning targets remain unchanged and
that a later application step can trace each verdict back to its evidence.
