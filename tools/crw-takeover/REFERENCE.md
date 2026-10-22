# Operational switch reference

Read-only inspection of CRW commit
`7e65fcb82d426d228e18958d728fe63a0991ddd0`; no relay, hook, review workflow or
operator command was run.

## Seen

- `internal/relay/service/ownership.go`: `Disable` writes disabled intent before
  stopping the supervisor and worker. A refusal or an unknown stop is not success.
- `internal/relay/service/status.go`: status reports `enabled`, `running`, `lock`,
  `stateDirectory`, `scopeAuthority`, and conflicting scopes separately.
- `internal/relay/service/launch.go`: start refuses disabled intent; enabling is
  an explicit separate operation. `docs/runtime-install.md` describes the same
  gate for the user service's boot start.
- `docs/runtime-install.md`: install rollback changes a runtime pointer; it is
  not a coordination takeover rollback. Source records and worktrees have their
  own ownership and are not disposable installation output.

## Inferred

- LINA needs a durable task admission gate before the external coordinator can
  be stopped. Owner approval must bind the exact task set and snapshot.
- Disabled intent and an independently observed stopped service are both needed;
  a successful command exit alone is insufficient.
- Rollback must fence LINA first, wait for its active executions to end, and only
  then permit the operator to restore the old coordinator. An interruption must
  leave the gate closed and the journal available for explicit recovery.
- Only the operator tool may know the external runtime or its paths. Core stores
  LINA task identities and opaque evidence, not resolvable source paths.

## Unknown

- The service status does not prove that separately launched flow engines,
  interactive parent sessions, or other hosts are stopped. The single owner
  must inventory and quiesce those actors before transfer; unknown coverage
  cannot be treated as exclusive control.
- The one-time record import is issue #214. This procedure does not invent an
  importer, copy CRW code, or infer a verified snapshot from live source data.
