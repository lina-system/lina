---
name: kanban-work-plan
description: "Split a goal into gated kanban work cards."
version: 1.0.0
author: Jun (thisisjun786), Hermes Agent
license: MIT
platforms: [linux, macos, windows]
metadata:
  hermes:
    tags: [kanban, goal, planning, work-cards, gates, delegation]
    category: autonomous-ai-agents
    related_skills: [hermes-agent]
---

# Kanban Work Plan Skill

For a main session that was handed a goal: split it into work cards yourself, write them as one
plan file, and lay them out with `hermes kanban plan <file>`. Each work card runs as its own
goal-loop session and is done only when its gates pass. The automatic decomposer
(`hermes kanban decompose`, triage cards) is not used for this.

## When to Use

- A goal needs more than one session's worth of work, or parts for other agents.
- Keep it one card when the work is one short change with one gate; do not plan a single to-do.

## Prerequisites

- Every assignee is a Hermes profile (`hermes kanban assignees` lists them).
- Every work card has at least one gate command; the gates are `hermes kanban gate <gate> --help`.

## How to Run

```bash
hermes kanban plan plan.yaml --dry-run
hermes kanban plan plan.yaml
hermes kanban plan plan.yaml --json
```

`--dry-run` prints the cards, gates and links and writes nothing. The real run creates every card
and link in one transaction; running the same plan again returns the same card ids.

## Quick Reference

Plan file (YAML or JSON):

```yaml
key: lina-m0-work-sessions          # plan id; card idempotency keys are <key>/<card key>
goal:
  title: "Run a goal through work sessions"
  assignee: sion
  body: "Why, and what done looks like for the whole goal."
  ledger_goal: G63                   # optional
  # goal_mode: true                  # optional; the goal card is not goal-mode by default
cards:
  - key: gates
    title: "Gates in the card goal loop"
    assignee: sion
    todos: ["lina-system/lina#108", "[ ] write the gate doc"]
    outcome: "What exists when the card is done."
    verification: "How it is checked."
    constraints: "Decisions shared with other cards, repeated here."   # optional
    boundaries: "What the card must not touch."                        # optional
    stop_when: "The observable end state."
    worker: "omo — multi-file change"
    gates:
      - "hermes kanban gate prs"
      - "hermes kanban gate dev lina-system/lina"
      - "hermes kanban gate review --owner rev"   # rev's approval on THIS card
    max_turns: 40
    max_runtime: 12h
    workspace: "dir:/srv/work/lina"  # optional: scratch, worktree, worktree:<p>, dir:<p>
  - key: review
    title: "Review the gates PR"
    assignee: rev                    # another agent
    todos: ["[ ] review the gates PR, write reviews/gates.md, comment `approve` on the gates card"]
    outcome: "A review note with a verdict, and rev's approval comment on the gates card."
    verification: "The note has a Verdict line."
    stop_when: "The note is written and the approval is posted."
    worker: "rev — reviewer"
    gates: ['hermes kanban gate artifact reviews/gates.md --check "grep -q Verdict: reviews/gates.md"']
    max_turns: 10
    max_runtime: 2h
    # no `after: [gates]`: the gates card waits for this approval, so a link would deadlock
```

What gets created: a goal card, then one goal-mode work card per entry with its gates, turn budget
and maximum runtime. A work card body has the sections Goal, To-dos, Outcome, Verification,
Constraints, Boundaries, Stop when and Worker. Every work card is a link-parent of the goal card,
so the goal card becomes ready only when all of them are done; `after:` adds a link from the named
card.

Gates: code cards use `gate prs` (every To-dos issue closed by a merged PR with green checks) and
`gate dev owner/repo` (the branch is green after the merge); non-code cards use
`gate artifact <path> --check "<cmd>"` and `gate review`; add `gate ask` when the card may ask
the owner questions.

`gate review` never counts the card assignee's own approval: put it on the card being reviewed
(`--owner <reviewer>` for an agent reviewer), and gate the reviewer's own card on what the reviewer
writes (`gate artifact`).

## Procedure

1. Read the goal and list every to-do: GitHub issues as `owner/repo#n`, other items as `[ ] text`.
2. Group to-dos into cards. One card holds the same code or data, or one dependency chain: about
   3–8 code to-dos, or 1–3 deliverables. Split a card that would touch two unrelated areas.
3. Put each to-do in exactly one card.
4. Order cards with `after:` only when one really needs another's result; leave the rest parallel.
5. A decision two cards share (a name, a format, an interface) is written into both bodies, in
   `constraints`. Do not rely on one card reading the other.
6. A review by another agent, or a non-code part, is its own card assigned to that agent, gated on
   what that agent writes (`gate artifact`); the reviewed card carries `gate review --owner <agent>`
   and the two run side by side, without an `after:` link between them.
7. Give every card gates, `max_turns` and `max_runtime`, then run `--dry-run`, read it, and run the
   plan.

## Pitfalls

- The plan is refused as a whole, and nothing is written, when a work card has no gate, a to-do is
  in two cards, an assignee is not a profile, `after:` names an unknown card or makes a cycle, or
  the plan or a card sets `triage` / `decompose`. The error names the card.
- A laid-out plan cannot gain or lose cards: a changed card list under the same `key` is refused.
  Use a new `key`, or add the card with `hermes kanban create` and `hermes kanban link`.
- Unknown fields are refused, so a typo like `gate:` instead of `gates:` fails loudly.

## Verification

- `hermes kanban plan <file> --dry-run` lists every card with its gates and every link.
- After the run, `hermes kanban show <goal id>` lists the work cards as parents and the goal card
  is `todo`.
