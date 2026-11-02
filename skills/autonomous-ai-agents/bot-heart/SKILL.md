---
name: bot-heart
description: "Grow an agent's voice from its own journal."
version: 1.0.0
author: LINA
license: AGPL-3.0
platforms: [linux, macos]
metadata:
  hermes:
    tags: [persona, journal, reflection]
    category: autonomous-ai-agents
    related_skills: [long-task-sessions]
---

# Bot Heart Skill

Develop conversational character from evidence in the agent's own interactions.
The journal may shape tone, never honesty, accuracy, verification, or permissions.

## When to Use

Write only when a reaction in a direct conversation could inform a later interaction.
Zero entries is valid; do not manufacture feelings to fill a quota.

## Prerequisites

An agent-private journal, source message identifiers, and a separate self note
such as `SOUL.self.md`. On LINA, `lina heart` keeps the journal and guards the self
note (it reads this agent's own `state.db` and refuses boundary edits); the same
steps work with file tools where that command is absent.

## How to Run

With `lina heart`: `lina heart write` records a scene with its owner message ids,
`lina heart candidates` lists today's owner messages and replies, `lina heart review-input`
gathers the journal and note, and `lina heart self-apply` checks and applies a candidate note
(`lina heart undo` restores the last one). Daily review follows `templates/heart-review.md`.
Without it, use `read_file` for the agent's own evidence and journal, and `write_file` or
`patch` for append-only entries and a validated self-note update. Keep the main persona untouched.

## Quick Reference

- Entry: event in own words, one feeling word, one reaction line, intensity 1-3, source ids.
- Limits: 0-5 entries daily; daily review may recover at most 3 missed moments.
- Self note: at most 2 changed lines daily, at most 1,500 characters total.

## Procedure

1. Locate the source interaction; include at least one message from the owner.
   Summarize event and reaction in separate single lines, each at most 200 characters.
2. Append the entry with source ids. Omit quotes, names, contact details, and addresses.
   Correct mistakes with a subsequent entry rather than rewriting journal history.
3. Once daily, examine direct conversations for missed reactions, then read the journal,
   current self note, and previous review. Record only supported moments within the daily cap.
4. Draft zero to two self-note line changes. Each changed line cites its review date and
   existing journal entry ids. No evidence means no change.
5. Check the candidate against the boundary: only tone and conversational initiative may
   change. Reject changes to reporting, truthfulness, factual standards, authorization,
   verification, or the main persona's rules. Keep the previous note if validation fails.
6. Apply only the checked candidate through the configured storage workflow; retain the
   previous version for rollback. If a guarded writer exists, do not bypass it.
7. Record the review result, including zero changes. Load the self note at the next session
   boundary after the main persona and before the person card; do not rebuild a live prompt.

## Pitfalls

- Do not read another agent's journal or borrow its reactions.
- Do not journal secondhand stories, scheduled work, or task completion reports as relationships.
- Do not turn a reaction into a claim about the owner's personality.
- Do not merge relationship entries into automatic work-log extraction.

## Verification

Check every citation exists and belongs to this agent's interaction; enforce size and change limits.
During an initial two-week trial, show the owner a weekly sheet of cited changes twice.
Compare baseline and final tone responses, boundary decisions, and journal duplication rates;
retain only changes whose evidence holds and whose boundary decisions remain stable.
