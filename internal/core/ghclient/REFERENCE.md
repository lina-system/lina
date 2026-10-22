# GitHub observation reference

Reference inspected before implementation: codex-relay-workflow commit
`7e65fcb82d426d228e18958d728fe63a0991ddd0`, `internal/tracking/github.go`,
`internal/tracking/github_test.go` and its MIT license. No upstream source is
copied, translated or linked. This is an independent implementation of LINA's
C-58 and GitHub completion-signal contract.

- **Seen:** the reference bounds response bodies and pagination, distinguishes
  permission refusal from primary/secondary rate limits, and carries Retry-After
  and rate reset information to its caller. Its transport does not retry itself.
- **Inferred:** Core should retain an unfinished CI observation with a specific
  rate-limit cause and let its one PR watcher own the next permitted read.
  GitHub response content and review comments are data, never commands.
- **Unknown:** the inspected tracker does not establish a PR check watcher or
  conditional GraphQL support. A direct gh probe showed that GraphQL GET returns
  the introspection schema rather than the supplied query. The implementation
  therefore uses fixed GraphQL POST queries for reviews and conditional REST GET
  for PR identity and checks, using only returned validators. No worker relay,
  outbox, Hermes code or CRW state is consumed by this increment.

## Head-pinned merge (#141)

The same reference commit was read through GitHub's read-only contents API:
`internal/relay/dagsched/mergeorder.go` and
`internal/relay/dagsched/mergedreading_test.go`.

- **Seen:** merge ordering labels measured heads current, stale or unknown;
  merged PRs remain readable but incomplete evidence remains a read failure.
  A merged candidate does not manufacture a delivery judgment.
- **Inferred:** LINA must serialize one repository/base merge at a time, compare
  the freshly read head with the accepted head, and never use a stale measurement
  as merge authority. Observing a merge must remain separate from issuing one.
- **Unknown:** those files do not establish GitHub's atomic head-pin behavior or
  the local protected-ref algorithm. LINA implements its own C-58 contract:
  fixed `gh pr merge --match-head-commit` arguments, local Git ref snapshots,
  and explicit commit/ref/check reads before accepting an external merge.
  No reference code or operational records are copied.

## Head changes during evidence reads (#139)

The pinned `internal/relay/dagsched/mergeorder.go` was read again through the
read-only contents API before this correction.

- **Seen:** current, stale and unknown heads describe the relationship between
  a measurement and the node's current head; the reading is not a merge gate.
- **Inferred:** when PR identity and review reads disagree, LINA must refresh
  the evidence and return it without admitting a merge, as C-58 requires.
- **Unknown:** the reference does not define gh read races or retry counts.
  LINA refreshes once, remains blocked even if the original head returns, and
  leaves further observation and explicit head acceptance to the caller.
