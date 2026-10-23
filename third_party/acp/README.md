# ACP version 1 wire types

The unmodified stable schema and method metadata are pinned to
`schema-v1.26.0`, commit `e2d83349bc9e28a4059ea3acefa1c91c966f74e2`,
from https://github.com/agentclientprotocol/agent-client-protocol (Apache-2.0).
The upstream license is preserved. This release contains no upstream NOTICE.
No v2 or unstable schema is included. This pin does not certify a worker version.

Run `node third_party/acp/generate.mjs` to regenerate, or append `--check` to
verify without network access. `vendor.json` records source hashes, tools and
commands. There are no patches. Never edit generated files by hand.

The generator emits the same bytes to `upstream/types.go` and
`kit/acp/types.gen.go`. The second output lets the independent kit module use the
wire types without depending on the Core module. The ACP package's regeneration
test checks both outputs in CI.

Structs, string enums and tagged unions come from the schema. Optional fields
use pointers and `omitempty`. Tagged unions emit exactly one branch and retain
unknown variants as raw JSON. Untagged unions, including string/integer/null request
IDs and compound elicitation scopes, remain raw JSON; their object alternatives
also have generated structs. Arbitrary JSON is never converted through float64.
These are wire types, not JSON Schema validators. The client checks the fields
it consumes. Higher layers validate user input and enforce approval policy.
