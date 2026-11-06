# Handing work out from a card session

A work card's session does not run the work itself. It splits the work, hands it out with `lina worker`, judges
what comes back and talks to people. These are the only verbs it needs. The hub is a node too: a step only the hub
can do is handed to it the same way (`--host <the hub's local device>`).

The card's end of a run is written on the card, not in a chat. With `worker.card_link: {kind: hermes, command:
[hermes]}` in LINA's `config.yaml`, the card is a Hermes Agent card, and every write goes through that command
(`hermes kanban [--board <b>] comment | attach | schedule | unblock`). LINA never opens Hermes's kanban database.
Without `card_link`, cards are LINA's own board, as before.

| Verb | What it does |
|---|---|
| `lina worker submit --brief <file> [--card <id> --board <slug>]` | Hands a brief to a free node (or the one named with `--host`). The card is the card session's own (`HERMES_KANBAN_TASK`, `HERMES_KANBAN_BOARD`) unless `--card`/`--board` name another. The run's start is a comment on the card. |
| `lina worker send <ID> "<message>"` | Steers a running run, answers its question, or starts its next run in the same session. |
| `lina worker wait [<ID> ...]` | Parks the card (`kanban schedule`) on the card's open runs. End the turn right after it: waiting costs no turns. The first run end that follows wakes the card (`kanban unblock`) once. With no open run on the card it refuses and parks nothing. |
| `lina worker status <ID>` | Reads where a run is: its node, state, what the agent does now, delivery and verdict. |
| `lina worker result <ID>` | Fetches `report.md`, `final.md`, `out/` and `git.txt` to the hub and checks them against the node's manifest. |
| `lina worker accept <ID> --note "<what you checked>"` | Records that the result met the done conditions. |
| `lina worker reject <ID> --why "<what is missing>"` | Records that it did not. Follow with `send` (or a new `submit` if the work is lost). |

## What lands on the card

- The run's start: one comment by `lina-worker` with the run ID, runner, mode, model and branch.
- The run's end: one comment by `lina-worker`, the same short notice the chat used to get (state, where the result
  is, what to do), and `report.md` as an attachment (`report-<ID>-<run>.md`). A repeated end report adds nothing.
- If the card was parked with `wait`, the end wakes it once. A card that is running gets the comment only, and its
  session sees it during the turn (Hermes folds new comments of other authors into a running card's turn).
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

The command runs without LINA's `HERMES_HOME`, `HERMES_PROFILE` and `HERMES_KANBAN_*` (Hermes would follow
LINA's home, and `kanban unblock` refuses inside a card session). `wait` runs `kanban schedule` with the card
session's own environment, so Hermes ends the card's current run itself.
