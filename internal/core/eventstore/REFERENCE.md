# internal/core/eventstore implementation notes

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-33 event store increment

The #41 package increment implements the closed emitter, strict twelve-field codec, boot/turn/task root constructors, query filters and citation oracle.
`internal/core/eventstore` takes a caller-supplied `database/sql.DB` for a dedicated SQLite event database and a typed full-disk classifier.
It requests WAL/FULL on one reserved connection, commits every append and releases a separately allocated reserve for one full-disk retry.
[The diagnosis contract](../../../docs/design/self-diagnosis-log.md#storage) defines its ownership, query and reserve behavior.
The SQLite binding remains #39 work; package and scripted SQL tests do not establish reopen/crash durability or an actual full-disk commit.
Core startup, flow roots and the conversation query tool attach in their consuming issues; this increment changes no protocol or TUI operation.
