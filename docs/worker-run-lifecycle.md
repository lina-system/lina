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
