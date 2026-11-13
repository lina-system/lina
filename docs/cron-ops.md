# `lina cron ops`

`lina cron ops` installs the operations jobs as script-only cron jobs of the current profile (run it on the hub).
Each job is a script under `<HERMES_HOME>/scripts/lina-<name>.py` with `no_agent` (the job itself calls no model)
and `deliver: local` (the output is saved in the job's run, not sent). Running it again updates the jobs in place;
`lina cron ops --remove` deletes the jobs and their scripts.

| Job | Schedule | What it runs |
|---|---|---|
| `learned-collect` | `5 * * * *` | `lina learned collect` |
| `learned-card` | `5 11 * * *` | `lina learned card --day yesterday --send` |
| `agent-review` | `40 23 * * *` | one daily review turn per agent, started through that agent's gateway |
| `undo-snapshot` | `50 23 * * *` | `lina undo snapshot` |

Change a time with `lina cron edit <job_id> --schedule '<cron expression>'`; `lina cron list` shows the ids.

## Trying a job by hand

The `agent-review` and `undo-snapshot` scripts take `--dry-run`, which prints what the job would do and changes
nothing:

```bash
python <HERMES_HOME>/scripts/lina-agent-review.py --dry-run
python <HERMES_HOME>/scripts/lina-undo-snapshot.py --dry-run   # same as: lina undo snapshot --dry-run
```

Use the interpreter the cron worker uses (the one `lina` runs on).

## Daily agent review

The job starts one turn per agent the default gateway serves that keeps a heart journal (`heart/` in its home) or
a self note (`SOUL.self.md`). Each turn goes to that agent's gateway: `POST /p/<agent>/v1/runs` on the default
profile's api_server (`/v1/runs` for the default profile), with the agent's own `API_SERVER_KEY` from its `.env`.
The port is `platforms.api_server.extra.port` of the default profile, then `API_SERVER_PORT` in its `.env`, then 8642.
The turn runs in the gateway; the job only starts it and prints the run id. The turn's message asks the agent to
run `lina heart review-input --date <today>` and follow the bot-heart skill's daily review
(`templates/heart-review.md`), which applies self-note changes through `lina heart self-apply`.

An agent without a key, or a gateway that does not answer, is reported on stderr and the job exits 1; the other
agents still get their turn. Every start sends `Idempotency-Key: lina-daily-review-<agent>-<date>`, so a second
start on the same day (a rerun, `lina cron run`) replays that day's run instead of reviewing the agent again.

## Daily undo snapshot

`lina undo snapshot` covers, per agent profile, `memories/MEMORY.md`, `memories/USER.md` and `SOUL.md`, plus the
files matched by `undo.rules` in the LINA root `config.yaml` (journaled under the bot name `lina-rules`).
Upstream checkpoints do not cover these files: a checkpoint is taken only before an agent's own `write_file` or
`patch` call in its working directory, only with `checkpoints.enabled`, and never for memory-tool writes, curator
runs or hand edits.

The snapshot keeps the last copy of each file (an upstream checkpoint in the undo store, recorded in
`undo/snapshot.json`). Each run journals the files changed since that copy as one revert entry per agent, then
records the new copies; a file's first run only records its copy. `lina undo last --bot <agent>` shows the entry
and `lina undo apply <change_id>` puts the earlier copy back, as long as the file has not changed again since.

## Switching from the old timers

Switch on one day, before 23:40 (the review time of both the old timer and the LINA job), in one sitting:

1. Turn off the old daily review timer and the old undo snapshot timer on the hub.
2. Run `lina cron ops` on the hub.

That day the agents are reviewed once, by the LINA job. If the old review timer already ran that day, run
`lina cron pause <agent-review job id>` right after `lina cron ops` and `lina cron resume <id>` the next morning.
The weekly quiz timer has no job here; the nightly taste measurement replaces it.
