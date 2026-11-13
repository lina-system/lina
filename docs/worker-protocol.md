# Worker protocol

Nodes and their workers are LINA's own (`lina worker`). GOAT, the earlier fleet tool, is reference only: LINA
reads its designs (#224) and never runs, calls or depends on it. The owner's personal laptops are never nodes
([work settings](lina-work-settings.md#keys)).

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

`capabilities.proxy` (#586) is the node's local magpie:
`{url, health, models, remote_id, service}`, where `url` is the address its
proxy check asks, `health` is the HTTP status of that check (null when nothing
answers), `models` is the number of models its local `/v1/models` lists,
`remote_id` is its link to the hub (`""` when its own magpie holds the models),
and `service` is `{name, state}` of its local magpie service (#589). No key is
read. A file node reports no `proxy`.

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
The verb also takes `service <proxy|worker> <action> [instance_id]` (#222):
`worker status|stop|restart` (stop and restart through the same instance gate)
and `proxy status|restart` (#589) through the node's service layer
(`lina_runners.node_service`): the node's own local magpie service
(`worker.proxy.service`, default `magpie`), refused with `busy: <n>` while a
run holds a slot on the node; the older form still means the worker. Its
effect stays `destructive`, the strongest of the actions it carries.
Identity publication and compare-and-stop share a file lock, so a newly published
instance cannot be stopped using the previous record. Restart starts the service
after releasing that lock, allowing the new loop to publish its identity. A stale
node response refreshes hello without retrying the action. There is no new human
service-control CLI in this change. This guard is instance checking, not access
authorization; the SSH account remains the existing trust boundary.

## Bid skip reasons

Besides its slots, the bid (`bidding.readiness`) passes a node for these reasons
and names them in the bid row (`why`, carried by the `award` and `queued`
receipts), in `lina worker hosts` and in `lina devices`. None of them changes
the score order, and none touches a run already on the node: the node watch
speaks for a lost node.

- `model path does not answer (magpie <url>: HTTP <code>)` and
  `model path lists no model (magpie <url>: HTTP 200, 0 models)`: `probe`
  asks the node's model path on the node (#586). The address is
  `worker.proxy.health` when set, else `http://127.0.0.1:3425/v1/models`
  (`settings.MODEL_CHECK`); magpie has no `/healthz` (it answers 404), and up
  is a 200 whose `data` lists at least one model. A local `/v1/models` 200
  does not prove the node's link to the hub: the node's magpie lists the
  hub's models without a call through that link, and the real-call probe
  (#531) stays the proof. The probe reports `model_path: ok` / `down` with
  `model_path_why`. Only a runner that reaches its model through the node's
  magpie is asked for: the omo runner, or a pinned runner whose sign-in is
  `magpie` (claude, codex and their ACP adapters; `device.needs_model_path`).
  A command runner carrying its own credentials (grok, agy, cursor-agent) and
  a pin whose sign-in is unknown (dsh) needs no model path; its probe and
  hello then carry no `model_path` field at all and the node bids as usual,
  the same exemption `proxy_down()` makes for a node that reports no proxy.
  The hello asks for the node's default runner and carries the same two
  fields in `capabilities`; they stand in only for a probe reply that does
  not name the runner it asked (an older node). A node whose model path does
  not answer would end its run `provider_error` at once, so the bid passes it
  and a `--host` submit there is refused with the same line. A node without
  the field is bid on as usual.
- `magpie not answering` (`bidding.proxy_down`): the `capabilities.proxy` of
  the node's hello has `health` not 200 or lists no model (`models: 0`), for
  a runner that reaches models through it (its probe carries `model_path`);
  the model path reason above usually says it first. A node that reports no
  proxy (a file node, an older node) is never passed for this. `lina worker
  hosts` and `lina devices` show each node's `magpie <health>, <n> models` or
  `proxy none`, and the `lina devices --json` row carries `proxy`. The hub
  reads it from the hello the same contact asks before its probe
  (`bidding.with_node_facts`), so there is no second round trip. A named run
  (`--host`) is not started there either.
- `memory <n>GB < <floor>GB`: `probe` reports `mem_available_gb` (available
  memory, whole GB rounded down) and a node under `worker.min_free_mem_gb`
  (default 4, read on the hub like `min_free_gb`) is passed, a named run too.
  An older node whose probe has no `mem_available_gb` is not passed for this.
- `cooling off after <n> quick failures`: the hub keeps the last run ends of
  each node on its node record (`hub/nodes/<node>.json`, `ends`: `at`,
  `state`, `took`). When the newest `worker.cooloff_failures` (default 2) of
  them all ended `failed`, each ran no longer than `worker.cooloff_quick_s`
  (default 120) and they ended within `worker.cooloff_window_s` (default 1800)
  of each other, the bid passes the node for `worker.cooloff_s` (default 900)
  after the last one. A newer end that is not `failed` (`done`) clears it. A
  run's length (`took`) is how long it ran on the node: `run_s` of the run's
  fetched `manifest.json` (the node's `started.<run>` to `ended.<run>`), so a
  late end report does not lengthen it, for every run of a task. An end whose
  result the hub did not fetch (a `--wait` waiter shows it, or the fetch
  failed) or whose fetched manifest is another run's has no known length and
  never counts as quick. The node record keeps at least
  `worker.cooloff_failures` ends (and at least 10). A named run still starts there, and `lina worker
  hosts` (which shows named-run readiness) says it on the node's line as
  `cooling off after <n> quick failures (named runs still start)`. The four
  numbers are work settings (`lina worker settings set worker.cooloff_s 600`).

## Request keys

`lina worker submit` and `send` take `--key <k>` (Python: `opts["request_key"]`,
`Hub.send(..., request_key=)`); without one the CLI makes `rk-<uuid4 hex>` and
prints `request key: <k>` on stderr before anything else. The same key with the
same arguments (a sha256 over canonical JSON of what defines the request: the
brief bytes, mode, repo, inputs, to-do, topic, host, runner, model, labels,
size, access, card, owner and schedule for submit; task, message, access and
card for send) returns the first result and starts, places or sends nothing:
`<ID> replay of request key <k>`, receipt `request_replayed`. Other arguments
are refused, naming the key and the first request, and nothing is written.
A key must match `^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`; any other is refused
(`request key must match <pattern>: <k>`) before anything is written.

The hub keeps `hub/keys/<sha256(key)>.json` (`key, op, args_sha256, task, run,
action, at`), written under the key's lock before any device is called and
filled in after; the job carries `request_key` and a `request_keys` map.
Before a send calls the device, the record also gets `device_verb`,
`device_args`, `device_phase` and `device_key`. A replay of a record left without a result
never bids or routes again: it returns the recorded task, or calls the
recorded verb again with the recorded arguments and key, so a question that
closed after a lost `answer` reply is not steered or followed up anew. A call
that fails with no reply is an error that names the key to send again with.
On the device `follow_up` (payload `request_key`), `answer` and `send` (an
optional trailing argument) keep `{key: reply}` in the task's `keys.json`
under the task lock, so any write verb with a key it has seen returns the
stored reply and changes nothing; replies that changed nothing are not kept.
A device call never carries the caller key itself but `<k>#<phase>`: `req`
for the request's first device call, `next` for the run an answer starts after
its question closed. A caller key cannot hold `#`, so it never equals a device
key; the device treats the key as opaque.
A reply the device marks `replayed` is reconciled, never applied as new: the
hub fills in the key's record, writes `request_replayed` and prints the replay
line, moves the task's run, state, delivery and verdict only to a run newer
than the one it holds, and never raises its access. An end reported for a run
newer than the hub's (its follow-up reply was lost, keyed or not) adopts that
run (receipt `run_adopted`): the hub sets what that reply would have set, the
run, a fresh state, delivery and verdict, the run's access from the device's
`meta` and the task's access lowered to it (never raised), and delivers the
end as usual; only an end of an older run is recorded silently. A later
replay of the request that started an adopted run that has ended changes
nothing but the key's record and lowers the task's access to the request's.
Before a send chooses its access, the hub lowers its job to the device's
access from `meta` when that is lower (receipt `access_synced`, `from`/`to`);
the device's value never raises it.
`hello` lists `request_keys` in `capabilities`.
A device without it ignores the key; the hub's record is the only guard there,
and the hub writes one `device_without_keys` receipt per task. `lina work`
uses `lina-work:<outbox row id>`, schedules `lina-schedule:<ID>:<due time>`.

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
- Before starting a restricted dsh run, `honours(mode)` reads the version of the same executable the run would
  launch, including LINA's managed shim. `lina_runners.ACP_PERMISSION_OBSERVATIONS` records whether that adapter
  version mediates native tools through ACP permission requests. The observed dsh versions `0.2.1-rc.5` and
  `0.2.2-rc.1` do not: native writes can bypass requests (the latter requests approval for explicit escalation).
  They refuse `read-only` and `workspace-write` with the agent, version and mode in the reason, before task
  delivery. Unknown versions also refuse until observed; `full` keeps its existing path without this probe.
  Recheck this observation whenever the pin changes; a newly verified version can be admitted without changing
  the runner policy. This is a version observation, not a permanent ban on the dsh name. Initialization does not
  advertise complete permission mediation, and the current profile dump contains runtime expressions rather
  than a resolved per-mode guarantee, so neither is treated as evidence of enforcement.
  Claude and Codex retain their existing ACP permission path, Grok retains its required blocking hook, and
  other generic ACP adapters retain their existing protocol contract. Live access narrowing calls the same
  `honours(mode)` gate before delivering input; a dsh `full` session cannot silently narrow to an unsupported mode.
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

`workspace-write` and `full` both omit `permissionPreset`, matching omo's
own default (`full-access` without project/global overrides). Their session
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

Installed omo-ai 5.1.69 engine sources (`@code-yeongyu/senpi` 2026.11.10-2) under
`dist/core/extensions/builtin/permission-system/`: `config.js:4,8` defines the
full-access default; `config.js:9-17` makes workspace ask by default and permits
read/list/grep/edit/bash, but not eval. `parsers.js:180-196` always adds a separate
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
