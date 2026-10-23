# kit/acp implementation notes

## Implementation notes

Design authority: [Go Core](../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-60 generated client baseline

The shared client uses the stable `schema-v1.26.0` schema.
Its generated baseline in `third_party/acp/upstream/types.go` and kit copy are byte-identical and checked by regeneration.
