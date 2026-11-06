# Handing work out from a card session

A work card's session does not run the work itself. It splits the work, hands it out with `lina worker`, judges
what comes back and talks to people. These are the only verbs it needs. The hub is a node too: a step only the hub
can do is handed to it the same way (`--host <the hub's local device>`).

The card's end of a run is written on the card, not in a chat. With `worker.card_link: {kind: hermes, command:
[hermes]}` in LINA's `config.yaml`, the card is a Hermes Agent card, and every write goes through that command
(`hermes kanban [--board <b>] comment | attach | show | unblock`). LINA never opens Hermes's kanban database.
Without `card_link`, cards are LINA's own board, as before.

Always pass `--card <id> --board <slug>` to `submit`, `send` and `wait` from a card session: Hermes removes
the card ID from its child-process environment. Explicit flags take precedence; `HERMES_KANBAN_TASK` and
`HERMES_KANBAN_BOARD` remain fallbacks. A delegated Hermes child without a card is refused at submit, not
silently sent to chat. The workspace directory name is never used to infer a card ID.

With an explicit Hermes card and no `--bot`, submit uses `HERMES_PROFILE`, then `HERMES_PROFILE_NAME`, when
present; otherwise it keeps the current profile selection. The submit line names the owner and its source.
`send` checks the named card against the run's recorded card; it does not move a run to another card.

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
- The hub reads `hermes kanban show <id> --json` before waking a marked card. A scheduled card is unblocked once;
  running/ready means the model has not parked yet, so the wake stays pending for the next tick. Other states
  (including done, archived and blocked) finish without a wake. The end comment is already on the card.
- No chat notice is sent for a run on a Hermes card. The agent's main session hears about it from the card's own end.

## The owner's private files

Files under `worker.private_paths` are never copied to a node. A run that needs them goes to the hub node only (a
`host: local` device; `--host` may name it, and without `--host` the run is pinned to it) and reads them where they
are. Any other node is refused.

## Where the state lives

The worker's state root is `LINA_HOME` when it is set, whatever `HERMES_HOME` says. A Hermes card session's
`HERMES_HOME` (Hermes Agent's home) and its `HERMES_KANBAN_*` never choose it.

## Setup

```yaml
worker:
  card_link:
    kind: hermes
    command: [hermes]   # or [env, HERMES_HOME=<Hermes's root>, hermes] when Hermes runs from a custom root
```

Hub commands run without LINA's `HERMES_HOME`, `HERMES_PROFILE` and `HERMES_KANBAN_*` (Hermes would follow
LINA's home). The delegated-child marker is never removed or bypassed: card-session terminals cannot mutate
the board. Start/end comments, attachments and unblocks belong to the external hub; scheduling belongs to the
card session's model tool.
