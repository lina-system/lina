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

`--dry-run` prints the cards, gates and links, the record to-dos each card would create or link and
the notices that would go out, and writes nothing. The real run creates every card and link in one
transaction; running the same plan again returns the same card ids.

With `ledger_goal`, the run then writes the work record (`lina work`): the goal card is linked to
the record goal, each card's `ledger_todos` are created or linked under it, and every other agent
with a card gets one notice line with only its card id, title and to-do ids. A card for another
agent is how work is asked of that agent: the work sits in the record as its to-do, and only the
ids are sent. A re-run creates no to-dos and sends no notices.

## Quick Reference

Plan file (YAML or JSON):

```yaml
key: lina-m0-work-sessions          # plan id; card idempotency keys are <key>/<card key>
goal:
  title: "Run a goal through work sessions"
  assignee: sion
  body: "Why, and what done looks like for the whole goal."
  ledger_goal: G63                   # optional: the open goal in the work record
  # goal_mode: true                  # optional; the goal card is not goal-mode by default
cards:
  - key: gates
    title: "Gates in the card goal loop"
    assignee: sion
    size: M                         # optional: S, M or L
    kind: implementation            # optional: work category, free text
    todos: ["lina-system/lina#108", "[ ] write the gate doc"]
    ledger_todos:                    # optional, needs ledger_goal: record to-dos of this card
      - T120                         # an open to-do under G63, linked to the card
      - title: "게이트 문서 작성"         # a new to-do: by the assignee, run = this card
        why: "무엇이 카드를 끝내는지 분명해진다"
        done: "게이트 문서가 병합된다"
        check: "gh pr list --state merged --search gates"
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
    ledger_todos: [T121]             # rev is told: card id, title, T121
    outcome: "A review note with a verdict, and rev's approval comment on the gates card."
    verification: "The note has a Verdict line."
    stop_when: "The note is written and the approval is posted."
    worker: "rev — reviewer"
    gates: ['hermes kanban gate artifact reviews/gates.md --check "grep -q Verdict: reviews/gates.md"']
    max_turns: 10
    max_runtime: 2h
    # no `after: [gates]`: the gates card waits for this approval, so a link would deadlock
  - key: board-screen
    title: "Show gate results on the board screen"
    assignee: sion
    todos: ["lina-system/lina-app#40"]
    outcome: "The board screen shows each card's gate results."
    verification: "QA on a disposable instance against the screen feature map finds no defects."
    stop_when: "The PR is merged and the QA result for its head has zero defects."
    worker: "omo — screen change"
    gates:
      - "hermes kanban gate prs"
      - "hermes kanban gate ui-qa"           # a card that changes a screen always lists ui-qa
    max_turns: 40
    max_runtime: 12h
```

What gets created: a goal card, then one goal-mode work card per entry with its gates, turn budget
and maximum runtime. Optional `size` (`S`, `M`, `L`) and `kind` (free text) become `Size: X` and
`Kind: X` lines in the work body; omitting them adds no metadata lines. A work card body has the
sections Goal, To-dos, Outcome, Verification, Constraints, Boundaries, Stop when, Worker and Record.
Record asks the work session to keep the work record current: add a card comment marking discovered to-dos with
`added-by: work-session` on checklist lines, for example `- [ ] check migration — added-by: work-session`.
Record work that needs a bigger runner in a card comment with `escalate: todo -> runner - reason`, replacing `todo` and
`reason` with the item and why the runner must handle it. These are record markers, not plan fields.
Every work card is a link-parent of the goal card,
so the goal card becomes ready only when all of them are done; `after:` adds a link from the named
card.

Gates: code cards use `gate prs` (every To-dos issue closed by a merged PR with green checks) and
`gate dev owner/repo` (the branch is green after the merge); non-code cards use
`gate artifact <path> --check "<cmd>"` and `gate review`; add `gate ask` when the card may ask
the owner questions. A card that changes a screen people see also lists `gate ui-qa`.

`gate ui-qa` reads the QA record on the card. Leave the aside-qa answer (`answer.md`) as it is, with
one marker line on top, as a card comment (`hermes kanban comment <card> "<text>"`). The marker
is `ui-qa-result v1 head=<sha> instance=disposable` (`<sha>` the head of the card's PR, at least 7
hex characters). The answer's sections are `## (A) Defects`, `## (B) Verified`, `## (C) Suggestions`
and `## (D) Map drift`; each list item under Defects is a defect (`- None.` is not), and so is each
`- FAIL: ...` line under Verified. A run in progress is a comment `ui-qa-run v1 head=<sha> state=running`. The gate passes on
the newest result for the PR head with all four sections and zero defects, fails with the defect
list, fails on a result taken anywhere but a disposable instance or only for an older head (run QA
again), and waits (exit 75) while the newest marker for the head is a running run.

`gate review` never counts the card assignee's own approval: put it on the card being reviewed
(`--owner <reviewer>` for an agent reviewer), and gate the reviewer's own card on what the reviewer
writes (`gate artifact`).

Waiting costs no turns. A goal-mode card whose judge says WAIT is parked in `scheduled`, with the
event it waits for recorded on its `scheduled` event (`wait_for`): another card done, a review
card's result, a PR's required checks finished, or a time. Each dispatcher tick checks that event
and unblocks the card once when it happens. A worker records the same by hand with
`hermes kanban schedule <id> --wait-for card:<id>|review:<id>|pr:<url>|until:<time>`, or
`hermes kanban block <id> --wait-for ...` (a `needs_input` block that returns to ready by
itself); a `needs_input` block without `--wait-for` waits for a person. A spent `max_turns`,
`max_runtime` or agent iteration budget is a planned stop: the run leaves a handoff comment and the
card goes back to ready without counting toward `max_retries`. After `kanban.planned_stop_limit`
(default 5) planned stops in a row the card is blocked `needs_input` for a person.

## Procedure

1. Read the goal and list every to-do: GitHub issues as `owner/repo#n`, other items as `[ ] text`.
2. Group to-dos into cards. One card holds the same code or data, or one dependency chain: about
   3–8 code to-dos, or 1–3 deliverables. Split a card that would touch two unrelated areas.
3. Put each to-do in exactly one card. When the goal is in the work record, set `ledger_goal` and
   give each card its record to-dos in `ledger_todos` (an existing `T<n>`, or a new to-do with
   `title`, `why`, `done`, `check` under the record's writing rules).
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
- The record side is checked before anything is written, too: `ledger_goal` must be an open record
  goal, every `T<n>` an open to-do under it and in one card only, every new to-do must pass
  `lina work add 할 일`, and `ledger_todos` needs `ledger_goal`.
- When the record step fails after the board was written, the command exits non-zero and names the
  step and the cards; run the same plan again to finish it (nothing is duplicated).
- A laid-out plan cannot gain or lose cards: a changed card list under the same `key` is refused.
  Use a new `key`, or add the card with `hermes kanban create` and `hermes kanban link`.
- Unknown fields are refused, so a typo like `gate:` instead of `gates:` fails loudly.

## Verification

- `hermes kanban plan <file> --dry-run` lists every card with its gates and every link.
- After the run, `hermes kanban show <goal id>` lists the work cards as parents and the goal card
  is `todo`.
