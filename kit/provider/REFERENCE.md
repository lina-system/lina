# kit/provider implementation notes

## Implementation notes

Design authority: [Go Core](../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-43 provider increment

#53 implements this boundary in `kit/provider`.
Credentials are per-call, omit their bearer from JSON, and print only a placeholder; `Authorize` applies the bearer to the HTTP request without returning it.
Model-list network calls also require a lease.
The future secret-store adapter supplies these credentials.
Items and tool declarations retain provider JSON so opaque state can round-trip without reconstruction.

### Capabilities and cache metadata

#877 adds per-model `Capabilities` declarations. The Responses adapter reports
its existing reasoning-state preservation; Claude reports an empty set until
its cache wire increment is accepted. `capabilities.json` is the embedded route
admission policy copied to the build manifest. It admits only existing Responses
reasoning-state behavior, on the ChatGPT and explicit API-key routes.

Core computes cache identity separately from transmitting it. Assembly records
exclusive item/tool positions in `RequestLayout`; turn preparation refreshes
follow-up positions and selects admitted values before recording the digest.
The merged #878 layout places the input before the ContextPacket. Boundaries
identify that input independently of packet length. #880 owns the ChatGPT cache
key, and #881 owns Claude prompt blocks and marks.

`Usage.CacheByLifetime` preserves independent read/write pointers by lifetime.
Older aggregate values are retained without inventing bucket counts or adding
cache subsets to budget totals. Legacy evidence omits the new metadata and
retains its original digest. Replay restores metadata from new evidence.

Verification uses synthetic adapters and installed headless Core. It does not
claim live V1/V4 acceptance, cache savings or first-token improvements.

### On-device Apple model

`apple` is admitted with an empty optimization set. Its context size, tool
calling, guided generation, and image input are a helper profile, not entries
in `capabilities.json`. The helper protocol is one JSON line in and one JSON
line out (`profile` or `complete`). Guided generation is the schema field;
Core discards a schema miss and does not parse a code fence from this adapter.
The Swift helper is `adapters/applefm`. Headless tests use a fixture with the
same line protocol. The measured samples are in
[apple-foundation-models.md](../../docs/research/apple-foundation-models.md).
