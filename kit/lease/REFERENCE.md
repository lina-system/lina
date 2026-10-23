# kit/lease implementation notes

## Implementation notes

Design authority: [Go Core](../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-05 lease API increment

The #37 lease API is `Acquire(ctx, lease.Activity{Class, Reason}) (lease.Lease, error)`.
Classes are `Hard`, `Live` and `Reported`; an unknown class or empty reason is refused.
Kit adapters carry the lease in their typed API and enter external work through `lease.Do(ctx, lease, call)`.
Its missing-argument fixture must fail compilation, and its companion with a lease must compile.
Zero and released leases are rejected before the callback runs.
A lease is an activity record, not an authorization grant.
Leaser implementations register activity before constructing its handle with `lease.New` and supply the callback that removes it.
