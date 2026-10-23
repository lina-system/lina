# internal/core/accounts implementation notes

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-47 account store increment

#80 implements `internal/core/accounts` as a callable Core store.
One store owns each fenced canon writer, and one actor per account serializes credential admission, replacement, status changes and scheduled refresh.
Account metadata lives in canon; the person/provider/account key selects one opaque access-and-refresh record in the secret store.
No token enters canon or the account projection.
`Credential` returns only the requested account's opaque provider credential.
The provider credential keeps its bearer as `secret.Secret`, including under nested formatting, and `Authorize` applies it only through `secret.InjectHeader`; the constructor and printable representations retain their existing API.
ChatGPT OAuth admission also requires `chatgpt.tokens.use.direct`.

### C-47 sign-in integration

The store tests use real canon and private secret files, injected issuer/storage failures and a virtual clock.
#81 connects the sign-in service and fixed-issuer refresh exchange to this store, installs client relay operations, and exercises owner binding through the real client socket in `e2e/m1/chatgpt-signin.json`.
Live OAuth and request-surface acceptance remain pending the owner-run [V1 check](../../../evidence/v1-chatgpt.md); provider-side revocation remains separate from local sign-out.
The owner-run probe records every undocumented field V1 names, including `prompt_cache_key`, and refuses to record an accepted cache identity unless the value has the accepted shape.
It sends a fixed probe-only identity and never a conversation's identity, and it never adds a field to the production request field list.

### C-47 installed account services

#112 wires this store and the existing sign-in relay into installed Core even without a configured conversation model.
SignInSchema is fixed at version 16 in canon, after the published turn and compaction migrations.
The composition opens one secret store and one account store on the same fenced canon writer.
The production issuer remains fixed; only the in-process test composition injects an HTTP transport.
The client combination advertises accounts and relay independently of conversation.
Core startup, backup, restore and the read-only doctor share `boot.ApplicationMigrations`, which returns the fixed canon catalog, so each recognizes the account schema installed by the same binary.
