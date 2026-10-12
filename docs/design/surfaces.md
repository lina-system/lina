# Surfaces

This contract fixes how a person meets LINA and its agents on screen: the TUI, the LINA app on each supported platform, and the launcher. It defines what a surface may do, the one design language every surface shares, the one app codebase and how it adapts to each platform, the quality bar it must meet, and the screens for conversation, agents, plans, work, the library and feeds. It is normative: surfaces must follow it, and any change to it is made in this file and joins dev under [integration.md](../policy/integration.md). Finishing this document does not mean any surface is implemented or accepted; runtime proof belongs to the implementation issues that consume it.

## Scope

This contract covers:

- the TUI as the first surface
- the LINA app as a thin client, and the only state it may hold
- the design language: terms, flows, the visual language, design tokens, icons and how agents are presented
- the app framework, platform adaptation and the "no web feel" quality bar
- the screen skeleton: home, conversation, work panel, layers and threads on screen
- agents on screen: choosing, creating, tuning and deleting agents
- plans, intent cards and work cards; the library; the today feed, the news feed and the LIFE feed; settings, including the plugin list
- the launcher and its clipboard history
- Korean input and accessibility acceptance

It builds on these documents and does not repeat them:

- product boundaries, supported operating systems, canon writers, agents and the speaker rule: [product-families.md](product-families.md)
- the envelope, generated protocol types, version negotiation and client connections: [host-protocol.md](host-protocol.md)
- conversation behavior, threads, agents and their personas, the first run and memory: [conversation-and-memory.md](conversation-and-memory.md)
- the work engine, plan data, the intent card data model and observation of workers: [work-and-delegation.md](work-and-delegation.md)
- materials, connected sources, passage anchors and citations: [materials-and-knowledge.md](materials-and-knowledge.md)
- packaging, remote access and the sandbox: [runtime.md](runtime.md)
- Klotho, project supervision, the LIFE world engine and the backlog: [cognition-and-life.md](cognition-and-life.md)
- grants and approvals: [main-authority.md](main-authority.md)
- plugins, including GitHub and Google: [integrations.md](integrations.md)

## Surfaces and platforms

A surface is a program through which a person sees LINA and talks to its agents. There are three:

| Surface | What it is | Where it runs |
| --- | --- | --- |
| TUI | The terminal screen of the `lina` command, built on Bubble Tea | Ships with LINA Core |
| LINA app | The desktop and mobile app (component LINA APP) | Every supported OS, as one Flutter app built natively for each platform |
| Launcher | A global launcher with clipboard history, part of the LINA app | macOS |

LINA APP is the component name in code, artifacts and documents. People see it as the LINA app, named LINA, and no other app name exists ([product-families.md](product-families.md)).

The LINA app targets every operating system that [product-families.md](product-families.md) supports: Linux (Omarchy), macOS and Windows on desktop, and iOS and Android on mobile. A platform's app enters release builds, and with them the supported-combination table of a release ([host-protocol.md](host-protocol.md)), only after its build passes the acceptance in this file. Until then it is built only as a development build.

Every surface talks to the one main LINA Core, locally or across the tailnet ([runtime.md](runtime.md)). No surface reaches the model path, the model proxy or a connected source on LINA's behalf; LINA reaches them only through LINA Core.

## TUI

The TUI is the first surface. It is complete on its own: conversation with every agent, creating and tuning agents, memory and its corrections, delegated work with its questions, results, cancellation and approvals, and plans with several tasks across devices all work from the TUI with no GUI installed.

- The TUI is built in Go on [Bubble Tea](https://github.com/charmbracelet/bubbletea), with Bubbles and Lip Gloss as needed, and ships in the same static binary as LINA Core ([runtime.md](runtime.md)).
- Bubble Tea, and Bubbles and Lip Gloss when used, are pinned to exact versions recorded in the release manifest. Code taken into the repository records its source commit, files, modifications and notice in `THIRD-PARTY-NOTICES.md` ([dependencies.md](../policy/dependencies.md)).
- The TUI is the terminal screen of the `lina` command. It runs as its own process, separate from LINA Core, and uses only LINA Core's public event and control API over the client connection in [host-protocol.md](host-protocol.md). The socket location is set with the install paths ([runtime.md](runtime.md)).
- The TUI uses every capability of the client connection: conversation, task, approval, sign-in relay and multi-task control ([host-protocol.md](host-protocol.md)).
- The plugin list and its actions (see Settings) work from the TUI as commands.
- Agents work from the TUI as commands (see Agents): list the person's agents, switch to an agent's main conversation, create an agent from a preset or from a description, tune an agent, and delete one. Each command is a request to LINA Core over the client connection ([host-protocol.md](host-protocol.md)).
- Message types are generated from the protocol JSON Schema. The TUI is tested alone against a fake LINA Core built from the conformance fixtures ([host-protocol.md](host-protocol.md)).
- Protocol errors and LINA Core logs go to the status line and the log, never into the conversation ([host-protocol.md](host-protocol.md)).
- The TUI shows the conversation experience that [conversation-and-memory.md](conversation-and-memory.md) defines: streaming answers, the earlier conversation, connection failures and readiness failures with their cause, and the same conversation after a restart.
- The TUI uses the same terms, flows, state labels and agent marks as the LINA app. A card in the TUI is a labeled block, and an agent appears in the TUI by its name.

## The LINA app is a thin client

The LINA app shows and asks. It never writes canon. All logic lives in LINA Core.

- The app shows what LINA Core reports and asks the person what LINA Core needs. It sends the person's input, answers and decisions to LINA Core.
- Every edit made in the app (an intent field, a front page, a document, a plan, a memo, an agent, a setting) is a request to LINA Core. LINA Core applies it and records the saved version; the app then shows the saved result.
- The app holds client display state only: what is on screen, layout, reading position and unsent drafts. Display state is never authoritative ([product-families.md](product-families.md)).
- The app makes no judgment of its own: no ranking, recall, completion, plan decision or permission check. It shows LINA Core's result, including "unknown".
- The app's protocol types are generated in Dart from the protocol JSON Schema. An unsupported app and LINA Core combination is refused and the refusal is shown to the person ([host-protocol.md](host-protocol.md)).
- When the connection to LINA Core drops, the app shows that state, keeps unsent drafts and the reading position, and shows nothing as saved until LINA Core confirms it.
- Work in progress or an open question never blocks the main conversation or other work. The app never loses a conversation draft or a reading position while work runs.

The app provides conversation with each agent and its threads, the agents screen, work threads, questions, results, plans and intent cards, the library, the today feed, the news feed and the LIFE feed, and settings. On macOS it also provides the launcher.

## One design language

The LINA app is one Flutter codebase, built natively for iOS, Android, macOS, Windows and Linux. The TUI is its own Go code on Bubble Tea. One design language unifies every surface, and every surface shares the generated protocol types. The design language has six parts, each defined once for all surfaces.

**Terms.** Every surface takes its user-facing words from one shared term list.

- The app is called LINA. The personas the person creates and talks to are agents (에이전트 in Korean). LINA is also the name of the default agent, made from the LINA preset ([product-families.md](product-families.md)).
- Versions are "saved version", "draft" and "apply". No surface shows git terms such as commit, branch or merge. A draft in a GitHub-connected scope links to its pull request.
- A state that is not observed is "unknown". "Unknown" is never shown as none, zero, empty or done.
- Object names (goal, initiative, project, issue) are provisional. The design names roles and permissions, not words, so a rename touches only the term list. Naming is on the backlog in [cognition-and-life.md](cognition-and-life.md).

**Flows.** A task has one flow on every surface. Desktop and mobile carry the same features, flows and terms; only layout differs by screen size. The TUI follows the same flows in terminal form.

**Visual language.** LINA's brand is a visual language in the style of Apple's own apps and Things 3. It looks the same on every platform: the LINA app draws it itself and never imitates a platform's chrome, while the behavior people rely on follows the platform (see Platform adaptation).

- Surfaces are calm, light and airy, with generous whitespace. The conversation, cards and documents are the subject.
- Titles are large and firm.
- One accent color marks the primary action, the selection, LINA's mark and "Waiting for me". It never fills a large surface.
- Corners are soft, with continuous curvature (squircles).
- Hairline separators and steps of lightness divide layers, never heavy shadows.
- Pretendard sets Korean and Latin text, bundled with the app so that text looks the same on every platform; code uses a monospaced face. Korean text breaks between words, and body text has a line height of at least 1.5.
- Motion is restrained: gentle springs with no exaggerated bounce, and nothing moves unless a state changes. One or two small signature interactions carry LINA's delight; everything else is familiar.
- Every feed has an end.

**Tokens.** One design token source, in the Design Tokens Community Group (DTCG) format, holds color, type, spacing, radius, motion and icon size, with light, dark and high-contrast values for every role. A build generates the Flutter theme, as a `ThemeExtension`, and the TUI's Lip Gloss palette from it. No widget or TUI view hard-codes a visual value. The foundation gate fails when a generated file drifts from the source or a text and background pair falls below WCAG 2.2 AA contrast ([foundation.md](../policy/foundation.md)). The LINA repository's `DESIGN.md` holds the rules that people and coding tools follow when they build screens, with do's and don'ts; its token values are generated from the token source and never edited by hand. The token source and the shared widgets live in the LINA repository as one Dart package. One shared widget is the citation chip: it names the cited source and opens the viewer at the cited passage through its passage anchor ([materials-and-knowledge.md](materials-and-knowledge.md)), so a citation looks and behaves the same everywhere.

**Icons.** One icon source serves every surface: one cross-platform licensed icon set, such as Lucide or Phosphor, and LINA's own icons drawn on the same grid. SF Symbols are licensed only for Apple platforms, so they are not part of it. The TUI maps each icon to a text glyph. A state icon always appears with its text label. An agent is marked by its face and its name (see How agents are presented).

**How agents are presented.** Each agent is the same on every surface: one name, one form, one voice. An agent made from a preset takes its form and its expressions from that preset, and every preset has a form of its own; the LINA preset's form is LINA's own simple abstract character. An agent made from a description takes the common default form, marked with a color and the initial of its name. Emotion changes an agent's expression, never its form, and shows only in expression: wording, length, emoji, and the agent's face and motion on screen ([conversation-and-memory.md](conversation-and-memory.md)). The agent's face appears in the conversation header, beside each message it writes, on the working indicator of work it asked for, on the agents screen and as the author mark in the LIFE feed. Its name goes with its face wherever two agents could be confused, such as in lists, on cards and in notifications. Approvals, evidence, results, failures and "unknown" carry no expression. When the person turns an agent's emotional expression off, its face stays neutral. At a goodbye, an agent's expression is neutral or warm. Only the agents of a conversation speak in it, and every message carries the face and name of the agent that wrote it ([product-families.md](product-families.md)). Engines and workers never appear as speakers; work progress appears on work cards.

## Platform adaptation

Flutter draws LINA's brand the same way on every platform. The behavior people rely on follows the platform: text input and input methods, menus and keyboard shortcuts, scrolling, focus, notifications, the back gesture, text size, dark mode, reduced motion, high contrast and, on Omarchy, the active theme. The person's settings override LINA's defaults; LINA's mark and each agent's color mark keep their colors.

Two acceptance checks of the first app milestone confirm Flutter as the app framework:

- Korean input: every platform passes the Korean input acceptance below with every input method listed there.
- The macOS launcher: the global shortcut, the non-activating panel and clipboard history that skips concealed items work in the Flutter app with platform code (see Launcher).

A platform that fails a check gets native input code or native launcher code for that part, behind the same design tokens.

| Platform | Follows the platform |
| --- | --- |
| macOS | Menu bar commands, the menu bar extra, windows and full screen, keyboard shortcuts, notifications, text input |
| iOS | Dynamic Type, sheets and the share sheet, notifications, text input |
| Android | Font scale, the back gesture, notifications, text input |
| Windows | Text scale, the title bar, keyboard shortcuts, notifications, text input |
| Linux (Omarchy) | The active Omarchy theme, read from `~/.local/state/omarchy/current/theme/colors.toml` through a `~/.config/omarchy/themed/*.tpl` template and a hook in `~/.config/omarchy/hooks/theme-set.d/`; Hyprland window rules through a fixed app id, and Hyprland key bindings; the Omarchy shell for the bar, launcher entries and notifications; fcitx5-hangul for text input |

- Flutter, and any native code a platform gets, satisfies every rule in this file: the thin client, the design language, the "no web feel" bar, Korean input and accessibility.
- On desktop the app offers its own text size and high-contrast settings where the platform gives the app no signal.
- On Omarchy the app draws no window shadow, rounded window corner or blur of its own; Hyprland draws the window. The TUI follows the active Omarchy theme as well.
- The app on every platform uses the Dart protocol types generated from the JSON Schema ([host-protocol.md](host-protocol.md)).
- LINA OS ships the LINA app ([product-families.md](product-families.md)). On Omarchy it is the Linux build, beside the TUI.

## No web feel

The LINA app on every platform meets one quality bar: no web feel.

- No surface renders its interface with web technology, in a WebView or in an embedded browser engine. The LINA app never runs in a web-technology shell, which packages web pages as an app, or in a WebView shell, and never ships as a Flutter web build.
- An app has no web feel when nothing in normal use reveals a web page. Text input and input methods, selection, scrolling, focus, menus, keyboard shortcuts, drag and drop, windows and notifications behave as in the platform's own apps. No screen shows a page load, a blank frame, a browser context menu or browser-style navigation.
- The app draws its kept display state, such as the reading position and unsent drafts, in its first frame. No control shows a hover state that the platform's own apps don't show.
- The bar is judged by using the installed app on the real platform.

## Screen skeleton

The person writes intent, LINA organizes, and the person zooms in and out. A person who wants to can edit documents and plans directly. Screens are built and corrected through use; no prototype gate precedes implementation.

### Layout

- The center is the conversation with the chosen agent, and its header shows that agent's face and name. Selecting the header switches to another agent's main conversation.
- Home is the starting state of that conversation: above the composer sit the cards "Waiting for me" and "Today". They gather the items of every agent, each with the face of the agent it belongs to. Selecting a card continues the conversation of that agent in the card's context. An item whose agent was deleted continues in the chosen agent's conversation.
- Home is not a list. The goal list and the map show everything.
- The left sidebar navigates: Home, Goals, Map, Library, Memo inbox and Agents. Goals with something waiting for the person come first.
- The right side is the work panel. It shows the result being handled now: a goal's front page, a page, a document viewer, a version comparison or an apply request.
- The work panel is closed by default, and the conversation takes the full width. It opens to at least half the screen when a document is open, and to full screen for long reading or editing, with the conversation collapsed to its composer. Closing it returns to the conversation.
- The panel opens from the conversation. What the person edits in the panel enters the conversation's context.
- On mobile the conversation is the default screen, and the left and right panels slide in.
- The conversation is beside every screen. From any layer, "Talk about this" brings that context into the conversation with the chosen agent.
- A surface opens to the conversation with the agent the person talked with last. The first run creates the default agent from the LINA preset and asks for no setup before it talks ([conversation-and-memory.md](conversation-and-memory.md)).

### Layers

| Layer | Name | Content |
| --- | --- | --- |
| 0 | Front page | A one-screen summary of one goal |
| 1 | Map | The tree of intent cards |
| 2 | Page | One card as a block page: a summary on top and the person's intent field |
| 3 | Work | Pull-request-sized tasks with acceptance criteria, run records and evidence |

Who writes each layer, and the rules that protect the person's intent text, are defined in [work-and-delegation.md](work-and-delegation.md). A person sees layer 0 by default and expands to go down. Layer 3 opens only when needed.

The front page holds five things and nothing else:

- Why: usually written first by the person; LINA may edit it.
- What exists when it is done: LINA drafts it and the person confirms it.
- Where it stands: computed from actual results in the work layer, and "unknown" when not known.
- Waiting for me: the most prominent item.
- Recent updates: what, why and by whom, the person or an agent.

Nobody maintains the front page by hand; LINA rewrites it when the work layer moves.

### Result surfaces

- Block documents in the style of Notion: goal pages, designs and memos, with to-dos as blocks inside them. A block document is the person's input surface (intent fields, memos) and LINA's output surface (front pages, summaries), never a giant document the person has to maintain.
- A to-do list in the style of Things.
- Summary documents.
- PDF reports and slide decks, made only when the person asks.
- Version comparisons for code results.

### Memo, to-do, goal

Memos, to-dos and goals, and the rule for promoting one to the next, are defined in [work-and-delegation.md](work-and-delegation.md). On screen:

- Memos accumulate quietly in the Memo inbox.
- A promotion proposal appears in the conversation, and the person confirms it once. Goal drafts come from Klotho ([cognition-and-life.md](cognition-and-life.md)).
- Archiving a goal that has not moved for a long time, and splitting a goal that has grown too large, are proposed in the conversation by the agent the person is talking with.
- A parent goal's front page gathers the summaries of its children's front pages.

### Threads on screen

Thread semantics are defined in [conversation-and-memory.md](conversation-and-memory.md). On screen:

- Every document and goal has its own thread with each agent that talks about it. To revise one, the person continues that thread instead of opening a separate editor. The agent carries the earlier discussion, decisions and edits as context.
- An edit made by chatting in a thread appears in the work panel at once and becomes a saved version. Only delegated work results arrive as a draft and an apply request ([work-and-delegation.md](work-and-delegation.md)).
- When an agent proposes moving talk to an artifact's thread, the proposal appears in the conversation and the person accepts or ignores it.
- Conversation threads and work threads look different, and a work thread is always reached through its work card.

## Plans and work

### Intent cards

Intent cards are a common feature of the LINA app on every platform. LINA Core owns their data ([work-and-delegation.md](work-and-delegation.md)). They are the map (layer 1) and the intent pages (layer 2) of the plan screen.

- The map is a tree of cards. Each card states something that must be true of the product or goal.
- Each card has an intent field that saves as the person types.
- "Take a look" (봐봐 in Korean) asks LINA to read the card's intent and carry it into the page summary and the map.
- Each card shows its history of saved versions.
- Klotho's periodic tidy of the cards is defined in [cognition-and-life.md](cognition-and-life.md).
- A card shows its execution state, defined in [work-and-delegation.md](work-and-delegation.md), and shows "unknown" without a fresh observation.
- Desktop and mobile use the same cards, intent input and history. A wide screen lays the map out as a mind map; a narrow screen lays it out as a list.

### Plans

Goals, projects, to-dos and milestones are LINA's own plan feature ([work-and-delegation.md](work-and-delegation.md)). The plan screens include milestone creation, ordering, completion conditions, optional dates, assignment and filters. A small manual to-do never gets a forced hierarchy or execution.

### Work cards

- Work appears as live cards per unit (goal, project, task), not as a list. Cards appear on home and in the Goals menu. Selecting one opens that object's thread and work panel.
- A task's card shows the face and name of the agent that asked for it. Its recent updates name the person or the agent behind each change, so work that another agent changed shows that agent.
- A card shows one of the work card states defined in [work-and-delegation.md](work-and-delegation.md), each with its text label and icon. Input needed that the person owns shows as "Waiting for me". While LINA works, the card shows a working indicator.
- Opening a card shows a one- or two-line summary per stage. Run records and acceptance criteria appear only when expanded.
- Stage summaries come from actual work results. When a result is not known, the summary says "unknown". A model's sentence that it finished never marks a card done.
- A dense list with sorting and filtering is an advanced view.
- Questions from work appear in the conversation that started the work, which belongs to the agent that asked for it; when that agent has been deleted, they appear on home cards. The person never relays messages ([work-and-delegation.md](work-and-delegation.md)).
- An approval request shows what, where and whether it can be undone in one view, with a plugin tool's arguments and the choices of its tier ([main-authority.md](main-authority.md)).
- A delegated result arrives as a draft with an apply request in the work panel. The person can compare and revert at any time.
- Observation rules for work cards, including the developer view and direct-control handover, are in [work-and-delegation.md](work-and-delegation.md).

### Development mode

There is no separate kind of development project. Connecting a GitHub repository to a goal or a part of it turns that part into development mode. The links between tasks and issues and between drafts and pull requests are defined in [integrations.md](integrations.md). On screen:

- a task and a draft show their linked issue and pull request
- applying a draft is the same apply action as everywhere else
- GitHub reviews appear in the thread of the draft they concern
- the work panel shows a code view and CI status

Unconnected parts stay document-centered, and both may mix within one goal. When code comes up in conversation, the agent may propose connecting a repository.

## Library

What the library holds, its ownership rules, viewers, versions and states are defined in [materials-and-knowledge.md](materials-and-knowledge.md). On screen:

- The library is never drawn as a folder or file tree. Files appear as pages and collections, grouped by conversation, work, source and topic. Media has its own library view.
- A library item opens in the work panel beside the conversation.
- Saved versions can be compared and reverted from the item at any time.
- In a GitHub-connected scope, the advanced view shows paths, a file tree, search and a code view.
- Connected sources appear as sources, each with its state. The library has no action that connects a source on LINA's initiative; connecting is the person's act ([filesystem.md](filesystem.md)).

A collection or a connected source can be attached to a conversation, from the library or from the composer's attach menu. A chip shows its name and source count, and a scope chip chooses what the answer draws on: the attachment only, the attachment and this agent's personal knowledge, or those and common knowledge as well ([materials-and-knowledge.md](materials-and-knowledge.md)). The answer marks document evidence and memory evidence separately, and a citation chip opens the viewer at the cited passage.

## Feeds

The today feed, the news feed and the LIFE feed are separate streams and never mix. The news feed and the LIFE feed are separate menus.

**Today feed.** The today feed is a LINA feed feature, not a separate product. LINA gathers to-dos from mail, meetings and conversations, removes duplicates, attaches the materials each one needs and posts them to the feed. When the person wants, LINA places them in free calendar time.

**News feed.** The news feed shows news matching the person's interests. Klotho collects and selects it in the background ([cognition-and-life.md](cognition-and-life.md)); the app only shows it.

**LIFE feed.** The LIFE feed is a personal feed in the style of a social photo feed. It shows the agents' activities, posts, reactions, media and conversation experiences, each with the face of the agent it belongs to. It is not connected to any real social network. The world engine keeps the world, its time, events and daily activity in the background ([cognition-and-life.md](cognition-and-life.md)); the feed shows the result. LIFE has no separate app, installer or login and no persona of its own; the agent in LIFE is the agent whose life it shows ([product-families.md](product-families.md)).

## Settings

Settings include agents, plugins, command network, notifications, models and processing, cognition, expression and the developer view.

**Agents.** The person's agents, each opening its agent settings (see Agents).

**Plugins.** The plugin list shows LINA's plugins ([integrations.md](integrations.md)), not a fixed set of items.

- Each plugin shows its name, its state (available, installed, on, needs sign-in, or failing with its cause), its account and where its credential comes from, for example the person's `gh` login or a token LINA keeps.
- The actions are add, turn on or off, sign in and remove. Adding takes a local folder, a Git URL, or a remote MCP URL or local server command to wrap as a plugin. A local server command is shown in full before it first runs.
- Opening a plugin lists its tools with their tiers and the person's policy (by tier, raised to outward or irreversible, or blocked), and its standing grants, each with a revoke action ([main-authority.md](main-authority.md)).
- When a plugin needs sign-in, the LINA app or the TUI the person is using opens the system browser and relays the result to LINA Core, or takes the pasted URL where it can't ([integrations.md](integrations.md)).

**Command network.** The standing grants that give sandboxed commands network access ([runtime.md](runtime.md)), each with its command prefix and a revoke action ([main-authority.md](main-authority.md)).

**Notifications.** LINA notifies the person when something needs them or ends while they are not looking: an approval or a worker's question waiting, and delegated work finished or failed. The mode is one of four. The person sets one mode for every agent and may set another for any one agent; a notification shows the face and name of the agent it comes from, when it comes from one.

| Mode | How it reaches the person |
| --- | --- |
| System | The platform's own notifications. The default where the platform has them |
| Terminal | A notification escape sequence the terminal shows, for the TUI in a terminal or over SSH |
| Bell | The terminal bell |
| Off | No notification; the item waits in the conversation and on its card |

A surface never notifies about what it is already showing. Opening a notification opens the item.

**Models and processing.** The chat model (through opencodex) and, as separate items, the judge (Jev), embeddings and transcription ([runtime.md](runtime.md)). Each item shows its connection state, its key and its usage. The compaction threshold sits here as well ([conversation-and-memory.md](conversation-and-memory.md)).

- An item that is off shows only its state and what LINA does without it, for example "keyword search in use instead of semantic search".
- No surface asks for any of these at first run. An agent may suggest a plugin once in a conversation when it would help ([integrations.md](integrations.md)).
- A setting the person edits is a request to LINA Core, like every other edit in the app.

**Cognition.** Each product cognition feature, with a switch that turns it off on its own ([cognition-and-life.md](cognition-and-life.md)).

**Expression.** For each agent, the switch that turns its emotional expression off ([conversation-and-memory.md](conversation-and-memory.md)).

**Developer view.** The switch that shows the developer view of work cards ([work-and-delegation.md](work-and-delegation.md)).

## Agents

What an agent is, the persona layers, presets, personal and common knowledge, and deletion are defined in [product-families.md](product-families.md) and [conversation-and-memory.md](conversation-and-memory.md). Creating, changing and deleting an agent is the person's act; every action below is a request to LINA Core and is recorded with the person id. On screen:

- The agents screen lists the person's agents, each with its face, its name, its source (the preset it was made from, or "from a description") and its last conversation. Choosing one opens its main conversation.
- Creating from a preset shows each preset with its form and lets the person name the new agent. Creating from a description takes the person's description and shapes the agent through the same questions as voice tuning ([conversation-and-memory.md](conversation-and-memory.md)). Either way, the new agent's main conversation opens.
- An agent's settings hold its name, its expression switch and its notification mode. Its persona changes by asking the agent in conversation, and each change is a new persona revision ([conversation-and-memory.md](conversation-and-memory.md)). An agent made from a preset shows which layers come from its preset.
- An agent's settings show what it knows: its personal knowledge and the common knowledge every agent shares, each item marked as one or the other. The person moves an item between the two, and the move is recorded.
- Deleting an agent shows what goes to the trash for 30 days (its persona revisions, emotional state, conversations and personal knowledge) and offers to move any of its personal items to common knowledge first. Tasks the agent asked for keep running, and their questions and results move to home cards.

## Launcher

- On macOS, LINA's launcher does what the person uses Raycast for, without copying Raycast's structure. Which Raycast features it covers is filled in a feature map during implementation.
- The launcher opens from a global shortcut. Whatever the person says from the launcher goes into the main conversation of the agent the person talked with last, or of another agent the person names in the launcher, and the answer continues there.
- The launcher includes clipboard history. Items marked concealed, as password managers mark them, are never stored. The history stays on that Mac. It never goes to LINA Core and never becomes memory or material.
- A clipboard item reaches LINA only when the person explicitly puts it into a message, like typed text.
- The person turns the launcher and clipboard history on. Because the person turns them on, they are the only exception to the clipboard rule in [non-competition.md](non-competition.md); nothing else LINA does reads or writes the person's clipboard.
- The launcher is part of the LINA app on macOS. Flutter draws it with the app's design tokens; platform code owns its global shortcut, its non-activating panel that floats above the person's apps, and its clipboard access. The launcher is one of the two acceptance checks that confirm Flutter (see Platform adaptation); if it fails, the launcher's shortcut, panel and clipboard use native code behind the same design tokens.
- On Omarchy, launching LINA belongs to the Omarchy shell's launcher.

## Korean input

Hangul composition input is an acceptance criterion of the LINA app on every supported platform: Omarchy with fcitx5-hangul, macOS, Windows, iOS and Android. An app that fails it is not accepted on that platform. It is one of the two acceptance checks that confirm Flutter (see Platform adaptation).

It applies to every text input: the composer, intent fields, documents, memos, search and the launcher. Each platform is judged on a real device with these input methods:

| Platform | Input methods |
| --- | --- |
| Windows | Microsoft IME in 2-beolsik and 3-beolsik, including fast typing |
| Linux (Omarchy) | fcitx5-hangul |
| macOS and iOS | The system Korean input |
| Android | Gboard and the Samsung keyboard |

A platform passes when all of the following hold with each of its input methods:

- The syllable being composed shows inline at the caret, and the candidate window opens at the caret.
- No syllable is lost, however fast the person types.
- Sending while a syllable is in composition sends the whole text exactly once. The composing syllable is neither dropped nor duplicated, and Enter to send never breaks the composition.
- Keys the input method consumes never trigger a shortcut or a send.
- Backspace, arrow keys and focus moves during composition behave as in the platform's own text fields.

The TUI accepts the committed text the terminal's input method delivers, renders Korean text at its correct cell width and keeps the cursor on the correct cell.

## Accessibility

- No state is conveyed by color alone, and no agent is told apart by color alone: an agent's color goes with its initial and name. Paused, waiting for me, done and every other state carry a text label and a distinct icon or shape; color only reinforces them. This applies to the TUI as well.
- The working indicator has a text equivalent.
- "Unknown" is a visible state with its own label.
- Text meets WCAG 2.2 AA contrast in the light, dark and high-contrast values. Korean text counts as large text only from 18 point, or 14 point bold.
- Text size, reduced motion and high contrast follow the person's settings (see Platform adaptation). With reduced motion on, motion becomes a fade or stops.

## Deferred

- LINA's accent color, the form and expressions of each preset, the common default form and how its color and initial mark is drawn, the signature interactions and the icon set: set by design exploration and recorded in `DESIGN.md` and the token source.
- How the LINA app joins the Omarchy shell's bar, launcher and notifications: set before the first Omarchy GUI work.
- An Omarchy edition of the LINA app's visual design that matches Omarchy's own identity: a follow-up design task. Until it exists, the app keeps LINA's visual language on Omarchy and follows the active theme as Platform adaptation defines.
- The foundation component, dependency and generation rules for the Flutter app (pub lockfile, exact versions, generated Dart types and theme): added to [foundation.md](../policy/foundation.md) and [dependencies.md](../policy/dependencies.md) by the first app implementation.
- Exact sizes and motion of the screen skeleton: tuned during implementation and recorded in the token source.
- Menu placement of the today feed, and the screen design of the today feed, meeting notes and the plugin list: set during their implementation.
- The launcher's feature map: filled in during launcher implementation.
- The layout of the agents screen, the agent picker in the conversation header and the creation flow: set during their implementation.
- How often LINA proposes promotions, archiving and splitting: set by measurement during development and recorded in the implementation issue.
- The views of the plan map (structure, execution and gap): the UI choice is set during implementation of the plan screen.
