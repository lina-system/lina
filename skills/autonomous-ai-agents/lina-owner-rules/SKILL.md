---
name: lina-owner-rules
description: "Standing rules the owner set for every agent on LINA."
version: 1.0.0
author: LINA
license: AGPL-3.0
platforms: [linux, macos, windows]
metadata:
  hermes:
    tags: [owner, rules, defaults, privacy, reporting]
    category: autonomous-ai-agents
    related_skills: [writing-review, long-task-sessions]
---

# LINA Owner Rules Skill

The owner's standing rules for agents working on LINA, each in the owner's terms. Rules that
already have a home in another issue are linked there, not repeated here.

## When to Use

Before you review cards, handle personal source text, create a scheduled job, change compaction
settings, write text that relays what an agent did, write a report, close a design conversation,
or wait on a blocked item. Also when anyone asks what the owner's rules are.

## Prerequisites

Issue references point to `lina-system/lina` (`lina#<n>`) and `lina-system/lina-app`
(`lina-app#<n>`). Read one with `terminal`: `gh issue view <n> -R lina-system/lina`.

## Quick Reference

| Documented default | Value |
|---|---|
| Scheduled (cron) sessions | the Sonnet model at medium effort |
| Memory compaction threshold | 0.5 (`compression.threshold: 0.50`) |

## Rules

1. **Reviewer agent.** The reviewer agent takes no work. It only approves or sends back the
   cards in the review column, each against the done condition of its goal kind.
2. **Personal source text.** Personal source text may go to a worker and to the model services in
   use. It never goes to a public place: public repositories, public issues, outside posts.
   Secrets are written nowhere. This is a general LINA rule, not only a capture rule; the design is
   in `docs/design/work-and-delegation.md` (lina#656), and capture follows it (lina#498).
3. **Scheduled sessions.** Scheduled (cron) sessions run on the Sonnet model at medium effort.
   When you create or edit a job, pin both on it with `terminal`:
   `lina cron edit <job> --model <the Sonnet model> --reasoning-effort medium` (the same flags on
   `lina cron create`), unless the owner asked for another.
4. **Compaction threshold.** The memory compaction threshold default is 0.5: compaction starts
   when the context reaches half of the model's window. Leave it at 0.5 unless the owner sets
   another value.
5. **Neutral work wording.** Text that relays what an agent said, decided or is doing is written
   in one neutral work style (Korean noun endings), the same for every agent. Only the owner's own
   words and the first line of a message an agent really sent show as written.
6. **Report body.** The report body is written by the `agy` runner from the situation and the
   raw material; its facts are checked afterwards.
7. **Design decisions.** The design decisions agreed in a conversation are filed as issues by the
   agent that had the conversation.
8. **Blocked items.** An item that is blocked, or waiting on an answer, never stops the lines of
   work that do not depend on it; they run at the same time.

## Rules kept elsewhere

Read these where they live; they are not restated here.

- How an agent talks in chat: lina#261.
- How a long report is sent: lina#263.
- The one record for goals, tasks and runs, and the Desk goal folder: lina#291.
- Goal kinds and their workflow presets: lina#317.
- The work session log kept by a background light model: lina#326.
- Browser and computer work on the owner's devices: lina#331.
- Screen rules (one detail frame for every detail view, one-line history rows, what counts as an
  in-progress line): lina-app#123.

## Pitfalls

- A documented default is the owner's rule even where a config default differs; follow the rule
  and report the difference instead of editing settings on your own.
- Sending personal source text to a worker is allowed; quoting it in a public issue, commit or
  pull request is not, even as an example.

## Verification

Asked about any rule above, you can state it in the owner's terms and name its number or link.
