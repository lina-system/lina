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
The current ContextPacket order is unchanged here; #878 owns its relocation,
#880 owns the ChatGPT cache key, and #881 owns Claude prompt blocks and marks.

`Usage.CacheByLifetime` preserves independent read/write pointers by lifetime.
Older aggregate values are retained without inventing bucket counts or adding
cache subsets to budget totals. Legacy evidence omits the new metadata and
retains its original digest. Replay restores metadata from new evidence.

Verification uses synthetic adapters and installed headless Core. It does not
claim live V1/V4 acceptance, cache savings or first-token improvements.
