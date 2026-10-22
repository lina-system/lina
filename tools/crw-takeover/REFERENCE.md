# CRW takeover reference

Read-only reference: CRW commit `7e65fcb82d426d228e18958d728fe63a0991ddd0`,
inspected through GitHub because the designated local checkout was absent.
No CRW executable, relay, hook or review workflow was run or copied.

## Seen

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

## Inferred

- Takeover imports observations, never active workers, permissions or executions.
  Planning kinds map to goal drafts, projects and to-dos in LINA's versioned
  records. Relay facts remain reference documents with their complete payload.
- A private operator journal must reserve random LINA ids durably before any
  publication. Retries reconcile those ids through the normal records API.
- SHA-256 supplied separately by the operator verifies the exact export bytes.
  Inventory totals catch incomplete exports; a report validator catches omitted,
  duplicate or invalid outcomes. Missing/invalid payloads are lost, unknown kinds
  are unsupported, and neither is silently imported.

## Unknown

- No live tracker export or owner intent-card data was inspected. The documented
  versioned envelope is an operator export format, not a claimed native tracker
  schema. Fixtures are synthetic. The operator must verify export completeness
  and retain the original snapshot and journal.
- Importing observation records does not complete the owner-approved operational
  switch, stop CRW, resume work, or establish historical external effects.
