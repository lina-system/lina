# Work area reference

Read-only reference: CRW commit `7e65fcb82d426d228e18958d728fe63a0991ddd0`,
`docs/flow/journal.md`, “The first executor call of the suffix”. No CRW process,
relay, hook or review loop is run for this implementation.

## Seen

CRW creates a detached Git worktree at a recorded commit, preserves previous
work under named refs, records the allocation, and does not reset an existing
worker's files when resuming. The pinned completion receipt identifies artifact
paths and hashes, but does not itself keep their bytes.

## Inferred

LINA's own work-area contract needs one allocation per task, an immutable base
commit reference, and independent material copies of results without a PR.
A local-only repository also needs a self-contained branch bundle. Material
metadata and the referring work event must publish in one canon transaction.
A Git reference-transaction hook can reject ordinary writes to protected refs;
it is an accident guard, not confinement of a worker running as the user.

## Unknown

The reference does not establish LINA's directories, protected-ref names,
material ownership or backup policy. Those come from LINA's design contracts.
This increment does not port CRW scheduling, restore lineages or review behavior.
