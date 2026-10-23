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
