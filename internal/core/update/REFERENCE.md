# internal/core/update implementation notes

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-38 preparation increment

The #149 preparation boundary is `lina update --prepare --state-root <path> --archive <tar.gz> --sha256 <digest>`.
`internal/core/update` freezes and hashes the download before unpacking, checks every manifest artifact, then runs the candidate's `lina core --preflight` through `kit/proc`.
Candidate versions live under `system/updates/<archive-sha256>`; creation is exclusive and never replaces another attempt.
A successful candidate gets a durable `prepared.json` receipt, including the verified manifest digest.
Activation rechecks that digest and all copied artifacts.
The active binaries, service, control records and canon remain unchanged.

### C-38 activation increment

The #150 activation command is `lina update --activate --state-root <path> --candidate <prepared-directory>`.
Both binaries must advertise `update_protocol: 1` in preflight; older binaries without the durable boot/READY guard are refused before drain.
It verifies the installed service's exact paths and refuses foreign units or drop-ins before controlling it.
An independent `update.lock` excludes competing updaters and grants no canon authority.
Core's existing `core.lock` remains the only writer lock.
The updater persists its attempt before requesting the running Core's explicit drain, waits for durable commitment and lock release without forcing active work, and then stops the owned service.
It takes a new verified generation through the installed old binary, verifies the generation/version pair and reruns candidate preflight on the stopped store.
Release-owned binaries, manifest and adapter files move as one recorded composition; the prior directories and failed candidates are kept.
Settings stay in their installed control records, outside binary replacement.
The installed binary's backup and rollback restore (including resumed rollback) use the updater context without an added installer command deadline; data size must not impose a fixed 30-second failure boundary.
Both retain `kit/proc` ownership and cancellation, filtered environments and bounded command output.
Activation and interrupted-update recovery share the service start/readiness check.
A recovery start or readiness failure persists `held` with `update_resume_failed` without clearing READY, even from a terminal record.
Readiness failure stops the owned service using a bounded context independent of caller cancellation before returning the failure.
