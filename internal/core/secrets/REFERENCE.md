# internal/core/secrets implementation notes

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-15 masking increment

The masking/store increment (#48) supplies `internal/core/secrets.Mask` as the native-text admission boundary, within the issue's package scope.
It replaces common provider-token shapes, credential assignments, bearer/basic credentials, private-key blocks (including an unfinished block), and caller-flagged secret spans.
Its immutable `Masked` value contains only replaced text and `secret` spans using half-open UTF-8 byte offsets in that text.
`Text` is the display/storage projection; `DerivedText` excludes the spans for indexes, summary inputs, ContextPackets and exports.
Serialized records retain the flags and reject unmasked recognized credentials on read.
Detection is heuristic: callers must pass explicit sensitivity spans for credentials without a recognized shape.
The future ledger, assembly and egress paths must use this boundary; the current protocol still has no conversation capability.

### C-15 credential integration status

OS-keychain platform adapters, credential forms and turn/boot wiring are not exposed by this increment.
