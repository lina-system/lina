# Product families and agents

This contract fixes what LINA is made of: the product families and their components, the agents the person creates, where canon lives and who writes it, how roles are shared, which platforms and installation modes exist, and how LINA is built and versioned. It is normative: implementations follow it, and any change to it is made in this file and joins `dev` under [integration.md](../policy/integration.md).

## North star

The person does not look at memory context, development issues, detailed plans or pull requests. They exist inside LINA and open only when the person explicitly asks for them, for every person, power users included. The person sees two things: the design at a glance, with how far each piece of work has come, and the list of what they must do or decide. Replies are short and lead with the conclusion; detail goes to the work panel, never inline.

Every contract follows this section. [surfaces.md](surfaces.md#default-view) defines the two views and what stays out of the default view, [conversation-and-memory.md](conversation-and-memory.md#reply-shape) defines how replies are shaped, and [work-and-delegation.md](work-and-delegation.md#progress-display) defines how work reaches the person as outcomes and decisions. A screen or reply detail in any contract that conflicts with this section is changed to follow it.

## Scope

This contract covers:

- the north star: what the person sees by default
- the people LINA serves: the person id and the owner
- each component's responsibility, and what it never takes on
- canon, its one writer, and the other kinds of state
- agents: the persona unit, the two knowledge layers, what agents share, the speaker rule, and where agents speak: external channels and agent exchanges
- roles and the two rules that every actor follows
- supported platforms and installation modes
- the repository, the artifacts and the kinds of version
- what LINA owns itself for delegated work

It does not define the envelope, version negotiation, the supported-combination table or conformance fixtures; those are in [host-protocol.md](host-protocol.md). Grants, epochs, receipts and approval are in [main-authority.md](main-authority.md). The runtime, packaging, updates and remote access are in [runtime.md](runtime.md). Persona layers, memory items and recall are in [conversation-and-memory.md](conversation-and-memory.md). This contract proves nothing about an implementation. Runtime proof belongs to the implementation issues that consume it.

## LINA

LINA (Lifelong Intelligent Navigator & Ally) is a companion platform. The person creates agents in it, and each agent is one persona and one companion (see Agents). LINA ships eight starter presets: Selene (system), Lina (`lina`), Sion (dev), Rumi (knowledge), Arin (life), Cleo (creative), Kai (investing) and Kanna (legal). The first run creates one agent from each, preserving those names and personas. Lina is the default agent whose conversation opens first; LINA names the product.

Product text calls the personas the person creates agents, in Korean 에이전트. The coding tools that LINA delegates work to, such as Codex, Claude Code, Antigravity and Devin, are workers ([work-and-delegation.md](work-and-delegation.md)); product text never calls them agents. LINA's own background helpers, the sub-agents, are neither: they have no persona, name, memory or conversation, run inside LINA Core, and are described in [work-and-delegation.md](work-and-delegation.md#background-runs).

LINA is licensed under the Apache License, Version 2.0 ([LICENSE](../../LICENSE), [NOTICE](../../NOTICE)), and is not a sold product. The LINA repository holds LINA Core, the terminal UI, Node, LINA OS and the published protocol between the LINA app and LINA Core, and all of it is under that licence. The LINA app is the one closed-source part: it is developed in a separate private repository and ships as a binary (see Repository, artifacts and versions). Every source file of the LINA repository carries `SPDX-License-Identifier: Apache-2.0`. The dependency rules in [dependencies.md](../policy/dependencies.md) keep every dependency that is linked into LINA permissive. Every client shows an about view that names the build's version and commit, with a link to that commit's source in the LINA repository when the build comes from it, and an agent's bot answers its `about` command with the same version, commit and link. No release gate, such as age verification, is required before a release.

## People

A person is a human LINA serves. Each person has a person id: stable, assigned by LINA Core in canon, and never reused for another person. The first person is the owner, the person who sets up the LINA. LINA Core records the owner when it creates the main's canon, so the first run asks nothing to create the owner. The owner's ChatGPT sign-in, which is LINA's login, later binds the owner's ChatGPT identity to this person id ([runtime.md](runtime.md#lina-login)). That binding identifies the owner to LINA Core once; clients and devices do not present it. A LINA admits only its owner. Admitting more people is multi-person operation, a backlog item that this canon does not define yet ([cognition-and-life.md](cognition-and-life.md)).

- A person is not an agent. The person axis and the agent axis are separate (see Agents).
- A main holds one canon under one state root ([filesystem.md](filesystem.md)): its agents, people, knowledge, work and grants. The main id names the main that confirms canon ([main-authority.md](main-authority.md)).
- Every record of something a person said, decided, granted, connected or signed in to names that person's id. Each contract that lists a record's fields names the field. A client connection acts for one person ([host-protocol.md](host-protocol.md)).
- Because a LINA admits only its owner, every person id in canon is the owner's.
- A human who writes in a shared chat of an external channel without being the owner is not a person of this LINA. LINA keeps the platform's user id of such a human on the messages as evidence only. It creates no person id, no person model and no memory item for them, and their text is never the owner's words, an instruction or an approval ([integrations.md](integrations.md#channels)).
- "A person" in the role rules below means any human actor, whether or not LINA serves them.

## Product families and components

There are three product families: LINA Core, LINA APP and LINA OS. Node is a separately installed device execution component of the LINA Core family. It is not a fourth family, not a feature of LINA APP and not a separate LINA. LIFE is a background world engine plus a personal feed in the LINA app. It is not a separate product.

Users see LINA APP as "the LINA app". No other app name is used.

| Component | Owns | Never owns | Ships as |
| --- | --- | --- | --- |
| LINA Core | The engine and the terminal UI (`lina`). The canon of the main: agents, people, knowledge, work and grants. Inside it: the conversation engine ([conversation-and-memory.md](conversation-and-memory.md)), the work engine ([work-and-delegation.md](work-and-delegation.md)), the Moirai engine ([cognition-and-life.md](cognition-and-life.md)) and the skill system. The message gateway that dispatches agents to external channels ([integrations.md](integrations.md#channels)). Coordination of delegated work, approved scope and decisions. The person's model-provider accounts, their credentials and the provider adapters ([runtime.md](runtime.md#model-path)). | Device execution on another machine. GUI screens. Installation, update and recovery of an operating system. | Independent artifact with its own release cadence. `moirai-worker` is installed with it, and the Claude adapter's files ship in it ([runtime.md](runtime.md)). |
| LINA APP | The LINA app on desktop and mobile: conversations with the person's agents, work threads, questions, results, settings and the LIFE personal feed. It shows and asks; the logic lives in LINA Core. Desktop and mobile have the same functions, flows and terms; only the layout differs by screen size ([surfaces.md](surfaces.md)). | Canon. Product logic. Execution on the device it runs on. A direct path to a model, a worker or an external channel. A second main. | One React codebase in TypeScript, closed source, kept in a separate private repository and shipped as a binary, bundled with a Wails v3 shell on desktop (macOS, Windows and Linux; Electron is the fallback behind the same bridge interface) and with a WebView host on mobile, Capacitor by default ([surfaces.md](surfaces.md#app-front-end)); each platform build is an independent artifact with its own release cadence |
| LINA OS | An integrated product on Omarchy 4 Linux that combines verified LINA Core, Node and LINA app artifacts with an operating, update and recovery environment and a Hangul input setup. It may offer to install a coding tool such as Codex as the user's own tool, which LINA then runs as a worker, and Node.js for the Claude adapter ([runtime.md](runtime.md)). Manifest-based installation, diagnosis and recovery. | Product logic of LINA Core, the LINA app or Node. Copies of their code or ledgers. A second canonical LINA Core. | Composition of pinned artifacts with its own release cadence |
| Node | Device execution under a grant from the main, on the device where Node is installed, as if the main were sitting there. LINA uses many devices, local or remote, as one machine. Receipts for what it did. | Independent planning. Offline execution. Any canon. Screens. A second main. | Independent artifact per supported OS with its own release cadence |
| LIFE | A background world engine that keeps a world, its time, its events and the daily activity of the person's agents, and a personal feed in the LINA app that shows the agents' activity, posts, reactions, media and conversation experiences ([cognition-and-life.md](cognition-and-life.md)). | A separate app, installer or login. A persona of its own. A connection to a real social network. A multi-person product. | The world engine runs on the main as part of the LINA Core family, because the LINA app holds no logic. The feed is part of the LINA app. |

Responsibilities do not overlap. A release of one component never carries a copy of another component's logic, and integration acceptance checks for duplicated responsibility across components.

LINA uses tools the user installs as they are: coding tools such as Codex, Claude Code, Antigravity and Devin as workers ([work-and-delegation.md](work-and-delegation.md)), the command-line tools that plugins rely on, and the servers of plugins the user turns on from the catalog or adds ([integrations.md](integrations.md)). LINA records their versions and never ships, pins or owns them ([runtime.md](runtime.md)).

## Canon and writers

LINA Core on the main is the only writer of agents, people, conversation, memory, work intent, permissions and results. There is exactly one main per LINA: the one LINA Core that confirms canon. The LINA app and Node attach to the main as screens and executors. They never confirm canon themselves.

State comes in three kinds, and each has one writer:

| Kind | Writer | Meaning |
| --- | --- | --- |
| Canon | LINA Core on the main | The authoritative agents, people, conversation, memory, work intent, permissions, approved scope, decisions and results |
| Receipt | Node, or a worker | The statement of what an executor did under a grant from the main ([main-authority.md](main-authority.md)) |
| Client display | The LINA app or the TUI | What a client currently shows, derived from the main and never authoritative |

Canon has two stores, and LINA Core is the only writer of both: LINA Core's database, and the records repository, a Git repository of the person's human-readable work and knowledge records that the database indexes ([filesystem.md](filesystem.md), [work-and-delegation.md](work-and-delegation.md#storage-of-records)).

Rules:

- The LINA app and Node never write canon. The LINA app shows and asks; Node executes and reports.
- Agents are canon objects written by LINA Core. A preset ships its working persona publicly as core and default voice layers; backstory and world material stay private. The agent created from it is canon.
- When a client display disagrees with the main, the main wins and the client refreshes.
- When a receipt disagrees with the main's expectation, the main reconciles. The executor never rewrites the main's record.
- Canon, events, evidence, scope and permissions are kept mechanically, and surfaces translate them for each role. Completion, memory and sync are never decided by a model's sentence.

One main across many devices is not the same as cloning a disk or an account. Artifacts on different devices are linked by provenance, as defined in [main-authority.md](main-authority.md). The same name, path or content alone never makes two files on different devices the same file.

## Agents

An agent is one persona and one companion. An agent has an agent id, a name, the four persona layers with their revisions, an emotional state and personal knowledge ([conversation-and-memory.md](conversation-and-memory.md)). LINA ships eight starter presets with their existing personas: Selene (system), Lina (schedule, plans, business and coordination), Sion (development), Rumi (people and wiki), Arin (hobbies and daily life), Cleo (lyrics, music, writing and visuals), Kai (investment advice) and Kanna (legal, tax, accounting, contracts and copyright drafts). Their preset keys, methodologies and source layout are defined in [Presets](conversation-and-memory.md#presets). Public `persona/<preset>/core-v1.md` and `default-voice-v1.md` contain the working personas, excluding owner-identifying and operational private details; only backstory and world material stay in the private world repository. First run creates all eight agents and opens Lina's conversation, asking only for the ChatGPT sign-in. Afterwards the person adds agents from a preset or description, and renames, tunes and deletes them. Initialization never recreates an explicitly deleted starter.

Sion, the dev preset agent, owns coding work: issues, workers, pull requests and merge decisions. Lina coordinates agents and plans, including handing coding requests to Sion. Investing is advise-only and never places orders; legal is draft-only. Core enforces those boundaries through [effect classes and approval policy](main-authority.md#preset-effect-limits), independently of persona text or an approval answer.

An agent created from a preset takes its core and default voice layers from the preset, and a preset update gives those agents the next revision of those two layers. The agent owns its user voice and growth snapshot layers and the values of its blank settings, and no preset update touches them. An agent created from a description owns all four layers. An agent is data that the one LINA Core runs. There is no process, engine or worker per agent. The model policy is a setting of the main with one entry per model role, used by every agent.

Knowledge has two layers. Personal knowledge is held by one agent and known only to it. Common knowledge is held by every agent. Every memory item names its holder, an agent id or `all`, and the holder is checked before an item becomes a search candidate. Recall, the person brief, the standing profile, ledger search and context assembly read only the speaking agent's personal knowledge and the common knowledge. A new item about the person and the world (facts, preferences, commitments, decisions, verified work results) is common knowledge. A new item about the relationship (relationship, emotion observations, shared history, how the person likes that agent to talk) is that agent's personal knowledge. The person can say otherwise for any item. Moving an item between layers is a recorded act. The person does it, or an agent does it when the person asks or under a grant the person gave. Correcting or forgetting a common item applies to every agent. When the evidence of a common item is a turn in one agent's conversation, another agent sees the source agent, the time and the event id of that evidence; the turn itself is shown to the person and to the source agent. The same fact learned in two agents' conversations is one common item with two pieces of evidence.

Everything else in the main is shared, like one computer used by several people: the work area and its file system, materials and connected sources, the work and knowledge records (목표 (goal), 일 (project), 할 일 (to-do), 계속 챙기는 것 (area), 메모 (memo)) and intent cards, the work queue and work ledger, workers, Node devices, plugins and their accounts, model-provider accounts, channel connections and their bot accounts, standing grants, and the model budget. One Moirai engine and one `moirai-worker` serve every agent. A background job that reads one agent's personal records, such as extracting memory from its conversations, runs as that agent's job, and each item it writes takes its layer by the rule above. A job for the whole main, such as Klotho's goal discovery, supervision, forecasts and news, reads only common knowledge, plans and the work ledger, runs once, and writes common output; the agent in the conversation speaks its suggestions. A grant applies to every agent unless the person names agents in it. Every record of something an agent did, asked for or changed names the agent id next to the person id. This covers tasks, effects, receipts, approvals, asset revisions, commits and grants.

Agents never lock each other out. When two agents touch the same thing, the existing checks decide and the record shows both agents. A write to a material, a connected source or a record against a stale revision is refused with `stale_revision` and is retried on the current revision. In the shared conversation workspace, `apply_patch` fails when its context lines no longer match, and the agent reads the file again and retries; a shell write is recorded in the effect ledger with the changed paths, their hashes before and after, and the agent id ([runtime.md](runtime.md)). A merge waits for the merge slot. A GUI session has one seat-lease holder at a time, and another task waits or gets another session. Nothing else blocks an agent.

Creating, changing and deleting an agent is the person's act and is recorded with the person id. Deleting an agent moves its persona revisions, emotional state, conversations and personal knowledge to the trash for 30 days, and then deletes them permanently with tombstones. Common knowledge the agent wrote stays, with the agent id as its source. Before deleting, the person can move any of the agent's items to common knowledge. Tasks and background runs the agent asked for keep running; their questions and results go to the person's home cards, and the person can hand them to another agent's conversation. A grant that named only the deleted agent applies to no agent, and the record says so. Its channel bindings end at once, and its bot accounts stay with the person. An agent exchange it took part in stays as a record of the other agent, with the deleted agent named by its id.

The agent axis is separate from the person axis. The person id records who said, decided, granted or signed in. The agent id records which persona answered, asked or acted. A LINA admits only its owner. Admitting other people is a backlog item on the person axis and does not change agents.

### Speaker rule

A conversation has a list of participating agents. A conversation with the person holds one agent, and an agent exchange holds two (see Channels and exchanges). Only a participating agent speaks in a conversation, and every event in the conversation ledger names the agent that spoke it, except a system-originated event, such as the notice of a finished background run, which speaks for no one, is marked as system-originated and names the agent whose conversation it belongs to. Each person has one main conversation with each agent, and it is the same conversation on every surface: the TUI, the LINA app and the owner's own chat with the agent's bot in an external channel. A thread is attached to one artifact and one agent. A conversation ledger is the personal record of the agents in it. Group conversations, in which several agents take part with the person in one conversation, are built on this model later; they are a backlog item in [cognition-and-life.md](cognition-and-life.md#backlog).

### Channels and exchanges

LINA Core runs a message gateway. An agent is dispatched to an external channel, such as a Telegram bot or a Discord bot, by a binding: a channel connection (a bot account that belongs to the person) plus the permission scope the agent has in that chat ([integrations.md](integrations.md#channels), [main-authority.md](main-authority.md#channels)). Agents own no accounts; the bot accounts are the person's, as every other account is.

- The speaker rule holds in a channel. Each dispatched agent has a bot of its own, so the platform shows that agent's name and face. A bot that serves several agents is bound per chat, topic or channel and never interleaves agents in one chat; the agent names itself in the first line of each message. A chat has at most one bound agent. Two agents in one chat would be a group conversation, a backlog item.
- The owner's own chat with an agent's bot is the owner's main conversation with that agent. A chat shared with other people is a separate conversation of the bound agent ([conversation-and-memory.md](conversation-and-memory.md#channel-conversations)).
- Agents talk to each other in an agent exchange: a background conversation between exactly two agents, with no person turn ([conversation-and-memory.md](conversation-and-memory.md#agent-exchanges)). It never interrupts the person and never travels through a channel. The person sees a log with one line per exchange, not the content by default ([surfaces.md](surfaces.md#agent-talk-log)).
- A channel or an exchange never moves an item between the two knowledge layers.

## Roles

Roles name a default owner, never an exclusive one.

| Actor | Default role |
| --- | --- |
| LINA Core | Operating delegated work: defining it, choosing the worker, reading the worker's pull request, and merging or closing it ([work-and-delegation.md](work-and-delegation.md), [integrations.md](integrations.md)) |
| A worker | Writing the code, the branch and the pull request of delegated work, and opening it |
| A person | Anything |

LINA Core and a person may each merge a pull request in a repository the person connects to LINA. LINA Core authors no pull request of delegated work. A person may fix and create pull requests.

Every actor follows two rules, and only these two are enforced:

1. Pre-merge safety. Before any merge, by any actor, the required checks pass, the protection rules hold and the protected refs match.
2. Result record. Every merge, close, fix and pull request creation leaves a record by its actor: LINA Core's merge or close in canon, with the agent that asked for the work; a worker's pull request in its receipt and in the repository's own history; a person's in the repository's own history. In a repository connected to LINA, LINA Core applies every such result to canon. Its own result arrives as a receipt, and a merge by a person as an observed result after LINA Core checks it against the repository ([main-authority.md](main-authority.md)).

## Platforms

- The main runs LINA OS, either as the device operating system or in a VM. A LINA Core-only install on another Linux system works, but it is not the intended way to use LINA.
- Node and the LINA app support Linux (Omarchy), macOS and Windows. For the LINA app, macOS usability comes first, and LINA OS and Linux come second ([surfaces.md](surfaces.md#app-front-end)).
- On mobile, the LINA app supports iOS and Android. A mobile device never runs Node or LINA Core.
- Platform limits of the sandbox and of coding work are in [runtime.md](runtime.md). The LINA app's platform builds and how it adapts to each platform are in [surfaces.md](surfaces.md).

## Installation modes

There are two intended installation modes. Both use the Linux filesystem of [filesystem.md](filesystem.md).

- Device-OS install. LINA OS on a machine the user also uses. LINA works inside the user's environment, so the non-competition rules of [non-competition.md](non-competition.md) apply. Non-competition is the responsibility of this install, not of Node.
- VM install. LINA OS as a virtual machine on an existing Mac, Windows or Linux host. This is LINA's own environment. Its permissions stay inside the VM sandbox and never reach the host. To act on the host or on any other user device, LINA uses a Node installed on that device.

A desktop install is not a third mode. It is the LINA app and Node installed on an existing Mac, Windows or Linux machine and connected to the main of a device-OS or VM install.

The LINA app and Node install independently. Installing the LINA app never requires Node, and installing Node never requires the LINA app. Both connect to the one main.

LINA OS has three profiles:

- Main. LINA Core, Node and the LINA app in one install, with exactly one update owner.
- Headless main. The main profile with the local LINA app autostart turned off. Users reach it through a remote LINA app. Headless never makes the LINA app a second brain.
- Connected device. Node and the LINA app, connected to the main. It never starts a second LINA Core.

Each mode and profile is built from one of the runtime compositions in [runtime.md](runtime.md), which also defines what stays available when a piece is missing.

## Repository, artifacts and versions

LINA is the private monorepo `thisisjun786/lina`, which holds LINA Core, the terminal UI, Node, LINA OS and the published protocol between the LINA app and LINA Core. The LINA app lives in a separate private repository, `thisisjun786/lina-app`, and ships as a binary. Work in the monorepo branches from `dev` and joins `dev` through the integration script after the local foundation verification passes ([integration.md](../policy/integration.md), [foundation.md](../policy/foundation.md)). `main` mirrors releases, and releases are immutable tags of the form `v0.0.1-alpha.YYYYMMDD.N` ([versioning.md](versioning.md), [releases.md](../policy/releases.md)). Issue #1 of the repository is its roadmap.

Users report problems and ideas in one public place for every product, the closed LINA app included: the GitHub Issues of the LINA repository, with an `app` label and issue forms for a bug and for an idea. Ideas and open questions go to GitHub Discussions. Every client offers a report-a-problem action that drafts the issue and posts it only after the person confirms ([surfaces.md](surfaces.md#settings)). A person without a GitHub account has no route in the first release; a relay or an e-mail route is a later option.

No code, database structure or fixture is ported from another LINA codebase. Planning records and the owner's existing intent card data come in as data, once, through the one-time import at the operational switch ([work-and-delegation.md](work-and-delegation.md)). Vendored upstream code follows [dependencies.md](../policy/dependencies.md).

One source repository does not mean one release unit. LINA Core, the TUI, Node and the protocol files are built from one tag. The LINA app and LINA OS have their own artifacts and release cadences. Being in the monorepo is never a reason to merge release units.

There are three kinds of version (the version policy is in [versioning.md](versioning.md)):

| Version | Belongs to | Rule |
| --- | --- | --- |
| Product version | Each release unit | Names a release. It never decides compatibility. |
| Protocol and capability version | Each connection: client and Node | Decides compatibility between installed components ([host-protocol.md](host-protocol.md)) |
| LINA kit version | The LINA kit | Recorded in the release manifest of every build that uses the kit ([runtime.md](runtime.md)) |

LINA OS consumes pinned artifacts: a specific verified LINA Core, Node and LINA app, fixed by manifest with version and digest. LINA OS duplicates no code and no ledger from the components it composes. Replacing the OS is separate from migrating or rolling back state schemas. The release manifest is defined in [runtime.md](runtime.md).

An integration candidate that includes the LINA app is complete only when the real desktop package and the verified LINA Core and Node are pinned together. Server-side readiness alone never completes it.

## Compatibility and updates

Mixed versions are expected, because the artifacts release independently. The supported-combination table, version negotiation and refusal are defined in [host-protocol.md](host-protocol.md). The update order, rollback, the hold-and-report rule for a failed update and the failure injections are defined in [runtime.md](runtime.md).

## Delegated work and operations

Planning, documents, approved scope, decisions, execution permission, agents and receipts are owned by the main's canon. No external copy is canonical.

Installation, management, delegation and recovery work without any external issue tracker account, API, webhook, external ID, URL or cache. A required path never depends on an external planning service being reachable. The product has no import from or export to an external planning service. Every user gets LINA's own work and knowledge management; the only difference for a developer user is the link between a code 할 일 and GitHub, which is a reference and not a sync of planning data ([work-and-delegation.md](work-and-delegation.md#plans), [integrations.md](integrations.md)). A planning service's tools may be used in conversation through a plugin ([integrations.md](integrations.md)); they never become canonical or a required path. Records the operator tool keeps elsewhere are taken over once at the operational switch ([work-and-delegation.md](work-and-delegation.md)).

Operator functions live in the products:

- coordination, scope, permissions, reporting and recovery in LINA Core
- work, question and operating screens in the TUI and the LINA app
- per-device execution in Node
- manifest-based installation, diagnosis and recovery in LINA OS

A command-line wrapper alone never counts as providing one of these functions. LINA Core takes over operator work from the operator tool through a single-owner switch ([work-and-delegation.md](work-and-delegation.md)).

Model-provider accounts, their sign-in and model enablement are owned by LINA Core: one account per service, signed in by the person ([runtime.md](runtime.md#accounts-and-sign-in)). User-managed provider policy stays external.

The tools used to develop LINA itself (operator tooling, issue trackers and GitHub) are facts of the development process, never product runtime dependencies.

## Deferred

- The Omarchy distribution method, the supported Omarchy versions and devices, and measured performance and compatibility: set by the LINA OS install-profile implementation and its measurements.
