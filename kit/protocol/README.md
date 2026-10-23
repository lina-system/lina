# Client protocol types

[envelope.json](../../protocol/schema/client/1.0/envelope.json) is the canonical
client 1.0/1.1 schema (the original path is retained). It defines the envelope, the closed kind list, and the payload
fields already specified by the [host contract](../../docs/design/host-protocol.md).
`client.gen.go` contains the generated Go types, enum constants, payload dispatch,
and the schema used by the codec. Do not edit it by hand.

```sh
go generate ./kit/protocol
go test ./kit/protocol/...
node scripts/foundation.mjs
```

The generator uses only the Go standard library. The components check regenerates
in memory and compares the bytes with the checked-in file before building kit;
it fails on drift or a missing file and never rewrites the candidate. For a direct
comparison, run `go run ./internal/generate -check` from this directory. New schema
keywords must be supported by the generator and codec together: unrecognized
keywords, dialects, formats and references fail generation.

Use `encoding/json` with `protocol.Envelope`. Decoding validates against the
schema before selecting the concrete generated `Payload` type; encoding applies
the same validation. Unknown fields, missing required fields, invalid enum values
and invalid nulls fail, including inside nested objects. Envelope fields remain
required, nullable fields use pointers, and required arrays must be non-nil.
Optional payload fields use pointers with `omitempty`. Numbers use `json.Number`
to preserve their value without a float64 rounding step. Object key order and
JSON whitespace are not significant.

The original `Hello`, `Welcome`, `ErrorPayload`, envelope, reference and closed-code
API remains available. The [handshake fixtures](../../protocol/fixtures/client/1.0/)
round-trip without changing JSON values. A question's flat form has `plugin_name`
and `fields`; each field has `name`, `type`, and `choices` for a choice. A question
answer has `answer` (`accept`, `decline`, `cancel`), with a `values` map only for
accept. Its generated `FormValue` holds exactly one string, number or boolean.

## Kind inventory

The closed kind list is `properties.kind.enum` in
[envelope.json](../../protocol/schema/client/1.0/envelope.json). Every kind has its
own generated payload type. Names use snake case on the wire and exported Go
names, for example `output_delta`, `KindOutputDelta` and `OutputDelta`.

## Reserved shapes

Payloads whose fields the contract defers are closed empty objects. Each has a
`$comment` naming the corresponding [Deferred item](../../docs/design/host-protocol.md#deferred).
They generate distinct Go structs and can round-trip `{}`, but accept no invented
fields. A kind's presence is not a capability grant or an implementation of its
behavior. The conversation implementation supplies these fields later.

The concrete shapes cover hello/welcome/error, the starting agent, approval
choices, the question form and answer, the specified result card/get/edit/save
fields, and the readiness step. Result bodies, saved references and destinations
are reserved objects until the results issue supplies their schemas and size
limits. This does not make results usable with real bodies yet. The codec checks
result revisions, the preview/saved reference shape, and the save key; whether an
edit targets a current, editable result is a Core state decision.

The package handles wire shape and values. JSON-RPC framing, negotiation,
authorization and live conversation or result behavior belong to their
implementation issues. The schema evaluator implements the vocabulary used by this schema, not an
arbitrary draft 2020-12 validator.

## Receive boundaries and conformance

`ClientDecoder` ignores and counts a well-formed unknown kind, returns no message
for it, and counts malformed envelopes separately. For a known kind it discards
additive payload fields, including nested fields, before strict validation of
known data. Unknown snapshot item kinds are omitted after validating their common
metadata, without changing the cursor or frame counters. Envelope fields remain
strict. This behavior requires the updated receive boundary; an already installed
older strict binary still rejects new payload fields. Core therefore sends the
new activity shape only after negotiating `turn_activity` 1.0 and preserves the
legacy projection for other connections. Each connection owns a decoder
in its receive loop. `internal/clientapi.RequestDecoder` uses the strict codec and
returns a typed `malformed` refusal without a partial request. Neither decoder
negotiates versions, authorizes a request or implements application state.

The reusable `conformance` package loads the
[client 1.0 corpus](../../protocol/fixtures/client/1.0/README.md). `Suite.RoundTrip`
accepts an implementation's codec; `Suite.Run` accepts its receive adapter and
counter snapshot function. Case files specify input and output paths, per-input
error codes and final counts. See the package tests for the Go client adapter and
`internal/clientapi/conformance_test.go` for Core's adapter. Other language
implementations can consume the same JSON files.

```sh
go test ./kit/protocol/... ./internal/clientapi/...
```

Foundation selects kit and the Core adapter when fixtures change. The corpus
includes every currently defined kind, the closed effect and error codes, result
cards of every current type, exact numbers and references, version declarations,
and malformed/unknown-kind recovery sequences. Reserved payloads and result
bodies still need their application scenarios when their implementations arrive.

## Generated cards (client 1.1)

`surface_body` and `surface_widget_*` define the closed `lina.cards` 1.0 catalog.
`ValidateSurfaceBody` and `ValidateSurfaceComponent` expose that same schema to
Core; `kit/result` checks graph, bindings and action semantics and folds A2UI
batches. No renderer is a validation authority. The fixtures under the existing
1.0 corpus include explicit 1.1 frames and refusals for 1.0 surface frames.
`surface_action` is an authenticated input request, never an approval.
