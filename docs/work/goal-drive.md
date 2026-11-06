# The goal drive

The hub keeps a goal moving between a run and the gates by itself. Routine moves (a red check, a run without its
deliverable, a lost node, a usage limit) are rules the hub runs in its tick, not turns in a work card's session. The
card's session is woken only when a rule cannot decide (lina#225).

Code: `hermes_cli/worker/goal_drive.py`, called once at the end of `Hub.tick()`. No service and no timer of its own.

## What it drives

Hub runs handed out from a card (`lina worker submit --card <id> --board <slug>`) whose card is a work card of a goal,
as `hermes kanban plan` lays it out: the card body opens with `## Goal` and the line `<goal card> — <goal title>`.

The hub never opens a Hermes kanban database. It reads a card with `hermes kanban --board <b> show <id> --json` and
writes only with `hermes kanban block` and `hermes kanban schedule`, through `worker.card_link.command` with the same
environment as the run's start and end comments. A card on LINA's own board (`card_link: {kind: lina}`) goes through
LINA's board API.

The drive only drives the newest run of each to-do of a work card. It leaves a goal whose goal card is done or
archived, and a work card that is done or archived.

## Done stays ours

The drive never completes a card. A work card ends through its gates (`hermes kanban gate …`, the card's configured
gates) and then the judge, as before. A worker's report is evidence only, and a run that ended `failed` is never read
as done: the drive takes no action on it unless a usage limit stopped it.

## Rules

Each rule acts at most once per action id.

| Rule | When | What the drive does |
|---|---|---|
| `red-check` | The run ended (`done`, `done_empty`, `review`) and its branch has an open pull request with a red required check | One follow-up to the run naming the check, with the last 80 lines of its log. Required checks are read the way `hermes kanban gate prs` reads them. The same head is followed up once; a new push is needed for another. |
| `check-park` | Same, but the required checks are still running | Parks the work card with `schedule --wait-for pr:<repo>#<n>`. |
| `deliverable` | The run ended `done`, `done_empty` or `review` with no pull request for its branch (a run with a repository), no `report.md` and no file in `out/` | One follow-up asking for it. |
| `block` | A follow-up is due and the attempt has had 3 rounds already, or the attempt already has more than 3 | Blocks the work card with kind `needs_input` and one inbox line, `<card title> · 갈림길 · <one line>` naming who sent the last round, and stops driving that card. |
| `node-park` | The run's node is lost and its device is still unreachable | Parks the work card with `schedule --wait-for until:<time>` (`worker.unknown_after_s` ahead). |
| `resubmit` | The run's node was lost, and once the device answers again the run ended there or never started | Submits the same brief on another allowed node (the bid winner among the other devices), from the pushed branch: `origin/<branch>` when GitHub has the branch, else the repository's default. Not while another run of the same to-do is live. A new attempt. |
| `node-continued` | Same, but the device says the run went on | Nothing is moved; the ledger records it. |
| `usage-park`, `usage-resume` | The run ended `failed`, `stalled` or `done_empty` and its end text or stderr matches its runner's usage-limit pattern | Parks the work card until the reset time (an ISO time after `reset`, or `reset in <n> <unit>`; else 1 h), then sends one message that continues the same task. Not a new attempt and not a follow-up. |
| `usage-move` | Same, with `worker.goal_limits.usage_move: true` and another node free | Submits the same brief on that node instead of waiting. Same attempt. |

### Follow-up rounds

The limit of 3 counts every follow-up round of one attempt (one hub task), whoever sends it: the drive's own
follow-ups and the card session's (`lina worker send`, `omo-task send`, for example a review round with Codex
findings on the pull request). The drive reads them from the hub, not from its ledger: each run of the task after its
first one, plus each follow-up queued into a run that had not started yet (the hub's `follow_up` receipts). A steer
into a live run is not a round, and neither is the drive's own continue after a usage limit.

Three rounds are allowed. When a fourth is needed, the drive blocks the work card instead of sending it. The drive
cannot stop a fourth round the card session sends itself; on its next tick it sees four rounds and blocks the card
with the same line. The line names who sent the last round (`the goal drive` or `the card session`), taken from the
request key on that round's `follow_up` receipt.

"Node lost" is one function, `goal_drive.node_lost(job)`. Today it is the hub's `unknown` end: the device stayed
unreachable for `worker.unknown_after_s`. lina#223 switches that function to its `host.unavailable` event.

GitHub is read at most once per run per tick, and each pull request and check read is kept 60 s.

The usage-limit pattern is a runner's `usage_limit` in the work settings; without it the drive matches the 429,
"usage limit", "rate limit", "too many requests" and "quota exceeded" wording of OCX, Anthropic and OpenAI runners.

## Action ids and the ledger

Every action has the id `<goal card>:<work card>:<attempt>:<run>:<rule>`, with characters outside letters, digits,
`.` and `-` written as `-` (`t_goal` becomes `t-goal`). It matches `request_keys.CALLER_KEY`, and every follow-up and
submit carries it as its request key, so a hub restarted in the middle of a pass repeats nothing: the device writes
are replayed, not run again.

The ledger is `$LINA_HOME/worker/hub/goals/<goal card>.json`, written under the hub lock `goal-<goal card>`:

| Key | Holds |
|---|---|
| `attempts` | Attempt number per hub task. A new task of a to-do is the next attempt, except one the drive moved for a usage limit. |
| `followups` | Follow-ups the drive itself sent per task. The limit does not read it: it counts every round from the hub. |
| `runs` | Runs per task. |
| `started_at` | The goal's start: its first task's hand-out time. |
| `actions` | Action id → time and result. |
| `usage`, `moved`, `blocked`, `red_heads`, `github` | Usage waits, moved tasks, cards the drive blocked, followed-up heads, cached GitHub reads. |
| `stopped` | Set when a limit stopped the drive for this goal. |

A `block` the drive made just before the hub stopped is not made again: the drive reads the card first and leaves a
blocked card as it is. A `schedule` the board refuses is recorded with its error and not tried again (the card's own
session still parks itself on its runs).

## Limits

Per goal, in the work settings under `worker.goal_limits`:

| Key | Default | Reached when |
|---|---|---|
| `workers` | 3 | more live runs than this at once |
| `attempts` | 3 | a to-do has more attempts than this, or a move would start one more |
| `runs` | 40 | more runs than this, or a follow-up would start one more |
| `wall_h` | 24 | this many hours since the goal's start |
| `usage_move` | `false` | (not a limit) move a usage-limited run to another node instead of waiting |

Reaching a limit blocks the goal card (`needs_input`) with the limit and the numbers, for example
`Ship it · 갈림길 · the goal drive stopped at its runs limit (40): a follow-up would start run 41; raise
worker.goal_limits.runs or end the goal?`, and stops the drive for that goal. It never stops silently.

```sh
lina worker settings set worker.goal_limits.runs 60
lina worker settings set worker.goal_limits.usage_move true
lina worker settings set worker.runners.codex.usage_limit '(?i)usage limit|429'
```

## Switching it off

```sh
lina worker settings set worker.goal_drive false
```

The drive then takes no action on any goal; runs and cards go on as without it. `true` (or removing the key) switches
it back on.
