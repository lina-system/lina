# internal/core/boot implementation notes

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-01 control-file increment

The control-file increment (#36) starts with `lina core --state-root <path> --socket <path>`.
The state root must already exist, and installation supplies `system/core/main.json`; Core never invents a replacement identity.
`lina install` verifies the build bundle, creates a random main id in a new private state root, and registers the `lina-core.service` user unit with explicit state-root and socket arguments.
Existing state and services are refused, including an install's own previous identity; installation is not an update or implicit restart.
The concrete layout and failure behavior are specified in [runtime.md](../../../docs/design/runtime.md#the-lina-command).

### C-04 startup increment

The #36 startup coordinator executes these boundaries in order.
Event writer, read-only canon peek, canon open/probe/migrate, epoch mirror and reconciliation have explicit service interfaces for their owning implementations.
In this increment the CLI has no event writer or canon implementation and serves only the handshake; an absent or empty canon directory is allowed, but an existing canon store without its service is refused before minting an epoch.
The coordinator passes a null epoch to startup events by leaving the event writer unbound until step 5; it supplies the new epoch only after the file and directory fsyncs succeed.
Canon's writer cannot open before that boundary.
