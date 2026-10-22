# Codex app-server protocol types

This is a **schema baseline**, generated with installed Codex **0.163.0**.
It does not declare that version runtime-verified. The session and approval
round trips are tracked by #132 and #133.

`upstream/codex_app_server_protocol.schemas.json` is the unmodified experimental
bundle. `upstream/types.go` is generated from the request/notification envelopes,
the methods and responses required by the Codex adapter contract, and their
reference closure. `vendor.json` records every included file, the generators'
versions and SHA-256 digests, and the exact generation commands. The upstream
license and notice are preserved. There are no patches.

Run `node third_party/codex-app-server/generate.mjs --check` to check the Go
projection without starting Codex or accessing the network. Worker tests run
this check in foundation. To sync, generate a fresh schema with the selected
Codex binary, regenerate types, and update the inventory and notices together.
The installed worker is never installed or upgraded by this process.

The generator is LINA-owned tooling outside `upstream/`. It emits structs,
string enums and constants, optional fields as pointers with `omitempty`, and
nullable required fields without `omitempty`. Tagged unions have typed branches
and custom JSON methods that emit exactly one variant; unknown variants retain
raw JSON for compatibility with unverified versions. Untagged unions (including
string-or-integer request IDs) and unconstrained JSON remain `json.RawMessage`,
so no numeric precision or worker-specific content is lost. These are wire
representations, not JSON Schema validators. Adapters validate the fields they
consume and classify unknown signals without treating them as successful work.
