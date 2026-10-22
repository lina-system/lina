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
