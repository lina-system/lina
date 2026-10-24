# internal/core/runs implementation notes

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-55 display integration status

The display rows and notices are Core APIs in this increment; client 1.0 waiting/running payloads remain reserved until their surface implementation.

### Batch list sources

Boot rebuilds the running list on every run change and once a second. A source
whose items share one record implements `ListBatch`, and `Build` reads its
items in one pass of the same snapshot. The goal-task source decodes each goal
once for all of its tasks; reading one task's row from its goal made each
build quadratic in the steps of a goal and starved the clients' subscriptions
in `TestM3WorkPanelEventLatency`.
