# Worker protocol

Every hub contact created by `transport.for_device` asks `_device hello` before
its first verb, including local devices. The descriptor is cached only in that
transport object, never on disk or across ticks. It contains `protocol.min` and
`protocol.max` (currently both 1), `version` (the informational LINA release),
`instance_id`, `serve`, and `capabilities`: configured runner names and types,
access modes (currently empty, reserved for #220), and supported verbs.
Runner versions and login checks belong to #222.

Compatibility depends only on overlapping inclusive protocol ranges. Disjoint
ranges block every non-hello verb and report both ranges and which side needs an
update. Bidding, `lina worker hosts` and `lina devices` preserve that same reason.
Only the exact `unknown verb hello` response identifies a legacy device: it needs
an update and cannot accept new work, but `meta`, `human`, `pack`, `bundle` and
`cancel` remain usable. Other hello errors fail closed.

`protocol.verbs()` is the sole verb-to-information table. Device dispatch, stdin
handling, the advertised verbs and legacy recovery permissions all derive from
it. A future verb's effect classification belongs in this table, not a second
dispatch list.

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
