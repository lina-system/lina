# Goal evidence

`CriterionEvidence.Collected` retains the content acquired by a trusted Core
adapter. `currentEvidence` validates the evidence kind, goal revision, checking
round, target and required content. A task-bound outcome check, including a
custom criterion that names the task, uses that task's current result revision.
`merge.read` keeps the historical merge commit, so a passing repair does not
rewrite the merge receipt and does not let the failed commit satisfy the outcome.
`applySuccess` compares external records to the criterion's expected fields
before storing a passing result. Worker receipts
and model arguments cannot supply this execution projection.

The installed code driver implements `EvidenceReader`. `closingEvidence` reads
all current evidence asynchronously, with at most one reader per goal under the
runner's cancellation and shutdown lifetime; the close transaction checks
the revision/round/content token again. `RefreshEvidence` uses the same path at
startup and on the safety sweep. Invalidations clear the current checks, advance
the round and preserve observations in `work_events`. `TargetChanged` is trusted
integration ingress for a target or a source passage change. It grants no effect
authority and never redispatches a worker. Invalidation retires undispatched
checks, including authorization waits, before a late grant can refresh their
inputs. Startup derivation schedules no external reads until the runner starts.

Evidence data is additive JSON in the existing work ledger; no database schema
migration is needed. Previously saved reference-only checks are invalidated and
collected again. Replaying the journal preserves the original history, including
rechecked and invalidated evidence.

`e2e/m2/evidence-before-close.json` runs through the installed-Core harness with
stub worker and plugin executables. It covers wrong external fields despite a
success receipt, an event moved between collection and closure, and a completed
goal reopened after a target changes while Core is offline. Citation and rubric
checks read the worker's real artifact. The goal unit tests cover evidence types,
source retention, idempotent change hints and a concurrent change during closure.

Outside waits and decision rows hold final verification and closure after tasks
land. They do not hold independent task dispatch. A manager observation can
carry `waits` together with the legacy `wait`; Core validates the combined set
before committing any task or wait, rejecting duplicate wake identities.
Each retained wait has its own source, durable deadline and read receipt.
Several expiries append their recommendations and drafts to the same retained
row without replacing its identity or dropping an earlier unresolved proposal.

`e2e/trip_goal_test.go` drives real Core with calendar and mail service stubs.
It prepares an itinerary, reads the calendar and sent item, retains independent
host and booking waits, and restarts during the wait. A source signal closes
only the host wait. The booking either arrives by signal or expires while Core
is down into a draft row; a person correction then continues the same goal.
Final checking re-reads the records, and neither restart nor expiry sends again.
The existing `e2e/m1/work_waiting_deadlines_test.go` exercises payment/outward
defaults and late answers through Core's waiting-round service;
`e2e/m2/research_partial_test.go` covers the partial research follow-up and
citation hold. `e2e/run_to_done_test.go` covers the five-task code lifecycle,
worker death, conflict recovery and evidence summary.
