# Work ledger reference

Read-only reference: CRW commit `7e65fcb82d426d228e18958d728fe63a0991ddd0`,
`contract/schema/completion-receipt.json` and
`contract/schema/verification-verdict.json`. The adopted files and independent
fixture judgments are fixed by [the pin](pin/REFERENCE.md).

## Seen

- A receipt binds a relationship, execution generation, thread and turn. A
  completed turn alone is not a delivery. Child claims and parent verdicts are
  different records; a daemon cannot claim a reviewable product.
- A deliverable revision is SHA-256 of sorted absolute-path/content-hash pairs,
  joined with newlines without a trailing newline. Receipt identities are
  derived from their generation, revision and outcome; execution-only outcomes
  additionally use the turn and attempt.
- The pinned schemas constrain record shapes, not concurrent execution or
  artifact provenance. LINA's authority contract supplies provenance: artifact,
  task, node, observed path, revision/hash and observation time.

## Inferred

- A serialized canon transaction must append the event and update its card and
  active-run index together. Rebuilding those indexes must read only the ledger.
- Receipt validation must bind the claim to the current execution and check
  every manifest entry against its provenance before appending anything.
- A worker claim cannot write a Core verdict or make a task done. Acceptance of
  one revision must not leak into a later generation.

## Unknown

- The schemas do not establish worker dispatch, GitHub truth, observed artifact
  bytes or manager judgment. This increment records these facts from their
  owning services; accepting a receipt is not evidence of successful execution.
- Vendor worker adapters and workspace creation have their own implementation
  issues. No CRW executable, relay, hook or review loop is used here.

## Scheduler observations for #208

Read-only CRW reference at the same pinned commit:
`docs/relay/dag-scheduler.md` (ready set and edge satisfaction),
`internal/relay/dagsched/capacity.go`, and `integration.go`.

- **Seen:** readiness has one reason; acceptance alone does not satisfy an
  integrated edge; integration observation and capacity release share a store
  transaction. Missing capacity measurements do not imply free slots.
- **Inferred:** LINA reserves dispatch and write areas in its fenced canon
  transaction before an external start, retains uncertain starts on recovery,
  and distinguishes execution completion from an observed merged pull request.
  A duplicate event cannot create another worker or send another conflict return.
- **Unknown:** these references do not establish LINA's worker or forge behavior.
  CRW permits some nonexclusive overlaps; LINA's stricter design requires every
  overlapping area to remain exclusive. No CRW command or runtime is used.

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-57 work area integration

#127 wires `prepare_work` and `retain_work_result` into the running conversation tool registry.
`work.Area` owns leased Git subprocesses through `kit/proc`, detached allocation at the pinned base, protected refs and branch bundles.
Core closes its process owner during shutdown.
