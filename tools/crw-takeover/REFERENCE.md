# CRW takeover references

## To-do state compatibility

Read-only inspection of CRW commit
`821d6453aacfc9053b17c3b24a134c0ce4077f80`; no CRW workflow was run.

### Seen

- `contract/schema/completion-receipt.json` distinguishes execution completion
  from a deliverable ready for review. Neither is a LINA planning state.
- LINA's verified operator export has a separate boolean `done`. The to-do
  contract maps it to `open` or `completed`; goals stay `draft` and CRW records
  stay `observed`.
- Current Core projects earlier imported `active`/`done` to-dos with verified
  tracker provenance without rewriting their bytes or revisions. The importer
  compares unacknowledged publications against the current output bytes only.

### Inferred

- A retry after upgrading the importer must also recognize the exact earlier
  to-do encoding: `active`/`done` and no `deadline_source`. Recognition must use
  the same reserved identity, snapshot and complete content; it grants no
  permission to replace a later edit. Acknowledged imports remain untouched.

### Unknown

- No live imports or tracker exports were inspected. The number of affected
  records is unknown; compatibility is verified with synthetic records only.

## One-time import

Read-only reference: CRW commit `7e65fcb82d426d228e18958d728fe63a0991ddd0`,
inspected through GitHub because the designated local checkout was absent.
No CRW executable, relay, hook or review workflow was run or copied.

### Seen

- `contract/schema/relay-sqlite.sql` separates relationships, scope bindings and
  links, attempts, receipts, verdicts, delivery intent and assignment marks.
  Links bind source keys; they do not prove acceptance or authorize LINA work.
- `internal/relay/store/managed_records.go` records reserved, armed and attached
  starts. A receipt and its binding are separate facts, and uncertain starts
  remain distinguishable from accepted assignments.
- The five pinned JSON schemas in `testdata/crw/schema/` preserve source identity,
  generation, evidence and scope. A receipt is a claim, not LINA's verdict.
- LINA's operational-switch contract requires per-record accounting, one verified
  tracker snapshot, independent LINA identities, and no product source-id lookup.

### Inferred

- Takeover imports observations, never active workers, permissions or executions.
  Planning kinds map to goal drafts, projects and to-dos in LINA's versioned
  records. Relay facts remain reference documents with their complete payload.
- A private operator journal must reserve random LINA ids durably before any
  publication. Retries reconcile those ids through the normal records API.
- SHA-256 supplied separately by the operator verifies the exact export bytes.
  Inventory totals catch incomplete exports; a report validator catches omitted,
  duplicate or invalid outcomes. Missing/invalid payloads are lost, unknown kinds
  are unsupported, and neither is silently imported.

### Unknown

- No live tracker export or owner intent-card data was inspected. The documented
  versioned envelope is an operator export format, not a claimed native tracker
  schema. Fixtures are synthetic. The operator must verify export completeness
  and retain the original snapshot and journal.
- Importing observation records does not complete the owner-approved operational
  switch, stop CRW, resume work, or establish historical external effects.

## Coordination switch

Read-only inspection of CRW commit
`7e65fcb82d426d228e18958d728fe63a0991ddd0`; no relay, hook, review workflow or
operator command was run.

### Seen

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

### Inferred

- LINA needs a durable task admission gate before the external coordinator can
  be stopped. Owner approval must bind the exact task set and snapshot.
- Disabled intent and an independently observed stopped service are both needed;
  a successful command exit alone is insufficient.
- Rollback must fence LINA first, wait for its active executions to end, and only
  then permit the operator to restore the old coordinator. An interruption must
  leave the gate closed and the journal available for explicit recovery.
- Only the operator tool may know the external runtime or its paths. Core stores
  LINA task identities and opaque evidence, not resolvable source paths.

### Unknown

- The service status does not prove that separately launched flow engines,
  interactive parent sessions, or other hosts are stopped. The single owner
  must inventory and quiesce those actors before transfer; unknown coverage
  cannot be treated as exclusive control.
- The one-time record import is issue #214. This procedure does not invent an
  importer, copy CRW code, or infer a verified snapshot from live source data.
