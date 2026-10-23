# kit/provider/responses implementation notes

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-44 Responses increment

#54 implements `kit/provider/responses` with `net/http` and small JSON/SSE wire types.
`responses.ReadConfig` reads `providers.chatgpt` from the build manifest through the transport-independent `responses/config` package, which Core's manifest reader also uses; the adapter rejects an absent required field, an unsupported field, a different adapter identity, or a nonzero retry count.
`Request.Fields` may narrow that list but never widen it.
It sends only the manifest-approved fields, keeps input items and tool declarations byte-for-byte and in order, and rejects system-role items.
`apply_patch` is a custom tool or a function accepting one required string.
Auxiliary requests reject tools and omit separate instructions.
A stateless request asks for `reasoning.encrypted_content` when the request carries the selected reasoning-state capability, including when effort is empty and the model uses its default.
A route whose admission narrows that capability away sends the same request without the include option, so the recorded capability evidence and the wire agree.
A field list that excludes `include` refuses only a request that selected the capability; the adapter never silently loses selected reasoning state.

### C-44 adapter acceptance

Scripted wires establish these adapter invariants, and clean-build tests establish manifest production and validation.
The adapter is not yet reachable through Core's conversation protocol or the TUI, so this increment has no observable scenario under `e2e/m1/`.
It does not establish live OAuth, entitlement, or V1 acceptance of the provisional request-field list.
