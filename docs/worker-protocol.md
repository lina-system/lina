# Worker protocol

Every hub contact created by `transport.for_device` asks `_device hello` before
its first verb, including local devices. The descriptor is cached only in that
transport object, never on disk or across ticks. It contains `protocol.min` and
`protocol.max` (currently both 1), `version` (the informational LINA release),
`instance_id`, `serve`, and `capabilities`: the configured runners, access modes
(`read-only`, `workspace-write`, `full`), and supported verbs. Each runner row
(`lina_runners.node_report`, #222) carries `name` and `type`, and for a runner
LINA pins also `pin`, `pinned`, `installed` (read from the active install),
`person_installed`, `sign_in`, `ready` and one `why`. The runner probes run in
parallel within `lina_runners.HELLO_TIMEOUT_S`; a probe that fails or is late
gives `why: unknown` with `why_detail`, and hello still answers. The instance
identity is read apart from these probes (`protocol.identity`).

Compatibility depends only on overlapping inclusive protocol ranges. Disjoint
ranges block every non-hello verb and report both ranges and which side needs an
update. Bidding, `lina worker hosts` and `lina devices` preserve that same reason.
Only the exact `unknown verb hello` response identifies a legacy device: it needs
an update and cannot accept new work, but `meta`, `human`, `pack`, `bundle` and
`cancel` remain usable. Other hello errors fail closed.

`protocol.verbs()` is the sole verb-to-information table. Device dispatch, stdin
handling, the advertised verbs and legacy recovery permissions all derive from
it. Each verb declares its `effect` (`read`, `write`, `destructive`) in that same
table; completeness tests reject missing or unknown effects.

Each `serve()` start publishes a fresh UUID, PID and start time in
`worker/serve.instance.json` before writing its heartbeat. A missing or stale
heartbeat makes hello report no active instance. The instance is not a task ID:
running tasks survive worker service restarts.

For future hub service controls, `NegotiatedTransport.service(action, recorded_id)`
refreshes hello and refuses a changed instance without acting. The internal
`_device service <stop|restart> <instance_id>` gate compares again on the device.
The verb also takes `service <ocx|worker> <action> [instance_id]` (#222):
`worker status|stop|restart` (stop and restart through the same instance gate)
and `ocx status|restart` through the node's service layer
(`lina_runners.node_service`); the older form still means the worker. Its
effect stays `destructive`, the strongest of the actions it carries.
Identity publication and compare-and-stop share a file lock, so a newly published
instance cannot be stopped using the previous record. Restart starts the service
after releasing that lock, allowing the new loop to publish its identity. A stale
node response refreshes hello without retrying the action. There is no new human
service-control CLI in this change. This guard is instance checking, not access
authorization; the SSH account remains the existing trust boundary.

## Run access modes

`lina worker submit --access read-only|workspace-write|full` defaults to
`workspace-write`. The hub refuses a request above `worker.access.<agent>` in
`config.yaml` (also defaulting to `workspace-write`); it never silently lowers
the requested mode. This ceiling is outside the versioned work-settings keys.
The mode is recorded on the hub job, grant, submission receipt and device job,
and appears in status and notice headlines.

A command runner declares `access_args`, a mapping from modes to argv lists
appended on starts, follow-ups and answers. Without that mapping, its fixed
`access` is its only supported mode; with neither declaration it cannot start.
The ACP examples in `lina-worker.example.yaml` use protocol permission answers instead of CLI bypass flags.
The omo runner sets a session preset only for `read-only`. Both hub and
device refuse unsupported modes with a reason.

## ACP runners

A runner of `type: acp` speaks the Agent Client Protocol to a child process over stdio. Its `argv` is mandatory
(a non-empty list of non-empty texts); the optional `flavor` is one of `grok`, `dsh`, `agy`, `claude` or `codex`,
and an omitted flavor is generic. The entry points are `grok agent stdio`, `dsh acp`, `agy acp`, `claude-agent-acp`
and `codex-acp`. Settings carry no bypass flags.
Claude Code and Codex use the public `@zed-industries/claude-agent-acp` and
`@zed-industries/codex-acp` adapters; provisioning and version pins are maintained separately.

- The client advertises no file-system and no terminal capability (both false).
- Permission requests are answered by the runner, never a person. `read-only` allows `read`, `search`, `fetch`
  and `think`, and rejects writes and execution. `workspace-write` also answers `allow_once` for edits/deletions
  whose targets are all inside the worktree, and arbitrary commands with a known, nonempty cwd inside it.
  Missing, empty or outside command cwd and unknown kinds are rejected; `full` answers `allow_once` for all kinds.
  Grok uses the tool's explicit cwd, or the hook's explicit execution cwd when the tool omits it.
- Grok's native tools do not always ask ACP permission. Its flavor requires the advertised blocking
  `x.ai/hooks` pre-tool hook and registers it on every new or reopened session; a missing hook fails before
  a prompt runs. This gate uses the same access policy and logs its allow/deny decisions.
- A `question.md` moves the run to `waiting_human`; the answer is prompted into the same session.
- The next run of a task loads or resumes the session when the agent advertises it, else it starts a new session
  whose first prompt carries the previous final report summary. Events record which method was used.
- A live `send` steer cancels the active turn, then prompts the same session; prompts are never concurrent.
- Successful `end_turn` completes the run; `cancelled` cancels it, and refusal or exhausted limits fail it.
  `stall_grace_s` remains the idle limit. A run closes its agent process group before publishing its ending.
- Requested models use the session's advertised model config selector (`session/set_config_option`) or
  advertised legacy models (`session/set_model`). Only an agent-confirmed current model ID is recorded as
  `model` in the handle and run manifest; the original profile is retained as `requested_model`. An unsupported,
  refused or unconfirmed choice records `model: null` and a `model_not_applied` event. Explicit `--model` requests
  fail before prompting in that case; routing/default choices continue with the agent default. A run without a
  requested model is never blocked on model selection. Unconfirmed aliases and reasoning levels are not reported
  as applied.

The wire format follows the [public ACP schema](https://agentclientprotocol.com/protocol/schema).
Real-node runs of these agents, each ending on its work card, are checked when they move onto the nodes (#236).

workspace-write is enforced by the task boundary, not by a shell grammar; the runner keeps edits inside the worktree and refuses commands whose cwd is outside it.
As with omo, the LINA task boundary supplies the worktree, blocked push, read-only input copies and Linux
transient unit. The runner does not claim an OS sandbox or inspect shell command contents.

omo: workspace-write is enforced by the task boundary (worktree, blocked push, read-only inputs), not by an omo preset, because the engine's workspace preset asks on every codemode eval

`workspace-write` and `full` both omit `permissionPreset`, matching today's
omo-task default (`full-access` without project/global overrides). Their session
settings are identical; their distinction is the hub ceiling and recorded mode.
The task brief requires work inside the worktree, inputs are read-only copies,
push is blocked, and Linux workers run in transient units. This is the task
boundary, not an omo filesystem sandbox; the shared daemon is not reconfigured.

Omo permission dialogs are machine decisions, not human questions. The runner
answers `Deny` for read-only writes, external workspace targets and requests it
cannot classify; allowed in-workspace file edits receive `Allow once`. Decisions
are recorded as `permission_denied` or `permission_allowed` with tool, target and
mode. Hub, device and runner answer paths reject permission-dialog replies, even
if the text is `Allow once`. A larger grant requires a new task.

Installed omo-ai 5.1.51 engine sources under
`dist/core/extensions/builtin/permission-system/`: `config.js:4,8` defines the
full-access default; `config.js:9-17` makes workspace ask by default and permits
read/list/grep/edit/bash, but not eval. `parsers.js:119-135` always adds a separate
eval permission even when a cell's tools request narrower permissions.
Read-only retains its preset and automatically denies write/eval requests.
If project/global rules cause a dialog in workspace/full, the machine policy
still answers it, never a person: allowed targets receive Allow once and
forbidden or unknown requests receive Deny. The narrow literal eval bridge is
only a fallback for such dialogs, not a restriction on ordinary no-preset eval.

`send --access` can retain or lower access for subsequent work, never raise it.
Omitting the option preserves the recorded mode; answers preserve the current
run's mode. The device independently checks follow-up payloads against its
record, so bypassing the hub cannot escalate a task. A repository task retains
its worktree-local `remote.origin.pushurl` block even in `full` mode. The access
mode does not grant permission to push.
