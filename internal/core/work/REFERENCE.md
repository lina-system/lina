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

### C-57 parent acceptance (#136)

#137 and #138 carry the parent's five conditions. This note records where each
one is proved on the current head; the parent adds no code or command of its own.

| Condition | Proof |
| --- | --- |
| A definition without a done condition or write area is refused | `TestDefinitionRefusedBeforeAdmission` (`internal/core/worker/dispatch/definition_test.go`, cases `done`, `write_area`, `blank_write_area`) and the installed-Core cases `missing-done` and `missing-write-area` in `e2e/m2/work-definition.json` |
| Two tasks with overlapping write areas are never in flight together | `TestDirectoryAreaAdmissionAndLease` and `TestDirectoryAreaIntersections` (`internal/core/work/areas_test.go`), `TestAreaClaimsSerializeOverlapAndKeepFinishedLease` (`internal/core/worker/dispatch/choice_test.go`), and the installed-Core case `overlap` in `e2e/m2/worker-choice.json`, which requires the second task to wait with reason `area` before any launch |
| A worker whose quota row is blocked is not chosen before its reset | `TestChooserQuotaAndSituation` (`internal/core/work/chooser_test.go`) excludes the worker before `ResetsAt`, admits it at that time, and never infers a reset; the installed-Core case `blocked` in `e2e/m2/worker-choice.json` launches only the unknown-quota worker |
| Unknown quota is usable | The same chooser test ranks ok above warning and unknown, and admits a worker with no rows; the installed-Core case `quota-fallback` starts the worker that reports no quota |
| A quota stop hands the same packet once to the next worker | `TestQuotaHandoffWaitSurvivesCompletionOutbox` (`internal/core/worker/dispatch/handoff_test.go`) keeps one settled attempt; the installed-Core case `quota-fallback` in `e2e/m2/worker-choice.json` requires identical packet bytes, the same workspace, a reaped first process tree and one replacement, and its case `double-quota` makes a second stop terminal |

Both sub-issues are closed, and the listed commands pass on this branch. The
sub-issue scenarios stay the parent's end-to-end evidence; a change to any
condition must update the case named here.

### C-57 graph validation

#539 bounds dependency reachability in both `ValidateSchedule` preflight and
the transactional `Service.Schedule` check of proposed and stored tasks.
After checking missing dependencies and cycles, each queried task's ancestor
set is built once with an iterative visited traversal and cached for that
validation only. Positive and negative queries share it. With V scheduled
tasks and E dependency edges, all reachability queries take O(V(V+E)) traversal
work and at most O(V²) cached membership entries; overlap checks remain
pairwise. No cache survives admission or changes the ledger format.

`TestDependencyReachabilityTraversalBound` counts dependency enumeration on a
256-task DAG whose tasks depend on their previous two tasks. It bounds actual
vertex and edge reads without a clock. Admission tests cover both task orders,
idempotent retry, and atomic missing-dependency, cycle and unordered-fork
rejection with an existing ledger. `TestM3SharedAncestorGraphInstalledCore`
exercises the conversation tool, manager admission, real worker Git results,
dependency release and restart while the shared-ancestor tail stays queued.
