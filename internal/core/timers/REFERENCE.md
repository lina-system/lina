# Timer reference observations

Design authority: [C-26](../../../docs/design/go-core.md#timers-progress-and-waiting).

## Seen

CRW commit `7e65fcb82d426d228e18958d728fe63a0991ddd0`,
`plugins/crw/skills/crw-canon/references/schedule-contract.md`, explicitly does
not compute dates or provide a timer database. `docs/relay/dag-scheduler.md`
uses deterministic stored facts and digests: an unchanged observation has the
same result, and a duplicate wake does not authorize another execution.

## Inferred

LINA needs a native civil-time calculator and a canon transaction that commits
the unique fire and next instant before dispatch. Sensor observations can be
compared without a model; only a changed signature enters the conversation
reaction path. Scheduler claims never grant tool or external-effect authority.

## Unknown

The requested Hermes source and plugins are unavailable in this environment.
Their DST, recurrence and crash semantics have not been observed. No upstream
calendar algorithm is copied. The CRW documents establish neither DST handling
nor an exactly-once model or external-effect guarantee.

## Failure recovery follow-up

Seen at CRW commit `821d6453aacfc9053b17c3b24a134c0ce4077f80`: the same
schedule contract still computes no dates; the DAG scheduler retains an
`effect_unknown` result and treats duplicate wakes as hints, not permission to
repeat execution. Inferred for C-26: live failures should share the existing
bounded read-only replacement budget; acknowledgement retries should retain
the action result. Upstream recurrence-horizon and live timer retry semantics
remain unknown. This follow-up changes LINA's native scheduler only.

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-26 timer tool increment

#129 implements `timers` through the running Core.
`create_timer` admits an immutable schedule and action from an authenticated private main conversation; Core supplies the person, agent and conversation.
`timer_status` and `cancel_timer` recheck that binding.
Repeated admission under the same tool-call identity returns the saved receipt, including after expiry; a changed request conflicts.
Cancellation stops future fires; already claimed work may finish.
Channel scheduling is not admitted without a durable channel authority binding.

### C-26 scheduler migration

Canon migration 41 adds authored timer rows and scheduler-owned next instants, signatures and fire claims after completion events.
One transaction commits a unique `(timer, instant)` fire and its next instant before any dispatch.
One sleeper and four bounded delivery workers serve the scheduler.
A timer's next fire cannot overtake an unfinished claim.
On restart an older-epoch reminder or sensor claim is replaced at most once; an interrupted effect-capable job or second interrupted claim becomes `unknown` and remains inspectable.
This is one durable fire per instant, not an exactly-once external-effect promise.
