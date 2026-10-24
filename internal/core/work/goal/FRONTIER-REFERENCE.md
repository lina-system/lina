# Ready frontier reference

Design authority: [Step graph](../../../../docs/design/work-and-delegation.md#step-graph)
and [Next action](../../../../docs/design/work-and-delegation.md#next-action).

## Seen

The CRW reference's `internal/relay/dagsched/reading.go` defines deterministic
readings without reading a clock. `pass.go` records ready order and capacity in
one transaction and explicitly separates those facts from release authority.
`candidates.go` sorts node ids and rechecks current acceptance and integration.
The reference was inspected without running its scheduler or relay.

## Inferred

LINA can apply that separation to its own ledger: derive the ordered frontier,
reserve every admissible start transactionally, and observe the existing effect
identity on recovery. LINA's node samples, epochs, graph revisions and result
receipts remain authoritative. Waits consume no worker slot; background work
needs its own stored landing proof rather than a code merge.

## Unknown

The reference does not establish behavior for LINA background runs or named
waits. No upstream mixed-step execution behavior or performance is claimed.

## Implementation

`Derive` returns the ordered frontier separately from starts and holds. The
existing action journal owns claims for all three step kinds. Boot routes
background assignments to the run service using the action id as the native
start identity, and observes the same receipt after restart. Date and canonical
record waits run as read-only observations without worker reservations.

Generated graph/event tests compare each derivation with independent dependency,
area and integer-capacity accounting. The installed Core scenario is
`e2e/m3/ready-frontier.json`; its Go harness uses the shared m1 installation
helpers and exercises real run tools, worker execution and stored results.

Retirement reconciliation decodes a goal only for queued actions or finished,
superseded actions whose retirement is pending. Active actions and settled
history cannot change in this pass; decoding their whole graph for every
worker made a wide frontier compete with progress publication. Queued target
checks and unsettled-effect recovery retain their existing paths.
`TestRetirementSkipsUnchangedParallelGraphs` bounds allocation growth across
one-step and 82-step goals without an elapsed-time assertion. The running-Core
`TestM3WorkPanelEventLatency` scenario retains all 50 transitions across eight
PTYs and the unchanged T-16 E8/E9 bounds.
