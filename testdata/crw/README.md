# Pinned CRW work-ledger contracts

The pin is [pin.json](../../internal/core/work/pin/pin.json). It records CRW
commit `7e65fcb82d426d228e18958d728fe63a0991ddd0`, upstream paths and SHA-256
digests. Schema documents and gzip fixture files are exact upstream bytes.
The MIT license and attribution are in [THIRD-PARTY-NOTICES.md](../../THIRD-PARTY-NOTICES.md).

| Domain | Upstream fixture | Cases |
| --- | --- | ---: |
| record-schema | draft7-verdicts.json.gz | 55 |
| runtime-attribution | runtime-attempt-verdicts.json.gz | 12 |
| independent-review | independent-review-verdicts.json.gz | 18 |
| relationship-reasons | relationship-reason-verdicts.json.gz | 6 |

The fixtures are under CRW's `internal/relay/store/testdata/fixtures/`, where its
record-validation corpus lives at this commit. `contract/fixtures/` contains
CRW-specific executable surfaces and is not adopted. The five schemas remain
under upstream `contract/schema/`. These cases prove record shapes, not work
execution, durable transitions, semantic cross-record invariants or effects.

## Replaying normalized input

Upstream recorded independent Python Draft 7 judgments, but normalized volatile
input strings. `replay.json` supplies concrete **inputs only** for all 91 cases.
Its sources map records the upstream test-source paths and hashes consulted.
Each input is bound to its original fixture, case index, schema, label and the
SHA-256 of its normalized instance string. The replay table itself is hashed in
the pin and copied into build-manifest provenance by that hash.

The construction is independent of the expected judgments:

- `<TMP>` becomes `/synthetic`.
- `<noiseN>` in identifiers and hashes becomes N lowercase `a` characters.
- Timestamp placeholders become `2026-01-01T00:00:00Z`.
- Execution-only completion receipts use 64 zeroes for `revisionHash`, as the
  input constructors in `intake_fixture_test.go` and
  `independent_review_schema_test.go` specify.
- Case 14 of `draft7-verdicts` (`ready with the sentinel digest`) also uses 64
  zeroes, as `schema_conformance_test.go` explicitly sets `NoDeliverable` there.
  Upstream normalization otherwise makes this input identical to the valid
  `child ready_for_review` case. Both original judgments remain intact.

The runner checks complete one-to-one input coverage and refuses any change to
an unmasked literal, key, type or array entry. Unknown or remaining placeholders
fail. The validator receives only concrete input; expected validity comes solely
from the original fixture's `errors` array (empty means valid). Validator errors
are failures to execute, not successful negative judgments. Error prose is not
compared across validators.

Run `go test ./internal/core/work/pin/... ./internal/core/manifest/...` or
`go run ./internal/core/manifest/crwinfo` from the repository root. The latter
verifies the corpus and emits the full pin for the build manifest. Moving the
pin requires updating the adopted bytes, their hashes, replay inputs and the
affected record contracts together. Never generate expected judgments from LINA.
