# Outbound remote agents

Core owns the HTTPS A2A 1.0 client. The wire contract is the versioned A2A
v1.0.1 proto; no SDK is added. The package has no listener and exposes no Node
capability. Boot registers `remote_agent` through the ordinary tool registry.

Owners configure connections through the `a2a` client capability. The installed
`lina a2a --socket <path>` command reads one settings request from stdin, so
credentials do not enter command arguments or conversation history. Operations
are `configure`, `list`, `revoke`, `credential`, `sign_in`, `calls` and `answer`.
The canonical request schema is
`protocol/schema/client/1.0/envelope.json`; Go client types are generated.

A connection fixes the card URL, HTTPS origin, authentication scheme and task
profiles. Each profile names an effect class, input prefix/byte limit, output
media type/byte limit and expected SHA256. The expected hash is a local done
condition for the requested artifact. It does not prove unrelated effects on a
remote system. Card descriptions cannot change this classification. Unknown
classification asks for approval; restricted presets still refuse it.

The full card is admitted and hashed, including endpoint, tenant, authentication
and scopes. A changed card needs owner confirmation. Each send stores its exact
message, stable id, connection snapshot, profile, owner/persona/conversation,
epoch, grant id and deadline before HTTP. A successful tool receipt acknowledges
this local observation, not remote execution. A lost response leaves an unknown
call; a new model call id cannot repeat unresolved work with the same input in
the conversation. Different connections do not evade this reservation.

Task snapshots retain remote task/context ids before further reads. Interrupted
SSE recovers through `GetTask`, never another send. `status` can also subscribe
to an existing task. Every transmission rechecks current authority and current
connection admission. Observation after restart uses a fresh conversation grant
and keeps the original result provenance. Restart publishes already verified,
undelivered results; it does not resume old grants or send work automatically.

Input waits publish a question and a waiting row. Only the owner's recorded
answer permits a follow-up with the existing task/context ids. Authentication
waits direct the owner to connection settings. OAuth authorization-code/PKCE
uses the existing client loopback relay, a configured issuer/client registration,
one session-bound attempt, an issuer check, bounded expiry and the secret store.
No provider login, remote message, or task text supplies credentials. Expired
OAuth tokens require sign-in; refresh-token exchange is not implemented.

Network checks require HTTPS, no URL userinfo/fragments, one admitted origin,
no redirects or proxy inheritance, and checked IPs at dial time. Private and
loopback destinations require an owner exception naming exactly that host.
Metadata, link-local, multicast and unspecified addresses remain forbidden.
The same policy covers card, interface, OAuth and artifact access. Artifacts
cannot widen the origin or forward a credential. Credentials reflected by a
peer are refused before retaining evidence.

Artifacts retain bytes, media type, source origin, observed time and SHA256.
Parts of an artifact are assembled in order. Only content matching the admitted
done condition yields a result. Text appears in the existing typed result
journal; binary content uses the existing material writer and file result.
Result head insertion is transactional and deterministic per admitted call, so
restart cannot duplicate it. Remote failure and cancellation remain observations;
a lost cancel response remains `cancellation_unknown` and promises no reversal.

The migration appends connection/call tables to canon. It rewrites no existing
rows, runs no backfill and removes no data. An older binary refuses the newer
schema instead of interpreting the new state. All test state is disposable.
