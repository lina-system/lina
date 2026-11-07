# Worker run lifecycle and capacity

An omo task owns commands in its whole task directory, including additional
worktrees. Process ancestry and session terminal records establish ownership;
sharing a working directory alone does not. Linux reads process records from
`/proc`. The portable collector reads `ps` and `lsof` output. Monitor process
groups come from the session terminal manifest, so watcher identification on
macOS does not depend on reading another process's environment.

Watcher shells and waiting utilities are not work when they belong to a command
monitor. A Python, Node, test or build command inside the same monitor still is
work. Live work keeps a run open even if the engine reports a completed turn.

A final assistant answer, a report written during this run and only monitor or
delivery receipts may finish after `worker.answered_grace_s`. A tool result,
failed answer, old report, open question, running child or real background job
does not qualify. Actual dialogs and person questions wait for
`worker.human_max_s` (default 10,800 seconds), independently of notification
activity. Synthetic `question:<run>` receipts do not count as open questions.

Cancel aborts the turn, reaps task-owned commands, unloads the session and
removes persistent command monitors from its terminal record. The original
record is backed up in the task folder; cleanup receipts record killed and
remaining processes. File/RPC watchers and unrelated terminal data are retained.
The shared daemon and unrelated processes are not signalled. Locally imposed
timeouts and stalls also clean up before freeing the run.

Runs launch in a separate systemd unit or process session. Restarting the worker
service does not stop them. Finished runs remove their PID record, including
the race where a fast run finishes before its launcher publishes that record.
Device probes expose `memory_max`; it is `null` without systemd, including macOS.
The detached fallback does not enforce a memory limit.

## Two capacity pools

```yaml
devices:
  workers:
    primary: {host: local, cap: 2, named_cap: 1}
    overflow: {host: overflow, cap: 2, named_cap: 2, last: 1}
    reserve: {host: reserve, cap: 1, named_cap: 1, last: 2}
```

`cap` counts bid work. `named_cap` counts tasks explicitly sent with `--host`;
when omitted, its number defaults to `cap`, but its occupancy is independent.
The two pools therefore allow up to `cap + named_cap` tasks. Reservations and
subsequent runs retain their pool. Probes expose total `running`, `bid_running`
and `named_running`. Increasing hub capacity changes only these settings.

Automatic bids first choose the lowest available integer `last` tier and then
use the existing load score. Absent/false is tier 0; true is tier 1. Tier 2 is
eligible only when lower tiers cannot accept the task. Explicit hosts ignore
tier ordering and `auto: false`, but still require readiness, disk, repository
access and a free named slot.

## Delivered files

Hashing and fetching use the same pruned traversal. Under `out/`, directories
`.scratch`, `.venv`, `venv`, `.pytest_cache`, `__pycache__`, `node_modules`,
`.mypy_cache`, `.ruff_cache`, `.tox` and `.git` are excluded before descent.
Symlinks are omitted. Regular files with one of those names remain deliverables.
`git.txt` includes branches, heads and recent commits of extra worktrees within
the task directory, including detached worktrees, but excludes outside trees.

## Run ends: three events, one delivery each

Every end the hub records is one of three events:

| Event | When | Also carries |
|---|---|---|
| `run.completed` | a run ended | `end_state`: `done`, `review`, `done_empty`, `stalled`, `failed` or `cancelled` |
| `question` | a run has waited on a person for 120 seconds | the question key |
| `host.unavailable` | a node cannot be reached | the node and the outage start |

The event is on the `exec_end` and `notified` receipts, in the job's `notified`
list, in the card's event payload and comment, and in the chat notice. Notices
and comments start with it, for example `run.completed (done) · …`, followed by
the usual words. Their last line is `delivery <id>`.

The delivery id is `<task>:<run>:<event>`. A question adds `:<question key>`
(the omo runner's question ids, else a hash of the question). An outage's id is
`<node>:host.unavailable:<outage start>`. A run's end moves through three
states on its job, each saved under the job's lock and never undone. It is
**held** (`deliveries_pending`: its kind, run and time), then **staged** (the
chat send returned, or on a Hermes card its `card_pending` entry is written),
then **done** (its id appended to `hub/deliveries.jsonl` and marked in
`notified`). One step of the hub makes every move. A held end that the send
failed on, or that a stopped hub left, is moved on by the next report or tick. A
staged end is never built or sent again, and a Hermes card's steps (comment,
attachment, wake) run only once the end is done, each step once. An id in
`deliveries.jsonl` is done. A repeat of it, whether from a device retrying its
report, the tick seeing the same end or a hub that restarted, is recorded as a
`delivery_duplicate` receipt and sent nowhere. A late report of a run older than
the job's current run only adds its marker to `notified`. It never changes the
job's state. Older job files whose `notified` entries read `<state>:<run>` or
`human:<run>` count as already delivered.

The tick keeps one record per node in `hub/nodes/<node>.json` (`failures`,
`down_since`, `notified_outage`). A tick on which no call to the node about its
running or `unknown` runs was answered is one failed contact. A call counts as
unanswered when it is unreachable, or when it reports a running run's task as
missing. On the third failed contact in a row the hub sends one
`host.unavailable` notice. It names the node, the outage start and each running
run with its card, and goes to each agent the runs belong to, with one comment
per card. Those runs become `unknown` without a notice of their own. Nothing more
is sent while the node stays down. The first answered contact afterwards sends
one line, `<node> is back after <duration>; runs <ids> continue`, and resets the
record. Both notices are queued on the node record first, one item for each
agent chat and each card they go to (`pending`). An item leaves the queue once
it has gone out. The following ticks retry the ones that failed and send none of
them twice. The outage's delivery id is done only once every place has been told.
A tick that finds no running or `unknown` run on a node, or a placement
published on it, resets its record too, so its next outage is told again. An
`unknown` run whose device then reports it running or ended goes back under
watch, so its end is delivered once like any other.
