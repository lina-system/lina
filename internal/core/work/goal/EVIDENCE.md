# Goal evidence

`CriterionEvidence.Collected` retains the content acquired by a trusted Core
adapter. `currentEvidence` validates the evidence kind, goal revision, checking
round, target and required content. `applySuccess` compares external records to
the criterion's expected fields before storing a passing result. Worker receipts
and model arguments cannot supply this execution projection.

The installed code driver implements `EvidenceReader`. `closingEvidence` reads
all current evidence outside the canon transaction; the close transaction checks
the revision/round/content token again. `RefreshEvidence` uses the same path at
startup and on the safety sweep. Invalidations clear the current checks, advance
the round and preserve observations in `work_events`. `TargetChanged` is trusted
integration ingress for a target or a source passage change. It grants no effect
authority and never redispatches a worker.

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
