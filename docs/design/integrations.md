# Integrations

This contract fixes how LINA connects to external services. LINA's conversation engine reaches a tool service through a plugin: one installable integration unit with a manifest. LINA ships first-party plugins for GitHub and Google and a small reviewed catalog of others, and the person can add any other MCP server. LINA Core hosts every plugin for every agent, holds the credentials LINA itself obtains, marks the tools that need approval and records every write. The contract covers the plugin system and its catalog, credentials and sign-in, the rules every plugin follows, the channels through which agents talk with the person on external messaging platforms, the messenger sources through which LINA reads the person's own messenger accounts, and the GitHub and Google plugins. It is normative: implementations must follow it, and any change to it is made in this file and joins dev under [integration.md](../policy/integration.md). Turning on a plugin is the person's act. A first-party plugin or a catalog entry is part of a release, never a change to this contract.

Remote agents use the separate [A2A boundary](#a2a), hosted by Core alongside
the plugin and channel integrations.

## Scope

This contract covers:

- the plugin manifest, the catalog, where else plugins come from, installing, turning on and removing them
- optional memory importer add-ons, one adapter per source, and their source-read grants
- LINA Core as the host of plugin tools, and the questions a server asks the person
- service profiles and which plugin tools and commands need approval
- credentials, LINA's secret store and sign-in, including OAuth to remote MCP servers
- the rules every plugin follows: field ownership, change signals, external evidence, writes and failures
- channels: connections to external messaging platforms, their credentials, pairing, bindings and scopes, and the adapter boundary
- messengers: the person's own messenger accounts as sources, the platforms and how each is reached, the messenger add-on, sync and coverage, and writes by grant
- A2A: publishing a LINA agent and calling remote agents, including calls on behalf of Nodes
- the GitHub plugin: the user's `gh` login and git setup, the command profile, LINA Core's own reads, field ownership in a connected part and the GitHub-specific merge rules
- the Google plugin: the built-in connector, the user's own OAuth client, accounts, areas, consent and token failures

No external planning service is canonical or required ([product-families.md](product-families.md)). The one-time takeover of planning records at the operational switch is defined in [work-and-delegation.md](work-and-delegation.md).

This contract does not cover:

- the role rule itself (no exclusive owner; enforced pre-merge safety and result records): [product-families.md](product-families.md)
- grants, approval, standing grants, receipts and observed results: [main-authority.md](main-authority.md)
- approval of channel actions and who may answer an approval in a channel: [main-authority.md](main-authority.md#channels)
- the envelope, effect states, idempotency keys, and the sign-in relay on the `client` connection: [host-protocol.md](host-protocol.md)
- plugin tool names and declarations, the effect ledger, the sandbox and its network rule, how a local plugin server is sandboxed, and tools the user installs: [runtime.md](runtime.md)
- workers, which use the integrations connected in the worker itself, and delegated work, development mode, the merge slot, the protected-ref check and apply records: [work-and-delegation.md](work-and-delegation.md)
- where the plugin registry and LINA's secrets live: [filesystem.md](filesystem.md)
- meeting notes, connected sources in the library and deletion propagation: [materials-and-knowledge.md](materials-and-knowledge.md)
- screens, including the plugin catalog, the connect card, the plugin list and the channel connections in settings, approval cards, code view, CI status, the today feed and the agent talk log: [surfaces.md](surfaces.md)
- Klotho's repository supervision and its judgments: [cognition-and-life.md](cognition-and-life.md)
- third-party plugins outside the dependency gates: [dependencies.md](../policy/dependencies.md)

## Plugins

### Manifest

A plugin's manifest states:

- its name, version and description
- its connection: an MCP server, either a local command (stdio) or a remote URL (Streamable HTTP), or a built-in connector whose code ships with LINA. A local command is stated with its arguments, its working directory and the names of the environment variables it receives. A catalog entry is always a remote URL on Streamable HTTP. HTTP with SSE, which the current MCP specification deprecates, is available only as the explicit choice of a person who adds a server by URL, never as a catalog default.
- its authentication: an OAuth sign-in that LINA performs, an API key the person enters and LINA sends as the request header the manifest names, or the login of a command-line tool it relies on, such as `gh`
- for a server that does not publish the metadata the current MCP specification asks for: the authorization server to trust, the initial scopes, a pinned OAuth client id, and the protocol era of a known older server
- the longest time a call to one of its tools may run before LINA Core moves the call to the background (see Host)
- optionally, skills and instructions for using it
- optionally, a service profile (see Service profiles)

Built-in connectors exist only in first-party plugins. A third-party plugin connects through an MCP server.

### Sources

- First-party plugins ship with LINA: GitHub and Google. They appear in the catalog as available and carry the label "Reviewed by LINA".
- The catalog holds reviewed third-party plugins that a person can turn on without writing anything. It is data, not a service: a small curated file, about twenty entries at first, signed and shipped in each release. LINA Core may replace it with a newer signed file from the project's release assets, by the route LINA's own updates use ([runtime.md](runtime.md)), and accepts a file only when its signature verifies against the key the installed release carries and its version is newer than the one in use. No LINA server answers catalog queries. The project owner curates the catalog and holds its signing key.
- A catalog entry holds a manifest, a service profile written by LINA, the pinned OAuth client where the service needs one, and its label. An entry is admitted only when all of these hold: it is the vendor's own remote server, reached over HTTPS and Streamable HTTP; it signs in with OAuth and publishes protected resource metadata, or the entry pins the authorization server; the official MCP registry lists it under a namespace the vendor owns that matches the entry's URL; and its service profile names the tools that send, pay, publish, share or delete. Local command servers are not in the catalog.
- Two labels are shown to the person in these words: "Reviewed by LINA" for a catalog entry and a first-party plugin, and "Added by you" for a plugin from any other source. A third label, "Listed in the MCP registry, not reviewed", is reserved for searching the official registry from the add flow, which the first release does not offer. LINA has no other verification labels.
- The person adds any other plugin one at a time: from a local folder or a Git URL that holds a manifest, from a remote MCP URL, or from a local server command. When the person gives a remote MCP URL or a local server command, LINA writes a minimal manifest for it.
- A server definition that arrives inside a folder, a Git URL plugin or a worker's workspace turns nothing on by arriving. It needs the same view of its manifest and the same approval from the person as any other plugin, and credential variables in it expand to empty.
- LINA never searches for, reuses or imports the configuration of other apps, such as the MCP servers or plugins set up in Codex or Claude. Plugins come only from the sources above.
- A messenger source is not a plugin. Its adapters ship with LINA, and the messenger add-on is a component pinned by the release (see [Messengers](#messengers)).

### Installing and turning on

Installing and turning on a plugin are the person's acts. LINA never installs or turns on a plugin by itself.

- Before installing or turning on a plugin, LINA shows its manifest as a card with the same fields for every plugin, in plain language: who makes it and its label; where it runs, which is the vendor's server or this computer; what the person signs in with and what the service will ask for at sign-in, in plain words, with a note that the sign-in page will show a loopback address; what it can do, in a line; what it can send, share, pay, publish or delete, which asks every time; and the pinned version, which is the version of the entry or manifest and, for a plugin from a Git URL, its commit. What asks every time comes from the service profile. For a plugin without a profile the card says that LINA does not know which tools send, pay, publish or delete. A tool the server marks `destructiveHint` shows "may delete" on the card as a hint only. A first-party plugin whose sign-in needs the person's own client, as Google's does, shows its guided setup steps on the card.
- A person turns on a catalog plugin by opening its entry, reading the card, connecting and signing in. Nothing in this path asks the person for a command, a URL or a file.
- A plugin the person adds shows the same card with the label "Added by you". For a local command the card shows the whole command, without truncation, labeled as running with the person's permissions, with the environment variables it receives (the values of credentials are hidden) and the sandbox allowance in Host. For a URL the card also says that the server can change its tools at any time.
- LINA records the source and version of every plugin the person adds, the commit for one from a Git URL, and the person who added it. A plugin changes only when the person updates it.
- A plugin is in one state: available, installed (off), on, needs sign-in, or failing with its cause. A plugin that is on also shows when its tools changed since the person turned it on (see Tool classification).
- Skills and instructions a plugin brings join LINA's skill catalog while the plugin is on ([conversation-and-memory.md](conversation-and-memory.md)).
- Removing a plugin turns it off, deletes the credentials LINA holds for it (a client registration kept for an authorization server is deleted with the last plugin that uses that server) and removes its skills from the catalog.
- Nothing about plugins is asked at first run. When a plugin would help in a conversation, LINA may suggest it once. A suggestion the person declined or put off is not raised again.

### Memory importers

A memory importer is an optional add-on for a tool the person used before. It appears with the other add-ons, with its source, version, supported formats and required reads on its install card. Each source has its own adapter. None is installed or enabled by default, none is an onboarding step, and Core's conversation, memory and recall work fully with no importer installed. Installing one does not start a run or discover other apps' stores or configuration. The person chooses the source and starts the import.

Every adapter implements [Memory importer add-ons](conversation-and-memory.md#memory-importer-add-ons) and uses [import staging](filesystem.md#importing-another-memory-system). It runs as a background job, makes no model call, and submits writes through Core's existing write paths. Core owns the resulting canon, materials, legacy references and backups; removing an adapter leaves those usable without it. An importer copies memory records only: it never imports another tool's persona, skills, scheduled jobs, credentials, plugins or configuration.

The adapter set is:

| Add-on | What it would read | Status |
| --- | --- | --- |
| Codex | Person-selected rollout/thread records and Codex memory files, in independently verified formats. | Future; separate from the live external work memory port. |
| Claude Code | Person-selected conversation transcripts and memory files from a prior Claude Code workspace or export. | Future. |
| oh-my-opencode | Person-selected session records and memory artifacts from the prior tool's workspace or export. | Future. |
| Honcho | Person-selected workspace peers, cards, conclusions, sessions, summaries and messages through a verified read API or export. | Future standalone adapter. |
| Hermes | Per-profile lossless-context and host message databases, large-output sidecars and the install's Honcho person data. | First adapter; the owner's M1 continuity test for existing memory and work patterns. |
| murmur | A stopped murmur data home: its spool and config. Its messages become messenger records, not sealed imported conversations ([conversation-and-memory.md](conversation-and-memory.md#importing-a-murmur-store)). | Implemented in `internal/addons/importer/murmur`, with `lina import-murmur` as its installed entry point; the owner's move off murmur. |
| OpenClaw | Person-selected conversation/session records and memory files from a prior install or export. | Future. |

These entries define intended sources, not accepted schemas or available readers. Work now covers Hermes only. Its embedded Honcho reader remains schema-gated; listing a future standalone Honcho adapter does not enable it. Each future adapter must independently verify its formats and implement the common contract before it can be offered.

Grant needs:

- **Local source reads.** The person hands over named files or roots for the run, including sidecars when needed. That grants read-only access to those sources, not to other apps' configuration or arbitrary home directories. SQLite reads use the online backup API on a read-only connection; no source write permission is needed.
- **Server source reads.** The person supplies the server address and a read credential for that run. Only the adapter's verified read/query routes on that origin are allowed. The credential never enters imported records, model requests or worker jobs and is released when the reader closes. Hermes preserves its GET-only transport, redirect refusal and remote HTTPS requirement.
- **LINA writes.** The run may write its private staging and submit imported records and materials through Core under the person's handover and confirmed maps. Rehearsal writes nothing to canon. No grant authorizes source mutation, automatic promotion or a model call during import; later promotion and embedding retain their separate person-confirmation rules.

### Host

LINA Core is the host of every plugin. It is the MCP client of plugin servers, using the official [MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk) at a pinned version that implements the current specification and the fallback to earlier ones.

- **Protocol version.** LINA Core speaks the current MCP specification first. It sends the protocol version with every request, probes a local server with the specification's discover call and a remote server with a current-era request, falls back to the earlier initialize handshake when the server does not answer the probe, and remembers the result for each local process and each remote origin.
- **Plugin tools.** Plugin tools are tools of LINA's conversation engine, of LINA Core's own background features and, as the tools that its brief names, of a background run ([work-and-delegation.md](work-and-delegation.md#background-runs)). They are named and declared as defined in [runtime.md](runtime.md). An enabled plugin is available to every agent in every conversation; there is no per-conversation switch.
- **Workers never receive plugin tools.** A worker uses the integrations connected in the worker itself ([work-and-delegation.md](work-and-delegation.md)).
- **Capabilities.** LINA uses tools, tool annotations and elicitation, and declares only the elicitation capability, in form and URL modes. Once the pinned SDK supports it, LINA also declares the Tasks extension. LINA never declares sampling, roots or logging and declines an earlier-era request for them, so no server can drive model calls on the person's accounts.
- **Connections.** LINA Core connects when a tool is first needed. A remote server that is offline fails only the call that needed it. There is one connection or process for each plugin, shared by every agent. A tool list belongs to one account: LINA Core never shares a cached list between two accounts and honors the server's private cache scope.
- **Local servers.** A local server runs as a child process of LINA Core. A server the person added by command runs inside LINA's OS sandbox ([runtime.md](runtime.md)) with the network and file allowance the person grants on its card. A first-party plugin runs outside the sandbox, as does a catalog entry that needs more than the sandbox gives, and only after the person accepts its card. Where the sandbox is unavailable, a server added by command does not start and the plugin shows failing with that cause. Control also sits at the tool call: the approval of outward or irreversible effects and the effect ledger.
- **Long calls.** A call that can take long runs as a background job of LINA Core, bounded by the time its manifest states, and reports in the running list when it lands. Once the pinned SDK supports the Tasks extension, such a call uses the task id and poll interval the server returns, and a task that needs input becomes a question card. LINA Core waits for the SDK and builds no Tasks support of its own.
- **Skills.** Skills and instructions that a server serves through the Skills-over-MCP extension are not loaded. Skills come only from the manifest or plugin folder the person saw at install.
- LINA doesn't show MCP Apps interfaces (see Deferred). It uses the tool's text result.

### Questions from a server

A server asks the person through elicitation. LINA Core presents the request as a question in the conversation whose tool call raised it ([conversation-and-memory.md](conversation-and-memory.md)). A question raised inside a background run stops that run in the state `needs_input` ([work-and-delegation.md](work-and-delegation.md#background-runs)).

- **Form mode.** The client shows the flat schema as a small form in the LINA app and as prompts in the TUI. It names the asking server on every request, lets the person edit before anything is sent, and always offers decline and cancel. LINA never fills a field from memory or conversation without showing it. A form that asks for a password, key, token or payment detail is refused and the person is told, because a server must use URL mode for those.
- **URL mode.** The client shows the full URL with its domain highlighted and a warning for a punycode domain, asks for consent, never fetches the URL itself, and opens it through the system browser interface, never through a shell. The scheme rule of Consent through the client applies. URL mode is not MCP sign-in and does not use the sign-in relay.
- LINA Core does not rely on a signal from the server that a URL interaction finished. It learns the outcome by sending the original request again.
- **A call that waits for input never holds the conversation turn.** LINA Core parks the call as a background item with a question card and resumes it when the person answers. The request sent again after the answer is the same effect: the effect ledger keys it on the plugin, the tool and the argument hash ([runtime.md](runtime.md)).

### Registry

LINA Core keeps one plugin registry in canon ([filesystem.md](filesystem.md)). Each entry holds the plugin's manifest and source, the recorded version and commit, its label, its state, its account with the person the account belongs to and the account's subject or profile id, the authorization server it signed in to, the hash of its local command when it has one, the sandbox allowance the person granted to a local server, and a snapshot of its tools, each with a hash of its definition and whether the service profile marks it as outward or irreversible. A plugin has one account in the first release. Recording the account's subject lets a second account be added later without changing the registry.

## Service profiles

A service profile is data in a plugin's manifest. It states:

1. how LINA detects the login the plugin relies on, when it relies on one
2. which tools or commands are outward or irreversible effects, that is, they send, pay, publish, share or delete
3. how to name a write's target, and how to read the service again when a write's outcome is unknown
4. the provenance fields its results carry, such as a revision or an etag

The first-party plugins carry profiles. LINA also ships profiles for major messaging services such as Slack, Microsoft Teams and Discord; a plugin for one of those services takes the shipped profile. A plugin without a profile has no tool that LINA Core marks (see Tool classification). A catalog entry carries a profile that LINA wrote. When an entry reaches a gateway whose tool list differs from one person to the next, its profile names the outward or irreversible tools by name pattern. A messaging plugin acts in the person's own account when the model calls it. It is not a channel connection (see Channels): an agent's bot is the agent's own face on a platform, and a plugin send is the person's message. When the person also connected that account as a messenger source, the plugin's sends obey the chat's write grant ([Messengers](#messengers)).

### Tool classification

LINA Core marks a plugin tool or command as an outward or irreversible effect from one source: the plugin's service profile. A permitted call to a marked tool asks every time, with the choices once and deny ([main-authority.md](main-authority.md#plugin-tools)). Core first applies the [preset effect limits](main-authority.md#preset-effect-limits): profiles distinguish research and private drafts from investment orders and external or binding execution, and an unclassified operation cannot bypass advise-only or draft-only limits. Every other permitted tool of a plugin that is on is available to every agent, in the same way, and runs without asking. There are no tiers, plugin scopes or per-tool settings. An agent's work-method record changes which tool groups are declared first, never availability or effect classification; tools outside those groups remain reachable on demand.

Before that approval policy, Core applies [preset effect limits](main-authority.md#preset-effect-limits). A trusted service profile supplies an operation classifier over already validated arguments: a multipurpose service tool can classify its quote operation as research and its order operation as an investment order. Core-owned tools supply the same classification in their registered definitions. Schema validation runs first. Caller-supplied effect labels, server descriptions and annotations are never classification authority. Newly added or unprofiled operations remain unclassified and fail closed for advice-only and draft-only origins. Their availability to ordinary agents keeps the existing policy.

- Annotations that a server sends are untrusted and never mark a tool. `destructiveHint` is a display hint only.
- The shipped messaging profiles mark every send tool.
- A plugin without a profile has no marked tool. The card says so; restricted presets cannot dispatch an operation whose permitted effect Core cannot establish. For other agents, the agent rules still make an agent ask before it sends, pays, publishes or permanently deletes ([work-and-delegation.md](work-and-delegation.md#default-posture)).
- LINA Core stores a hash of each tool's definition: its name, description, input schema and annotations. When a server's tool list changes, or its cache lifetime ends and LINA Core reads the list again, a tool whose definition differs from the one stored when the person turned the plugin on is marked "changed since you turned it on" on the plugin card, which shows what changed. A tool the server adds is available like any other, and the profile still decides which tools are marked.

## Credentials and sign-in

A plugin's credential comes from one of three places:

- **A command-line login.** The tool keeps its own login, for example `gh`. LINA stores, issues and injects nothing for it.
- **An API key.** The person enters it in LINA. LINA keeps it in its secret store.
- **An OAuth sign-in that LINA performs.** LINA keeps the tokens, and for Google the client credentials, in its secret store.

LINA's secret store is the OS keychain. Where no keychain is available, it is files with mode 0600 in the secrets area of the state root ([filesystem.md](filesystem.md)). Only LINA Core reads it.

Model-provider accounts keep their credentials in the same secret store, under the same rules, and use the same sign-in relay. Their flows and records are defined in [runtime.md](runtime.md#accounts-and-sign-in). They are accounts of the model path, not plugins.

Every account and credential LINA keeps belongs to one person and is kept under that person's id ([product-families.md](product-families.md)). A LINA admits only its owner, so only the owner's exist. Agents own no accounts: every agent uses the person's accounts under the grants and approvals of [main-authority.md](main-authority.md), and every use records the agent. A bot account of a channel connection is the person's account too: the person creates it, LINA keeps its token under the person's id, and a binding only lets one agent speak through it.

Rules:

- A plugin's credential goes only to that plugin's server, in the way its manifest names (a header, or the server process's environment). It never enters a model request, a log or the sandbox.
- No token passthrough. A token goes only to the server it was issued for, never in a URL, and LINA never forwards a token it received to another server.
- LINA creates no service identity of its own, and never issues, injects or brokers a command-line tool's login. A command-line tool here is a tool that keeps its own login for the user, such as `gh`; the Claude adapter is LINA's own process and is not such a tool.
- A key that a power user's definition carries, as a request header or an environment variable, goes into the secret store when it is entered and is not shown again. It is expanded only into the request or the server process it is for. A definition exported as a file never carries secret values.

### OAuth for MCP servers

Sign-in to a remote MCP server follows the [MCP authorization specification](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization):

- LINA finds the authorization server through the server's protected resource metadata. When a server publishes none, LINA uses the authorization server the manifest pins. Without either, LINA does not sign in and the card says why.
- LINA registers as a client in this order and uses the first that works:
  1. the client the catalog entry pins: a public client id that LINA registered with that service, with no secret. LINA Core passes it explicitly and does not leave the choice to the SDK;
  2. a client ID metadata document;
  3. dynamic client registration with `application_type: native`, once for each authorization server, and reused, never once for each connection;
  4. a client id the person enters, with an optional client secret that LINA keeps in its secret store.
- The client metadata document is one static JSON file at an https URL on a domain the project owns. It lists the two loopback redirect URIs, declares no client authentication and carries LINA's name and logo. Serving a static file is not an inbound endpoint of LINA. Until the owner fixes the URL, step 2 is skipped.
- A service that offers neither dynamic registration nor a metadata document is used only through a catalog entry that pins a client the project owner registered with that service, or through the power user's own client (step 4). LINA does not register itself as a public client with it on its own.
- Client credentials are kept by authorization server and are never used with another one.
- Every authorization request uses PKCE with S256 and the `resource` parameter. LINA checks `iss` in the authorization response before it sends the code.
- The first request asks for the manifest's initial scopes, or for those of the server's 401 challenge when the manifest names none. A catalog entry asks for everything its tools need in that one consent: there is no read-only start. When a server answers `insufficient_scope`, LINA asks for the earlier scopes together with the new ones.
- Tokens are refreshed shortly before they expire and after a 401. When a refresh fails, including with `invalid_grant`, the plugin shows needs sign-in. LINA never falls back to another account, another client or a key found in the environment.
- LINA Core fetches protected resource metadata, authorization server metadata and metadata-document URLs over HTTPS only. It blocks destinations in private, loopback, link-local and cloud-metadata ranges, and redirects to them, using a vetted library. The one exception is a server the person added at an address on their own network or on loopback: that one host is allowed, and the card says "this address is on your own network".

### Consent through the client

LINA Core has no inbound OAuth callback endpoint, and a sign-in never happens inside the LINA app's own interface ([surfaces.md](surfaces.md#no-web-feel)). The optional A2A listener below is not a sign-in callback. Consent runs through the client the person is using, and the sign-in belongs to the person that client connection acts for ([host-protocol.md](host-protocol.md)):

1. The client opens a loopback port and reports it to LINA Core. A plugin definition can fix the port for a service that accepts only one registered redirect port, and the client then listens on it. LINA Core builds the authorization URL with that redirect, which uses the path that LINA Core names and the port the client reports, and keeps the PKCE verifier and the state.
2. The LINA app or the TUI opens the URL in the system browser and receives the redirect on that port.
3. The client relays `code`, `state` and `iss`, or the `error` fields of a failed sign-in, to LINA Core over the `client` connection ([host-protocol.md](host-protocol.md)). It relays and never judges.
4. Where a client can't receive a loopback redirect, such as on mobile or in a TUI over SSH, the person pastes the redirected URL instead.

A sign-in URL is never opened through a shell command. A URL with a `javascript:`, `data:` or `file:` scheme is refused. Google sign-in uses the same relay. The sign-in of a model-provider account uses the same relay ([runtime.md](runtime.md#chatgpt)). A URL that a server sends through URL elicitation is not a sign-in and does not use it (see Questions from a server).

## Plugin rules

These rules apply to every plugin.

- **Every field has one owner.** The data model names the canonical side of each field. LINA Core never treats its copy of an externally owned field as canonical. When the two disagree, LINA Core reads the external side again.
- **Change signals are hints.** A notification or a change feed tells LINA Core to read again. It isn't a record of what changed. Periodic reconciliation compares LINA's links and derived data with the external side and catches missed signals. LINA Core reaches plugin services with outbound requests only. No plugin service calls in to LINA Core. The channel gateway follows the same rule: it opens its own outbound connection to the platform, and no platform calls in. A signal can also start inside LINA, such as a worker's completion event, and is treated the same way: it starts a read and is not the record of what changed.
- **External content is evidence, not instruction.** Everything read through a plugin, tool results included, carries provenance: the plugin, the account, the object id when there is one, the fetch time, and the revision or etag when the service has one. It enters model requests as untrusted input ([conversation-and-memory.md](conversation-and-memory.md)). It never becomes a fact, an approval, a current instruction or a persona change by itself, and it never changes an approval decision ([main-authority.md](main-authority.md)).
- **Every write is an effect.** A plugin write is an effect that runs under the grant of its task or conversation, and an outward or irreversible one only after approval ([main-authority.md](main-authority.md)). The effect ledger ([runtime.md](runtime.md)) records the plugin, the account, the tool, the argument hash, whether the call was outward or irreversible, the approval basis (a grant, or approval of this call), the effect state ([host-protocol.md](host-protocol.md)), the conversation or task id, the agent that called the tool, and the person the write acts for. Its idempotency key takes the plugin and tool as the action and the argument hash as the intent, and a normalized target only when the service profile names one.
- **Unknown outcomes are never sent again.** When a write's outcome is unknown, LINA Core reads the service to settle it if the service profile says how. Otherwise it tells the person that the outcome is unknown.
- **A lost response stream is not a reason to send a write again.** The MCP specification tells a client to re-issue a request whose response stream was lost. LINA Core does so only for a tool that the server marks `readOnlyHint: true`, with a new request id. It never does so for any other tool: the outcome is unknown and is settled as above. Cancelling a write by closing the stream likewise leaves its outcome unknown, not undone.
- **A failing plugin never blocks conversation.** The plugin shows as failing, with its cause, and the rest of LINA keeps working.

## A2A

This M3 contract covers publishing a LINA persona and calling an outside
agent. It targets the [A2A v1.0.1 specification](https://github.com/a2aproject/A2A/blob/v1.0.1/docs/specification.md),
with **1.0** on the wire. Its versioned
[a2a.proto](https://github.com/a2aproject/A2A/blob/v1.0.1/specification/a2a.proto)
defines message shapes and ProtoJSON representation; this section defines
LINA's admission and behavior. The [research note](../research/a2a.md) records
source findings and limits. Core implements publication and outbound connections
through its existing conversation, effect, work and result paths. Both use the
standard library without an SDK dependency. The Node relay remains a separate
implementation scope.

A2A is a separate Core integration, not an MCP plugin, channel, worker adapter
or `node` connection. An A2A peer does not become a LINA person, client, Node
or local worker. The owner explicitly enables each publication or remote
connection. Discovering a card or installing a worker enables neither.

### Protocol profile

The first implementation uses HTTPS JSON-RPC 2.0 and SSE. Requests carry
`A2A-Version: 1.0`; cards declare `supportedInterfaces` with
`protocolBinding: "JSONRPC"` and `protocolVersion: "1.0"`. An incompatible
interface or required extension is refused. A missing version means 0.3 by
the specification and is unsupported here. There is no automatic fallback.
gRPC, HTTP+JSON and push notifications are outside this first profile. Published
cards declare streaming true, push notifications false and extended cards
false; unsupported push and extended-card operations return their specified errors.

| Operation | Published LINA agent | Calling a remote agent |
| --- | --- | --- |
| Agent Card | One approved card per persona at `/.well-known/agent-card.json` or its configured card URL | Fetch the configured card and select a compatible interface; carry its tenant when present |
| `SendMessage`, `SendStreamingMessage` | Admit durable Core work; return a task, then progress and artifacts | Record the outgoing effect before sending; accept a task or direct message |
| `GetTask`, `ListTasks` | Return only the caller's authorized task projections | Reconcile a known task; listing proves a send only if it unambiguously identifies that send |
| `SubscribeToTask` | Start with a current task snapshot, then updates | Reconnect without sending the initial message again |
| `CancelTask` | Ask Core to stop; report canceled only after execution stops | Ask the remote to stop; an acknowledgment cannot prove reversal of effects |

Success is `{jsonrpc: "2.0", id, result}` with the operation's specified
result. Send results wrap `task` or `message`; SSE results use `StreamResponse`.
Errors use `{jsonrpc: "2.0", id, error}` with numeric `code`, a short `message`
and optional typed `data`. States use the proto enum names, such as
`TASK_STATE_WORKING`. A LINA host envelope, grant, receipt or raw conversation
history is never an A2A response.

Outbound `SendMessage` requests set `configuration.returnImmediately: true`.
Inbound non-streaming `SendMessage` requests honor that field: true returns
after durable admission; false or absent waits for a terminal or interrupted
task as [section 3.2.2](https://github.com/a2aproject/A2A/blob/v1.0.1/docs/specification.md#322-sendmessageconfiguration)
specifies. `SendStreamingMessage` always delivers updates in real time;
`returnImmediately` has no effect on streaming operations.

HTTP timeout or stream closure does not cancel admitted work. Lists use cursor
pagination, descending status timestamp order, an empty `nextPageToken` on the final page,
and the specified history and `includeArtifacts` limits. Tokens are bound to
the caller and query; neither counts nor pagination reveal another caller's work.

### Publishing a LINA agent

Publication is off by default. The owner chooses the persona, published name
and description, HTTPS address, permitted caller identities, offered tasks and
the local conversation that receives the work. Under the existing
[remote-access rule](runtime.md#remote-access), the first A2A listener binds
only a tailnet address, with the owner's TLS setup and Tailscale access policy.
It exposes no `client`, `node` or administrative handlers. Callers must have
network access to that address as well as A2A authorization. Public Internet
ingress, tunnels and proxies are outside this design and need a separate
change to the remote-access contract. Enabling publication provisions none of
those or an identity provider.

The Agent Card contains approved public metadata only: display name,
description, implementation version, interface, security schemes and
requirements, input/output media types and skills with stable ids. It carries
no private persona instructions, memory, conversations, credentials, local
paths or private examples. Implementation version and protocol version are
separate. An interface tenant routes to its configured publication and grants
nothing.

**Sign-in.** The owner configures a trusted external OAuth/OIDC issuer and
service audience. The card declares that scheme through `securitySchemes` and
`securityRequirements`. Callers obtain their own access tokens from the issuer
outside A2A. Core validates signature or issuer-bound introspection, issuer,
audience, expiry and scopes, then maps the authenticated subject and client
identity to an explicit caller binding. An ID token, proxy identity header or
person id in a message is insufficient. Core neither becomes an authorization
server nor reuses a model-provider login. Discovery may be unauthenticated
within that permitted network; every task operation requires authentication
and binding authorization. This service authentication admits no LINA client
or device and does not replace Tailscale admission.

**Offered tasks.** Each skill describes an owner-enabled task definition, such
as research over named materials, creating a document or a coding change in a
named repository. Its definition fixes inputs, outputs, materials, tools,
write area, effect limits, execution budget and done condition. Core matches
the request to a definition before model or tool work; ambiguity needs input
and work outside the definitions is rejected. A skill is descriptive, not a
tool grant; SendMessage gains no invented standard `skillId` field.

Admitted work reaches Core's existing conversation, tools and dispatcher. A
coding task reaches an installed worker with a complete work definition; a
model's plan cannot satisfy it. Scope and preset ceilings follow the whole
task. Only the owner's permitted LINA surfaces may approve an outward effect
or scope expansion. The caller may supply input and cancel its work, but may
not approve, publish another agent, change settings, read unrelated history
or enlarge its binding. Dispatch enforces these checks, not prompt wording.

**Results.** Core assigns opaque A2A task/context identities and durably binds
them to caller, publication, owner, requesting persona and local work. The
external context is scoped to the caller, not the persona's main conversation
id. A follow-up may continue only an authorized nonterminal task; guessing an
id cannot attach to another context.

The owner's conversation receives origin, progress, questions and durable
results through the existing running-list and result delivery paths.
Completion yields a verified local result and an A2A artifact projection
limited to the binding's permitted output. The binding covers these task
replies, not mail, public posts or other outward effects inside the work.
Artifacts have stable ids and media types, with provenance retained in Core.
Downloads require task authorization; they never expose local paths or ambient
credentials. Result delivery is deduplicated across restart. Core emits
`COMPLETED` only after verifying the task's done condition, not merely from
model text, a tool exit or a worker's completion claim. Deleting the published
persona rejects a task that has not started. Its terminal owner notice cannot
be delivered to the trashed conversation; that notice stays pending for the
task and does not fail publication recovery or stop other publications.
Restoring the persona before trash expiry delivers the notice.

#### Publication setup and task profile

The owner configures publication with `lina a2a publish --state-root <root> --config
<file>` while Core is stopped. `--credential-stdin` supplies the external
issuer's introspection client secret to Core's existing secret store. The
private `system/core/a2a.json` control record names the TLS certificate/key,
exact Tailscale bind, issuer, audience, introspection endpoint, required scopes
and publications. Missing or disabled configuration opens no A2A listener.
Changing listener or issuer setup requires a restart; binding and definition
changes are rechecked during execution and before disclosure. The configured
HTTPS introspection endpoint must share the issuer's origin, refuse redirects
and identify active Bearer access tokens with issuer, audience, expiry,
subject, client identity and scopes. Token bytes are never persisted in tasks.

Each publication uses `/a2a/<publication>` for JSON-RPC and
`/a2a/<publication>/.well-known/agent-card.json` for discovery. The initial task
profile accepts one JSON data part with `task` (an offered definition id) and
`input` (bounded task data). The card advertises `application/json` input.
This application data is not a protocol-level skill selector. Owner definitions
pin exact Core record/web tool operations, a complete worker definition, an
execution deadline, a tool-call ceiling and a committed-file output rule.
Caller data can fill the work description; it cannot choose tools, paths,
worker configuration, write area, branch, effects or budget.

Each configured tool operation and the worker dispatch may run once. The
published task uses an isolated conversation with forwarded input and no
personal history or recall. Preset checks, tool effects, worker admission,
local approvals and manager verification remain the existing Core paths.
An incoming binding authorizes task admission and permitted replies; it does
not by itself authorize the installed worker's OS-user permissions. Before any
provider, tool or worker execution, Core shows the owner the exact caller input,
task definition, tools, repository, write area and deadline. The approval states
that the installed worker uses the owner's files, login and integrations. Only
an authenticated owner can allow that request once or deny it. No eligible
surface means refusal. The answer is bound to the request and current Core
epoch; a caller message, another task or a restart cannot reuse it. Pending
execution approval is `INPUT_REQUIRED`. Approval does not remove the preset
ceiling or the worker's own later approval requirements. A denied worker
approval stops that published task and cannot trigger an automatic worker retry.
Core reports cancellation only from a recorded terminal observation; missing
or uncertain evidence remains unknown.
The publication does not authorize the manager to merge or close a PR.
Supplementary messages on an authorized nonterminal task retain their origin
and reach the owner's conversation as untrusted input. They do not change the
accepted definition, extend its budget, restart execution or answer an approval.

The committed output must be among the worker's in-scope changed files, match
the configured content condition and size limit, and belong to the exact head
of a verified manager decision and worker receipt. Core reopens those commit
bytes before exposing an artifact, retaining its content hash and commit.
The owner receives the same permitted content through the existing typed result
path. Its result head, ledger revision and delivery marker commit atomically.
The task projection contains no private conversation, credential or local path.

Limits are 64 KiB per request and per task's retained input, 16 input messages,
256 KiB per artifact, 32 active admissions per Core, and a binding's lower
active-task limit. Owner deadlines are at most one hour and tool-call ceilings
at most 32. Streams and blocking sends have a five-minute observation limit;
ending observation does not cancel execution. List cursors are bound to the
caller and query and expire on server restart. Downloads at
`/a2a/<publication>/artifacts/<task>/<artifact>` require the same current
access-token and task authorization as RPC reads.

### Calling a remote agent

The owner adds a connection with a card URL, approved service origin,
authentication scheme and permitted tasks. Core stores the observed card,
interface, scopes and a secret-store reference to the owner's credential.
OAuth uses the existing client browser relay with a configured issuer and
client registration. A person-supplied API key or bearer credential is usable
when the card declares it. There is no credential discovery, account fallback
or forwarding of inbound credentials. Unsupported authentication needs setup.

Core admits a persona's call for its owner and conversation. A Node's call
also passes through Core under that Node's current grant, using the
[A2A relay](host-protocol.md#a2a-and-the-node-connection). Core owns the remote
HTTP transport in both cases; Nodes receive no remote credential.

Before sending, Core binds the local task and command to the connection,
origin/interface/tenant, credential account, operation and effect class, exact
outgoing payload and material revisions, return scope, budget and deadline.
[Remote A2A authority](main-authority.md#remote-a2a-agents) is checked before
each send and follow-up. No full conversation or memory is uploaded by
default. A request for more data or a different credential pauses the task;
remote text cannot widen its scope.

Card, interface, OAuth metadata and artifact URLs use HTTPS and the address
checks of remote MCP sign-in. A person-selected private-host exception covers
only that host. Redirects cannot widen the allowed origin, reach metadata
services or forward credentials. Changed endpoint, tenant, authentication or
required scopes invalidate admission until the owner confirms the connection.
Card descriptions and safety claims neither classify effects nor grant
authority. Unsupported media or required extensions fail before task data is sent.

Core persists remote task/context ids or the direct response with the local
binding. Status feeds progress; `INPUT_REQUIRED` becomes a task question and
`AUTH_REQUIRED` becomes sign-in on an owner surface. Secrets never travel in
task messages. Remote text and artifacts are external evidence: Core validates
and retains the permitted result with source, time and hash, then delivers it
to the originating conversation and any still-authorized Node command.
Remote `COMPLETED` is a claim checked against the local done condition, not a
LINA receipt or automatic acceptance of an external effect.

### Outbound connection controls

The owner uses the `a2a` client capability to configure, inspect, revoke and
sign in to remote connections. The installed `lina a2a` command accepts settings
on standard input. Credentials are transient settings fields and never enter a
conversation. Task profiles bind an effect class, input prefix and byte limit,
output media type and byte limit, and either an expected artifact SHA256 or
explicit owner review (an empty hash). In review mode, Core retains an unverified
preview; only an eligible owner confirming the inspected artifact hash can
produce a verified result revision. Model tools cannot perform that confirmation.
This local done condition verifies the requested content; it does not certify unrelated
remote effects. Unknown classification still asks, and preset ceilings apply.

The `remote_agent` conversation tool lists configured task definitions, sends a
bounded message, observes a retained call or requests cancellation. The initial
scope is the owner's direct Core conversation. Channel delegation, publication
and Node relay are not admitted by this tool. Material or history upload is not
implicit: only the validated text argument becomes a message part.

The running list exposes retained remote work. A bounded Core observer polls
known tasks after the model turn ends and after restart under fresh observation
authority. It never replays an unknown initial send. Current-epoch recorded
answers resume a follow-up through ordinary authority and approval; answers
left unused across restart cannot send work.

Input questions require the owner's recorded answer before a follow-up. A sign-in
wait points to connection settings. OAuth uses authorization code with PKCE,
configured issuer/client endpoints, a session-bound browser relay, issuer/state
checks and bounded token lifetime. Expiry requires another sign-in; no model
provider credential or refresh-token fallback is used.

Artifacts may arrive as text, structured data, bytes or same-origin HTTPS URLs.
Core retains bounded permitted content and checks the expected hash against
assembled artifact parts. Verified text and binary file results use the existing
result journal and material store, with the original conversation and remote
source, identity, time and hash. Other remote completion claims remain unverified.

### Recovery and failures

Published sends require a nonempty `messageId`. Before dispatch, Core stores
caller, publication, message id and canonical request hash with the local task
in one durable admission. Identical retries return that admission; reusing an
id with changed input is invalid. JSON-RPC ids are correlation ids only.
Outbound message ids are stable, but A2A makes remote send deduplication
optional. An uncertain send is **unknown**, never an automatic replay or a
reason to try another agent. Read or resubscribe to a known remote task;
otherwise the owner must resolve the uncertainty. Unresolved work also reserves
its exact input within the originating conversation, so a new model call id or
a different connection cannot silently repeat it.

Revocation, epoch change or Node disconnect blocks new sends and follow-ups.
Accepted remote work may continue. Core reconciles it through narrowly
authorized observation or cancellation; absent that authority, the outcome
stays unknown. A revoked credential or grant is never reused. Restart recovers
task bindings and result delivery before new dispatch. Cancellation reports
observations and never promises to reverse an effect already made.

| Failure | Response or local state | Retry rule |
| --- | --- | --- |
| Missing, expired or invalid caller credential | HTTP 401 with challenge | Sign in; no dispatch |
| Caller outside publication scope | HTTP 403 | Owner changes binding; no automatic retry |
| Missing or inaccessible task/context | `TaskNotFoundError` (-32001) | No existence hint or automatic retry |
| Invalid input, conflicting message id or invalid cursor | `InvalidParamsError` (-32602) | Correct input; no dispatch |
| Unsupported version, media or operation | Specified A2A error | Reconfigure; no fallback |
| Core recovering, storage unavailable or capacity exhausted | HTTP 503 or 429 with retry guidance | Retry reads or provably unadmitted sends; identical published sends are deduplicated |
| Tool or worker failure after admission | Failed task with safe cause | Existing work retry policy |
| Input, approval or sign-in needed | `INPUT_REQUIRED` or `AUTH_REQUIRED` and local waiting item | Resume only after the appropriate recorded answer |
| Lost acknowledgment, stream, cancellation confirmation or effect evidence | Unknown local effect with retained identity | Observe and reconcile; never blindly resend |

Logs retain correlation ids, versions, status and timing, never tokens,
request bodies, private task content or credential-bearing artifact URLs.
Implementation acceptance must drive a running Core with actual tool or
worker results; protocol fixtures alone do not establish this behavior.

## Channels

A channel is an external messaging platform through which the person talks with an agent: a Telegram bot, a Discord bot, or another platform added later. The message gateway in LINA Core connects agents to channels. A channel connection is not a plugin: a plugin gives the model tools, and a channel connection gives the person another place to talk with an agent. Reading the person's own conversations with other people is a messenger source, not a channel (see [Messengers](#messengers)). An agent's bot speaks as that agent and never in the person's name; a message in the person's name goes through a messaging plugin (see Service profiles).

### Connection, binding and scope

- **Channel connection.** One bot account on one platform, with its token. The person creates the bot on the platform and gives LINA the token, or uses the platform's own flow for creating bots from a link where one exists. The platform flow is the default and pasting the token is the fallback. LINA ships no bot of its own and creates no service identity. The connection records the person it belongs to.
- **Binding.** The record `(platform, bot account, chat) -> agent`, where the chat is a Telegram chat or topic, or a Discord direct message, channel or thread. The most specific binding wins. A chat with no binding reaches no agent, and a chat has at most one bound agent.
- **Dispatch.** A binding together with its scope. Creating, widening, narrowing and removing a dispatch are the person's recorded acts and name the agent.
- **One bot per agent.** Each dispatched agent has a bot of its own, so the platform shows its name and avatar. One bot may serve several agents only when every chat, topic or channel is bound to one of them; the agent then names itself in the first line of each message.
- **Scope.** Each binding carries a scope, stored in canon with the person who set it and the agent it applies to:

| Field | Values | Default |
| --- | --- | --- |
| where | The chats the agent may be in | Only the owner's own chat with the bot |
| reads | `dm_only`; `addressed` (mentions, replies, commands and buttons); `all_messages` | `dm_only`. When the person adds a chat that other people are in, `addressed` is proposed. `all_messages` is never the default. |
| writes | `reply_only`; `may_start` (the agent may post without being addressed) | `reply_only` |
| tools | None, or the same tools and approvals as in the app | In the owner's own chat, the same as in the app. In any other chat, none. |
| memory | Whether the agent may use the person's memory in that chat | In the owner's own chat, the same as in the app. In any other chat, none. |

- The platform rights LINA asks for are the smallest that the scope needs. It never asks for an administrator right on either platform, and it never asks for a group administrator right on Telegram. The connect step explains once what `all_messages` costs on the platform, because it needs the platform's wider message-reading right.
- Slash commands and buttons are a convenience. The gate is always LINA Core's check of the paired owner's id and of the binding, never the platform's role system.
- Work completion turns retain the channel binding and exact originating thread address recorded at task admission, including private chats sharing the app conversation. They revalidate that binding before model dispatch and tools, and keep its memory restriction. A legacy task without a provable origin cannot obtain authority from a current channel binding.
- Accepted steering retains its originating channel scope. If it becomes a deferred follow-up, Core uses that scope and rechecks the current binding before each model dispatch and tool execution, including after an app-started turn.

### Identity and pairing

- A connection starts denied. The owner pairs it: the app or the TUI shows a one-time code, the owner sends it to the bot from the platform, and LINA Core records the platform's numeric user id against the owner's person id. LINA Core never uses a user name as an identity. The code works once and expires soon after it is shown.
- Anyone else in a chat is an external participant. Their text is evidence, never an instruction or an approval, and it creates no person, person model or memory item ([conversation-and-memory.md](conversation-and-memory.md#channel-conversations)). By default the agent does not answer them. The person may choose a mode in which the agent answers when it is addressed; it then answers without tools and without the person's memory, whatever the scope's tools and memory fields say for the owner.
- A bot answers an `about` command with the build's version, commit and source link ([product-families.md](product-families.md)).

### Transport

- The gateway is outbound only, like plugin connections (see Plugin rules). Telegram is read by long polling and Discord by the Gateway session, with interactions received on that session and never at an endpoint URL. There is no webhook mode in the first design, because it would need a public listener ([runtime.md](runtime.md#remote-access)).
- LINA Core owns exactly one poller or Gateway session per bot token, and only the main runs it. After a restart the previous generation is fenced ([runtime.md](runtime.md#recovery)).
- The adapter's cursor, such as the Telegram update offset, is stored durably before an update is acted on. The platform's ids (update, interaction and message ids) are the idempotency keys of inbound events, so a repeated event is handled once.
- When the main was down for longer than the platform keeps undelivered messages, LINA tells the person that messages may have been missed.

### Inbound and outbound rules

- A channel turn follows the same turn rules as a turn in the app. A result that lands later arrives as a new message in the chat, not as an edit of an old one, so the person's notification fires.
- The reply is written once into the ledger; the adapter shapes it for the platform: it splits text on paragraph boundaries at the platform's limit, and when a reply would need more than three parts it sends a summary with a link or a file instead. Streaming in a channel is off, in the owner's private chat as well: a working indicator and one final message.
- Rates are the adapter's. Core keeps one outbound queue per bot account and chat, honours the platform's retry-after value and resumes from the refused part. A send refused before delivery is retried. A send whose outcome is unknown is not sent again; the reply stays in the ledger and the app shows it ([host-protocol.md](host-protocol.md#delivery)). A "blocked" or "cannot start" answer is a permanent state of that chat: the binding shows unreachable, nothing more is sent, and the person is told. Core opens no direct message to anyone except in answer to that person's action.
- An inbound file from the paired owner is an import ([filesystem.md](filesystem.md#import-and-export)); a file from anyone else is never imported, and text in any file is data. A result that is not text reaches a channel in the form its card takes there; the card must stand alone, because the app may not be reachable from where the person reads.
- An approval request in a channel is a message with buttons, governed by [main-authority.md](main-authority.md#channels).
- The progress digest. The person can ask an agent in a channel how things are going. A binding that lets the agent write first may also send a digest at a time the person sets; it is off by default. The digest is one message made from the progress view ([surfaces.md](surfaces.md#progress-at-a-glance)): a first line that says how many things need the person and names the first, then one line for each running item with its name, its stage and what comes next, a mark on a line that waits on the person, at most five lines and a count of the rest. It holds no issue number, pull request, log or id, and it ends with one link that opens the progress view in the app when the app is installed. LINA Core builds the digest and the adapter only sends it.

### Adapter boundary

Core talks to each platform through one adapter and knows no platform limit. An adapter offers: connect and disconnect with a durable cursor; an inbound event carrying the platform's ids; send, returning a result id or an error class (retry later, permanently blocked, too long, unauthorized); edit; answer an interaction; fetch an attachment; and a static capability descriptor (text length, edit, buttons, files, threads, streaming, and which result types the channel can show). The first adapters are Telegram and Discord. A further platform is a new adapter behind this boundary, added by a release and not by a change to this contract. The adapter code is LINA's own, and its client library is a Go dependency under [dependencies.md](../policy/dependencies.md).

Discord uses API v10 and an outbound Gateway WebSocket, with no inbound
listener. The client protocol's optional `platform` selects `telegram` or
`discord`; omission retains Telegram for existing clients. `/channels` offers
Discord connection alongside Telegram and preserves the selected platform for
pairing, binding, scope changes and disconnect. Discord bot identity comes from
`users/@me`, not from decoding the token. Only the credential store retains the
bot token. Explicit Discord connect registers only the `/lina text` command,
preserving other application commands. Its interaction is acknowledged once
after durable admission while Core finishes the turn. Interaction tokens remain transient and never enter inbox or canon.

The Discord adapter uses `github.com/coder/websocket` v1.8.15 for framing and
standard-library HTTPS for individual REST calls. Core owns heartbeat,
identify/resume, reconnect backoff and the durable session/sequence cursor.
Gateway close 4007 (invalid sequence) or 4009 (session timed out) durably clears
only that bot's session/sequence cursor before reconnecting with Identify,
including a close received before Hello. Native receipts, pairing and bindings
survive; other transport closes retain the cursor for Resume.
The privileged message-content intent is requested only for an explicit
`all_messages` binding; changing that requirement reconnects the session.
Native message and interaction IDs remain deduplicated across sessions and
restarts. Bot-authored input cannot begin a conversation. Guild thread ancestry
comes from channel metadata, so the most specific thread binding wins.

Discord text is shaped into at most three parts of 2,000 UTF-16 units. A
paragraph separator is retained at the split; an oversized individual paragraph
splits at a Unicode rune boundary without losing text. Longer replies send one `reply.txt` attachment containing the complete text
(up to 8 MiB). Automatic mentions are disabled. Streaming stays off; this
increment supports outbound text files; inbound attachment import remains
unavailable.
A 429 keeps the full retry-after; ambiguous responses and transport failures
remain unknown. A permanent refusal marks the chat unreachable through the
shared queue and appears in channel controls.

Discord approval buttons contain only a canonical request ID and once/deny.
The durable presentation binds bot, chat, thread and message to that request.
Before accepting a press, Core rechecks the numeric paired owner, current tools
scope, agent, epoch and presentation. A non-owner cannot change the approval or
edit the owner's message. A repeated interaction never repeats a decision or
platform edit. The pending-button inbox contains no interaction token.

Both adapters are attached to Core's existing turn lanes and outbound queue.
Committed owner input starts the existing model/tool flow with its channel
conversation and scope. The paired owner's private chat shares `main:<agent>`
and its live app/TUI history; shared chats keep separate conversations. Admission
continues during an active turn and uses the same steering coordinator. A
durable consumed-steering marker prevents replay after restart. Unclaimed
private inbox rows from the earlier gateway are moved to the main conversation
without dropping their native receipts. Memory-disabled bindings omit personal memory and
relationship context; tools-disabled bindings offer no tools. Binding changes
are checked again before model dispatch and tool execution at the original
inbound address. A new thread binding supersedes a captured parent fallback
even when the parent binding remains unchanged. A channel with tools
may start the existing read-only background run; its verified landing returns
as a new message in the originating chat after the turn boundary. The delivery
projection retains native background-notice provenance and its existing notice
JSON shape, and remains untrusted external evidence in subsequent model history.
The channel adapter receives the projected summary text; restart restores the
notice through the ordinary history path. The outbound queue accepts that
committed notice only for its bound agent and conversation; relabeling a generic
system event as a notice does not authorize delivery. Recovery uses
the original inbox and turn claims and does not rerun an already started turn or
resend an unknown delivery. Private, reachable bindings with tools can present
canonical approvals while the Gateway is active.

The headless `e2e/m2/discord.json` scenario runs production boot in its own
process with synthetic Gateway, REST and model endpoints. It covers protocol
connection/pairing/binding, duplicated interactions, owner-only admission,
foreground and background execution, long reply shaping, blocked delivery and
restart. Adapter tests independently cover owner-only approval decisions,
presentation matching, token-free inboxes and REST error classification.
The separate `e2e/m2/discord-recovery.json` scenario injects both non-resumable
close codes into the running Core, checks fresh Identify and duplicate receipt
handling for both commands and approval buttons, then completes tool/background
work and delivers its results. After restarting Core it requires Resume with
the replacement session ID and its last committed sequence (#547).

Telegram's initial adapter uses `github.com/PaulSonOfLars/gotgbot/v2` at
`v2.0.0-rc.37`, with no transitive modules. It advertises text, edits,
buttons, files and topics; streaming remains off. The adapter shapes queued text
into at most three parts of 4096 UTF-16 units, preserving paragraph boundaries
where possible, closing and reopening code fences across parts without trimming
their body whitespace. Markdown prose becomes native Telegram entities with
UTF-16 offsets; code remains literal. A definite entity-format rejection permits
one plain-text fallback on the same destination or edited message. Unknown
outcomes never trigger that fallback. A longer reply needs Core's summary and
result link. Approval text remains literal. Approval
presentation reads the canonical request and offers only once and deny; it never
truncates arguments to make buttons fit. The request is bound durably to the
returned bot/chat/topic/message identity, which must match before a button can
answer it. A too-long approval remains pending for
full presentation in the app or TUI, with a button-free link card sent by Core.
Downloads use Telegram file IDs and validated paths at the same fixed origin,
with a 20 MiB limit. API JSON responses are bounded at 8 MiB.

An unavailable platform does not hold startup: the adapter is constructed without
contacting Telegram, and its cancellable poller runs independently of Core's
READY transition. Polling reads retry with bounded backoff and jitter; five
consecutive competing-poller conflicts stop that poll generation without dropping
updates. A strict bot/self/anonymous-author guard prevents reply loops. Sends
return an outcome for the durable outbound queue, which owns retries and each
part's receipt. Full representable flood waits are honored, with a shared live
send/edit cooldown; an unrepresentable delay fails closed. Edits retain the
supplied message identity and an explicit not-modified refusal counts as success.
Transport failures,
malformed responses and server failures have unknown delivery outcome and are
never automatically sent again.

Startup independence also applies to saved connections: Core restores their
credentials locally and performs bot identity verification in the channel's
supervisor. While that request is unavailable, Core reaches READY and the
channel control service projects the connection as unavailable, including when
no model route is configured. Shutdown cancels and joins the supervisor before
closing its stores. `e2e/m1/telegram.json` verifies this through production boot
and the client socket with a request held until cancellation.

### Gateway core persistence

The gateway core (#97) implements the adapter boundary, pairing, bindings, admission and the outbound queue as packages. Its composition root supplies the existing fenced canon writer, installs the channel schema in a numbered migration, routes committed deliveries to the conversation coordinator, and starts adapters only after recovery. The packages contain no bot token and make no platform connection at startup.

Pairing codes contain 128 random bits, are stored only as SHA-256 hashes, expire after five minutes and work once. A replacement code invalidates the previous code; it does not authorize transferring an existing pairing to a different platform user. The control flow establishes the owner's person id and the adapter supplies the platform's numeric sender id and private chat. Default owner-only admission checks that pairing, the most specific binding and its read scope. External participants do not start turns in this increment; the optional addressed-participant mode is not exposed yet.

Admission uses the bot account and native event id as its durable deduplication key, independently of the currently bound conversation. The cursor comparison, receipt and accepted inbox row commit together. Repeated events do not rewind the cursor, and refused input advances the cursor without starting a turn. Dispatch happens only after commit and is never automatically repeated after an error or crash. A stored receipt acknowledges storage, not successful turn execution.

The outbound queue receives a ledger reference and already shaped parts, requires that reference to name a committed agent event in the bound conversation, checks current pairing, binding and reply provenance, and records each send attempt before calling the adapter. One bot/chat has at most one in-flight send, including across its topics. A refusal before delivery retains the same part and its retry-after deadline; an uncertain outcome terminates the message without resending any part. Restart converts in-flight attempts to unknown before adapters run. A permanent block marks the chat unreachable and stops queued delivery. Binding removal and agent deletion prevent queued sends from starting. Queue records retain the reply and its delivery outcome for the consuming app view; this increment adds no client endpoint, platform transport or approval-button handler.

A [backup restore](filesystem.md#backup-generations) converts both queued and in-flight deliveries to unknown before publication: either may have been delivered since capture. The restored queue retains parts and existing delivery receipts, rejects old attempt completions and never retries these messages. Ordinary restart still retains queued deliveries and their retry-after deadlines.

### Credentials

A bot token is an API key in the sense of "Credentials and sign-in". The person enters it once; LINA Core keeps it in the secret store under the person's id and never returns it to any client. It goes only to the platform's API through the adapter. It never enters a model request, a log, the sandbox or an event of the self-diagnosis log.

## Messengers

A messenger source is one of the person's own messenger accounts that LINA reads into its store, such as KakaoTalk, Discord or iMessage. It is neither a channel nor a plugin. A channel is an agent's own bot that talks with the person. A plugin gives the model tools. A messenger source brings the person's conversations with other people into LINA, so that agents can search them with evidence and, where the person allows it, act in them. Per-platform evidence, terms and the upstream revision read: [messengers.md](../research/messengers.md). Implementation: #1069 to #1087, in the order of its plan.

### Messenger sources

- Connecting a source is the person's act. LINA never connects one on its own and never looks for messenger apps, sessions or exports on the machine.
- A source is one account on one platform. It records the person it belongs to, the account's platform user id, its mode (official or unofficial), its adapter and its state: connected, needs sign-in, failing with its cause, unreachable (its Node is away), import-only, or disconnected.
- A source holds chats. Each chat records its platform id, name, kind (direct, group, channel or thread), the time of its first message when known, its cursor and its coverage. The person can leave a chat out of sync, out of learning or out of embedding.
- What a source reads becomes messenger records in canon ([conversation-and-memory.md](conversation-and-memory.md#messenger-records)). There is no second database.
- Every agent can search a connected source, as every agent can use an enabled plugin. The person can name the agents that may read a source instead.
- When a channel connection and a bot-mode source use the same bot token, Core still runs one Gateway session or poller for that token and gives each its events.

### Platforms

| Platform | Account | How LINA reaches it | Writes |
| --- | --- | --- | --- |
| KakaoTalk | Personal | The messenger add-on, signed in as a tablet sub-device | None: Kakao's terms forbid automating an account |
| Discord | A bot the person adds to their servers | LINA's Discord adapter | By grant |
| Discord | Personal | The messenger add-on, QR sign-in | None: Discord's terms forbid self-bots |
| iMessage | Personal | A Mac Node reads a read-only copy of `chat.db` under a grant | By grant, through Messages automation on that Node |
| Slack | Personal | Go, with the person's own Slack app and an OAuth user token | By grant |
| Microsoft Teams | Work or school | Go, Microsoft Graph with the person's own Entra app | By grant |
| Webex | Personal | Go, Webex REST with the person's own integration | By grant |
| Telegram | Personal | Go, the official client API with the person's own `api_id` | By grant |
| WhatsApp | Personal | The messenger add-on, as a linked device | None: WhatsApp's terms forbid automating an account |
| LINE | Personal | The messenger add-on, as a secondary device | None: LINE's terms forbid automating an account |

- Instagram, Channel Talk, WeChat, the WhatsApp Cloud API and the Telegram Bot API are not sources. Their reasons are in the research. Adding a platform or changing a platform's mode is a change to this table.
- An official platform signs in with the person's own app or client, as Google does (see Google). LINA ships no Slack app, Entra app, Webex integration or Telegram `api_id`; the setup guide walks the person through making one. Tokens live in the secret store under the person's id. An administrator's policy is never worked around.
- The connect card of an unofficial platform says in plain words that the platform's terms forbid automating a personal account, links the term, says that even reading may get the account limited, and says that LINA will only read. The person accepts before connecting, and LINA records the acceptance with the person and the date.
- LINA never takes tokens, cookies or passwords from other apps or browsers, never signs in as an official client with that client's id or secret, never scripts a developer portal and never evicts the person's other devices. Reading never marks a message read, shows presence or typing, or joins or leaves a chat.

### Microsoft Teams source

Teams reads work and school accounts through Microsoft Graph v1.0 with the
person's public Entra app and tenant ID. Core requests delegated chat, channel,
member and basic profile read scopes plus offline access; it requests no write
permission. The client browser relay uses authorization code with PKCE S256,
one-use state and a ten-minute, connection-bound attempt. The tenant-specific
Entra authority is fixed before sign-in. A supplied response issuer must match
that authority; Entra responses without `iss` remain bound to that one authority.
Tokens live in the person's secret store and refresh before expiry or once after
a 401. There is no fallback account or Microsoft-owned client registration.

Core polls each account independently and resumes it after restart. Graph paging
links must stay on the same collection, and HTTP redirects are refused. Core
contacts only `graph.microsoft.com` and `login.microsoftonline.com`; browser
consent and the client's loopback callback are outside the Core HTTP client.
Chat-message reads pair `lastModifiedDateTime` filters with matching descending
ordering. Channel-message and reply endpoints do not support those filters, so
Core pages them and filters locally. A complete traversal advances the durable
watermark; a failed page advances neither records nor cursor. Explicit soft
deletions retract records, and absent messages do not imply deletion.

Senders use tenant-qualified platform IDs. Profile emails are retained as
identity evidence, never a replacement key or a name-based merge. A tenant that
blocks the app or withholds delegated consent reports `unsupported` with a short
reason; it is not retried through another service. Transient failures retain the
cursor and obey Retry-After. Disconnect cancels the account worker before deleting
its tokens and leaves the previously read history available.

### Webex source

Webex reads the person's spaces through the public REST API and their own
integration, using only room, message, membership and profile read scopes.
Core uses plain HTTPS, authorization code with PKCE S256 and the client browser
relay. Tokens and the integration secret stay in the person's secret store;
refresh replaces tokens and expiry together. A rejected refresh or a second 401
marks the source as needing sign-in. Core never substitutes another account.

The first implementation supports one connected Webex account per person.
Changing the integration or account requires disconnecting first; signing in
again must resolve the same platform identity. Disconnect cancels and joins the
reader before deleting credentials. Already-read history remains available.

The integration must register the exact callback address. The installed client
uses `http://127.0.0.1:8400/auth/callback`, including for pasted callbacks when
it cannot listen. Other clients report their registered listener port. Core
validates one-use state, expiry, a supplied issuer and the pasted callback's
address; it has no callback listener. A registration or administrator refusal
is reported without trying another authentication method.

Core polls independently every five minutes, including after restart. It walks
older messages with `beforeMessage`, follows room and membership paging, and
catches up across multiple pages before moving its newest-message anchor.
Paging must stay on the same origin and collection. HTTP redirects are refused.
429 and 423 respect Retry-After; waits over one minute or exhausted attempts
leave the cursor unchanged and report a retryable source failure. A failed
source retries on its next interval; it does not stop other sources or conversation.
Edits are detected when a reread reports a later `updated` timestamp. The API
supplies no deletion feed here, so an absent message never deletes history.
This source exposes no write operation.

### Messenger adapter boundary

Each way of reaching a platform has one adapter. Discord's bot and Discord's personal account are two adapters of one platform; every other platform in the table has one. A source records the adapter it uses. An adapter offers: its sign-in state; listing chats; reading a page of a chat from a cursor, oldest first; members; live events where the platform has them; fetching one attachment on request; and a static capability descriptor (history depth, cursor kind, edit and deletion signals, live events and write verbs). Every operation except the capability descriptor names the source account it acts for, so two accounts of one platform never share a read. Only adapters of official platforms have write verbs. A ported adapter is LINA's own Go code, and its client library is a Go dependency under [dependencies.md](../policy/dependencies.md).

### The messenger add-on

Unofficial personal platforms are read through the messenger add-on: LINA's own small program that runs the agent-messenger SDK at a version pinned by the release, as a child of LINA Core ([runtime.md](runtime.md#processes)).

- It offers read operations only. Write methods and reads that change read state have no route in it.
- It runs in the OS sandbox with network on, and writes only in its data directory ([filesystem.md](filesystem.md#state-root-and-layout)).
- The person signs in through the add-on's own flow, such as KakaoTalk's passcode or Discord's QR code, typing into it through the app or the TUI. LINA relays and keeps nothing. The session stays in the add-on's data directory, outside backups; after a restore or on a new main the person signs in again.
- One process runs for each account and stays running, so the platform sees one session.
- A pinned version changes only through a reviewed update that reads upstream's changes to the platforms LINA uses.
- When the add-on or its runtime is missing or failing, only its sources fail.

### Sync and coverage

- Core reads each source on its schedule or from its live events. The records, the chat's cursor and a coverage record (the range read completely and when) commit in one transaction. Reading the same messages again adds nothing.
- A failing source is read again on its schedule and returns to connected when a read completes; the person sees its cause in between. A source that needs sign-in waits for the person, and an unreachable, import-only or disconnected source is not read.
- A chat's first fill reads back as far as the platform allows.
- A message missing from a later read is never treated as deleted. Edits and deletions the platform reports follow [Messenger records](conversation-and-memory.md#messenger-records).
- Each chat is fresh, stale (no complete read within its interval) or in a gap (the time since its last complete read passed the platform's read window, so messages may be lost). A gap is shown on the source and told to the person once. An archive import can fill it later.
- Rates are the adapter's: it honours the platform's retry-after and keeps its cursor.
- A failing source never blocks conversation or another source.

### Writes by grant

- Only adapters of official platforms write. A write in the person's name is an outward effect under [main-authority.md](main-authority.md#messengers): off by default, turned on by the person per chat, and asked every time where it reaches other people.
- A write runs through the effect ledger with the platform, account, chat, verb and argument hash. Where the platform takes a client message id, that id carries the idempotency key. An unknown outcome is settled by reading the chat and is never sent again.
- What LINA sends returns through sync linked to its effect, with speaker kind `agent` and the requesting agent's id. It is never read as the person's words or learned as evidence about the person ([Messenger records](conversation-and-memory.md#messenger-records)).

### Disconnecting and deleting

- Disconnecting stops sync at once and deletes the source's credentials or the add-on's session. The records stay, searchable as the person's history and labelled with the disconnected source.
- Deleting a source's records, a chat or one record is the person's deletion of conversation content ([conversation-and-memory.md](conversation-and-memory.md#deleting-a-conversation)). A tombstone keeps the legacy reference, so a later sync or import does not bring the content back.

## GitHub

### The plugin

The GitHub plugin is a first-party plugin that relies on the user's own setup: the `gh` login and git with the user's credential helper or SSH agent. LINA has no GitHub identity of its own; on GitHub, LINA's reads and writes are the user's. LINA records its GitHub writes in its own records ([work-and-delegation.md](work-and-delegation.md)). LINA has no webhook endpoint.

- LINA detects the login with `gh auth status`, which names the host, the account and the token scopes. Each repository's remote names its transport: HTTPS with git's credential helper, or SSH. When `gh` is not logged in, the plugin shows needs sign-in and names the `gh` command that signs in; git transport keeps working.
- In a conversation, the model runs `gh` and `git` as shell commands. The plugin's service profile names which of them are outward or irreversible (see Command profile), and LINA Core decides a command's network access from the profile ([runtime.md](runtime.md)).
- LINA Core's own reads are not model tool calls: the work it coordinates, the head, review threads and check results of a delegated pull request, Klotho's project supervision ([cognition-and-life.md](cognition-and-life.md)), the read right before a merge, and the check of a merge a person made. LINA Core runs them with `gh` as its own child process. Reads need no approval.
- When the login lacks a scope that a command needs, for example `workflow` for a push that changes workflow files, LINA tells the person the command that adds it, such as `gh auth refresh -s workflow`, and never works around it.
- A person who works with a GitHub MCP server can add it as a plugin like any other MCP server.

Workers do their GitHub work with the GitHub setup of the worker itself, on the main and on a Node device: pushing the task's branch, opening and editing the pull request and replying to reviews. LINA does none of these for delegated work. Its own writes on delegated work are merging and closing the task's pull request ([work-and-delegation.md](work-and-delegation.md)).

LINA's GitHub writes include pushing branches, opening and updating pull requests, replying to reviews, editing issues and merging: whatever the user's setup can do. Every write is recorded under the plugin rules. In delegated work, the worker's push writes only the task's own branch, and a protected ref moves only through a merge that passes pre-merge safety ([work-and-delegation.md](work-and-delegation.md)).

### Completion signals

LINA has no GitHub webhook endpoint, so LINA Core reads what GitHub knows, and an event inside LINA decides when ([work-and-delegation.md](work-and-delegation.md)).

- A worker's completion event starts the reads. The first is one view of the pull request: its state, head SHA, review threads and the check results at that head.
- While the checks of a task's pull request are unfinished, LINA Core watches that pull request only: one watcher per pull request, never two at once, using conditional requests and the interval that GitHub's responses ask for. The watch starts with the completion event and ends with the checks' conclusion, or with the pull request closing or merging. A rate-limit response backs the watch off and shows as "CI pending" with its cause.
- The read implementation uses fixed `gh api` arguments. One observation reads PR identity/state, paginated review threads and comments, and check runs plus legacy statuses pinned to that head. REST reads retain each response's ETag and send `If-None-Match`; a 304 reuses only that endpoint's cached body. Review threads use fixed GraphQL queries and are fetched once per observed head, or for an explicit fresh read. A changed head invalidates its checks and reviews. Unknown conclusions, missing checks, incomplete pages and malformed or oversized evidence stay pending with a cause; they never establish green.
- Core's `gh` children return plain HTTP headers and JSON even when Core inherits a color-capable terminal. Their environment removes `GH_FORCE_TTY`, `CLICOLOR_FORCE` and `FORCE_COLOR`, sets `NO_COLOR=1` and `CLICOLOR=0`, and disables prompts, paging and debug output. `GH_FORCE_TTY=0` is not a disabling value. These child-only settings preserve the user's GitHub credentials and tool configuration; Core does not edit the parent environment or stored login.
- The watcher uses `X-Poll-Interval`, `Retry-After` and exhausted-quota reset times, with a 30-second default poll interval and a one-minute fallback for a rate limit without a retry time. It shares one live watcher across callers, stops after all observed checks conclude or the PR closes/merges, and cancels and joins its children on Core shutdown. Terminal snapshots remain readable without another GitHub call; an explicit read starts a fresh observation need. The cache retains at most 256 PRs, evicting terminal entries first. Reads are observations only: success describes the fetched checks, not a merge authorization or verification verdict. Required-check policy and merge safety belong to the work engine's merge gate.
- Core exposes these observations through the negotiated `github` client capability and the `github_read` / `github_watch` operations of `work_request`. A watch request returns its current snapshot, including CI pending and the rate-limit cause/deadline; repeated requests share that watcher. No model account is required. Work-ledger event/reconcile callers can use the same service; this increment does not create task records or dispatch workers.
- LINA Core uses neither the repository events feed nor a webhook-forwarding tool. The feed's delay is too long, and a forwarder needs administrator rights on the repository and covers one repository at a time.
- A notification feed is read only if the user's own `gh` credential can read it, and then as a hint under the plugin rules.
- The periodic reconcile pass of the work engine re-reads every pull request of a task that is in progress or watched, and catches a signal that was lost.
- A merge is confirmed by one read after the merge call returns success or the queue reports the pull request merged (see Merges).

### Command profile

| Commands | Default |
| --- | --- |
| `gh repo delete`, making a repository public, publishing a release, force-pushing a protected ref, and `gh api` with the method DELETE | Outward or irreversible: asks every time |
| Every other `gh` and `git` command, including a merge after pre-merge safety with the head pinned, closing the task's own pull request, issues, comments, labels and draft releases | Never asks |

### Field ownership in a connected part

[work-and-delegation.md](work-and-delegation.md) defines development mode, where a 목표 (goal), an 일 (project) or part of one is connected to a repository. In a connected part, each field has this canonical side:

| Data | Canonical side |
| --- | --- |
| Code, branches, commits and tags | GitHub |
| Pull request state, merge state, review threads and check results | GitHub |
| Issue title, body, labels and state | GitHub |
| Branch protection, rulesets and required checks | GitHub |
| Goals, 일, 할 일 (to-do), plans, done conditions, approved scope, decisions and grants | LINA Core |
| The link between a code 할 일 and an issue, and between a draft and a pull request | LINA Core |
| Tasks, their order, write areas and hand-off state | LINA Core |
| The design-conformance review, the verdict and the apply record of delegated work | LINA Core |

A GitHub issue is optional. LINA creates one for a code 할 일 only when a developer user wants it (see Command profile).

### Merges

The shared role rule is in [product-families.md](product-families.md). [work-and-delegation.md](work-and-delegation.md) defines LINA Core's merge procedure, including the merge slot and the protected-ref check, and the apply records. On GitHub, these rules add:

- LINA Core never uses an administrator bypass of protection rules, even when the user's credential would allow one.
- LINA Core merges the head it read last, by passing that head to the merge call, so a commit pushed after its read is never merged unseen. If the merge is refused for a moved head, LINA Core reads the pull request again instead of merging.
- When the repository has a merge queue, LINA Core hands the pull request to the queue and treats queued as intermediate. The queue's own tests on top of the pull requests ahead replace nothing in pre-merge safety.
- When the repository has required checks and auto-merge, LINA Core may request the merge with the head pin after its review, so that GitHub merges when the checks pass. The checks and protection rules are the same ones, enforced by GitHub. This is off by default.
- Right before it merges, LINA Core reads the pull request again. If someone else has already merged it, LINA Core records that merge and doesn't try again.
- Before LINA Core records a merge made by a person as an observed result ([main-authority.md](main-authority.md)), it checks on GitHub that the merge commit exists and that the target ref points to it. It also reads the check results on that commit.
- A merge that went in without passing required checks is recorded as observed and reported to the user. LINA Core never reverts another actor's merge without a user decision.
- A merge is not verification. It records that a draft was applied. LINA Core still judges the task's delivery from evidence ([work-and-delegation.md](work-and-delegation.md)).

The Core implementation exposes merge control separately from read/watch as the
`github_merge` protocol capability. Its connection captures named local
`refs/heads/` in the parent clone (including the target branch) and verifies that
`origin` identifies the connected GitHub repository. A later request cannot
replace those hashes. Head acceptance and merge admission both compare the
captured refs. Before writing, Core rereads PR identity after reading checks and
reviews; a changed head/base blocks the call, and a changed head is read again.
If review reads detect a different head from the initial PR read, Core also
refreshes the observation once and returns it with the merge blocked. It never
continues to admission from that refresh, even if the original head returns.
Another head change or read failure ends that attempt without a write.
The fixed command is `gh pr merge <number> --repo github.com/<repository> --merge
--match-head-commit <head>`; Core supplies neither admin bypass nor auto-merge.
An unresolved P0/P1 marker blocks admission, including an outdated unresolved
thread. Pending, missing or failed check evidence blocks admission. GitHub also
enforces its protection rules at the pinned call. The explicit hostname keeps
reads and writes on the same service even when the user configures `GH_HOST`.

A durable intent reserves the repository/base before the call. A queued or
uncertain outcome retains that reservation across restart and cannot dispatch
again. Observation can settle it only with a merged PR, a matching existing merge
commit, the exact target ref pointing to that commit, and readable check/status
evidence on that commit. A target that has already advanced remains unconfirmed;
Core does not infer ancestry or silently accept different evidence. Missing or
failed checks on a confirmed person's merge are reported with
`merged_without_green_checks`, without reverting anything. The stored result
keeps checks, source and commit separately from any delivery judgment.


## Google

### The plugin

The Google plugin is a first-party plugin with a built-in connector. Its tools are named `google__<tool>`. Every tool takes an `account` argument, and `google__accounts` lists the connected accounts. Background features (the today feed, meeting notes and Drive material sources) call the same connector inside LINA Core.

### The user's own client

LINA ships no Google OAuth client. Every user connects through an OAuth client in their own Google Cloud project. A client shared by every LINA install would be a public app that requests restricted scopes such as full Gmail access. Google requires verification and a yearly CASA security assessment for such an app. A personal client used by its owner needs neither.

LINA's setup guide tells the user to:

1. Create a Google Cloud project and an OAuth client in it.
2. Configure the consent screen for external users, and set the publishing status to In production without submitting the app for verification. The client then runs as an unverified personal app.
3. Enable the Google APIs for the areas the user chooses.

The publishing status must be In production. In Testing status, Google issues refresh tokens that expire after 7 days for this kind of app, which would break every connection each week ([Google OAuth 2.0: refresh token expiration](https://developers.google.com/identity/protocols/oauth2#expiration)). An unverified app in production shows Google's unverified-app warning on the consent screen and is limited to 100 users, which a personal client never reaches ([Google Cloud: exceptions to verification](https://support.google.com/cloud/answer/13464323)).

The client id, the client secret and every refresh token live in LINA's secret store, under the person who set up the client or connected the account, and only LINA Core reads them.

### Accounts

The user can connect several Google accounts, both personal and company accounts, through the same client. Each account has its own consent, tokens and areas, and belongs to the person who connected it. Every item read carries the account it came from, and every write names the account it goes to.

A company's Google Workspace administrator can block the user's client or mark it as trusted. If an administrator blocks it, that account is unsupported and LINA tells the user why. LINA never works around an administrator's policy.

### Areas and consent

When the user connects an account, the user chooses the areas to use and consents once to the scopes of all of them. Adding an area later asks for consent to that area at that time. Full Gmail and Drive access use restricted scopes. That is allowed because the client belongs to the user.

The Google profile marks, for each area, the tools that are outward or irreversible effects and ask every time ([main-authority.md](main-authority.md#plugin-tools)). Every other tool of an area runs without asking. Deletion goes to the Google trash first, so only a deletion that can't be undone is irreversible.

| Area | What its tools do | Asks every time |
| --- | --- | --- |
| Gmail | Read mail and threads, search; labels, archive, drafts, move to trash | Sending, permanent deletion |
| Calendar | Read calendars and events; events on the user's own calendars with no other attendees, such as placing a task in free time | Invitations, and changes that notify attendees |
| Drive and Docs | Read files, documents and their revisions; create and edit files the account owns | Sharing, permission changes, publishing, permanent deletion |
| Contacts | Read, create and edit contacts | None |
| Meet records | Read conference records, transcripts and recordings | None |

Google is canonical for mail, events, files, contacts and meeting records. LINA keeps links and derived data, such as indexes, summaries and task candidates, and keeps them current by reconciliation. When an item is deleted in Google, LINA stops showing the data derived from it ([materials-and-knowledge.md](materials-and-knowledge.md)).

What each area feeds:

- Gmail and Calendar feed the today feed ([surfaces.md](surfaces.md)).
- Meet records and recordings feed meeting notes ([materials-and-knowledge.md](materials-and-knowledge.md)).
- Drive and Docs are material sources ([materials-and-knowledge.md](materials-and-knowledge.md)).

### Token failures

A refresh token stops working when any of these happens:

- the user revokes access
- the user changes the password of an account whose token holds Gmail scopes
- the token goes unused for six months
- an administrator restricts a service the token uses

When a token fails, LINA Core marks that account as needing sign-in and tells the user. It never falls back to another account, another client, or a key found in the environment.

## Deferred

- The manifest file name and schema, including the fields for the authorization server, initial scopes, pinned client, protocol era and call time limit: set during implementation acceptance of the plugin system.
- The exact OAuth scope list for each Google area: set during implementation acceptance of that area.
- Polling intervals, the watch interval for checks and the reconciliation period of each first-party plugin: measured and set during implementation acceptance of that plugin.
- The messaging services that get a shipped profile: set during implementation acceptance of the plugin system.
- Whether every catalog entry, and common servers on earlier protocol versions, connect through the pinned SDK: a smoke test over the catalog at implementation acceptance of the plugin system.
- Whether Google's desktop OAuth client accepts the loopback redirect of the client relay: set by the first implementation of the Google plugin.
- Showing MCP Apps interfaces: not supported; designed as a follow-up. If it is added, it arrives as the reserved `app` result type ([host-protocol.md](host-protocol.md#typed-results)): the view runs in a sandbox with no network, never reaches LINA Core directly, and the tool's text result is its fallback.
- The URL of the client metadata document: set when the project's domain is chosen; until then LINA registers dynamically.
- The catalog file format, the signing key, who curates the catalog and which entries it holds, with their service profiles: set during implementation acceptance of the plugin system; the project owner curates the catalog and holds its signing key.
- Timeouts of discovery and token calls, the refresh lead time and the default call time limit: measured and set during implementation acceptance of the plugin system. The starting values are 10 seconds for discovery and token calls, 30 seconds for a refresh and five minutes before expiry.
- Support for the Tasks extension: set when the pinned SDK supports it; LINA Core builds none of its own.
- Searching the official MCP registry from the add flow: not in the first release.
- Narrowing a local plugin server's network to named hosts: needs a filter that the sandbox wrapper does not have ([runtime.md](runtime.md)).
- A second account for one plugin: not in the first release.
- Whether the token that `gh` stores can read GitHub's notifications, and whether the check-run reads honour conditional requests: measured during implementation acceptance of the GitHub plugin.
- The lifetime of a pairing code, the rate numbers of each platform adapter and the channel client libraries: set during implementation acceptance of the channel gateway.
- Whether the owner's own messages in a Discord server may feed LINA's memory under Discord's developer policy: the owner confirms the reading before the Discord adapter ships. Until then the default `addressed` reads only what the agent is addressed with, and no memory is written from another person's message.
- A webhook mode for a channel, which would need a public listener: never provided.
- Rich messages and components of a platform, such as Telegram rich messages: shown only after the adapter's capability probe, with plain text as the fallback.
- The sync interval, page size, read window and use of live events of each messenger platform: measured and set during implementation acceptance of its adapter.
- Whether Slack accepts the loopback redirect of the client relay: set by its first implementation; the fallback is pasting the redirected URL. Webex uses the exact registered callback described above; real-tenant registration acceptance remains a live setup check. Pasting relays that same callback and does not bypass registration restrictions.
- Whether Discord's developer policy allows learning from the owner's own messages that a bot-mode messenger source reads: confirmed before that source learns. Until then it is searchable and not learned from.
- The progress digest of a channel: its line cap, whether a regular digest can be set, and the scheme of its link: set during implementation acceptance of the channel gateway.
