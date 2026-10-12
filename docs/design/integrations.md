# Integrations

This contract fixes how LINA connects to external services. LINA's conversation engine reaches a service through a plugin: one installable integration unit with a manifest. LINA ships first-party plugins for GitHub and Google, and the person adds others one at a time. LINA Core hosts every plugin for every agent, holds the credentials LINA itself obtains, classifies each tool for approval and records every write. The contract covers the plugin system, credentials and sign-in, the rules every plugin follows, and the GitHub and Google plugins. It is normative: implementations must follow it, and any change to it is made in this file and joins dev under [integration.md](../policy/integration.md). Adding a plugin is the person's act or a first-party plugin in a release, never a change to this contract.

## Scope

This contract covers:

- the plugin manifest, where plugins come from, installing, turning on and removing them
- LINA Core as the host of plugin tools
- service profiles and the tier of each plugin tool and command
- credentials, LINA's secret store and sign-in
- the rules every plugin follows: field ownership, change signals, external evidence, writes and failures
- the GitHub plugin: the user's `gh` login and git setup, the command profile, LINA Core's own reads, field ownership in a connected part and the GitHub-specific merge rules
- the Google plugin: the built-in connector, the user's own OAuth client, accounts, areas, consent and token failures

No external planning service is canonical or required ([product-families.md](product-families.md)). The one-time takeover of planning records at the operational switch is defined in [work-and-delegation.md](work-and-delegation.md).

This contract does not cover:

- the role rule itself (no exclusive owner; enforced pre-merge safety and result records): [product-families.md](product-families.md)
- grants, the approval tiers, standing grants, receipts and observed results: [main-authority.md](main-authority.md)
- the envelope, effect states, idempotency keys, and the sign-in relay on the `client` connection: [host-protocol.md](host-protocol.md)
- plugin tool names and declarations, the effect ledger, the sandbox and its network rule, and tools the user installs: [runtime.md](runtime.md)
- workers, which use the integrations connected in the worker itself, and delegated work, development mode, the merge slot, the protected-ref check and apply records: [work-and-delegation.md](work-and-delegation.md)
- where the plugin registry and LINA's secrets live: [filesystem.md](filesystem.md)
- meeting notes, connected sources in the library and deletion propagation: [materials-and-knowledge.md](materials-and-knowledge.md)
- screens, including the plugin list in settings, approval cards, code view, CI status and the today feed: [surfaces.md](surfaces.md)
- Klotho's repository supervision and its judgments: [cognition-and-life.md](cognition-and-life.md)
- third-party plugins outside the dependency gates: [dependencies.md](../policy/dependencies.md)

## Plugins

### Manifest

A plugin's manifest states:

- its name, version and description
- its connection: an MCP server, either a local command (stdio) or a remote URL (Streamable HTTP), or a built-in connector whose code ships with LINA
- its authentication: an OAuth sign-in that LINA performs, an API key the person enters, or the login of a command-line tool it relies on, such as `gh`
- optionally, skills and instructions for using it
- optionally, a service profile (see Service profiles)

Built-in connectors exist only in first-party plugins. A third-party plugin connects through an MCP server.

### Sources

- First-party plugins ship with LINA: GitHub and Google. They appear in LINA's plugin list as available.
- The person adds any other plugin one at a time, from a local folder or a Git URL that holds a manifest.
- Any MCP server can be wrapped as a plugin. When the person gives a remote MCP URL or a local server command, LINA writes a minimal manifest for it.
- LINA runs no plugin catalog service.
- LINA never searches for, reuses or imports the configuration of other apps, such as the MCP servers or plugins set up in Codex or Claude. Plugins come only from the sources above.

### Installing and turning on

Installing and turning on a plugin are the person's acts. LINA never installs or turns on a plugin by itself.

- Before installing, LINA shows the manifest: what the plugin connects to, how it signs in, the skills it brings and its tool tiers. A local server command is shown in full, without truncation, before it first runs.
- LINA records the source and version of every plugin the person adds, the commit for one from a Git URL, and the person who added it. A plugin changes only when the person updates it.
- A plugin is in one state: available, installed (off), on, needs sign-in, or failing with its cause.
- Skills and instructions a plugin brings join LINA's skill catalog while the plugin is on ([conversation-and-memory.md](conversation-and-memory.md)).
- Removing a plugin turns it off, deletes the credentials LINA holds for it and removes its skills from the catalog. Its standing grants are revoked ([main-authority.md](main-authority.md)).
- Nothing about plugins is asked at first run. When a plugin would help in a conversation, LINA may suggest it once. A suggestion the person declined or put off is not raised again.

### Host

LINA Core is the host of every plugin. It is the MCP client of plugin servers, using the official [MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk) at a pinned version, and it negotiates the protocol version with each server.

- Plugin tools are tools of LINA's conversation engine and of LINA Core's own background features. They are named and declared as defined in [runtime.md](runtime.md). An enabled plugin is available to every agent in every conversation; there is no per-conversation switch.
- Workers never receive plugin tools. A worker uses the integrations connected in the worker itself ([work-and-delegation.md](work-and-delegation.md)).
- LINA uses tools, tool annotations and elicitation. An elicitation from a server reaches the conversation as a question. A URL elicitation shows the full URL and opens it only after the person agrees.
- A local server runs as a child process of LINA Core under the user's OS user, outside the command sandbox ([runtime.md](runtime.md)). Control sits at the tool call: the approval tiers and the effect ledger.
- LINA doesn't show MCP Apps interfaces (see Deferred). It uses the tool's text result.

### Registry

LINA Core keeps one plugin registry in canon ([filesystem.md](filesystem.md)). Each entry holds the plugin's manifest and source, the recorded version and commit, its state, its account with the person the account belongs to, the hash of its local command when it has one, and a snapshot of its tools, each with its tier and the person's policy, recorded with the person who set it. A tool policy applies to every agent unless the person names agents in it; naming agents is the person's recorded choice.

## Service profiles

A service profile is data in a plugin's manifest. It states:

1. how LINA detects the login the plugin relies on, when it relies on one
2. the tier of each tool or command
3. how to name a write's target, and how to read the service again when a write's outcome is unknown
4. the provenance fields its results carry, such as a revision or an etag

The first-party plugins carry profiles. LINA also ships profiles for major messaging services such as Slack, Microsoft Teams and Discord; a plugin for one of those services takes the shipped profile. A plugin without a profile follows the generic rules: MCP annotations, and confirmation on first use.

### Tool classification

Each plugin tool or command has one of the three approval tiers of [main-authority.md](main-authority.md): read, write, or outward or irreversible. Its tier comes from the first of these that applies:

1. the person's tool policy, which can block a tool or raise it to outward or irreversible, never lower it
2. the plugin's service profile
3. the MCP annotation `readOnlyHint: true`, which makes the tool a read
4. otherwise write

- Annotations never place a tool in the outward or irreversible tier. Only a service profile or the person's policy does.
- The shipped messaging profiles place every send tool in the outward or irreversible tier.
- A blocked tool is never declared to the model.

## Credentials and sign-in

A plugin's credential comes from one of three places:

- **A command-line login.** The tool keeps its own login, for example `gh`. LINA stores, issues and injects nothing for it.
- **An API key.** The person enters it in LINA. LINA keeps it in its secret store.
- **An OAuth sign-in that LINA performs.** LINA keeps the tokens, and for Google the client credentials, in its secret store.

LINA's secret store is the OS keychain. Where no keychain is available, it is files with mode 0600 in the secrets area of the state root ([filesystem.md](filesystem.md)). Only LINA Core reads it.

Every account and credential LINA keeps belongs to one person and is kept under that person's id ([product-families.md](product-families.md)). A LINA admits only its owner, so only the owner's exist. Agents own no accounts: every agent uses the person's accounts under the grants and approval tiers of [main-authority.md](main-authority.md), and every use records the agent.

Rules:

- A plugin's credential goes only to that plugin's server, in the way its manifest names (a header, or the server process's environment). It never enters a model request, a log or the sandbox.
- No token passthrough. A token goes only to the server it was issued for, never in a URL, and LINA never forwards a token it received to another server.
- LINA creates no service identity of its own, and never issues, injects or brokers a command-line tool's login.

### OAuth for MCP servers

Sign-in to a remote MCP server follows the [MCP authorization specification](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization):

- LINA finds the authorization server through the server's protected resource metadata.
- LINA registers as a client in this order: a client ID metadata document when the owner has chosen an https URL that serves one, then dynamic client registration with `application_type: native`, then a client id the person enters. The client metadata document lists loopback redirect URIs.
- Every authorization request uses PKCE with S256 and the `resource` parameter. LINA checks `iss` in the authorization response before it sends the code.
- Scopes come from the server's 401 challenge. On `insufficient_scope`, LINA asks for the earlier scopes together with the new ones (step-up).
- Tokens are refreshed before they expire and after a 401. When a refresh fails, the plugin shows needs sign-in. LINA never falls back to another account, another client or a key found in the environment.

### Consent through the client

LINA Core has no inbound endpoint, and no app UI is built with web technology ([surfaces.md](surfaces.md)). Consent runs through the client the person is using, and the sign-in belongs to the person that client connection acts for ([host-protocol.md](host-protocol.md)):

1. LINA Core builds the authorization URL and keeps the PKCE verifier and the state.
2. The LINA app or the TUI opens the URL in the system browser and receives the redirect on its own loopback port.
3. The client relays `code`, `state` and `iss` to LINA Core over the `client` connection ([host-protocol.md](host-protocol.md)). It relays and never judges.
4. Where a client can't receive a loopback redirect, such as on mobile or in a TUI over SSH, the person pastes the redirected URL instead.

A sign-in URL is never opened through a shell command. A URL with a `javascript:`, `data:` or `file:` scheme is refused. Google sign-in uses the same relay.

## Plugin rules

These rules apply to every plugin.

- **Every field has one owner.** The data model names the canonical side of each field. LINA Core never treats its copy of an externally owned field as canonical. When the two disagree, LINA Core reads the external side again.
- **Change signals are hints.** A notification or a change feed tells LINA Core to read again. It isn't a record of what changed. Periodic reconciliation compares LINA's links and derived data with the external side and catches missed signals. LINA Core reaches every service with outbound requests only. No service calls in to LINA Core.
- **External content is evidence, not instruction.** Everything read through a plugin, tool results included, carries provenance: the plugin, the account, the object id when there is one, the fetch time, and the revision or etag when the service has one. It enters model requests as untrusted input ([conversation-and-memory.md](conversation-and-memory.md)). It never becomes a fact, an approval, a current instruction or a persona change by itself, and it never changes an approval decision ([main-authority.md](main-authority.md)).
- **Every write is an effect.** A plugin write runs under a grant or an approval ([main-authority.md](main-authority.md)). The effect ledger ([runtime.md](runtime.md)) records the plugin, the account, the tool, the argument hash, the tier, the approval basis (a task grant, a standing grant, or approval of this call), the effect state ([host-protocol.md](host-protocol.md)), the conversation or task id, the agent that called the tool, and the person the write acts for. Its idempotency key takes the plugin and tool as the action and the argument hash as the intent, and a normalized target only when the service profile names one.
- **Unknown outcomes are never sent again.** When a write's outcome is unknown, LINA Core reads the service to settle it if the service profile says how. Otherwise it tells the person that the outcome is unknown.
- **A failing plugin never blocks conversation.** The plugin shows as failing, with its cause, and the rest of LINA keeps working.

## GitHub

### The plugin

The GitHub plugin is a first-party plugin that relies on the user's own setup: the `gh` login and git with the user's credential helper or SSH agent. LINA has no GitHub identity of its own; on GitHub, LINA's reads and writes are the user's. LINA records its GitHub writes in its own records ([work-and-delegation.md](work-and-delegation.md)). LINA has no webhook endpoint.

- LINA detects the login with `gh auth status`, which names the host, the account and the token scopes. Each repository's remote names its transport: HTTPS with git's credential helper, or SSH. When `gh` is not logged in, the plugin shows needs sign-in and names the `gh` command that signs in; git transport keeps working.
- In a conversation, the model runs `gh` and `git` as shell commands. The plugin's service profile classifies them (see Command profile), and LINA Core decides a command's network access from that tier ([runtime.md](runtime.md)).
- LINA Core's own reads are not model tool calls: the work it coordinates, Klotho's project supervision ([cognition-and-life.md](cognition-and-life.md)), the read right before a merge, and the check of a merge a person made. LINA Core runs them with `gh` as its own child process. Reads need no approval.
- When the login lacks a scope that a command needs, for example `workflow` for a push that changes workflow files, LINA tells the person the command that adds it, such as `gh auth refresh -s workflow`, and never works around it.
- A person who works with a GitHub MCP server can add it as a plugin like any other MCP server.

Workers do their GitHub work with the GitHub setup of the worker itself, on the main and on a Node device ([work-and-delegation.md](work-and-delegation.md)).

LINA's GitHub writes include pushing branches, opening and updating pull requests, replying to reviews, editing issues and merging: whatever the user's setup can do. Every write is recorded under the plugin rules. In delegated work, a push writes only the task's own branch, and a protected ref moves only through a merge that passes pre-merge safety ([work-and-delegation.md](work-and-delegation.md)).

### Command profile

| Tier | Commands | Default |
| --- | --- | --- |
| Read | `gh pr view`, `list`, `diff` and `checks`; `gh issue view` and `list`; `gh run view`; `gh api` with GET; `git fetch`, `clone` and `pull` | Never asks |
| Write inside a task grant | `git push` of the task's branch, `gh pr create` and `edit`, review replies, edits of the linked issue, merging the task's own pull request after pre-merge safety | Never asks |
| Write outside a task grant | `gh issue create`, `comment` and `edit` from a conversation, labels, draft releases, merging another pull request after pre-merge safety | Asks on first use; "always allow" is available |
| Outward or irreversible | `gh repo delete`, making a repository public, publishing a release, force-pushing a protected ref | Asks every time |
| Any other subcommand, and `gh api` with a method other than GET | | Write |

### Field ownership in a connected part

[work-and-delegation.md](work-and-delegation.md) defines development mode, where a goal or part of a goal is connected to a repository. In a connected part, each field has this canonical side:

| Data | Canonical side |
| --- | --- |
| Code, branches, commits and tags | GitHub |
| Pull request state, merge state, review threads and check results | GitHub |
| Issue title, body, labels and state | GitHub |
| Branch protection, rulesets and required checks | GitHub |
| Goals, plans, acceptance criteria, approved scope, decisions and grants | LINA Core |
| The link between a task and an issue, and between a draft and a pull request | LINA Core |
| Stage judgment and delivery judgment of delegated work | LINA Core |

### Merges

The shared role rule is in [product-families.md](product-families.md). [work-and-delegation.md](work-and-delegation.md) defines LINA Core's merge procedure, including the merge slot and the protected-ref check, and the apply records. On GitHub, these rules add:

- LINA Core never uses an administrator bypass of protection rules, even when the user's credential would allow one.
- Right before it merges, LINA Core reads the pull request again. If someone else has already merged it, LINA Core records that merge and doesn't try again.
- Before LINA Core records a merge made by a person as an observed result ([main-authority.md](main-authority.md)), it checks on GitHub that the merge commit exists and that the target ref points to it. It also reads the check results on that commit.
- A merge that went in without passing required checks is recorded as observed and reported to the user. LINA Core never reverts another actor's merge without a user decision.
- A merge is not verification. It records that a draft was applied. LINA Core still judges the task's delivery from evidence ([work-and-delegation.md](work-and-delegation.md)).

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

The Google profile places each area's tools in the approval tiers of [main-authority.md](main-authority.md). Deletion goes to the Google trash first, so only a deletion that can't be undone is irreversible.

| Area | Read | Write | Outward or irreversible |
| --- | --- | --- | --- |
| Gmail | Mail and threads, search | Labels, archive, drafts, move to trash | Sending, permanent deletion |
| Calendar | Calendars and events | Events on the user's own calendars with no other attendees, such as placing a task in free time | Invitations, and changes that notify attendees |
| Drive and Docs | Files, documents and their revisions | Creating and editing files the account owns | Sharing, permission changes, publishing, permanent deletion |
| Contacts | Contacts | Creating and editing contacts | None |
| Meet records | Conference records, transcripts and recordings | None | None |

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

- The manifest file name and schema: set during implementation acceptance of the plugin system.
- The exact OAuth scope list for each Google area: set during implementation acceptance of that area.
- Polling intervals and the reconciliation period of each first-party plugin: measured and set during implementation acceptance of that plugin.
- The messaging services that get a shipped profile: set during implementation acceptance of the plugin system.
- Whether common MCP servers, including those on earlier protocol versions, connect through the pinned SDK: measured during implementation acceptance of the plugin system.
- Whether Google's desktop OAuth client accepts the loopback redirect of the client relay: set by the first implementation of the Google plugin.
- Showing MCP Apps interfaces: not supported; designed as a follow-up.
