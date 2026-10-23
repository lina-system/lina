# internal/core/canon implementation notes

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-16 published migration order

The published development order is retraction at version 11, turn records/control/outcomes at versions 12/13/14, compaction at version 15, and account sign-in at version 16.

### C-16 development catalog compatibility

No release has shipped: incompatible older development layouts are refused with a published-catalog error before writer initialization, without a compatibility shim, renumbering or normalization.

### C-16 initial catalog versions

The registry retains assets at version 1 and appends `InboxSchema` at version 2.
