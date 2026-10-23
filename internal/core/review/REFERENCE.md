# Design-conformance findings reference

Read-only reference: CRW commit `7e65fcb82d426d228e18958d728fe63a0991ddd0`,
`internal/review/schema_v1.json` and `internal/review/finding.go`, read through
GitHub's contents API. No upstream implementation is copied or executed.

- **Seen:** findings are structured records; grades are P0 through P3. The
  artifact distinguishes complete, partial and unavailable results. Review text
  is data and cannot authorize execution. Schema validation alone does not
  establish that a finding belongs to the expected head or diff.
- **Inferred:** LINA validates its smaller C-58 finding shape before storage,
  binds the manager judgment to the delivered head and rejects prose as a
  verdict or finding. Core supplies scope and merge-precondition evidence
  independently of model output.
- **Unknown:** these files do not establish LINA's once-per-head claim, work
  definition, base-commit design references or required-check policy. Those
  behaviors come from LINA's contracts. No review panel or relay is adopted.

The issue's owner correction and #272 select the existing manager check. The
original C-58 background reviewer and failed-check P1 mapping are superseded.
No reference review loop is executed; only the finding data boundary is reused.

#144 also read the local CRW `internal/review/finding.go` read-only.
Seen: grades and unverified results remain distinct structured data. Inferred:
LINA returns a P0/P1 subset without changing finding ids and retains the other
records. Unknown: that reference establishes neither LINA's one-return
allowance nor its GitHub close or merge authority. Those come from the owner
correction and Judgment and delivery. No CRW review or relay was executed.

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-58 review claims migration

Canon migration 48 claims the normalized repository, PR number and head before manager dispatch, binding the task, execution generation and receipt.
