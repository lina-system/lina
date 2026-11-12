# Switching callers to `lina worker` and `lina work`

The switch is finished: agents, skills and cron jobs call the worker as `lina worker <verb> ...`. There is no
other name for it and no shim (lina#434); `omo` is only the name of a runner (`worker.runners`).

| Call | Notes |
| --- | --- |
| `lina worker <verb> ...` | `submit --brief --mode --repo --input --todo --host --model --topic --bot --at --every --wait --card --card-board`, `status`, `attach` (prints the status), `card`, `list`, `send --card --card-board`, `cancel`, `result`, `accept --note`, `reject --why`, `receipts`, `hosts`, `schedules`, `unschedule` |
| `work <verb> ...` | the `work` shim: runs `lina work <verb> ...` with every verb and option; task IDs imported with `lina work import` keep their IDs |

`lina worker` has no `fork`: submit a new brief, or `send` for the next run in the same session.

Called from a Hermes Agent terminal, where `HERMES_HOME` is that agent's profile home (`~/.hermes/profiles/<bot>`),
`lina` drops that home without a word and keeps it as `LINA_CALLER_HERMES_HOME`: it names the owner of a run when
`--bot`, `HERMES_PROFILE` and `HERMES_PROFILE_NAME` do not. From a card session the card is found without `--card`
(docs/worker-card-session.md).

## The `work` shim

`lina install --shims` writes `work` into `$LINA_HOME/bin/`. It installs nothing else; re-run it after moving the
LINA checkout or its Python. It also removes an `omo-task` shim an older `lina install --shims` wrote there (it
says so in one line); a file of that name it did not write is left alone and named.

The shim prints nothing of its own on success and exits with `lina work`'s code, so callers that parse the output
see the same text. It does not depend on the caller's current directory: Python starts with `-P` and imports LINA
through its pinned `PYTHONPATH`, and it always runs with `LINA_HOME` set to the root it was installed for.

## Runs the old tool started before the switch

`worker.legacy_omo_task` names the old tool's path for runs it started before the switch. Nothing calls it for a
caller any more; the hub only asks it (`<legacy> status --json <ID>`) whether such a run has ended, so a card
parked on both kinds of runs wakes after the later end (docs/worker-card-session.md).

```yaml
worker:
  legacy_omo_task: /path/to/the/old/tool   # only for runs started before the switch
```

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
   desk:
     path: /path/to/the/Desk                       # default profile only (docs/desk.md)
   ```

   Move the work record once with `lina work import --from <work.db|ledger.jsonl>`.

3. **Shim and PATH.** Run `lina install --shims` and put `$LINA_HOME/bin` before the folder of the old `work`, for
   the account the agents run under and for its services (launchd `EnvironmentVariables`, systemd `Environment=`,
   cron lines):

   ```sh
   export PATH="$LINA_HOME/bin:$PATH"
   command -v lina work           # lina, and $LINA_HOME/bin/work
   ```

4. **Cron jobs and skills on the Desk.** A cron job's `workdir: desk` or `workdir: desk:<sub path>`
   follows the `desk.path` setting, so it keeps working when the Desk moves, a synced folder
   included. LINA's launchd jobs set `MaterializeDatalessFiles`, so a not yet downloaded file opens
   instead of failing the job; nothing is needed on Linux.

5. **Check one agent end to end.** From that agent's shell (same user, same PATH):

   ```sh
   lina worker submit --brief "$TMPDIR/brief.md" --topic switch-check --wait 1800
   lina worker status <ID>
   lina worker result <ID>
   work list                       # same text as `lina work list`
   ```

   Then let the agent run one real to-do through its usual skill and read its end notice.

## Going back for `work`

Take `$LINA_HOME/bin` off PATH (and out of the service definitions), so `work` resolves to the old tool again.
The tasks `lina worker` started stay readable with `lina worker status <ID>`.

### Going back after the record moved (lina#480)

Once the live record lives in the store, `work` and the 15-minute check have two sides to put back, in this order:

1. Stop the new check timer first (`systemctl --user stop lina-work-check.timer`): two reminder paths must not run
   together.
2. Take what the store gained since the move out of it: `lina work export-jsonl --to <file>`. Within an hour of the
   move, `lina store restore <backup name from the move>` is the other way; it drops everything newer.
3. Point `work` (and `work-probe`) at the old tool again, and `board.hermes.work` in the LINA config if it was moved.
4. Thaw the old record (`<old tool> freeze` was run at the move): `<old tool> thaw`, then write the exported lines
   back with the old tool.
5. Enable the old check timer (`systemctl --user enable --now <old check timer>`). Its files and the old record are
   kept for this.
