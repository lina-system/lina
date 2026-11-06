# Worker protocol

Every hub contact created by `transport.for_device` asks `_device hello` before
its first verb, including local devices. The descriptor is cached only in that
transport object, never on disk or across ticks. It contains `protocol.min` and
`protocol.max` (currently both 1), `version` (the informational LINA release),
`instance_id`, `serve`, and `capabilities`: configured runner names and types,
access modes (`read-only`, `workspace-write`, `full`), and supported verbs.
Runner versions and login checks belong to #222.

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
See `lina-worker.example.yaml` for Codex sandbox and Claude permission flags.
The omo runner applies `read-only`, `workspace` or `full-access` to the individual
session, rather than changing its shared daemon's configuration. Both hub and
device refuse unsupported modes with a reason. ACP is not implemented here.

Omo permission dialogs are machine decisions, not human questions. The runner
answers `Deny` for read-only writes, external workspace targets and requests it
cannot classify; allowed in-workspace file edits receive `Allow once`. Decisions
are recorded as `permission_denied` or `permission_allowed` with tool, target and
mode. Hub, device and runner answer paths reject permission-dialog replies, even
if the text is `Allow once`. A larger grant requires a new task.

The installed engine's default without an override is `full-access`. Its
`workspace` preset allows file edits and bash but asks for external directories;
`read-only` asks rather than denying writes, which is why the runner resolves
dialogs. Codemode may hide bash behind eval. The worker permits only a single
literal JSON `tool.bash`/`tool.monitor` bridge for `ls`, `git status --short`,
`git add` of a local filename, or `git commit -m` with a literal message. General
eval code is denied. These engine rules are not an OS sandbox for arbitrary
shell code; no preset offers full-access eval with guaranteed filesystem
confinement. The worker keeps the narrower workspace preset rather than
silently raising access to match the unrestricted default.

`send --access` can retain or lower access for subsequent work, never raise it.
Omitting the option preserves the recorded mode; answers preserve the current
run's mode. The device independently checks follow-up payloads against its
record, so bypassing the hub cannot escalate a task. A repository task retains
its worktree-local `remote.origin.pushurl` block even in `full` mode. The access
mode does not grant permission to push.
