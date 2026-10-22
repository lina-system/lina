# CRW contract reference

Reference: [codex-relay-workflow](https://github.com/thisisjun786/codex-relay-workflow),
commit `7e65fcb82d426d228e18958d728fe63a0991ddd0` (MIT). Inspected read-only;
no CRW process, relay, hook or review workflow was run.

## Seen

- `contract/schema/` contains the five Draft 7 record contracts: relationship,
  completion receipt, delivery attempt, acknowledgement and verification verdict.
  `SOURCE.md` records their SHA-256 hashes. References are local to each document.
- `contract/fixtures/` exercises CRW CLI, hook, MCP, app-server, Git dispatch and
  operation-ledger surfaces. It is not a record-validation corpus.
- `internal/relay/store/testdata/fixtures/` contains independently recorded
  `draft7-verdicts`, `runtime-attempt-verdicts`, `independent-review-verdicts` and
  `relationship-reason-verdicts` JSON gzip files. Together they contain 91 cases
  over the five schemas, each with a schema name, label, instance and error list.
  Empty errors mean valid. Supplemental files also record the judged schema hash.
- Instances are normalized strings: temporary paths become `<TMP>` and volatile
  values become `<noiseN>`. The reference schema tests compare normalized
  instances to those recorded judgments. They do not run a Go Draft 7 validator.
- This normalization is lossy: `child ready_for_review` and `ready with the
  sentinel digest` in `draft7-verdicts` have identical instances but opposite
  judgments. `schema_conformance_test.go` shows the latter sets `revisionHash`
  to `NoDeliverable` (64 zeroes). The fixture alone cannot recover that input.
- Schema validation establishes record shape, not semantic cross-record rules
  such as a relationship's current generation belonging to its history, nor
  evidence that a worker performed an effect.

## Inferred

- Pinning must cover exact upstream bytes, including compressed fixture bytes,
  and retain upstream paths so independent verification needs no CRW runtime.
- A LINA runner must supply only the case input to the validator and obtain its
  expected validity exclusively from the recorded error list. An always-accept
  or always-reject implementation must fail the same corpus.
- Replaying normalized inputs requires a deterministic, input-only expansion of
  volatile placeholders plus explicit source-backed restoration of lost values.
  It must not consult expected errors or observed output.

## Unknown

- These fixtures cannot establish durable work-ledger transitions, dispatch,
  transport reconciliation, worker readiness or end-to-end job completion.
  Those remain the responsibility of the consuming implementation issues.

## Adoption

The four record-verdict files are adopted unchanged, with a separately pinned
input-only replay table. This is a test-harness implementation choice; it changes
neither the product's record formats nor the upstream judgments. The source
paths, transformation rules and the normalization collision are recorded in
[the corpus README](../../../../testdata/crw/README.md). No CRW executable surface
or implementation is adopted.
