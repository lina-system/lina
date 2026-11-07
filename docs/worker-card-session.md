# Handing work out from a card session

A work card's session does not run the work itself. It splits the work, hands it out with `lina worker`, judges
what comes back and talks to people. These are the only verbs it needs. The hub is a node too: a step only the hub
can do is handed to it the same way (`--host <the hub's local device>`).

The card's end of a run is written on the card, not in a chat. With `worker.card_link: {kind: hermes, command:
[hermes]}` in LINA's `config.yaml`, the card is a Hermes Agent card, and every write goes through that command
(`hermes kanban [--board <b>] comment | attach | show | unblock`). LINA never opens Hermes's kanban database.
Without `card_link`, cards are LINA's own board, as before.

From a card session, `submit`, `send` and `wait` find the card without flags: `HERMES_KANBAN_TASK` and
`HERMES_KANBAN_BOARD` of the calling process, else of its nearest ancestor process that has them (Linux
`/proc/<pid>/environ`, macOS `KERN_PROCARGS2`, up to 16 levels). Hermes removes them from a card session's
terminal, so the card's worker process above it is the usual source. `--card <id> --board <slug>` (`--card-board`
is the same as `--board`) takes precedence. The card is only read; every board write stays with the hub. A
delegated Hermes child with no card found is refused at submit, not silently sent to chat. The workspace directory
name is never used to infer a card ID.

The owner of a run is `--bot`, else `HERMES_PROFILE`, else `HERMES_PROFILE_NAME`, else the profile that the
caller's Hermes Agent `HERMES_HOME` names (`<root>/profiles/<name>`; the root itself is `default`), else LINA's
active profile. The submit line names the owner and its source; `accept` and `reject` record the same name as
`by`. `send` checks the card against the run's recorded card; it does not move a run to another card, and
`lina worker card <ID>` only shows it.

| Verb | What it does |
|---|---|
| `lina worker submit --brief <file> --card <id> --board <slug>` | Hands a brief to a free node (or the one named with `--host`). The hub's next report/tick writes the start comment on the named card. |
| `lina worker send <ID> "<message>" --card <id> --board <slug>` | Steers a running run, answers its question, or starts its next run in the same session. |
| `lina worker wait [<ID> ...] --card <id> --board <slug>` | Records the open runs to wait on and prints the model's next action: call Hermes `kanban_schedule` with the printed `workers` and `reason`, then end the turn. With no open run it refuses and records nothing. |
| `lina worker status <ID>` | Reads where a run is: its node, state, what the agent does now, delivery and verdict. |
| `lina worker result <ID>` | Fetches `report.md`, `final.md`, `out/` and `git.txt` to the hub and checks them against the node's manifest. |
| `lina worker accept <ID> --note "<what you checked>"` | Records that the result met the done conditions. |
| `lina worker reject <ID> --why "<what is missing>"` | Records that it did not. Follow with `send` (or a new `submit` if the work is lost). |

## What lands on the card

- The run's start: one comment by `lina-worker` with the run ID, runner, mode, model and branch. Submit records
  this as pending; the hub outside the card session writes it on the next report/tick, retrying failures.
- The run's end: one comment by `lina-worker`, the same short notice the chat used to get (state, where the result
  is, what to do), and `report.md` as an attachment (`report-<ID>-<run>.md`). A repeated end report adds nothing.
- After `wait`, call the Hermes model tool `kanban_schedule` with `workers=["<run id>", ...]` and
  `reason="waiting on lina worker <run ids>"`, then end the turn. `wait` does not call the scheduling CLI.
- On a run's end the hub reads `hermes kanban show <id> --json`. The card waits on the run when its last
  `scheduled` event names it, in `reason` (any wording: the ID as a whole token) or in `goal_park.workers`, with or
  without `wait`; else when `wait` recorded it. A schedule that names several runs is woken once, after the last of
  them ends; one that names a run this hub did not start is left to the tool that started it. A scheduled card is
  unblocked once; running/ready means the model has not parked yet, so the wake stays pending for the next tick.
  Other states (including done, archived and blocked) finish without a wake. The end comment is already on the card.
- The wake is `unblock --judge-wake <run id>` (the batch ID for an `OG-` batch), so the card lands `ready` to judge
  the run even behind an open parent; a Hermes without that option gets a bare `unblock` once (receipt `unblock: bare`).
- A run that waits on a person's answer writes one comment with the question, once per question (no attachment,
  no wake); answer it with `lina worker send <ID> "<answer>"`.
- No chat notice is sent for a run on a Hermes card. The agent's main session hears about it from the card's own end.

## A card ends only when its runs are judged

On LINA's own board a card is finished only when every run it started (each `worker_run` event on it) has a verdict:
`accept`, `reject` or `cancel`, as the hub's job record says. Finishing the card is not a verdict. `complete`,
`request_review` and a goal judge's "done" are refused while one has none, naming each run and its state; an operator
may close past it with `hermes kanban complete|request-review --allow-unjudged`, recorded on the card as
`unjudged_runs_allowed` (`hermes_cli/kanban_db_verdicts.py`).

- A run's end or question that reaches a card waiting in `todo` behind an unfinished parent, or `scheduled` on
  something else, moves it to `ready` with a `run_verdict_wake` event and a comment: the card is claimed past its
  open parent to judge the run. After the verdict its `kanban_complete` (or the next dispatcher tick, when the
  verdict came from elsewhere) puts it back where it waited, the same parent wait or the same schedule and
  `wait_for`. A schedule that names the run is met by its end and woken as usual.
- A run that ended over an hour ago without a verdict is raised once: a comment on its card, or, when the card is
  done, archived or deleted, the starting agent's main session through `worker.notice.command`. Never the owner.
  - A card counts as gone when it is done, archived or deleted: no card session reads a comment there any more.
- A pause (`scheduled`) whose reason names no run ID, card ID or time and that has no `wait_for` is raised once per
  pause to the main session of the agent that paused it, after 24 hours.
  - That agent is the profile of the run that made the pause, else the card's assignee.
- The board API lists a finished run waiting for a verdict with `waitingOn: agent:<name>` and counts it under that
  agent (`counts.agents`), never in the owner's `inbox.review`, `review` notices or `counts.inbox`; a card's detail
  lists its `runsAwaitingVerdict`: the agent's to-do, not the owner's.

## The owner's private files

Files under `worker.private_paths` are never copied to a node. A run that needs them goes to the hub node only (a
`host: local` device; `--host` may name it, and without `--host` the run is pinned to it) and reads them where they
are. Any other node is refused. A run placed on a `host: local` device also gets the text of `worker.hub_rules`
after the standard rules in its first prompt (unset or empty adds nothing).

## Where the state lives

The worker's state root is `LINA_HOME` when it is set, whatever `HERMES_HOME` says. A Hermes card session's
`HERMES_HOME` (Hermes Agent's home) and its `HERMES_KANBAN_*` never choose it.

## Setup

```yaml
worker:
  card_link:
    kind: hermes
    command: [hermes]   # or [env, HERMES_HOME=<Hermes's root>, hermes] when Hermes runs from a custom root
  todo_link:            # optional: the owner's to-do record, shaped like card_link
    kind: hermes
    command: [my-todo]  # `link <to-do> omo:<ID>` after submit --todo; `evidence` and `note` after accept
```

A failed to-do command prints one line and fails neither the submit nor the verdict.

Hub commands run without LINA's `HERMES_HOME`, `HERMES_PROFILE` and `HERMES_KANBAN_*` (Hermes would follow
LINA's home). The delegated-child marker is never removed or bypassed: card-session terminals cannot mutate
the board. Start/end comments, attachments and unblocks belong to the external hub; scheduling belongs to the
card session's model tool.
