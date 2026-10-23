# Core A2A publication

Design authority: [Integrations](../../../docs/design/integrations.md#a2a),
[authority](../../../docs/design/main-authority.md#remote-a2a-agents) and
[host protocol](../../../docs/design/host-protocol.md#a2a-and-the-node-connection).

## Source boundary

Seen: A2A v1.0.1 `specification/a2a.proto` defines the 1.0 JSON method names,
message/part/task shapes, enum names, wrapped send/stream responses, optional
history limits and security requirements. The versioned specification defines
errors -32001 through -32009 and distinguishes stream loss from cancellation.

Inferred: publication is an authenticated admission and projection boundary
around Core's existing conversation turn, effects, worker dispatcher, review,
canon and result journal. It must not expose those private protocols or infer
execution authority from a card or incoming message.

Unknown: interoperability with an independent third-party A2A implementation,
a deployed identity provider and a physical two-host tailnet. Controlled peers
can prove the local boundary, not those deployment properties.

This implementation adds no SDK. It does not port Hermes, Honcho, LCM-X or CRW
runtime behavior; the existing Core work machinery remains its implementation.
The handwritten wire subset follows the pinned proto, not older SDK examples.

## Configuration

Publication is absent by default. The owner saves a private JSON configuration
with `lina a2a --state-root <installed-root> --config <file>` while Core is
stopped. `--credential-stdin` reads the external issuer's introspection client
secret without putting it in command arguments or configuration. Core's existing
secret store retains the issuer-bound HTTP Basic credential under the owner,
issuer/client-specific service key and configured `secret_name`. Changing the
issuer or client requires a newly supplied credential; a saved name alone
cannot authorize forwarding an old credential to another destination.

The configuration has `enabled`, `address` (literal Tailscale IP:port),
`tailscale_socket`, absolute `certificate` and `private_key` files, `oauth` and
`publications`. `oauth` has `issuer`, `audience`, `introspection_url`, `client_id`,
`secret_name` and required `scopes`. The introspection endpoint must share the
issuer's HTTPS origin. It must return active Bearer access tokens with issuer,
audience, expiry, subject, client identity and scopes. Responses are uncached;
redirects and ID tokens are refused. Credentials never enter task records.

Each publication declares `id`, `agent`, owner `conversation` (`main:<agent>`),
public `name`, `description`, HTTPS `url` ending in `/a2a/<id>`, `definitions`
and `bindings`. A binding contains exact `subject` and `client`, allowed
`definitions`, an absolute Unix `expires` and `max_active` between 1 and 16.

Each definition declares `id`, public `name`/`description`, fixed `tools`, a
complete existing dispatcher `work` definition, an `artifact` rule, `seconds`
(1–3600), and `max_calls` (1–32). Tools currently use Core's named record reads,
record writes and web search/extract handlers, with exact JSON arguments.
`dispatch_work` is supplied from the fixed work definition. It cannot be
substituted with a free-form worker or shell tool. Each operation runs at most
once. The input only extends the work description; repository, design sections,
write area, branch, worker, tools and limits remain owner-selected.

The artifact rule names a relative committed `path`, `media_type`, required
`contains` content and `max_bytes` (at most 256 KiB). Completion additionally
requires the existing verified manager decision, matching worker receipt and
PR head, green checks/review, an in-scope changed file and reopening that exact
commit's bytes. The artifact is hashed and retained before projection. Neither
provider prose nor worker exit success can satisfy those checks.

## Transport and storage

The separate TLS listener uses the existing tailscaled local address check.
It registers no client, Node, or administrative route. Card discovery is at
`/a2a/<id>/.well-known/agent-card.json`; RPC is at `/a2a/<id>`.
Requests carry `A2A-Version: 1.0`. The input is one `application/json` data part:
`{"task":"<definition-id>","input":"<bounded task input>"}`. This is application
data, not a new protocol skill selector. Result bytes are an A2A raw part;
authorized downloads use `/a2a/<id>/artifacts/<task>/<artifact>`.

Migration 56 appends task and message-dedup tables. No existing data is rewritten
or deleted. Older binaries retain the existing newer-schema refusal. Publication
can be disabled independently of retained work and results. Task/context IDs
are opaque; message IDs are scoped to publication and issuer/subject/client.
Canonical hashes ignore JSON object ordering and response-only configuration.
Claims survive restart and are never reset to queued.

Private task conversations use `a2a:<task>` with forwarded, untrusted input.
Only allowed tools and the fixed dispatcher definition reach the provider.
Owner approval uses the existing worker bridge; remote callers cannot answer it.
Before any task input reaches a provider or tool, the owner must additionally
approve that exact external request for installed-worker execution. The card
includes its input, fixed definition and deadline and explicitly describes the
worker's access to owner files, login and integrations. `approvalRequest` derives
the facts from retained admission; `approved` checks the consumed native answer,
owner, epoch and identical arguments. Missing surfaces, denials and stale epochs
refuse execution. This boundary requires explicit owner delegation; it does not
claim that the installed worker runs in a sandbox. Later worker approvals remain
independent. An intake-denied acceptance case requires zero provider/tool/worker
execution.
Owner typed result, result head and delivery marker commit together. Reads,
SSE frames, lists and downloads recheck current binding ownership and definition.
An expired/changed binding blocks execution and disclosure; worker launches and
approval replies have additional checks. Deadlines cancel both turns and workers.
An unconfirmed stop remains unknown, preserving the prior work identity.

List cursors are authenticated against caller, publication and query and expire
when the serving process restarts. Streams begin with snapshots and reconnect
through SubscribeToTask; disconnecting never re-admits or cancels work.

## Evidence

Focused tests cover access-token claims, redirects, canonical send deduplication,
caller isolation, protocol version, card filtering, task claims and recovery,
fixed tool/worker scope, and offline owner configuration.
The installed-Core scenario is `e2e/m3/a2a-publication.json`, driven by
`TestM3A2APublicationInstalledCore`. It checks a committed Core record operation,
a worker's file and Git commit, verified artifacts, isolated reads/downloads,
duplicate sends, stream loss, owner delivery and restart. Additional cases
exercise unsupported completion claims, an owner-denied worker approval,
uncertain cancellation and revocation while a worker is running. The identity
and provider peers are controlled fixtures; installation, Core, tools, worker
processes, Git effects and durable result delivery use their ordinary paths.
