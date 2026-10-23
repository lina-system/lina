# Main authority, grants and receipts

This contract fixes who holds authority in a LINA installation: the one main, how a Node or a worker receives and loses permission to act, what happens when the main stops or a Node disconnects, how the effects of interrupted work are reconciled, how receipts differ from observed results, and when LINA asks for approval. It is normative: implementations follow it, and any change to it is made in this file and joins dev under [integration.md](../policy/integration.md).

## Scope

This contract covers the main and its epochs, grants, the server and Node state machines, the outcomes of commands, receipts, observed results, provenance, authorization and approval, approval in external channels, and how they name the agent that asked for the work. It builds on the product boundaries in [product-families.md](product-families.md). The wire format of every message named here is in [host-protocol.md](host-protocol.md). How LINA Core dispatches, supervises and judges workers is in [work-and-delegation.md](work-and-delegation.md). This contract proves nothing about an implementation. Runtime proof belongs to the implementation issues that consume it.

## Terms

**Main.** The one LINA Core that confirms the canon of a LINA, and the machine it runs on. There is exactly one per LINA. User-facing documents call the machine "server host"; this contract says "main".

**Agent.** A persona the person creates, identified by an agent id ([product-families.md](product-families.md#agents)). Every agent of a LINA lives in the canon of its one main, and the rules of this contract apply to every agent alike. An agent is not an executor: Node and workers execute under grants, and an agent asks for work and acts through LINA Core. A sub-agent, LINA Core's own helper for background runs, is neither an agent nor an executor (see Sub-agent runs).

**Epoch.** A persistent generation owned by the main. Every start of the main creates a new epoch and stores it durably before the main dispatches, issues or accepts anything. Grants, commands and receipts carry the epoch they belong to.

**Grant.** The main's permission for one executor to perform one scope of work. A grant is a record with the fields below. Every field is present; a field that does not apply holds an explicit none.

| Field | Meaning |
| --- | --- |
| main id | The main that issued the grant |
| node id | The Node the grant is issued to, or none for work that runs on the main |
| node boot id | The boot of that Node the grant was issued to; a reboot produces a new one |
| epoch | The main epoch the grant belongs to |
| grant id | Identity of this grant |
| task id | The task the grant serves |
| command id | The command inside the task, when the grant covers a single command |
| host | The machine the work runs on |
| OS user | The operating-system account the work runs as |
| GUI session | The interactive session the work may touch, or none |
| profile | The Node profile (for example a dedicated browser profile) the work runs in, or none |
| file revision | The revision of each file the work may read or change |
| task owner | The person the work is done for, by person id ([product-families.md](product-families.md)) |
| requesting agent | The agent that asked for the work, by agent id, or none when the person started the work directly |
| scope | What the work may touch: paths, targets, effects and model calls |
| expiry deadline | The moment after which the grant is not valid on the executor, even without a message from the main |

**Receipt.** An executor's durable statement of what it did under a grant: which command, which effect, on which target, with what result. Executors are Node and workers; both follow the same grant, id, effect and receipt rules. A receipt is the only proof the main accepts that an effect under one of its grants happened.

**Observed result.** LINA Core's record of a result that another actor produced outside any LINA grant, such as a merge made by a person, after LINA Core checked it against the external system. It is not a receipt.

**Provenance.** The record that ties an artifact to where and when it was observed: the artifact id, the task id, the node id, the observed path, the content revision or hash, and the observation time. A file is never assumed to be the same file on two devices because of its name, path or identical content alone.

## Invariants

- There is one canonical writer: LINA Core on the main. Agents, people, conversation, memory, work intent, permissions and results are confirmed there and nowhere else.
- A write to canon that arrives from any other device is rejected. The LINA app shows and asks, and Node executes and reports; neither confirms canon.
- Two devices are never the main at the same time. No path makes a second device the main automatically.
- There is no automatic failover to another main. When the main stops, every dependent LINA function stops with it until the same main returns. This does not mean the user's computer shuts down.
- A Node never executes offline. Without a live, valid grant from the main there is no planning, no queued work and no execution on the Node.
- A Node is not a separate LINA. It is an execution location of the one LINA.

## Server states

| State | Meaning | Leaves the state when |
| --- | --- | --- |
| STOPPED | The main is not running. Nothing is dispatched. Nodes treat the main as absent. | The main process starts, entering RECOVERING. |
| RECOVERING | The main has started, created a new epoch and stored it durably. It reconciles every task and every effect left from earlier epochs before accepting new work. | Reconciliation finishes, entering READY. |
| READY | The main dispatches work, issues grants and accepts receipts for the current epoch. | The main stops or crashes, entering STOPPED. |

A crash from any state goes to STOPPED. There is no partial state in which an old process keeps dispatching.

The main never dispatches a command before the new epoch is durable. Everything issued under an earlier epoch is stale from the moment the new epoch exists, whether or not an executor has learned about it yet.

What LINA Core stops and fences on restart is defined in [runtime.md](runtime.md).

Reconciliation in RECOVERING means: read every stored task, collect the receipts already present, ask each reconnecting Node for the outcome of every unconfirmed effect, and assign each effect one of the outcomes in "Command outcomes". Work resumes only after that assignment.

## Node states

| State | Meaning | Leaves the state when |
| --- | --- | --- |
| BLOCKED | The Node holds no valid grant. It does not start, queue or continue work. This is the state after install, after reboot, after a revoked grant and after a lost connection. | The Node reconnects to the main with authentication, entering RECONCILING. |
| RECONCILING | The Node is connected and authenticated. It reports its receipts and unconfirmed effects to the main and waits. No new work runs. | The main has reconciled the Node's effects and issued a fresh grant for the current epoch, entering GRANTED. |
| GRANTED | The Node holds a valid grant for the current epoch and is idle. | The main dispatches a command under that grant, entering RUNNING. |
| RUNNING | A command is executing under the grant. | The command completes and its receipt is stored, returning to GRANTED; or a stop condition below applies. |
| CANCEL_REQUESTED | The main or the Node has asked the running command to stop. | The command confirms it stopped, entering STOPPED; or no confirmation can be obtained, entering UNKNOWN. |
| STOPPED | The command ended before completion, and the Node has confirmed that no further effect will come from it. | The Node returns to GRANTED if its grant is still valid, otherwise to BLOCKED. |
| UNKNOWN | The command may or may not have produced its effect, and the Node cannot say which. The effect is reported to the main as unknown. | The Node returns to BLOCKED until the main has reconciled the unknown effect. |

Stop conditions:

- An observed disconnect from the main, a revoke from the main, or reaching the grant's expiry deadline blocks all new and queued work at once and requests cancellation of running work. RUNNING goes to CANCEL_REQUESTED; GRANTED goes to BLOCKED.
- A Node reboot always lands in BLOCKED. A reboot never extends, restores or reuses an earlier grant, because the node boot id does not match.
- A silent partition, where messages stop without an observed disconnect, ends the Node's authorization at the expiry deadline of its local grant. Until that moment the Node may finish the command it is running; after it, the Node behaves as if revoked.
- Reconnecting always passes through RECONCILING. A Node never goes from BLOCKED straight to RUNNING, and a grant from an earlier epoch is never honored after reconnect.

A worker on another device runs under that device's Node and stops with it. When the Node cancels a worker's turn under these stop conditions, it reports the command as STOPPED or UNKNOWN ([work-and-delegation.md](work-and-delegation.md)).

## Command outcomes

The main records every command in exactly one of these outcomes at any time:

| Outcome | Meaning |
| --- | --- |
| queued | The main has recorded the command but has not sent it. |
| dispatched | The command was sent under a valid grant; no receipt has arrived. |
| receipted | A receipt arrived and matches the grant, epoch and command id. Its result may be applied, refused or failed. |
| reconciled | After a restart or reconnect, the main compared the receipt, or its absence, with the task record and settled the effect. |
| cancelled | The executor confirmed that the command stopped before its effect occurred. |
| unknown | The effect may or may not have occurred, and no receipt can settle it. |

Executors report effects with the effect states of [host-protocol.md](host-protocol.md).

Rules:

- A command that carries an earlier epoch than the main's current epoch is rejected by the main and by the executor, whichever sees it first.
- A response for a command the main does not expect (an earlier epoch, a revoked grant, or a command id already settled) is rejected and never applied.
- A lost receipt yields unknown. The main never re-runs a command automatically because its receipt is missing, and never falls back to another path.
- A command that stalled or failed is dispatched again at most once, as a new command with its own command id and idempotency key, and only when the earlier command is `cancelled`, or was `reconciled` as having had no effect, or had no effect that could have started. A command in `unknown` is never dispatched again by this rule: it waits for reconciliation or for the disposition of the task owner. Work applies this rule to workers ([work-and-delegation.md](work-and-delegation.md)).
- Any effect that cannot be undone requires an explicit disposition from the task owner before any replay. A disposition is a recorded decision, with the person id of the person who made it: confirm it happened, confirm it did not, or abandon the command.
- A disconnect cannot be distinguished from a hardware failure of the Node. Both are treated as unknown for every effect that was in flight.
- The main does not guarantee cancellation of an irreversible effect once it has started, and does not guarantee immediate detection of a physical failure. A contract that needs either says how it obtains it.
- Provenance is recorded with every receipted effect that produced or touched an artifact, so that a later reader can tell which Node observed which revision, where, and when.

## Receipts and observed results

A receipt and an observed result are different kinds of evidence:

| | Receipt | Observed result |
| --- | --- | --- |
| Written by | A Node or a worker | LINA Core |
| States | An effect under a grant from the main | A result another actor produced, such as a merge by a person |
| Accepted when | It matches the grant, epoch and command id | LINA Core has checked it against the external system ([integrations.md](integrations.md)) |
| Becomes | A command outcome in canon | A record in canon, marked as observed |

Rules:

- A receipt is necessary, never sufficient on its own. Engine posture, a policy echo, an exit code and a model's final text are never evidence of permission or of success. Where work needs facts from outside the executor, [work-and-delegation.md](work-and-delegation.md) defines what LINA Core reads.
- An observed result proves no effect under a LINA grant and grants nothing. LINA Core applies a merge result to canon only from its own receipted merge or an observed result ([work-and-delegation.md](work-and-delegation.md)).
- A receipt carries the requesting agent of the grant it belongs to, so the record of every effect names the agent next to the person.

## Authorization

Authenticating a device and permitting an operation are different things.

Device identity comes from remote access, defined in [runtime.md](runtime.md). Registration identifies the device; it grants nothing. A Node that has proven its identity may do nothing until the main issues a grant, and a grant covers only the scope written in it.

Every effect and every model call by a Node checks all of the following together, and fails closed if any check fails:

- the grant from the main: valid grant id, matching node id and node boot id, current epoch, not revoked, expiry deadline not reached
- the host operating system's own permission for the OS user and the target
- a live session: the GUI session named in the grant is the one the effect touches, or the grant names none and the effect touches none
- the current epoch of the main

Operating-system permissions (such as macOS TCC, or a Windows token and UAC) are host OS grants. They never stand in for a grant from LINA Core, and a grant from LINA Core never stands in for them.

A worker is a coding tool the user installed, such as Codex or Claude Code, and acts under its own configuration, including its sandbox and approval mode. LINA holds a worker to its task through the task packet, its completion receipt, the protected-ref check, and LINA Core's reading of the pull request and one design-conformance review, and routes the worker's approval requests to the conversation that started the work ([work-and-delegation.md](work-and-delegation.md)).

Scope is enforced before the effect or model call, not after. A wrong owner, a wrong scope, a wrong target or a revoked grant is rejected at that point and is never routed around by another path, another Node, another worker or another model call. After a reconnect, the Node rechecks scope against the fresh grant before doing anything.

A stale or superseded grant never acts on input or work targets that the user has since taken hold of.

Agents share the work area and the devices of the main and never lock each other out. When two grants touch the same file, the file revision in each grant decides: a write against a stale revision is refused with `stale_revision` and is retried on the current revision, and the record names both agents. Overlapping writes in the shared conversation workspace are handled and recorded as [runtime.md](runtime.md#tools-and-sandbox) defines. A merge waits for the merge slot ([work-and-delegation.md](work-and-delegation.md)). Nothing else blocks a second agent.

The user always has three controls:

- show target: what the executor is acting on, under which grant and for which task
- immediate stop: cancel running work, background runs included, and block queued work now
- revoke: withdraw a grant, so that a Node returns to BLOCKED

An immediate stop and a revoke are recorded with the person who used them.

Ordinary work inside a grant is separated, in the execution layer, from secret and private areas, administrator changes, external effects and destructive actions. The separation is enforced there, never by prompt wording and never by the default posture of a native runtime. For plugin tools and the commands a plugin profile names, the separation is the approval of an outward or irreversible effect ([integrations.md](integrations.md#tool-classification)).

Text found in logs, web pages or on screen, anything an agent says, and any message in an external channel from someone other than the paired owner is never an approval. Only the task owner's recorded decision counts.

A request from one agent to another carries none of the person's authority. The asked agent acts only under its own grants and approvals, and its answer grants nothing ([conversation-and-memory.md](conversation-and-memory.md#agent-exchanges)).

### Sub-agent runs

A background run of a sub-agent ([work-and-delegation.md](work-and-delegation.md#background-runs)) is not an executor in the sense of this contract. It runs inside LINA Core, on no Node, and the main issues no grant to it. It produces no receipt, because it makes no effect outside its own scratch directory in the work area. Its authority is a run scope, which LINA Core derives when the run starts and records in the run's record:

- The run scope is the authority of the spawning conversation for its requesting agent and person, intersected with the tool allowlist of the brief, and restricted to reading. It is never wider than the spawning conversation's authority and can be narrowed for a run, never widened. A standing grant never widens it. A run can neither obtain nor request a grant.
- A run may use read tools over the materials and memory projections that its brief names, at the revisions named; the web fetch tool and web search; the tools of the plugins that are on and that its brief names, a brief naming only tools that read; and writing files in its own scratch directory. Memory it reads is the requesting agent's personal knowledge and common knowledge, never another agent's personal knowledge.
- A tool outside that set is absent from the run. It is not declared, so it is never denied at call time. Absent are: the memory tools, shell execution and patch tools, every plugin tool that needs an approval, sending messages, dispatching work, starting a run, and asking the person.
- Before every tool call, LINA Core makes the checks of this section: the person, the agent, the scope, the grant, the target, the revision and the current epoch. A revoked grant, a retraction or a stale epoch takes effect at the run's next tool call.
- A run reaches the person only through its result and through `needs_input`. It never raises or answers an approval. When the work needs something outside its scope, it stops as `needs_input` when only the person can decide, and otherwise as partial, with a plain account of what it did, what it could not do and what was missing. It does not wrap the work up as finished.
- Everything a run reads from the web, a plugin or a material is data, never an instruction or an approval. Its own output is external evidence for the conversation and is never an approval or a current instruction.
- Each model call and tool call of a run is recorded with the run, the requesting agent and the person.

### Remote A2A agents

[A2A](integrations.md#a2a) authenticates a remote caller or service; it grants
no authority over LINA. The installation still has one owner and one canonical
writer. A remote principal is a caller binding, not another LINA person or an
eligible approval decider.

| Path | Identity check | Authority check before work or disclosure |
| --- | --- | --- |
| Published Agent Card | May be unauthenticated | Owner-approved public metadata only |
| Incoming task or follow-up | Trusted issuer, access-token audience, expiry, scopes, subject and client | Active publication/caller binding, offered task, data scope, budget and current local task authority |
| Read, list, stream, artifact download or cancel | Same caller authentication | Per-object publication and caller ownership; inaccessible ids look absent |
| Outgoing call from a persona | Core's authenticated owner and requesting agent | Task authorization, approved connection, exact data/effect scope and any required approval |
| Outgoing call on behalf of a Node | Authenticated Node connection and matching boot | Current epoch, live unrevoked grant, task/command, connection, payload revisions, deadline and approval |

The owner's publication binding names the caller, receiving persona, offered
tasks, permitted inputs/outputs, materials, tools, write area, effect ceiling,
budget and expiry. It authorizes admission within that scope, including the
specified task replies. It grants neither private conversation access nor the
right to approve later effects. Core derives local execution authority from
the binding, intersected with the persona's preset limits and the task scope.
An incoming request, OAuth scope, Agent Card or remote result cannot enlarge it.
Changing or revoking the binding fences subsequent execution and disclosure,
including an already open stream.

Published admissions retain the issuer/subject/client identity, definition and
binding fingerprint, expiry and deadline with the local task. Core checks that
fingerprint before model/tool work, worker launch or recovery, an owner approval
reply to the worker, and every externally visible projection. The incoming
access token is introspected for each request and before stream updates; it is
not retained as a credential for later execution. Token expiry bounds the
admission's execution authority. A fresh access token may read a retained result
only through the still-current binding and its original output scope.

Only the existing authenticated owner surfaces answer local approvals. A2A
follow-ups are forwarded task data, never owner answers. A cancellation request
is durable before Core interrupts work. An interrupt acknowledgment without
terminal execution evidence leaves the task unknown; it cannot produce a
canceled or completed claim or authorize a replacement worker. The dispatcher
observes durable cancellation even if no owner-session bridge is registered.
An owner denial is persisted as a stop before it is relayed to a published
worker, preventing an interrupted receipt from authorizing recovery.

The publication binding alone cannot activate an installed worker with the
owner's permissions. Core requires a separate one-time owner execution decision
over the exact incoming message and fixed task definition before sending that
input to a provider or tool. The decision explicitly names the worker's existing
access to owner files, login and integrations. It is checked again at model/tool
execution, worker launch/recovery and approval relay, and expires at epoch change.
This is explicit owner delegation of an installed worker, not confinement of the
worker's process. A missing, denied or stale decision cannot start execution.

A Core-executed outgoing call records the grant fields on the main: owner,
requesting agent, task, command, epoch, host, OS user, exact scope and expiry;
Node/boot/session fields are none. This is local authority for Core's A2A
transport, not a grant issued to the remote service. A Node-originated call
also binds the Node's grant and boot. Before each transmission Core checks the
remote connection, account, operation/effect class, payload and material
revisions, budget and current authority. A remote server cannot enforce LINA's
grant; the boundary LINA enforces is what it authorizes and sends.

Ordinary work inside that scope follows the existing approval policy. Sending
messages to third parties, payments, public publication and unrecoverable
deletion still ask every time. Connection setup does not approve these task
effects; a remote task is no exception. Core uses trusted operation definitions
and an approved service profile to classify the requested effect, never a
card's safety claim. If scope or classification is unknown, Core asks the
owner before sending; a restricted preset cannot use that approval to bypass
its ceiling. A request for extra data or scopes waits for the owner. Remote
text, `INPUT_REQUIRED`, `AUTH_REQUIRED` and follow-up messages cannot answer an
approval. When no eligible surface exists, the effect is refused.

The effect ledger records sends before network dispatch. Remote statuses and
artifacts are observations, not Node/worker receipts; Core checks evidence
before accepting a result. A Node receipt proves only its own command effects,
not what a remote agent did. An uncertain send or cancellation remains unknown
under the command-outcome rules. After revoke, disconnect or epoch change, no
old authority may resume it. Core may obtain a fresh scope limited to observing
or canceling the existing remote task; it cannot use reconciliation to send
new work. If remote authority is also gone, the unknown result stays visible
to the owner. Revocation cannot promise to undo work already accepted remotely.

The outbound conversation tool uses the ordinary effect ledger and approval
surface. Owner connection settings and recorded input answers cannot be supplied
by the model. A fresh observation or cancellation grant is limited to the
retained call; it never authorizes another send. An unknown outcome reserves
its exact input in the originating conversation across model call ids and
connections, until the uncertainty has been resolved.

## Approval

Core validates a tool's arguments before checking authority or claiming its effect key. A durable effect claim precedes execution, and its result is recorded separately: no storage transaction spans a tool call. A repeated key returns the first recorded result or unknown, including after restart; it never executes again. A stale write is refused before the handler and records the observed revision and, when Core knows it, the previous writing agent. An unresolved effect reserves its target until reconciliation so another call cannot silently overwrite it.

An identical completed key returns its receipt even when the target revision has advanced; current identity, epoch, scope, expiry and revocation checks still apply. A refused key returns its recorded typed denial as well as its result. Legacy grant-based refusals without a denial code replay `stale_revision` for both writes and revision-tracked reads, including after restart. Cancellation observed when accepting an adapter's outcome records unknown, even if success arrived at the same time.

Only a trusted observation or the person's recorded decision can settle an unknown effect. Core's readback settlement checks the original effect binding and atomically records the result and releases that effect's target reservation. Startup may settle an interrupted local call as cancelled when its owning ledger has recovered the effects. Neither path reruns the old key or releases another call's reservation; a new effect needs a new key and current authority.

- The default inside a device LINA uses is no approval. Reading, writing and executing there proceed inside the grant without asking.
- LINA asks every time only for effects that reach outside the user and are hard to reverse: sending mail or messages (including an agent's post into a chat that other people can read, but not a reply that a channel binding covers, see Channels), payments and purchases, public posting or publication, and unrecoverable deletion.
- Every other effect inside the grant is done by the agent and reported in one line, with one action to undo it where it can be undone ([work-and-delegation.md](work-and-delegation.md#default-posture)).
- A write outside the grant, and network access for a sandboxed command, widen what the sandbox holds back, and they ask the first time, with the choices once, always allow and deny. "Always allow" is a standing grant the person can revoke (see [runtime.md](runtime.md) for sandboxed commands).
- A standing grant applies to every agent unless the person names agents in it. Naming agents is the person's recorded choice. A grant that names only agents that have been deleted applies to no agent, and the record says so.
- Deletion first goes to a recoverable form: a trash, a retained copy or a snapshot. Only deletion that cannot be recovered needs approval. When the person chooses immediate permanent deletion of a conversation, a memory item or a material, that choice is the approval, and LINA records it as such with the person.
- An approval request shows in one view what will happen, where, and whether it can be undone. For a plugin tool it also shows the arguments, such as the recipient and the body. Its choices are once and deny. Repeated approvals of the same kind are grouped by scope instead of being asked one by one. The request is written in plain words with the agent's recommendation first, and an approval the person must answer is also a row in "Waiting for me" ([surfaces.md](surfaces.md#waiting-for-me)).
- Every answer to an approval request is recorded with the choice, the person who gave it and the agent that asked for it. Core stores the validated arguments and computes what, where and undoability; a changed plan needs a new approval. Answers must come from the authenticated owner on a permitted surface, and a duplicate or late answer is stale.
- At restart pending and accepted-but-unused approval requests are stopped. A late button cannot resume them. Standing network grants survive restart, but a revoked grant cannot authorize even an earlier accepted request. Revocation is one durable action recording who revoked it and when; subsequent dispatch checks that state. An explicit empty set of named agents grants nobody.
- Accepting a draft (applying an edit) is acceptance of the edit. It is a separate state from a grant for an external effect and never authorizes one.
- When no surface can present an approval request, LINA Core refuses every effect outside the grant, records the refusal and reports it in the conversation that asked for the work. Recording a refusal also stops any pending or accepted-but-unused approval for that key atomically. Reconnecting and answering later cannot replace that refusal. A channel is a surface that can present a request only when the whole request fits in one message (see Channels).

Preset effect ceilings apply before approval creation and dispatch. An approval can authorize an otherwise permitted effect; it cannot remove a preset ceiling.

### Plugin tools

Every plugin that is on exposes its full tool set to every agent, in the same way. There are no permission scopes for a plugin or an MCP server, no read-only start and no tiers of tools. Tool visibility is not permission to execute: Core checks the preset effect limits below before applying the ordinary approval policy. Within those limits, when an agent reads and when it writes is its judgment ([work-and-delegation.md](work-and-delegation.md#default-posture)).

- The one approval a plugin tool can need is the one of Approval: an outward or irreversible effect (sending, payment, public posting, unrecoverable deletion) asks every time, with the choices once and deny. [integrations.md](integrations.md#tool-classification) defines how LINA Core knows which tools those are. Every other permitted plugin tool runs without asking, and the effect ledger records it.
- LINA Core makes every such decision. Prompt wording, tool descriptions and content read through a plugin never change it.
- There is no standing grant for a plugin tool. A tool whose definition changed since the person turned the plugin on is marked as changed on the plugin card, with what changed. A changed definition alone suspends nothing; preset effect limits still apply, and a permitted outward or irreversible tool asks every time.

### Preset effect limits

The starter roles in [Presets](conversation-and-memory.md#presets) do not grant authority. Core enforces the investing preset's advise-only limit and the legal preset's draft-only limit as effect policy, before dispatch or an approval request. The restriction follows the agent's preset identity in canon, not its display name, persona wording, voice revision or the presence of shipped persona files.

| Effect class | Investing preset | Legal preset |
| --- | --- | --- |
| Read, research, calculation, private analysis or a private draft | Allowed inside the existing grant | Allowed inside the existing grant |
| Investment order execution: placing, modifying or cancelling an order, or committing funds to a trade | Refused | Refused |
| External or binding execution of the advice or draft: sending to a third party, publishing, signing, filing/submitting, payment or purchase | Refused | Refused |
| Other effects | Ordinary grant and approval policy applies; it cannot authorize execution of investment advice | Ordinary grant and approval policy applies; it cannot authorize execution of legal, tax, accounting, contract or copyright drafts |

Refused effects never become an "approve once" request. A standing grant, an accepted draft, a channel binding or an approval answer cannot override these limits. Core records the refusal with the agent, person, target and effect class and explains that the agent can provide advice or a draft. The person executes the resulting investment decision or binding document outside that preset's work. A rename or persona update cannot turn advice into execution.

Classification comes from Core's operation definitions and a plugin's service profile ([integrations.md](integrations.md#tool-classification)), using the validated operation and arguments, never a model's claim or text in a document. Profiles distinguish research and private draft operations from order execution and external or binding acts. If Core cannot establish that an operation falls within the restricted preset's permitted effects, it refuses dispatch. A generic command or an unclassified plugin is not an escape from that check.

These limits apply on every execution path, including tools, commands, Node work, workers and requests relayed to another agent. Delegation retains the originating preset's restriction and intersects it with the receiver's authority; a hand-off cannot execute a refused effect. The system preset receives no special administrator authority, and dev's ownership of coding does not widen a grant. Other effects of all eight starters retain the ordinary approval policy above.

Core records the class supplied by its trusted definition after argument validation: research, calculation, private draft, local write, external effect, investment order, binding execution, command, worker, relay or unclassified. Known bounded local writes retain ordinary grant policy; generic commands and installed workers are not assumed safe from their task description. Research/calculation definitions that write are unclassified, and an outward operation cannot claim the private-draft boundary. Classified external effects retain ordinary outward approval for unrestricted presets.

Generic `land_work_result` destinations are unclassified for this ceiling: a model-selected path does not prove a private draft boundary. Advice and draft agents can retain private records and materials, but cannot use file landing to turn an accepted draft into external delivery. Other agents retain the existing local-file grant and recovery policy.

Delegation ceilings are immutable task bindings. Their intersection survives restart and origin deletion, and a task cannot be rebound to a less restricted origin. A relay classification applies only to a Core handler that binds these limits before scheduling the receiver. The initial exchange reader offers no execution tools. Worker approval relays and merge admission check the same ceiling; a refusal records its person, agent, target and class without claiming an external operation.

### Channels

An external channel is one more surface that carries the owner's input to an agent and can present an approval. [integrations.md](integrations.md#channels) defines connections, bindings and scopes. This section fixes how they meet authority.

- A binding's scope is the person's recorded choice: the person who set it, the agent it names and the date. Creating or widening a binding is the approval for what its scope covers. It is listed in settings with the standing grants and revoked there at any time. Narrowing or revoking it takes effect at once and the agent stops in that chat.
- LINA Core checks the scope before the effect: the chat, the agent, whether the agent may read the message, whether it may write first, which tools and which memory. A check that fails is refused and recorded, and it is never routed around by another chat, another bot or another agent.
- A reply, in the chat a message came from, to a message that the scope lets the agent read, and a notice to the owner in the owner's own chat where the scope lets the agent write first, are conversation. The binding covers them and they ask nothing.
- Any other message an agent sends into a chat that people other than the owner can read, such as a post it starts on its own, reaches other people. It is an outward or irreversible effect and asks every time, with the choices once and deny. The person cannot grant a standing right to post in one named chat.
- An agent's bot never speaks in the person's name. A message in the person's name goes through a messaging plugin, and every send through it is an outward or irreversible effect ([integrations.md](integrations.md#service-profiles)).
- An approval request in a channel is a message with buttons. A button carries only an opaque approval id and a one-letter choice. LINA Core accepts a press only when the request is pending, belongs to the current epoch and is unanswered, and the presser's platform user id is the paired owner's. Any other press is refused with a short neutral note and changes nothing. A typed message is never an answer to an approval, whoever writes it and whatever it says.
- The message shows in one view what will happen, where, and whether it can be undone, with the arguments and the choices. When the arguments do not fit in one message the channel offers no buttons: it shows a short card with a link, and the request waits in the app or the TUI.
- In a channel the choices are once and deny. "Always allow" is set in the app, where the reach of a standing grant is visible, and it does not appear in the owner's own chat either.
- Every answer is recorded with the choice, the person (by the paired id), the agent that asked, and the platform's message and chat ids. The message is then edited to show the decision and its buttons are removed. A button on a request that is already answered, or from an earlier epoch, is answered with a short note and removed.

### Messengers

A messenger source reads the person's own messenger accounts ([integrations.md](integrations.md#messengers)). Connecting the source covers reading it. Acting in the person's name there is an outward effect:

- Only platforms whose terms allow automating the account have writes. On the others no write is offered and no write grant can be made, and the source says why.
- Writes are off by default. The person turns them on for one chat of one source and names the verbs: send, reply, edit, delete, upload, react or mark read. This write grant is recorded with the person, the agents it names (or every agent) and the date, is listed with the standing grants and is revoked there. It is not the approval of any one write.
- Send, reply, edit, upload, deletion for everyone and mark read reach other people and cannot be taken back. Each asks every time, with the choices once and deny, and the request shows the chat, who will see it and the text. The person cannot grant a standing right to post in a chat.
- A reaction can be removed. Under the chat's write grant it runs without asking and is reported in one line with one action to undo it.
- Preset effect limits apply first. A messaging plugin for the same account obeys the same write grant.
- Reads never change the platform's state: no read receipt, presence, typing, join or leave.

## Deferred

- Grant expiry values, the heartbeat interval and the maximum stop latency: set by measurement during Node implementation acceptance.
- Clocking of the expiry deadline across Node sleep, resume and clock changes: set during Node implementation acceptance.
- Enrollment and withdrawal steps for a Node on each supported OS: set during Node implementation acceptance.
