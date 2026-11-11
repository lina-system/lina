# The goal drive

The hub keeps a goal moving between a run and the gates by itself. Routine moves (a red check, a run without its
deliverable, a lost node, a usage limit) are rules the hub runs in its tick, not turns in a work card's session. The
card's session is woken only when a rule cannot decide (lina#225).

Code: `hermes_cli/worker/goal_drive.py`, called once at the end of `Hub.tick()`. No service and no timer of its own.

## What it drives

Hub runs handed out from a card (`lina worker submit --card <id> --board <slug>`) whose card is a work card of a goal,
as `hermes kanban plan` lays it out: the card body opens with `## Goal` and the line `<goal card> — <goal title>`.

The hub never opens a Hermes kanban database. It reads a card with `hermes kanban --board <b> show <id> --json` and
writes only with `hermes kanban block` and `hermes kanban schedule` (and `hermes kanban unblock` to wake a card it
parked, see [Parking](#parking)), through `worker.card_link.command` with the same environment as the run's start and
end comments. A card on LINA's own board (`card_link: {kind: lina}`) goes through LINA's board API.

The board blocks only a `running` or `ready` card and schedules only a `todo`, `ready`, `running` or `blocked` one. A
card in another state first takes the board's own step to `ready`: `unblock` for a `scheduled` card, `promote` for a
`todo` card (both only when the board allows them). On LINA's board each step is its own board transaction, because
`block_task` and `schedule_task` open one that cannot nest. Every write is checked: a Hermes command that exits
non-zero, or a LINA call that returns false, is a refusal. A refusal is noted (`card_write_refused: <verb> on a
<status> card: <why>`) and tried again on the next tick, at most once per card and verb per tick, and the drive
records nothing that depended on it. A `todo` card whose parents are still open cannot be promoted, so a goal card
waiting on open work cards stays unblocked; the drive keeps it stopped for this tick and tries again on each tick.

The drive only drives the newest run of each to-do of a work card. It leaves a goal whose goal card is done or
archived.

Which goal a work card belongs to, and its title, come from the card as the hub read it at its last run end (the
`show` the end already makes). That copy is not the card's status: before the drive acts on a work card (a follow-up,
a resubmit or move, a block or a park) it reads the card's status from the board, once per card per tick (on LINA's
board through its API). A card that is `done` or `archived`, or `blocked` by someone else, is not acted on; the
ledger's `notes` say `skipped: card <status>`.

## Done stays ours

The drive never completes a card. A work card ends through its gates (`hermes kanban gate …`, the card's configured
gates) and then the judge, as before. A worker's report is evidence only, and a run that ended `failed` is never read
as done: the drive takes no action on it unless a usage limit stopped it.

## Rules

Each rule acts at most once per action id.

| Rule | When | What the drive does |
|---|---|---|
| `red-check` | The run ended (`done`, `done_empty`, `review`) and its branch has an open pull request with a red required check | One follow-up to the run naming the check, with the last 80 lines of its log. Required checks are read the way `hermes kanban gate prs` reads them. The same head is followed up once; a new push is needed for another. |
| `check-park` | Same, but the required checks are still running | Parks the work card on `pr:<repo>#<n>` (see [Parking](#parking)). |
| `deliverable` | The run ended `done`, `done_empty` or `review` with no pull request for its branch (a run with a repository), no `report.md` and no file in `out/` | One follow-up asking for it. |
| `block` | A follow-up is due and the attempt has had 3 rounds already, or the attempt already has more than 3 | Blocks the work card with kind `needs_input` and one inbox line, `<card title> · 갈림길 · <one line>` naming who sent the last round, and stops driving that card. |
| `node-park` | The run's node is lost and its outage is still open (no tick has had an answer from it) | Parks the work card on `until:<time>`, `node_watch.FAILED_CONTACTS` hub ticks (3 × 30 s) ahead: no `--wait-for` event names a node's return, and that is the soonest the next outage could be told. |
| `resubmit` | The run's node was lost, and once the device answers again the run ended there or never started | Submits the same brief on another allowed node, from the pushed branch. The node is chosen by automatic placement among the other devices, so an `auto: false` device takes no moved run, except the device the original run was handed out to with `--host`. When no node can take it, the ledger's `notes` say why for each device, and the drive tries again on the next tick. The branch: `origin/<branch>` when GitHub has the branch, else the repository's default. Not while another run of the same to-do is live. A new attempt. |
| `node-continued` | Same, but the device says the run went on | Nothing is moved; the ledger records it. |
| `usage-park`, `usage-resume` | The run ended `failed`, `stalled` or `done_empty` and its end text or stderr matches its runner's usage-limit pattern | Parks the work card on `until:<reset time>` (an ISO time after `reset`, or `reset in <n> <unit>`; else 1 h), then sends one message that continues the same task. Not a new attempt and not a follow-up. |
| `usage-move` | Same, with `worker.goal_limits.usage_move: true` and another node free | Submits the same brief on that node instead of waiting, chosen as for `resubmit`. Same attempt. |
| `check-wake`, `node-wake`, `usage-wake` | A card the drive parked with a plain `schedule` (see [Parking](#parking)), and its event happened | Unblocks the card once, with `--judge-wake <run>` and the event as the reason. |

### Parking

Run-end notifications leave the primary scheduled goal card asleep until the drive has chosen its next action.
Additional cards attached with `lina worker card` retain their normal run-end wake; the drive does not visit them.
A follow-up
or moved run retargets that wait to `WAIT_EVENT=omo-task:<task>/<run>`. The drive waits for that run's end and applies
its rules before releasing the card, so another follow-up does not briefly expose a ready card.

Hermes cannot schedule an already scheduled card. Instead of unblocking it first, the drive uses one
`kanban edit --body` write to replace its `## Goal drive resume` section while preserving the scheduled status.
That section contains the wait, action, and schedule generation, and tells a new session to reread the goal,
to-dos, run-end comments, reports, and worker result or pull request. LINA does not manufacture Hermes's
`goal_park` session record: actual same-session selection belongs to the external Hermes dispatcher.
A later manual schedule invalidates the body's wait, and the drive relinquishes it. After a manual unblock,
a successful new drive park clears that old ownership-release note.

The goal ledger is the wait owner: `waits[card].items[todo]` stores each to-do's event, worker, action and whether
the board write succeeded. Intent is saved before writing the board. The body and schedule marker identify the
park and give the next session context; they are not the source of the card's complete wait set. A worker wait
for one to-do cannot replace another to-do's check wait. All waits must settle and all drive rules must run before
`release_ended` wakes the card. Run-end delivery consults this same ledger instead of interpreting the last body
marker independently.

At the start of each tick, committed usage-resume request-key receipts recover a missing park even when the job
already holds the next run. Pending board writes are retried before evaluating wakes. Recovery never sends the
resume message again. Old single-wait ledger records are imported on their first owned-park read.

Which form a park takes depends on the board:

| Board | Park | Woken by |
|---|---|---|
| LINA's own board (`card_link: {kind: lina}`) | `schedule_task` with the `wait_for` event | LINA's kanban dispatcher |
| Hermes, with or without native `--wait-for` support | `hermes kanban schedule <card> -- <reason> · WAIT_EVENT=<event> GOAL_ACTION=<action id>` | the hub's own tick |

All new Hermes drive parks omit native `--wait-for`: the CLI has no option to atomically replace or clear a native
scheduled-event payload while keeping the card scheduled. A body edit cannot change that payload. Plain schedule
records no native wait in the first place, so an expired timer or settled PR cannot wake a later worker run.
The ledger's `waits` keeps the marker event and action. A preexisting native wait is not silently converted: the
drive refuses further work and records that an operator must unblock it first. LINA does not alter the external
board database or use a non-atomic unblock/schedule sequence to migrate it.

Retargeted scheduled Hermes cards also use the hub-owned wait, regardless of `--wait-for` support. A usage reset
continues the worker and replaces the timer with its new run wait; it does not wake the card session first.

Such a card is managed by the hub, because the installed Hermes wakes a scheduled card only on the worker ids its reason
names. On each tick the drive reads the card (`show --json`). Settled red checks go through the follow-up rule;
settled non-red checks wake the card (read as LINA's `pr:` wait reads them, kept 60 s like every check read).
Timers go through the usage or node rule before the next wait. The check wake is
`hermes kanban unblock --judge-wake <run> --reason "goal drive: wait met: <event>"` (a bare
`unblock --reason` on a Hermes without `--judge-wake`), once per park (`<rule>-wake`). The drive wakes only a card that
is still `scheduled` and whose latest `scheduled` reason carries a `GOAL_ACTION` that is in its ledger: a card a
person or another tool unblocked or parked again is let go and dropped from `waits`.

### Follow-up rounds

The limit of 3 counts every follow-up round of one attempt (one hub task), whoever sends it: the drive's own
follow-ups and the card session's (`lina worker send`, for example a review round with Codex
findings on the pull request). The drive reads them from the hub, not from its ledger: each run of the task after its
first one, plus each follow-up queued into a run that had not started yet (the hub's `follow_up` receipts). A steer
into a live run is not a round, and neither is the drive's own continue after a usage limit.

Three rounds are allowed. When a fourth is needed, the drive blocks the work card instead of sending it. The drive
cannot stop a fourth round the card session sends itself; on its next tick it sees four rounds and blocks the card
with the same line. The line names who sent the last round (`the goal drive` or `the card session`), taken from the
request key on that round's `follow_up` receipt.

"Node lost" is one function, `goal_drive.node_lost(job, store)`, on lina#223's outage record
(`hub/nodes/<node>.json`, read through `node_watch.outage_of` and `node_watch.outage_open`): the run is `unknown`
because its node failed `node_watch.FAILED_CONTACTS` (3) contacts in a row and `host.unavailable` was sent for it, and
that outage names the run (the node record's `runs`, or the outage's delivery id on the job). One or two unreachable
ticks are not a lost node, and the drive does nothing for them. When the node answers again, the tick puts the run
back under watch (`running`, then its end if it has one); the drive then moves it only when it did not continue, and
records the outage as settled for that task (`outages` in the ledger).

GitHub is read at most once per run per tick, and each pull request and check read is kept 60 s.

The usage-limit pattern is a runner's `usage_limit` in the work settings; without it the drive matches the 429,
"usage limit", "rate limit", "too many requests" and "quota exceeded" wording of OCX, Anthropic and OpenAI runners.

## Action ids and the ledger

Every action has the id `<goal card>:<work card>:<hub task>:<run>:<rule>`, with characters outside letters, digits,
`.` and `-` written as `-` (`t_goal` becomes `t-goal`). `<hub task>` is the run's hub task ID (`OT-…`). It is unique
per to-do, per attempt and per move, and it stays the same across a hub restart, so it alone tells two to-dos of one
card apart, and a moved task from the task it was moved from (each move is its own task with its own ids). An id that
would be longer than 128 characters writes `<hub task>` as the first 12 hex digits of its sha256. A goal limit's block
uses `goal` as its task and `0` as its run. The id matches `request_keys.CALLER_KEY`, and every follow-up and
submit carries it as its request key, so a hub restarted in the middle of a pass repeats nothing: the device writes
are replayed, not run again.

The ledger is `$LINA_HOME/worker/hub/goals/<goal card>.json`, written under the hub lock `goal-<goal card>`:

| Key | Holds |
|---|---|
| `attempts` | Attempt number per hub task. A new task of a to-do is the next attempt, except one the drive moved for a usage limit. |
| `followups` | Follow-ups the drive itself sent per task. The limit does not read it: it counts every round from the hub. |
| `runs` | Runs per task, read from the hub's records at each pass and after each send (informational: the limits count from the hub). An older ledger's held runs and its `workers` field are dropped on load. |
| `started_at` | The goal's start: its first task's hand-out time. |
| `actions` | Action id → time and result. |
| `usage`, `moved`, `blocked`, `red_heads`, `github` | Usage waits, moved tasks, cards the drive blocked, followed-up heads, cached GitHub reads. |
| `outages` | Per task, the outage (its delivery id) the drive settled: the run went on, or it was moved. |
| `waits` | Per card the drive parked with a plain `schedule`, the park's action id and the run it wakes the card to judge. |
| `notes` | Why the drive left a card (`skipped: card <status>`), a write the board refused (`card_write_refused: …`), or a run (`not moved: …`, `deferred: …`). |
| `stopped` | Set when a limit stopped the drive for this goal. |

A `block` the drive made just before the hub stopped is not made again: the drive reads the card first and leaves a
blocked card as it is. `stopped` and `blocked` are written only after the board took the block; a park only after it
took the `schedule`.

The run budget and the live workers are never kept as state of their own. Each goal pass reads them from the hub's
records: the runs used are the `runs` of every placed task of the goal, and the live workers are its tasks that have
not ended, both as the hub holds them at that moment (plus tasks the drive moved earlier in the same pass). Before
each follow-up that starts a run, each resubmit or move, and each continue after a usage limit, the drive holds one run
and one worker for that action, in memory, for this pass only and under the goal lock, and checks `runs` and
`workers` against the hub's count plus what it holds. The hold is given back as soon as the send is made (the hub's
records count the run and the task from then on) or fails. Two sends in one pass can therefore never pass a limit
together, and a hub that stops in the middle of a pass leaves nothing held: the next pass counts from the hub again.

A full `runs` stops the goal. A full `workers` defers the send to a later tick: it is not made, and the ledger's
`notes` say `deferred: the workers limit (<n>) is reached (<live> live)`. A deferred send does not stop the goal; it
goes out on the first tick with a free worker.

A hub that stops after a send but before the drive saved its ledger is caught by the send's request key: when the
drive meets that action again and its key record (`hub/keys/`) says the send went through, the action is recorded as
done from the key record (`recovered from its request key: …`) and is not sent again.

## Limits

Per goal, in the work settings under `worker.goal_limits`:

| Key | Default | Reached when |
|---|---|---|
| `workers` | 3 | more live runs than this at once (the card session started them); a send of the drive's own that would pass it waits instead |
| `attempts` | 3 | a to-do has more attempts than this, or a move would start one more |
| `runs` | 40 | more runs than this, or the next run (a follow-up, a move or a continue) would pass it |
| `wall_h` | 24 | this many hours since the goal's start |
| `usage_move` | `false` | (not a limit) move a usage-limited run to another node instead of waiting |

Reaching a limit blocks the goal card (`needs_input`) with the limit and the numbers, for example
`Ship it · 갈림길 · the goal drive stopped at its runs limit (40): the next run would be run 41; raise
worker.goal_limits.runs or end the goal?`, and stops the drive for that goal once the board took the block. It never
stops silently: while the board refuses the block, the drive takes no other action that tick and tries the block again
on the next one.

```sh
lina worker settings set worker.goal_limits.runs 60
lina worker settings set worker.goal_limits.usage_move true
lina worker settings set worker.runners.codex.usage_limit '(?i)usage limit|429'
```

## Seen on the real setup

Two runs on a hub with five nodes, a private practice repository whose base branch requires one status check, and
cards laid out as `hermes kanban plan` lays them out (`hermes kanban create`, because the installed Hermes has no
`plan`). Nobody touched the cards after they were released.

| Run | What happened | What the drive did |
|---|---|---|
| 1, card A | The first pull request failed the required check. | One `red-check` follow-up into the same run; the run fixed it and the head went green. |
| 1, card B | The node was stopped for 150 s (its omo process killed, the hub's traffic to it dropped). | `host.unavailable`, `node-park`, then `resubmit` to another node from the pushed branch (attempt 2). |
| 2, one card | The check stayed pending after the run ended. | `check-park`, then `check-wake` when the check settled; the card's session made no call in between. |

Where a session still started around a park:

- A park the drive makes carries no `goal_park` record, so the woken card starts a fresh session instead of resuming the
  one that was working on it. A park the card makes itself (`kanban_schedule` with a worker ID) resumes that session.
- A run's end wakes every card waiting on it. A `red-check` follow-up or a `resubmit` does not park the card, so the
  dispatcher can start a session before the next drive pass parks the card again. A `check-park` that came before the
  dispatcher's next claim started none.

Both are tracked in lina#461. A wait on a review was not part of these runs.

## Switching it off

```sh
lina worker settings set worker.goal_drive false
```

The drive then takes no action on any goal; runs and cards go on as without it. `true` (or removing the key) switches
it back on.
