# Client handshake skeleton

`protocol` contains handwritten Go types for the `client` 1.0 handshake in
[envelope.json](../../protocol/schema/client/1.0/envelope.json). G1-17 replaces
them with generated types and adds the full client schema and conformance runner.
This package implements the existing [envelope](../../docs/design/host-protocol.md#envelope)
and [handshake](../../docs/design/host-protocol.md#versions-and-capabilities) contract.

Use `encoding/json` with `protocol.Envelope`. Decoding checks the exact fields,
including nested objects, and selects `Hello`, `Welcome`, or `ErrorPayload` for
`Payload`. Encoding applies the same checks. All fields are required. Nullable
fields use pointers; required arrays must be non-nil, including when empty.
Round trips preserve JSON values, including timestamp and epoch strings; JSON
whitespace and object key order are not significant.

The schema gives each capability a `name` and `version`. `hello` lists
`protocol_versions`; `welcome` names the selected `protocol_version`, `main_id`,
`epoch`, `person_id`, and `agents`. An agent carries `agent_id`, `name`, and
`preset` (null for one created from a description). An `error` has an empty
payload and uses the envelope's `error.code` and `error.detail`.

The [fixtures](../../protocol/fixtures/client/1.0/) contain a hello and its welcome,
plus an alternative `unsupported_combination` reply when the peer has no supported
combination. They use synthetic identifiers. The handshake grants no capabilities
in this skeleton. The welcome covers agents created from both a preset and a
description. The fixture files are wire data, so they contain no extra metadata.

This package checks wire shapes and values. Core's handshake service will select
the highest supported version, grant capabilities, check consistency between
identities and epochs, record outcomes, and close a refused connection. JSON-RPC
framing, that service, supported-combination evidence, and non-handshake client
kinds are outside this skeleton.
