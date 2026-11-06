# Switching callers to `lina worker` and `lina work`

Agents, skills and cron jobs that call `omo-task` or `work` keep working after the switch: two
shims with the same names forward every verb and option to `lina worker` and `lina work`. The
switch of a machine is a config change plus one command; going back is taking one folder off PATH.

| Old call | Runs | Notes |
| --- | --- | --- |
| `omo-task <verb> ...` | `lina worker <verb> ...` | `submit --brief --mode --repo --input --todo --host --model --topic --bot --at --every --wait --card --card-board`, `submit --session` (accepted, ignored), `status`, `attach` (prints the status), `card` (shows the card; adding or dropping one is refused), `list`, `send --card --card-board`, `cancel`, `result`, `accept --note`, `reject --why`, `receipts`, `hosts`, `schedules`, `unschedule` |
| `omo-task fork <ID>` | nothing (exit 2) | `lina worker` has no fork: submit a new brief, or `send` for the next run in the same session |
| `work <verb> ...` | `lina work <verb> ...` | every verb and option; task IDs imported with `lina work import` keep their IDs |

A shim prints nothing of its own on success and exits with the wrapped command's code, so callers
that parse the output see the same text.

An installed shim always runs with `LINA_HOME` set to the root it was installed for. A caller's
`HERMES_HOME` (Hermes Agent's profile home, as in an agent's terminal) never moves it; it names the
owner of a run when `--bot`, `HERMES_PROFILE` and `HERMES_PROFILE_NAME` do not. From a card session
the card is found without `--card` (docs/worker-card-session.md).

## Who answers an ID

`lina worker` keeps a record of every task it started (`worker/hub/jobs/<ID>.json` under the LINA
home). For a verb that names a task (`status`, `attach`, `card`, `send`, `cancel`, `result`,
`accept`, `reject`, `receipts`, `unschedule`, `fork`), the `omo-task` shim sends an ID that
`lina worker` started to `lina worker`, and any other ID to the old tool named in
`worker.legacy_omo_task`. One call goes to one tool, never both. Without `worker.legacy_omo_task`
every ID goes to `lina worker`. `list` prints the tasks `lina worker` started and then, when
`worker.legacy_omo_task` is set, the old tool's `list` output (same options).

## Checklist

1. **Devices.** Every work device is in `devices.workers` and ready:

   ```sh
   lina devices add <name> --host <ssh alias> --cap <n>   # or --host local for this machine
   lina devices list                                      # ssh, pinned omo, free disk, cap
   lina devices check <name> --run-smoke                  # one tiny real task: done + its run ID
   ```

2. **Config** (the LINA home's `config.yaml`):

   ```yaml
   devices:
     workers:
       <name>: {host: <ssh alias>, cap: 2}
   worker:
     hub: {host: <ssh alias of the hub>, lina: lina}
     legacy_omo_task: /path/to/the/old/omo-task   # IDs started before the switch go there
   desk:
     path: /path/to/the/Desk                       # default profile only (docs/desk.md)
   ```

   Move the work record once with `lina work import --from <work.db|ledger.jsonl>`.

3. **Shims.** `lina install --shims` writes `omo-task` and `work` into `$LINA_HOME/bin/`. It installs
   nothing else; re-run it after moving the LINA checkout or its Python.

4. **PATH.** Put `$LINA_HOME/bin` before the folder of the old tools, for the account the agents run
   under and for its services (launchd `EnvironmentVariables`, systemd `Environment=`, cron lines):

   ```sh
   export PATH="$LINA_HOME/bin:$PATH"
   command -v omo-task work        # both must print $LINA_HOME/bin/...
   ```

5. **Cron jobs and skills on the Desk.** A cron job's `workdir: desk` or `workdir: desk:<sub path>`
   follows the `desk.path` setting, so it keeps working when the Desk moves, a synced folder
   included. LINA's launchd jobs set `MaterializeDatalessFiles`, so a not yet downloaded file opens
   instead of failing the job; nothing is needed on Linux.

6. **Check one agent end to end.** From that agent's shell (same user, same PATH):

   ```sh
   omo-task submit --brief "$TMPDIR/brief.md" --topic switch-check --wait 1800
   omo-task status <ID>            # same text as `lina worker status <ID>`
   omo-task result <ID>
   omo-task status <an ID from before the switch>   # answered by the old tool
   work list                       # same text as `lina work list`
   ```

   Then let the agent run one real to-do through its usual skill and read its end notice.

## Going back

Take `$LINA_HOME/bin` off PATH (and out of the service definitions), so `omo-task` and `work`
resolve to the old tools again. Nothing else changes: the old tools were left in place, and the
tasks `lina worker` started stay readable with `lina worker status <ID>`.
