# Delegation and Background Work

## Bounded delegation

Use `delegate_task` for independent subtasks with a concrete goal, allowed paths, output artifact, and completion check. A batch runs under `delegation.max_concurrent_children`; inspect the configured value rather than assuming a default. Child tools are a subset of the parent's tools.

Background delegation remains process-local. A returned handle is not proof the work survives the parent process. Keep the parent alive until results arrive and read each result before integrating it. Use independent worktrees for parallel code editors. For a separate CLI process, use `lina chat --oneshot -q <prompt>`; give it a bounded runtime and observe its exit and artifact.

## Scheduled work

Use the `cronjob` tool for scheduled work. Inspect existing jobs with `lina cron list`, scheduler state with `lina cron status`, and actual execution attempts with `lina cron runs`. Configuration alone does not prove delivery. Verify the job's working directory, profile, magpie model routing, and delivery target.

Scripts without an agent run avoid inference cost; scripts used as pre-run collection feed their output to the agent. Inspect `hermes_cli/subcommands/cron.py` before naming creation flags. Respect scheduler locks and preserve conversation role alternation when delivering results.

## Skill lifecycle and work records

The curator tracks usage and archives stale agent-created skills. Pinned skills are exempt from automatic maintenance; archives are recoverable. Do not edit bundled or hub-installed skills through automatic curation. Discover installed actions with `lina curator --help`.

For cross-process work, retain a durable work record with state, owner, dependencies, artifact references, and evidence. Keep owner files on the Desk and refer to them with `desk:<relative path>`. Do not confuse an in-session todo with a durable task ledger, or invent a worker CLI.
