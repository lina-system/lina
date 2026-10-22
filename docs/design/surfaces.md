# Surfaces

This contract fixes how a person meets LINA and its agents on screen: the TUI, the LINA app on each supported platform, and the launcher. It defines what a surface may do, the one design language every surface shares, the one app codebase and how it adapts to each platform, the quality bar it must meet, and the screens for conversation, agents, plans, work, the library and feeds. It is normative: surfaces must follow it, and any change to it is made in this file and joins dev under [integration.md](../policy/integration.md). Finishing this document does not mean any surface is implemented or accepted; runtime proof belongs to the implementation issues that consume it.

## Scope

This contract covers:

- the TUI as the first surface
- the LINA app as a thin client, and the only state it may hold
- the design language: terms, flows, the visual language, design tokens, icons and how agents are presented
- the app front end, its shell and UI components, platform adaptation and the "no web feel" quality bar
- the screen skeleton: home, conversation, work panel, layers and threads on screen
- the default view: the design at a glance and the list of what the person must do or decide, what stays out of the default view, and how it is reached on request
- progress at a glance: one line of progress for each running piece of work, with what needs the person on top, in the app, in the TUI and as a digest in an external channel
- results: their cards, how each type is drawn in the work panel, the TUI and external channels, the summary-first flow and saving
- agents on screen: choosing, creating, tuning and deleting agents, the channels they are dispatched to and the log of their talk with each other
- the work and knowledge records, intent cards and work cards; the library; the today feed, the news feed and the LIFE feed; settings, including the connected accounts, the plugin catalog, the plugin list, the channel connections and reporting a problem
- the launcher and its clipboard history
- Korean input and accessibility acceptance

It builds on these documents and does not repeat them:

- product boundaries, supported operating systems, canon writers, agents and the speaker rule: [product-families.md](product-families.md)
- the envelope, generated protocol types, version negotiation, client connections and typed results: [host-protocol.md](host-protocol.md)
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
| LINA app | The desktop and mobile app (component LINA APP) | Every supported OS, as one React app in a shell that is bundled with the app (see App front end) |
| Launcher | A global launcher with clipboard history, part of the LINA app | macOS |

LINA APP is the component name in code, artifacts and documents. People see it as the LINA app, named LINA, and no other app name exists ([product-families.md](product-families.md)).

The LINA app targets every operating system that [product-families.md](product-families.md) supports: Linux (Omarchy), macOS and Windows on desktop, and iOS and Android on mobile. A platform's app enters release builds, and with them the supported-combination table of a release ([host-protocol.md](host-protocol.md)), only after its build passes the acceptance in this file. Until then it is built only as a development build.

Every surface talks to the one main LINA Core, locally or across the tailnet ([runtime.md](runtime.md)). No surface reaches the model path or a connected source on LINA's behalf; LINA reaches them only through LINA Core.

## TUI

The TUI is the first surface. It is complete on its own: conversation with every agent, creating and tuning agents, memory and its corrections, delegated work with its questions, results, cancellation and approvals, and plans with several to-dos across devices all work from the TUI with no GUI installed.

- The TUI is built in Go on [Bubble Tea](https://github.com/charmbracelet/bubbletea), with Bubbles and Lip Gloss as needed, and ships in the same static binary as LINA Core ([runtime.md](runtime.md)). The TUI stays Go on Bubble Tea: it uses no React, no web technology and no WebView.
- Bubble Tea, and Bubbles and Lip Gloss when used, are pinned to exact versions recorded in the release manifest. Code taken into the repository records its source commit, files, modifications and notice in `THIRD-PARTY-NOTICES.md` ([dependencies.md](../policy/dependencies.md)).
- The TUI is the terminal screen of the `lina` command. It runs as its own process, separate from LINA Core, and uses only LINA Core's public event and control API over the client connection in [host-protocol.md](host-protocol.md). The socket location is set with the install paths ([runtime.md](runtime.md)).
- The TUI uses every capability of the client connection: conversation, task, approval, accounts, sign-in relay and multi-task control ([host-protocol.md](host-protocol.md)).
- The accounts, the plugin catalog, the connect card, the plugin list and the power settings, with their actions (see Settings), work from the TUI as commands. The connect card is a labeled block. A question from a plugin appears as prompts.
- Memory import and its promotion batches (see Settings) work from the TUI as commands.
- Agents work from the TUI as commands (see Agents): list the person's agents, switch to an agent's main conversation, create an agent from a preset or from a description, tune an agent, delete one. Each command is a request to LINA Core over the client connection ([host-protocol.md](host-protocol.md)).
- Channel connections and the agent talk log work from the TUI as commands (see Settings and Agents): list connections and bindings, connect and pair, bind an agent to a chat, change a scope, disconnect, and read and expand the log of agent exchanges. Each command is a request to LINA Core over the client connection ([host-protocol.md](host-protocol.md)).
- The running list works from the TUI (see Running list): its count sits in the status line, a key opens the list, and a row opens its card and result in a pager or side pane. Stop, stop all and a follow-up are commands.
- Message types are generated from the protocol JSON Schema. The TUI is tested alone against a fake LINA Core built from the conformance fixtures ([host-protocol.md](host-protocol.md)).
- Protocol errors and LINA Core logs go to the status line and the log, never into the conversation ([host-protocol.md](host-protocol.md)).
- The TUI shows the conversation experience that [conversation-and-memory.md](conversation-and-memory.md) defines: streaming answers, the earlier conversation, connection failures and readiness failures with their cause, and the same conversation after a restart.
- The TUI uses the same terms, flows, state labels and agent marks as the LINA app. A card in the TUI is a labeled block, and an agent appears in the TUI by its name.
- The TUI follows the north star of [product-families.md](product-families.md#north-star) with concise replies and structured detail on request. A card line carries a key that opens its detail as a side pane on a wide terminal, or as a full-screen view with a key to return on a narrow one. Structured results, background reports and logs stay behind their cards. Complete assistant prose stays inline and scrollable, with an optional pager affordance after a long reply ([Reply shape](conversation-and-memory.md#reply-shape)).
- Every result type has a complete text renderer in the TUI. A result card is a labeled block with a key that opens the result, and structured result bodies open on request (see Result surfaces). Generated reply-shape previews carry assistant prose, which the TUI displays inline once.
- The TUI starts at the Home of the conversation: the cards "Waiting for me", "At a glance" and "Today" above the composer, with the same rows and the same order as the app (see Waiting for me and Progress at a glance). A key opens the whole progress view, and a key on one of its lines opens the Details of that line in a pager or a side pane. Its status line shows the running count of background work, for example "2 running, 1 waiting for you", and Core logs and protocol errors.
- Tool narration, memory-maintenance notices and the wrapper lines of scheduled jobs are never lines of the conversation. They are in the activity log, which opens on request as a pager (see Default view).

## The LINA app is a thin client

The LINA app shows and asks. It never writes canon. All logic lives in LINA Core.

- The app shows what LINA Core reports and asks the person what LINA Core needs. It sends the person's input, answers and decisions to LINA Core.
- Every edit made in the app (an intent field, a front page, a document, a plan, a memo, an agent, a setting) is a request to LINA Core. LINA Core applies it and records the saved version; the app then shows the saved result.
- The app holds client display state only: what is on screen, layout, reading position and unsent drafts. Display state is never authoritative ([product-families.md](product-families.md)).
- The app makes no judgment of its own: no ranking, recall, completion, plan decision or permission check. It shows LINA Core's result, including "unknown".
- The app's protocol types are generated in TypeScript from the protocol JSON Schema. On desktop the shell, not the interface, holds the connection to LINA Core ([host-protocol.md](host-protocol.md#client-connection)). An unsupported app and LINA Core combination is refused and the refusal is shown to the person ([host-protocol.md](host-protocol.md)).
- When the connection to LINA Core drops, the app shows that state, keeps unsent drafts and the reading position, and shows nothing as saved until LINA Core confirms it.
- Work in progress, a background run or an open question never blocks the main conversation or other work. A landing never interrupts typing or an answer being written. The app never loses a conversation draft or a reading position while work runs.

The app provides conversation with each agent and its threads, the agents screen, work threads, questions, results, plans and intent cards, the library, the today feed, the news feed and the LIFE feed, and settings. On macOS it also provides the launcher.

## App front end

The LINA app is one React application, written once in TypeScript and drawn with React DOM. In this file the interface is the React code that draws the screens, and the shell is the host around it. The shell is bundled with the app. It draws none of the interface and owns the window, the connection to LINA Core and the platform features the interface cannot reach. The terminal TUI is not part of the app: it stays Go on Bubble Tea (see TUI).

macOS usability is the top priority of the app, and LINA OS and Linux come second. The first gate of the app milestone, described under Platform adaptation, runs on a real Mac.

React Native, including its macOS fork, is not chosen. The macOS fork trails the core project by several releases, the current Expo release has no macOS target, multiline text input does not scroll on macOS under the new architecture (an open upstream fault) and the old architecture is no longer available, React Native has no Linux target, and the work panel needs the web ecosystem for its markdown, tables, diagrams, charts and diffs. Tauri and a native Swift shell are not candidates either. The app keeps one React DOM interface on every platform.

The desktop shell is Wails v3, on macOS, Windows and Linux. It is Go, like LINA Core and the TUI. On macOS it hosts a WKWebView, so text input goes through Apple's own text system. It has the smallest footprint of the options, it generates typed TypeScript bindings from Go services, and on Linux it defaults to GTK4 with WebKitGTK 6.0. The risks are stated so that they are not hidden: Wails v3 is in beta, its desktop API is declared stable but its general-availability milestone is overdue, and its mobile support is experimental, so the mobile host is not part of this choice.

| Platform | Shell |
| --- | --- |
| macOS | Wails v3 on the system WKWebView |
| Windows | Wails v3 on the system WebView2 |
| Linux (Omarchy) | Wails v3 on GTK4 and WebKitGTK 6.0 |
| iOS, Android | A thin WebView host: Capacitor by default, confirmed or changed by the checks in Platform adaptation. It is not part of the desktop shell decision. |

Rules of the shell:

- The shell is a thin client. LINA Core stays a separate Go process and the only writer, and the shell never holds canon.
- The interface reaches the window, the connection to LINA Core, notifications, the global shortcut and the launcher panel, the clipboard, file drops, the system browser and the platform's secure storage only through one bridge interface that does not depend on Wails, and each shell implements that interface and nothing more. Electron is the drop-in fallback behind the same bridge. A different shell for an operating system is allowed when that platform fails its checks with Wails (see Platform adaptation). A change of shell changes no interface code, no protocol type and no design token.
- The composer handles WebKit Korean input as the Korean input section requires: it follows the input and replacement-text events, treats Enter after a composition (key code 229) as part of the composition, and never re-renders its DOM while a composition is active.
- The release manifest pins the Wails version ([runtime.md](runtime.md#release-manifest)).

What the Electron fallback costs, stated so that it is not hidden. These numbers were measured when the shell was first considered. They are the cost of the fallback and not budgets: about 118 MiB compressed download and 284 MB unpacked on Linux, about 302 MB of proportional memory for an empty window (measured under software rendering, not in a real Wayland GPU session), and a major upgrade at least about every six months to stay in Electron's support window, plus its minor security releases.

The packaged app records its idle memory on Linux and macOS in the release manifest, and the foundation gate fails when it regresses beyond the tolerance declared with the first measurement ([runtime.md](runtime.md#packaging-and-resident-budget)).

The app has one window. The work panel is a route of that window, not a second window or a frame, so what the person edits in the panel enters the conversation's context without crossing a bridge (see Layout).

**UI components.** The interface's components come from two libraries and LINA's own code, on one token set. The agent views (see Agent surfaces) use affectUI Pro. Every other component uses Untitled UI React (React Aria with Tailwind), copied into the app as source. The paid components and the shared token theme live in one private UI repository of the owner that the app repository consumes, and the LINA-specific screens stay in the app repository. The affectUI variables and the Untitled theme tokens map to the one LINA theme, with light and dark values (see Tokens under One design language). The Korean input rules of this file apply to the inputs of both libraries. A release build is minified and ships no source maps, the app package holds no component source file, and no paid component source appears in a public issue, a log or a crash report. The access key to a paid library is a secret, held as a CI secret or in a vault and never in a repository, a log or a commit.

## One design language

The LINA app is one React codebase in TypeScript, hosted by a shell on each platform (see App front end). The TUI is its own Go code on Bubble Tea. One design language unifies every surface, and every surface uses protocol types generated from the same schema. The design language has six parts, each defined once for all surfaces.

**Terms.** Every surface takes its user-facing words from one shared term list.

- The app is called LINA. The personas the person creates and talks to are agents (에이전트 in Korean). Lina is the name of the default agent, made from the `lina` preset ([product-families.md](product-families.md)).
- Versions are "saved version", "draft" and "apply". No surface shows git terms such as commit, branch or merge by default. The pull request of a draft in a GitHub-connected scope is reached from the draft's Details (see Default view).
- Internal vocabulary stays out of the default view. By default no surface, reply or notification shows the words issue, initiative, milestone, pull request, commit, branch, merge, CI, run, session, lane, hook, compaction, context or recall, or any id, hash, path or command. A surface says what happened in the person's words ("the draft is ready to apply") and offers Details for the rest. A word the person needs and does not know is defined in three words or avoided.
- A result that an agent presents and the person has not saved is a preview. "Draft" stays the word for a delegated result that the person applies. A result card is the card in the conversation that stands for one result; it is not an intent card, a work card or a home card.
- A state that is not observed is "unknown". "Unknown" is never shown as none, zero, empty or done.
- Work and knowledge are named 목표 (goal), 일 (project), 할 일 (to-do), 계속 챙기는 것 (area), 자료 (resource), 보관 (archive) and 메모 (memo) ([work-and-delegation.md](work-and-delegation.md#plans)). The words are the default; a card sort with non-developers confirms them. No surface shows an initiative, an issue, a cycle or a milestone as a LINA concept. The design names roles and permissions, not words, so a rename touches only the term list. Naming is on the backlog in [cognition-and-life.md](cognition-and-life.md).
- A background run appears by what it does for the person, such as "looking into X". The words sub-agent, run, budget and cap appear only in the developer view.
- UI copy and microcopy may get a polish suggestion from the polish model when they change ([work-and-delegation.md](work-and-delegation.md#operating-the-pull-request)). The suggestion is attached to the change, runs in parallel and never gates it; code, logs and chat replies are not polished.

**Flows.** A task has one flow on every surface. Desktop and mobile carry the same features, flows and terms; only layout differs by screen size. The TUI follows the same flows in terminal form.

**Visual language.** LINA's brand is a visual language in the style of Apple's own apps and Things 3. It looks the same on every platform: the LINA app draws it itself and never imitates a platform's chrome, while the behavior people rely on follows the platform (see Platform adaptation).

- Surfaces are calm, light and airy, with generous whitespace. The conversation, cards and documents are the subject.
- Titles are large and firm.
- Cobalt is the one accent: `#2F4BDB` on light, `#5685FF` on dark, and `#6795FF` in high contrast. It marks the primary action key, the active state glyph, the selection, LINA's mark and "Waiting for me"; it never fills a large surface. The mark is a lowercase `lina` wordmark with a small baseline accent dot (`lina.` in Unicode and ASCII), also generated as SVG. The wordmark is bold and bright; state words use regular text, while action labels and inactive state marks are secondary. The terminal connection screen uses one blank row between wordmark, state and optional bracketed-key footer, without a divider or redundant connected description. `q`, Ctrl+C and Ctrl+D quit without Enter and restore the terminal mode. A connection failure appears once in the terminal panel; redirected stderr and plain output keep their command diagnostics. Cool paper, ink and charcoal make up the rest of the palette. [DESIGN.md](../../DESIGN.md) records the brand rules and generated values.
- Corners are soft, with continuous curvature (squircles).
- Hairline separators and steps of lightness divide layers, never heavy shadows.
- Pretendard sets Korean and Latin text, bundled with the app as a web font so that text looks the same on every platform; code uses a monospaced face. Korean text breaks between words, and body text has a line height of at least 1.5.
- Motion is restrained: gentle springs with no exaggerated bounce, and nothing moves unless a state changes. One or two small signature interactions carry LINA's delight; everything else is familiar.
- Every feed has an end.

**Tokens.** One design token source, in the Design Tokens Community Group (DTCG) format, holds color, type, spacing, radius, motion and icon size, with light, dark and high-contrast values for every role. A build generates the app's CSS custom properties, with light, dark and high-contrast sets, and the TUI's Lip Gloss palette from it. No component or TUI view hard-codes a visual value. The foundation gate fails when a generated file drifts from the source or a text and background pair falls below WCAG 2.2 AA contrast ([foundation.md](../policy/foundation.md)). The LINA repository's `DESIGN.md` holds the rules that people and coding tools follow when they build screens, with do's and don'ts; its token values are generated from the token source and never edited by hand. The DTCG source lives in `design/tokens/`. `scripts/tokens.mjs` generates `internal/design`, CSS custom properties, the SVG mark and the tables in `DESIGN.md`; its check mode compares these without writing. The app's future shared TypeScript components consume these generated CSS properties. One shared component is the citation chip: it names the cited source and opens the viewer at the cited passage through its passage anchor ([materials-and-knowledge.md](materials-and-knowledge.md)), so a citation looks and behaves the same everywhere.

**Icons.** One icon source serves every surface: one cross-platform licensed icon set, such as Lucide or Phosphor, delivered to the app as React components, and LINA's own icons drawn on the same grid. SF Symbols are licensed only for Apple platforms, so they are not part of it. The TUI maps each icon to a text glyph. A state icon always appears with its text label. An agent is marked by its face and its name (see How agents are presented).

**How agents are presented.** The eight starters are Selene, Lina, Sion, Rumi, Arin, Cleo, Kai and Kanna, keeping their existing personas, with the roles and preset keys in [Presets](conversation-and-memory.md#presets). Lina opens the first conversation; the others are available from the agents list without a setup or selection step. Sion (dev) is shown as the owner of coding work, with coding tools shown as its workers. Each agent is the same on every surface: one name, one form, one voice. An agent made from a preset takes its form and its expressions from that preset, and every preset has a form of its own; the LINA preset's form is LINA's own simple abstract character. An agent made from a description takes the common default form, marked with a color and the initial of its name. Emotion changes an agent's expression, never its form, and shows only in expression: wording, length, emoji, and the agent's face and motion on screen ([conversation-and-memory.md](conversation-and-memory.md)). The agent's face appears in the conversation header, beside each message it writes, on the working indicator of work it asked for, on the agents screen and as the author mark in the LIFE feed. Its name goes with its face wherever two agents could be confused, such as in lists, on cards and in notifications. Approvals, evidence, results, failures and "unknown" carry no expression. When the person turns an agent's emotional expression off, its face stays neutral. At a goodbye, an agent's expression is neutral or warm. In an external channel the platform shows the agent's bot name and avatar, which stand for the agent's name and face; a bot shared by several agents prints the agent's name in the first line of each message ([integrations.md](integrations.md#channels)). Only the agents of a conversation speak in it, and every message carries the face and name of the agent that wrote it ([product-families.md](product-families.md)). Engines and workers never appear as speakers; work progress appears on work cards. Personas, workers and sub-agents never look alike on any surface. A persona has a round face (its preset form, or its initial on its color). A worker has a square tile with the tool's logo, and a worker without a known logo gets a square tile with a generic tool glyph, never an initial, so it cannot look like a persona made from a description. A sub-agent has no avatar: it appears as a small line under its persona, such as "리나가 조사 중". Every 일, 할 일 and run card shows its owner persona first and its workers after it, round face before square logos, and the board and the running list keep that order. A question, approval or result that comes from a worker is delivered by the owner persona, with its face and name, and carries the worker's logo as a small source badge; steering a worker's run is addressed to the persona. In labels a persona is called by its own name or 에이전트, and a worker by the tool's name (Codex, Claude Code, Antigravity and Cursor) under the category 코딩 도구 (open owner decision: the word, like other fine wording, is settled in app trials). A worker is never called 에이전트 in the default view, and the component names of the UI libraries (such as Agent Inbox or Delegate Board) never become LINA labels.

## Platform adaptation

The app's interface draws LINA's brand the same way on every platform. The behavior people rely on follows the platform: text input and input methods, menus and keyboard shortcuts, scrolling, focus, notifications, the back gesture, text size, dark mode, reduced motion, high contrast and, on Omarchy, the active theme. The person's settings override LINA's defaults; LINA's mark and each agent's color mark keep their colors.

The shell is accepted on each platform by the checks below, run on real devices and ended by a pass or fail table. No spike chooses the shell before the app is built. The first run is on a real Mac, and it is the first gate of the app milestone. The checks are:

- macOS Korean input and feel, on a real Mac, with the Wails shell: 2-Set Korean composition at the caret; Enter sends once and never twice; fast typing keeps every syllable; the input method's candidate window opens at the caret; scrolling feels like a Mac app's; and the launcher panel works (see the launcher check below).
- Korean input on the other platforms: every platform passes the Korean input acceptance below with every input method listed there, on the engine its shell uses. That means WebView2 on Windows, WebKitGTK 6.0 on Omarchy (Hyprland, fcitx5-hangul, native Wayland), the system WebView on iOS, and the Android System WebView with Gboard and the Samsung keyboard, including how the soft keyboard changes the layout on Android 15 and later.
- The mobile transport: the connection from a mobile host to LINA Core inside the tailnet ([host-protocol.md](host-protocol.md#client-connection)).
- The launcher: the global shortcut, the non-activating panel and clipboard history that skips concealed items work in the app with platform code (see Launcher).
- The Omarchy window: a fixed app id for Hyprland window rules, no decoration drawn by the app, and sharp text under fractional scaling. The test records the Hyprland, fcitx5 and shell versions.
- Idle memory and install size of the packaged app skeleton.

With Wails the web engine is the operating system's and LINA pins none; each check records the engine version it ran on. A platform that fails a check gets a fallback for that part, in this order: a fix in the composer or in the shell's own code; Electron as the shell of that operating system, behind the same bridge; and, for a part that still fails, a native composer view inside the host and native launcher code. Native parts sit behind the same design tokens.

| Platform | Follows the platform |
| --- | --- |
| macOS | Menu bar commands, the menu bar extra, windows and full screen, keyboard shortcuts, notifications, text input |
| iOS | Dynamic Type, sheets and the share sheet, notifications, text input |
| Android | Font scale, the back gesture, notifications, text input |
| Windows | Text scale, the title bar, keyboard shortcuts, notifications, text input |
| Linux (Omarchy) | The active Omarchy theme, read from `~/.local/state/omarchy/current/theme/colors.toml` through a `~/.config/omarchy/themed/*.tpl` template and a hook in `~/.config/omarchy/hooks/theme-set.d/`; Hyprland window rules through a fixed app id, and Hyprland key bindings; the Omarchy shell for the bar, launcher entries and notifications; fcitx5-hangul for text input |

- The React app, and any native code a platform gets, satisfies every rule in this file: the thin client, the design language, the "no web feel" bar, Korean input and accessibility. The app is accepted on macOS first; LINA OS and Linux follow.
- On desktop the app offers its own text size and high-contrast settings where the platform gives the app no signal.
- On Omarchy the app draws no window shadow, rounded window corner or blur of its own; Hyprland draws the window. The TUI follows the active Omarchy theme as well.
- The app on every platform uses the TypeScript protocol types generated from the JSON Schema ([host-protocol.md](host-protocol.md)).
- LINA OS ships the LINA app ([product-families.md](product-families.md)). On Omarchy it is the Linux build, beside the TUI.

## No web feel

The LINA app on every platform meets one quality bar: no web feel. The bar judges what the person experiences. It does not forbid the technology the interface is drawn with.

- The LINA app is a React application in a shell that is bundled with the app (see App front end). Web technology is allowed as the rendering technology. The web is not a runtime of the app: the app loads only its own bundled files from a custom scheme, loads no remote page for its interface, and shows no address bar, tab, page-load state or browser navigation. The person signs in to a service only in the system browser ([integrations.md](integrations.md#consent-through-the-client)), never in a page embedded in the app. The app is not offered as a hosted website. The TUI uses no web technology.
- Whichever shell hosts it, the interface runs with the fewest rights the shell can give it, and the shell follows its vendor's security guidance: no Node.js and no direct file or process access in the interface, the interface isolated from the shell's own code, a content security policy, navigation and new windows limited to the bundled origin, permission requests denied unless a handler for a feature the person started allows them (recording, for example), and every message from the interface to the shell validated by the shell.
- Content that the app renders from a model, a connected source or a plugin is data. The app's own components render it, from LINA's typed results where they exist. Content that must run as HTML runs in a sandboxed frame with no network by default and no access to the shell's bridge, and it never reaches LINA Core directly.
- An app has no web feel when nothing in normal use reveals a web page. Text input and input methods, selection, scrolling, focus, menus, keyboard shortcuts, drag and drop, windows and notifications behave as in the platform's own apps. No screen shows a page load, a blank frame, a browser context menu or browser-style navigation. These web tells do not show: text selection on interface chrome (conversation text, documents and fields stay selectable), pull-to-refresh and rubber-band overscroll on mobile, pinch and Ctrl-wheel page zoom (the app's own text size setting replaces them), a file drop that navigates the window (a drop imports the file, see [filesystem.md](filesystem.md)), link previews and tap highlights, and default focus rings that differ from the platform's.
- The app draws its kept display state, such as the reading position and unsent drafts, in its first frame. The window is shown after its first paint, with the background of the design token, so no white frame appears. No control shows a hover state that the platform's own apps don't show.
- macOS comes first. On a real Mac the app has the system menu bar and its shortcuts, text input through the system Korean input with its candidate window at the caret, the platform's scrolling, and the launcher panel (see Launcher). A fault on macOS blocks the release of the app before a fault on another platform does.
- The bar is judged by using the installed app on the real platform, on a real Mac first.

## Screen skeleton

The person writes intent, LINA organizes, and the person zooms in and out. A person who wants to can edit documents and plans directly. Screens are built and corrected through use; no prototype gate precedes implementation.

### Layout

- The center is the conversation with the chosen agent, and its header shows that agent's face and name. Selecting the header switches to another agent's main conversation.
- Home is the starting state of that conversation: above the composer sit the cards "Waiting for me", "At a glance" and "Today". "Waiting for me" is the list of what the person must do or decide (see Waiting for me), and "At a glance" is the progress of everything that runs, one line for each 목표 and 일 (see Progress at a glance), whose 목표 lines open their design at a glance (see Design at a glance). The cards gather the items of every agent, each with the face of the agent it belongs to. Selecting an item continues the conversation of that agent in the item's context. An item whose agent was deleted continues in the chosen agent's conversation. An agent exchange that needs a decision only the person can make appears there as an item with the face of the agent that raised it; no other exchange appears on Home. When anything runs in the background, the running list (see Running list) shows as a compact strip below those cards.
- Home is not the list of everything: "Waiting for me" shows at most five rows (see Waiting for me). The lists of 목표, 일 and 할 일 and the map show everything.
- The left sidebar navigates: Home, Progress, 할 일, 일 and 목표, Map, Library, Memo inbox, Running and Agents. Progress opens the whole progress view (see Progress at a glance). 할 일 opens the views below. Running appears when something runs or has landed unseen. An item with something waiting for the person comes first.
- The right side is the work panel. It shows the result being handled now: a result (see Result surfaces), a front page, the design at a glance, a page, a document viewer, a version comparison, an apply request, or the Details of a card.
- The work panel is closed by default, and the conversation takes the full width. It opens to at least half the screen when a document is open, and to full screen for long reading or editing, with the conversation collapsed to its composer. Closing it returns to the conversation.
- The panel opens from the conversation. What the person edits in the panel enters the conversation's context as one line that names the result and its revision or saved version, not its content ([conversation-and-memory.md](conversation-and-memory.md#results)).
- On mobile the conversation is the default screen, and the left and right panels slide in. A result opens in the right panel as a full-height sheet, and the composer stays.
- The conversation is beside every screen. From any layer, "Talk about this" brings that context into the conversation with the chosen agent.
- A surface opens to the conversation with the agent the person talked with last. The first run creates all eight starter agents and opens the conversation with Lina, asking for nothing before it talks except the ChatGPT sign-in ([conversation-and-memory.md](conversation-and-memory.md#first-run)).

### Default view

The person sees two things by default: the design at a glance, with how far every running piece of work has come (see Progress at a glance), and the list of what they must do or decide. Everything else is available on request.

- The conversation, Home and every card show outcomes and decisions in the person's words: what is done, what comes next, what waits on the person.
- These stay out of the default view: what LINA recalled and why (memory notes, the person brief, recall results), issues and tasks, detailed plans and their run records, pull requests, diffs, checks and logs, tool steps, and every id. They exist inside LINA and are its audit trail. This holds for every person, power users included.
- Details ("자세히" in Korean) is the one way in. Every card, answer and work card offers Details. It opens the audit trail of that item in the work panel: the memory behind an answer, the plan and tasks behind a card, the pull request, diff and checks behind a draft, the run records and the evidence. Opening Details is the person's explicit request. Nothing in it is pushed into the conversation or into a notification.
- The activity log is the audit trail that belongs to no single card: tool steps, memory maintenance, runs of scheduled jobs and Core events. It is a view over records that Core already keeps ([self-diagnosis-log.md](self-diagnosis-log.md), [work-and-delegation.md](work-and-delegation.md)), not a store of its own. It opens on request from the status line, from settings and from the Details of an item. Internal events never appear as lines of a conversation.
- The developer view (see Settings) adds developer fields to Details, such as the worker and its version, the thread, the attempts and the receipts. It moves nothing into the default view.
- A message about a failure or a missing component is one plain line that says what is wrong and what the person can do. Step names, sizes and ids are in Details.

### Waiting for me

"Waiting for me" is the list of what only the person can do or decide. It holds only the ask-only cases of the default posture ([work-and-delegation.md](work-and-delegation.md#default-posture)): an effect that reaches outside the person or cannot be undone, a choice that changes the outcome a lot, information the agent does not have, and what only the person can do. Work that an agent can start is not a row: the agent proceeds and the work shows as in progress. LINA Core builds and ranks the list; the app and the TUI only show it ([host-protocol.md](host-protocol.md)). It is the first card on Home and the first thing the TUI shows, on desktop and on mobile alike.

A row is one thing, of one of four kinds:

1. A decision with the agent's recommendation: approve, change or skip. It includes every approval that the policy reserves for the person ([main-authority.md](main-authority.md)): spending money, deleting for good, publishing or sending outside, a result that changes a lot, and anything that cannot be undone.
2. Something the person promised or must do personally. It is a to-do (할 일 in Korean) that has a deadline, that the person committed to someone, or that is overdue ([work-and-delegation.md](work-and-delegation.md)). The rest of the person's to-dos stay in the to-do views and the Today feed.
3. A request waiting for the person's reply, oldest first.
4. Work that is blocked because the agent cannot go on without the person.

Each row has one plain sentence that says what is needed, at most one line of context, the agent's recommendation, and the answer: one tap or one word (a number, or "add all"). It carries the face of the agent it belongs to. It shows no id, hash, path or ticket number.

Never a row: an item from a shared or group inbox that is not addressed to the person; something already done (Core re-reads the source of an item before it shows the row, and marks a finished item done without showing it); a reversible choice the agent can make itself, including whether to start work or to run it in parallel; an agent's own defects and findings; retry, check and quota noise; a memory notice.

Order. The default order is: first what is blocking, meaning something waits on the person right now (the agent is idle without the answer, or a deadline falls within 24 hours), and then what is overdue; then irreversible or costly decisions before reversible ones; then the soonest due date, then the oldest unanswered request. The person can switch the order to lightest first (about 5 minutes, about 30 minutes, longer); a client asks Core for the order the person chose and never ranks. Rows are drawn in at most three groups: decide, do and reply, or the effort groups of the lightest-first order.

Core uses source identity only to break otherwise equal ranks, keeping refreshes stable. In the default order, an upcoming deadline through exactly 24 hours counts as blocking; an overdue item comes next unless it also blocks an agent right now. Lightest-first uses the same default order within each effort group. The count is computed before the Home limit is applied.

Size. Home shows at most five rows and the count of the rest ("+7 more"); the whole list opens in the work panel. When the list is empty, this view says so in one line ("nothing to decide now"). No reply and no other view carries that line.

Answering. A row is answered in one tap or one word. Core re-reads the source and its answer mapping, refuses an outdated row or a different person, and records the source change and the answer with the person and requesting agent in one transaction. Repeated answers cannot create a second decision. When several decisions wait, the person can ask LINA to go through them one at a time: LINA asks one decision, records the answer and asks the next. A change to the list after an agent's action shows as a quiet badge; it never interrupts a reply that is being written or read.

### Design at a glance

The design at a glance is one diagram-level view of a goal (목표 in Korean): the stages of the work in the order in which the person can start to use them, with where each stage stands and which stage waits on the person. It answers "what will I have, and when". LINA Core generates it from the plan data and the work ledger on demand. Nobody maintains it by hand, and a stage whose state is not observed shows "unknown". It is the picture of the front page (layer 0). It is a `diagram` result that opens in the work panel. The conversation shows a card with a one-line headline, and on Home the line of the goal in the "At a glance" card opens it (see Progress at a glance) ([host-protocol.md](host-protocol.md)).

With several goals, the "At a glance" card on Home carries one line for each (see Progress at a glance), and the panel shows one diagram per goal.

A question about progress ("how much is left?") is answered in one line: where it stands as a stage or a percent, what remains in a phrase, and whether the person has to act. The stage and the percent come from the plan data ([work-and-delegation.md](work-and-delegation.md)), never from a model's estimate. For everything that runs at once, the answer is the digest of Progress at a glance.

### Progress at a glance

Progress at a glance is one screen that shows how far every running 목표 and 일 has come, without the person asking. LINA Core computes it ([work-and-delegation.md](work-and-delegation.md#progress-line)), and the app and the TUI only draw it. On Home it is the "At a glance" card, and in full it is the sidebar entry Progress.

- What needs the person is always on top. The first block holds the rows of "Waiting for me", in the same order, at most five and a count of the rest (see Waiting for me).
- Below it is one line for each running 목표 and each running 일. A line holds its name; a simple bar and a stage word, by default 설계 (design), 만드는 중 (building), 확인 (checking) or 끝 (done); what just finished, in a phrase; what comes next, in a phrase; and a mark when something waits on the person. An 일 that belongs to a 목표 is a line below it, and an 일 that stands alone is a line of its own.
- All running items are on this one screen. It scrolls and has no tabs or pages. Lines that wait on the person come first, then the others, the most recently moved first. A finished line leaves once the person has seen it, and a 목표 has a line only while one of its 일 runs or something waits on the person.
- A line shows no issue number, pull request, check, log or id, and no state word of the work engine. A stage or a bar that cannot be observed shows "unknown".
- Detail opens only on request, in the work panel: the Details of the line's front page, the design at a glance of a 목표, and the tasks and background runs behind the line as the running list (in progress, waiting and landed) limited to that item (see Running list). The line is the summary. The running list and the work cards are the working record behind it, and their state words are the same.
- In the app on a wide screen the lines are drawn as the board of Agent surfaces: the first column is "Waiting for me", then Running and Done.
- In the TUI the same lines are drawn as text: the stage word and a bar of block characters, one line each, with a key that opens the Details of a line in a pager or a side pane. The TUI Home shows the same compact card.
- In an external channel the same view is a digest of the lines: what needs the person first, then one line for each running item, at most five and a count of the rest, with no bar and a link that opens this screen when the app is installed ([integrations.md](integrations.md#channels)).
- A question in the conversation about how things are going is answered with the digest ([conversation-and-memory.md](conversation-and-memory.md#reply-shape)).

### Layers

| Layer | Name | Content |
| --- | --- | --- |
| 0 | Front page | A one-screen summary of one 목표, or of one 일 that stands alone |
| 1 | Map | The tree of intent cards |
| 2 | Page | One card as a block page: a summary on top and the person's intent field |
| 3 | Work | The 할 일 handed to agents, with their done conditions, run records and evidence |

Who writes each layer, and the rules that protect the person's intent text, are defined in [work-and-delegation.md](work-and-delegation.md). A person sees layer 0 by default. Layer 1, the map, is the design at a glance laid out as a diagram. Layers 2 and 3 open only when the person asks, from a card's Details or from the front page. Layer 3, with its tasks, run records and checks, belongs to the audit trail (see Default view).

The front page holds five things and nothing else:

- Why: usually written first by the person; LINA may edit it.
- What exists when it is done: LINA drafts it and the person confirms it.
- Where it stands: computed from actual results in the work layer, and "unknown" when not known.
- Waiting for me: the most prominent item.
- Recent updates: what, why and by whom, the person or an agent.

Nobody maintains the front page by hand; LINA rewrites it when the work layer moves.

### Result surfaces

Core presents what the person should open and keep, apart from the chat text, as a typed result ([host-protocol.md](host-protocol.md#typed-results)). The chat shows one short line and one result card. The full result opens in the work panel. A result is never dumped into the chat as raw text, and structured content (a table, a diagram, a chart, a comparison, a plan, an outline or a draft document) is shown only as a result. When a card replaces chat text is set by the reply shape rules in [conversation-and-memory.md](conversation-and-memory.md#reply-shape).

- Block documents in the style of Notion: 목표 pages, designs and memos, with checklist blocks inside them. A checklist block is a line of that document until the person promotes it to a 할 일. A block document is the person's input surface (intent fields, memos) and LINA's output surface (front pages, drafts), never a giant document the person has to maintain.
- 할 일 views in the style of Things (see To-dos).
- Results of the types below. A PDF report or a slide deck is a `file` result, made only when the person asks. A version comparison is a `comparison` result.

**Types.** Each type has a complete rendering on every surface. The last column is the card's default action; everything else is in the work panel.

| Type | In the app's work panel | In the TUI | Card action |
| --- | --- | --- | --- |
| `summary` | The outline as an editable list, with the key points, the decisions, the open questions to answer and the scope | The outline as text, edited by chat or in a text edit view | Write the document |
| `table` | A table; sorting and hiding columns belong to the viewer and are not saved | An aligned table with horizontal scroll | Save |
| `diagram` | The graph, laid out by the app from its nodes, edges and groups | An indented tree or a box outline for a small graph, an edge list for a large one | Save |
| `chart` | The chart, with its data table one action away | The data table with block-character bars or a sparkline | Save |
| `document_draft` | A block document, editable | A pager | Save |
| `comparison` | Side by side or unified; the person accepts or rejects each hunk when an apply request exists | A colored unified diff, with accept or reject for each hunk when an apply request exists | Apply, when an apply request exists |
| `plan_view` | The plan as an ordered, nested list with the state of each entry | A checklist | Confirm |
| `file` | The viewer for its media type | An export action that writes the file | Save |

**Result cards.**

- A result card is a labeled block: the type icon with the type's name, the title, the summary in at most three lines, and its state, which is preview or saved. While a background run is still making the next revision, the card shows the working indicator. It carries the face and name of the agent that presented it.
- A card offers Open and at most two other actions.
- Open puts the result in the work panel. A short result, such as an outline of a few lines or a plan of a few entries, may expand in place inside its card; a long one opens only in the panel.
- A result that a background run produces lands as the result card itself, at a turn boundary, and not as a second notice beside it ([work-and-delegation.md](work-and-delegation.md)).

**The work panel.**

- The panel shows one result at a time. Opening another result replaces it. A direct edit is a request to LINA Core, like every other edit in the app, so nothing is held back in the panel when it is replaced or closed.
- In the first version the person edits directly only a `summary` (the outline and the answers) and the blocks of a `document_draft`. Every other type is changed by telling the agent. A direct edit or an edit by chat of a preview makes the next revision. An edit of a saved result is an edit of the saved material and a saved version, as under Threads on screen.
- Sorting, hiding columns, zoom and collapsed branches belong to the viewer, are display state, and are never saved.
- The app draws a result from its typed body with its own components. It never renders HTML that a model or a connected source supplied. If a plugin's own view is ever shown (the reserved `app` type), it runs in a sandbox with no network and never reaches LINA Core directly.
- A type the app does not know shows the summary and the text. Text in a result is selectable, and the person copies it with the platform's own copy.

**Summary first.** For a document expected to run longer than about two screens, the agent first presents a `summary` result: the outline, the key points, what the document will cover and what it will not, the open questions and a length estimate. The person edits the outline in the work panel or by chat and answers the questions. The card's action, or a sentence in the conversation, then starts the writing, which is background work that the agent does not wait for ([conversation-and-memory.md](conversation-and-memory.md#results)). The finished `document_draft` arrives as a result card. The step is skipped when the request is one page or less, or when the person asks to have it written at once.

**Saving.** A result is a preview until the person saves it. Save is one explicit act, on the card, in the work panel, or as a sentence to the agent. It names the place the result will go, and the person can choose another there. After the save the card says where the result is and opens it. A preview does not expire and never becomes a library item on its own. The destination for each type and the rules of the saved record are in [materials-and-knowledge.md](materials-and-knowledge.md#saving-a-result).

**Other surfaces.**

- Mobile. A result opens as a full-height sheet reached from its card, with the composer kept. No type is available only on desktop.
- TUI. A result card has a key that opens the result in a split pane beside the conversation on a wide terminal, about 120 columns or more, and otherwise as a full-screen view with a key to return. Images appear only where the terminal answers the graphics-protocol query; otherwise an export action writes the file through `staging/export/` ([filesystem.md](filesystem.md)). The TUI keeps a result's card line in the scrollback and never prints its long form there.
- External channels (Telegram, Discord). A channel is a low-capability client: its adapter lists the result types it can render, and any other type shows the summary and the text. The default card in a channel is one short paragraph made from the title and the summary, with no heading, list, tag or frame, and a deep link that opens the result in the LINA app when the app is installed on the device. When the person asks for the detail in the channel, or asked in the channel for the content itself, the card carries the text up to about 1,500 characters, a table of up to about 8 rows and 4 columns goes inline as aligned text in a code block, and a larger table, a document, a comparison or a chart goes as an attached file (Markdown, CSV or PDF, or a rendered image where Core can make one) with the card text above it. A report of several alerts is one message per item. Only three message shapes are used: text, an image with a caption, and a file with a caption. Telegram's rich messages are used only after a capability probe, and plain text is the baseline. On Discord the card is classic message content with embeds, never the component-only layout, which cannot be mixed with content and cannot be removed from a message. A result built from material or memory that the agent's scope in that channel does not reach is sent as the title, the summary and the deep link only. A channel card has no result actions in the first version: the person saves or edits by telling the agent or in the app. Whether a result may be posted to a channel, and to whom, follows the channel's scope and the approval rule ([integrations.md](integrations.md), [main-authority.md](main-authority.md)). A card sent to a channel is a copy outside LINA's management.

### Cards and detail on request

- A card replaces text in the conversation when the reply rules say so ([conversation-and-memory.md](conversation-and-memory.md#reply-shape)). The card itself, its actions and its forms in the app, the TUI and external channels are defined under Result surfaces. The line above a card in the conversation says in a few words what is there, and the card's Open action is its Details action.
- A typed follow-up that asks for more ("why", "more", "the details") is answered with the next layer in at most three lines plus a card. A typed request for the whole thing ("write it all out") opens the full version in the work panel at once. This is the only case in which a long answer is acceptable, and it lands in the panel, not inline.

### Memo, to-do, goal

Memos, 할 일, 일 and 목표, with 계속 챙기는 것, 자료 and 보관, and the rule for promoting one to the next, are defined in [work-and-delegation.md](work-and-delegation.md#plans). On screen:

- Memos accumulate quietly in the Memo inbox. Each memo offers four actions: make it a 할 일, 보관, 나중에 (later) and merge into an existing item.
- LINA promotes a memo when its kind is clear and says so in one line, with one action to undo it; when the kind is not clear it asks the one question of the plans rule. 목표 drafts come from Klotho ([cognition-and-life.md](cognition-and-life.md)).
- Finishing an 일 archives it with its 할 일 and materials, and LINA says so in one line with one action to restore it. The agent the person is talking with archives a 목표 or an 일 that has not moved for a long time in the same way, and proposes splitting one that has grown too large.
- A 목표's front page gathers the summaries of the front pages of its 일.

### Threads on screen

Thread semantics are defined in [conversation-and-memory.md](conversation-and-memory.md). On screen:

- Every document, 목표 and 일 has its own thread with each agent that talks about it. To revise one, the person continues that thread instead of opening a separate editor. The agent carries the earlier discussion, decisions and edits as context.
- An edit made by chatting in a thread appears in the work panel at once and becomes a saved version. Only delegated work results arrive as a draft and an apply request ([work-and-delegation.md](work-and-delegation.md)).
- When the conversation turns to an artifact, the agent moves to its thread, says so in one line and offers one action to go back.
- Conversation threads and work threads look different, and a work thread is always reached through its work card.
- A chat of an external channel is a conversation like any other. The person's own chat with an agent's bot is that agent's main conversation, and the app shows it with a small mark of the channel on each message that arrived through it. A chat shared with other people is a channel conversation: the Agents screen lists it under the agent bound to it, the app shows it read-only, and the person writes in the platform ([conversation-and-memory.md](conversation-and-memory.md#channel-conversations)).

## Plans and work

### Pages and views

The work and knowledge surface is built from pages and views. Screen detail is the app's.

- Every work unit is a page: 목표, 일, 할 일, 계속 챙기는 것, 자료, a memo and a person record. A page is a document made of blocks: text, a checklist, a table, an embedded file, a result card, a link to another page.
- The PARA lists, the to-do list, the board (needs me, running, done) and the calendar are views over the same pages, filtered and grouped by their properties (kind, state, date, deadline, the agent it belongs to, the linked 일). A view never copies a page.
- Pages link by relations and @mentions, and every page shows its backlinks. A page may have sub-pages, but it can also be linked from many places, so nesting is never the storage model ([materials-and-knowledge.md](materials-and-knowledge.md#library)).
- Agents write pages and blocks only through LINA Core, the only writer. The person edits a page directly in the app, which sends the edit to LINA Core ([product-families.md](product-families.md#canon-and-writers)).

The work surface combines these pages with a list and board of 할 일 in the manner of a team tracker, and with the workers on each 할 일 shown beside it. Component mapping for the app (see UI components under App front end; screen detail is the app's, built after LINA Core and the TUI):

- A 할 일 row with its workers: the workers on a 할 일 are the coding workers of [work-and-delegation.md](work-and-delegation.md#workers) (Codex, Claude Code, Antigravity and Cursor), not persona agents. The row shows each worker as a small logo avatar (the Untitled avatar, company-logo variant) with a status dot (working, waiting for the person, done, failed), and several workers on one 할 일 (for example one codes and one reviews) as an avatar group. Beside them is one stage bar: queued, working, PR open, review, CI, merged, or failed or needs you. Progress is a stage and never a percentage, because workers report no real percent and LINA invents none. Stage and status come from the work engine's own records (the worker's session state, the pull request, its review and CI results), never from the worker's own report. Opening the row shows the affectUI Agent Inbox row for that run, and the Delegate Board lets the person drag a 할 일 onto a worker or leave it on Auto. Persona agents (for example Lina) appear separately, as the owner of the 일, and never among the worker avatars.
- The list and board of 할 일: the affectUI Task Board (columns, drag, quick add, detail panel, label filters, compact view) with Untitled tables, filter bars, command menus and tabs; the affectUI Filter Menu and Command Menu where an agent view needs them.
- An 일 summary: the affectUI Project Card (a progress ring with one segment per 할 일).
- Agents at work: the affectUI Agent Inbox (the main progress view), Agent Lanes, Agent Tree, Delegate Board (drag a 할 일 onto a worker), Agent Steering, Agent Plan and Tool Call Card, as Agent surfaces describes.
- Pages: an Untitled rich text editor as the page editor, Untitled tree views for optional sub-page navigation, Untitled activity feeds for page history and the affectUI Comments Panel; relations and backlinks are a small component of LINA's own.
- The calendar view: Untitled calendars and date pickers.
- Decisions for the person: the affectUI Decision Inbox, Approval Card and Permission Request.

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

목표, 일, 할 일 and 계속 챙기는 것 are LINA's own work records ([work-and-delegation.md](work-and-delegation.md#plans)). The plan screen of an 일 shows its done condition, its optional target date, its stages, the order and dependencies of its 할 일, assignment and filters. There is no milestone screen: a date is a field of the 일, and a checkpoint is a 할 일 with a deadline. A small manual 할 일 never gets a forced hierarchy or execution. Detailed plans are not in the default view: the person sees the front page and the design at a glance, and opens the map's cards and the tasks on request (see Default view).

A plan that LINA drafts for the person to confirm is shown as a `plan_view` result in the work panel (see Result surfaces). Confirming it is the plan confirmation point of [work-and-delegation.md](work-and-delegation.md).

### To-dos

The 할 일 views are lenses over the same records, computed by LINA Core ([work-and-delegation.md](work-and-delegation.md#the-to-do-record)). The app only draws them.

- The personal views are 오늘 (today), 예정 (upcoming), 언제든 (anytime), 나중에 (later) and the 메모함 (memo inbox). 오늘 shows the 할 일 due by the today rule, with overdue ones in their own block, 밀린 것 (overdue), each with one-tap rescheduling. The person can reorder 오늘. LINA may propose a day plan from free calendar time and never commits it on its own.
- The flow views are advanced: a board whose columns are the state categories, with a control that groups by 일, assignee, priority or tag, and a list. A person who never opens them never sees state, blocking or assignment.
- A one-line add shows chips for the date, the 일, the tags and the repeat before it saves. The TUI also accepts the quick-add tokens in the to-do record.
- A dependency shows as a hint ("do this first, it unlocks that"). A 할 일 that waits shows who it waits for and since when.
- A date and a deadline are two fields in two controls. A deadline is shown with its source.
- The pull request and issue of a code 할 일 of a developer user appear in its detail view, not in the lists.

### Work cards

- Work appears as live cards per unit (목표, 일, each 할 일 handed to an agent, or background run), not as a list. Cards appear on home and in the 일 and 목표 menu. Selecting one opens that object's thread and work panel; selecting a background run's card opens its result in the work panel.
- The card of a 할 일 shows the face and name of the agent that asked for it. Its recent updates name the person or the agent behind each change, so work that another agent changed shows that agent.
- A card shows one of the work card states defined in [work-and-delegation.md](work-and-delegation.md), each with its text label and icon, and says in one line, in the person's words, where the work stands. Input needed that the person owns shows as "Waiting for me". While LINA works, the card shows a working indicator. Work that only LINA handles, such as checking a result against the design, waiting for checks or merging, shows as working. The card's state and the 할 일's own planning state are separate; the card never sets the planning state.
- The Details of a card show a one- or two-line summary per stage, and its run records, acceptance criteria, linked pull request and checks.
- Stage summaries come from actual work results. When a result is not known, the summary says "unknown". A model's sentence that it finished never marks a card done.
- A dense list with sorting and filtering is an advanced view.
- Questions from work appear in the conversation that started the work, which belongs to the agent that asked for it; when that agent has been deleted, they appear on home cards. The person never relays messages ([work-and-delegation.md](work-and-delegation.md)).
- An approval request shows what, where and whether it can be undone in one view, in plain words with the agent's recommendation first, with a plugin tool's arguments and its choices, once or deny ([main-authority.md](main-authority.md)). It is also a row in "Waiting for me".
- A delegated result arrives as a draft. In a part without a repository it comes with an apply request in the work panel, shown as a `comparison` result (see Result surfaces); in development mode LINA Core applies it itself after its review, and the person sees the outcome. The conversation gets one line that says what changed for the person and, in a clause, what was checked, with the card; the line begins with "done" only when the done condition is met. The person can compare and revert at any time.
- Observation rules for work cards, including the developer view and direct-control handover, are in [work-and-delegation.md](work-and-delegation.md).

### Running list

One list shows everything LINA is doing in the background for the person: background runs and delegated coding tasks alike. LINA Core owns it, and the app and the TUI show it.

- A row is one run or task, with a one-line summary in plain words of what it does for the person. Rows are in three groups: in progress, waiting (not running, with one visible reason: a dependency, an area held by another task, a worker out of quota, host capacity, CI pending, a limit on background runs, or the person) and landed. Rows that need the person (input needed) and rows ready for review come first.
- The state words are those of the work cards ([work-and-delegation.md](work-and-delegation.md#progress-display)): working is in progress; waiting for a slot is waiting; input needed is "Waiting for me"; a landed complete or partial result is ready for review; failed and stopped are landed with their label. A landed row leaves the list once the person has opened or dismissed it, and stays reachable from its conversation.
- A task that stalls or fails shows its state word on its row until it has been dispatched again once; a second failure becomes a decision for the person under "Waiting for me".
- Within the attention-first ordering, ties use oldest start time and then a stable source key. An opened or dismissed landed row stays hidden after restart; an active row stays visible when opened.
- A row opens its work card. The card opens the result in the work panel. The developer view adds the run's brief, limits, usage and transcript.
- The list appears on Home as a compact strip, as the sidebar entry Running, as a count in the conversation header or beside the composer (for example "2 running, 1 waiting for you"), and in the TUI status line. The count is hidden when it is zero.
- A question about progress is answered in one line from the state of the list, without interrupting any run. "Also check X" reaches that run as a follow-up.
- Stop works per row and for all rows. A stopped row says so and keeps what was gathered.
- A change in the list updates its row in place. It is a quiet badge and never opens a panel, moves focus, sends a message or interrupts typing or an answer being written.
- The list shows tasks and background runs one by one. The progress view (see Progress at a glance) shows the same work as one line for each 목표 and 일. The state words are the same in both, and a line of the progress view opens this list limited to its item.

### Agent surfaces

The agent views of the app are drawn with affectUI Pro components (see App front end). Each shows data that LINA Core already computes. The TUI and the chat digest show the same data as text.

- **Board.** On a wide screen the progress view (see Progress at a glance) is drawn as a board of three columns: Waiting for me, Running (in progress, and waiting on something other than the person) and Done (landed, until the person has seen it). The first column comes first. A card is one line of the progress view and unfolds in place to its last steps in plain words, with Approve, Reject, Retry and Open where they apply. A general user sees one plain line for each card, with no model name, tool-call or search count, id or pull request number. A power user (power settings) may also see the model and the counts.
- **Steering.** The person adds to running background work without waiting for it: a note queued for the run's next step, which is the follow-up of the running list, or an interrupt, which is a stop.
- **Agent tree.** For power users, in the developer view: the delegated work of a conversation as a tree of the orchestrating conversation, its sub-agents and its workers, each with status, cost and time, in three visual kinds: the persona with its round face, sub-agents as indented text with no avatar, and workers with their square logo. It is folded to one line by default.
- **Lanes.** For power users, in the developer view: the parallel tasks as one lane for each worker, headed by the worker's square logo, with the hand-offs between lanes and the approval a task waits on shown on its block. It is not shown to general users by default.
- **Usage meter.** The plan usage and limits of each connected account, with a warning near a limit. It is the connected accounts view of Settings.

### Development mode

There is no separate kind of development project. Connecting a GitHub repository to a 목표, an 일 or a part of one turns that part into development mode, and it is the only difference between a developer user and any other user. The links between code 할 일 and issues and between drafts and pull requests are defined in [integrations.md](integrations.md). Development mode changes how LINA works inside; it adds no issue, pull request or check to the default view, and a card shows the outcome or "waiting for you", not what the checks reported. On screen, in the Details of the task or draft:

- a code 할 일 and a draft show their linked issue and pull request
- applying a draft is LINA Core's merge of the pull request after its review; the person is asked only when LINA Core is blocked
- GitHub reviews appear in the Details of the draft they concern
- the work panel shows a code view and CI status

Unconnected parts stay document-centered, and both may mix within one 목표. When code comes up in conversation, the agent may propose connecting a repository.

## Library

What the library holds, its ownership rules, viewers, versions and states are defined in [materials-and-knowledge.md](materials-and-knowledge.md). On screen:

- By default the library is never drawn as a folder or file tree; a person who wants one turns on the optional folder view ([materials-and-knowledge.md](materials-and-knowledge.md#library)). Files appear as pages and collections, grouped by conversation, work, source and topic. Media has its own library view.
- The library is where 자료 and 보관 live. Items group by 일, 계속 챙기는 것, conversation, source and topic. A 보관 view shows archived 일 with their 할 일 and materials. An archived item stays searchable, is marked archived, and can be restored.
- A library item opens in the work panel beside the conversation.
- A result that has not been saved is not a library item and is not indexed. Saving it makes it one ([materials-and-knowledge.md](materials-and-knowledge.md#saving-a-result)).
- Saved versions can be compared and reverted from the item at any time.
- In a GitHub-connected scope, the advanced view shows paths, a file tree, search and a code view.
- Connected sources appear as sources, each with its state. The library has no action that connects a source on LINA's initiative; connecting is the person's act ([filesystem.md](filesystem.md)).

A collection or a connected source can be attached to a conversation, from the library or from the composer's attach menu. A chip shows its name and source count, and a scope chip chooses what the answer draws on: the attachment only, the attachment and this agent's personal knowledge, or those and common knowledge as well ([materials-and-knowledge.md](materials-and-knowledge.md)). A document citation shows as a chip that opens the viewer at the cited passage. The memory evidence behind an answer is in the answer's Details, not inline.

## Feeds

The today feed, the news feed and the LIFE feed are separate streams and never mix. The news feed and the LIFE feed are separate menus. The agent talk log (see Agents) is none of the three feeds.

**Today feed.** The today feed is a LINA feed feature, not a separate product. LINA gathers 할 일 candidates from mail, meetings and conversations, removes duplicates, attaches the materials each one needs and posts them to the feed as candidates. A candidate becomes a 할 일 under the candidate rule of [work-and-delegation.md](work-and-delegation.md#the-to-do-record). The feed sits beside the 오늘 view. When the person wants, LINA places 할 일 in free calendar time and never commits a day plan on its own.

**Briefing.** A briefing is given when the person asks for it, and by default at the first activity after the person's day boundary, with a clock time as the fallback. It is made of these blocks in order, leaving out the empty ones: the schedule with clashes, today by area, deadlines within three days, overdue items, unanswered requests (at most five, with the days they have waited), what waits on others, and candidates. It ends with the first one or two things to do, each with its reason and what would change it. It follows the reply budget of a report ([conversation-and-memory.md](conversation-and-memory.md#reply-shape)).

**News feed.** The news feed shows news matching the person's interests. Klotho collects and selects it in the background ([cognition-and-life.md](cognition-and-life.md)); the app only shows it.

**LIFE feed.** The LIFE feed is a personal feed in the style of a social photo feed. It shows the agents' activities, posts, reactions, media and conversation experiences, each with the face of the agent it belongs to. It is not connected to any real social network. The world engine keeps the world, its time, events and daily activity in the background ([cognition-and-life.md](cognition-and-life.md)); the feed shows the result. LIFE has no separate app, installer or login and no persona of its own; the agent in LIFE is the agent whose life it shows ([product-families.md](product-families.md)).

## Settings

Settings include agents, accounts, plugins, channels, command network, notifications, models and processing, cognition, memory, expression, the developer view and reporting a problem. They come at two levels of one engine: general settings, which a person sees by default, and power settings, which one switch turns on. Both levels show the same state from LINA Core and make the same requests to it.

**Agents.** The person's agents, each opening its agent settings (see Agents).

**Accounts.** The person's model-provider accounts ([runtime.md](runtime.md#accounts-and-sign-in)).

- General settings. The first screen offers one action, "Continue with ChatGPT", which signs in, binds the owner and starts. Beside the model in use it says "Using ChatGPT plan", with a "Manage usage" action. Other services are "add account" choices: Claude (guided steps), Grok (starts the official command's sign-in) and Gemini (paste a key; the screen says that a Google AI plan is not a key's quota). Each account shows only a status chip: ok, needs sign-in, not eligible, or usage cap reached. When ChatGPT is usage-capped, "Manage usage" is the primary action. The connected accounts view lists every connection once, in plain words: the service, the plan it is on (for example ChatGPT Plus or Pro, Claude Pro or Max, SuperGrok, or a Gemini API key), whether it is signed in, and how much of its quota is left where the provider says so (the usage meter of Agent surfaces). It shows no model id. After each connect it shows one line of what LINA set up, such as "ChatGPT Pro and Claude Max connected: chat is Claude, coding is Codex", and one action changes it ([runtime.md](runtime.md#connected-accounts-and-automatic-setup)). When LINA moves work to another connected account because one is near its limit, it says so in one line. No screen asks about terms.
- Power settings. Choice of the model, with its effort, for each model role (`conversation` and `background`), from the model list of an account's adapter. The quota view: one table with the quota rows of every account, where an unreported value is shown as unknown. Several accounts for one service, each with its label. API-key accounts as alternatives (OpenAI, Claude, xAI). The raw status of each adapter. Direct registration of an MCP server (see Plugins). The coding-agent selection rules, an editable table that is provisional ([work-and-delegation.md](work-and-delegation.md)). Every choice that the automatic setup made is shown here, with the worker it picked for coding work, and can be overridden one by one.
- Adding, signing out and removing an account are requests to LINA Core, like every other edit in the app. A key the person types goes to LINA Core and is never shown again. A token never reaches a client.

**Plugins.** The plugin screen shows the catalog and the plugins that are on ([integrations.md](integrations.md)), not a fixed set of items. It is the same list in the general settings and in the power settings; the power settings add controls.

In both:

- Each plugin shows its name, its label ("Reviewed by LINA" or "Added by you"), its state (available, installed, on, needs sign-in, or failing with its cause), its account and where its credential comes from, for example the person's `gh` login or a token LINA keeps.
- Turning on a catalog plugin: the person opens the entry, reads the connect card, chooses Connect and signs in. The connect card has the same fields for every plugin: who makes it; where it runs; what you sign in with and what the service will ask for at sign-in, in plain words, with a note that the sign-in page will show a loopback address; what it can do, in a line; what it can send, share, pay, publish or delete (asks every time); and the pinned version. An unprofiled plugin says that LINA does not know which tools send, pay, publish or delete.
- The actions are turn on or off, sign in and remove.
- Opening a plugin lists its tools, with the ones that ask every time marked. A tool that changed since the person turned the plugin on shows what changed.
- When a plugin needs sign-in, the LINA app or the TUI the person is using opens the system browser and relays the result to LINA Core, or takes the pasted URL where it can't ([integrations.md](integrations.md)).
- A question from a plugin ([integrations.md](integrations.md)) arrives in the conversation as a card. A form question is a small form in the LINA app and prompts in the TUI. It names the asking plugin and always offers decline and cancel. A call that waits for the answer shows in the running list and never holds the conversation.

In the power settings only:

- Adding a plugin from a remote MCP URL, a local server command, a local folder or a Git URL. For a URL: the transport (Streamable HTTP, or HTTP with SSE as an explicit choice), the initial scopes, a fixed callback port, a client id and secret to use in place of registration, and fixed request headers for an API key, which are stored in the secret store and never shown again. For a command: the whole command without truncation and labeled as running with the person's permissions, its environment variables as secrets, its working directory, and its sandbox allowance, which is the folders it may write and whether it may use the network.
- The tool list of each plugin, with the tools that ask every time marked.
- Exporting a plugin definition as a file and importing one. A definition from a file shows the connect card before it does anything.
- The connection's protocol era, authorization server and last error.

**Channels.** The person's channel connections and the dispatch of each agent ([integrations.md](integrations.md#channels)).

- Each connection shows its platform, its bot's name, its state (connected, needs pairing, unreachable, or failing with its cause) and the agents bound through it. It never shows the token.
- The actions are add a connection, pair, bind an agent to a chat, change a binding's scope, and disconnect. Adding a connection takes the bot token the person creates on the platform, or the platform's own flow for creating a bot from a link where one exists. The platform flow is the default and pasting the token is the fallback. Pairing shows a one-time code that the owner sends to the bot from the platform.
- A binding shows its chat, its agent and its scope in plain words: where the agent listens, what it may read, whether it may write first, whether it may use tools and whether it may use the person's memory. The connect step explains once what listening to every message in a chat costs on the platform.
- Revoking a binding, or disconnecting, stops the agent in that chat at once. A scope is listed with the standing grants and revoked from there too ([main-authority.md](main-authority.md#channels)).

**Command network.** The standing grants that give sandboxed commands network access ([runtime.md](runtime.md)), each with its command prefix and a revoke action ([main-authority.md](main-authority.md)).

**Notifications.** LINA pushes only what needs the person now, or a finished outcome the person asked to track, while they are not looking: a row of "Waiting for me" that is blocking or due within 24 hours (an approval or a worker's question waiting, a background run or an agent exchange that needs the person, and the reminder of a 할 일 at its planned time, with the pre-warning of a deadline), and delegated work or a background run that has fully landed or failed. A failure notifies once the one dispatch again has failed. One task landing while other tasks of the request are still in flight updates the running list and does not notify. Landings that arrive together are one notification. Everything else is pull: it waits in the list or on its card. A notification is one short paragraph of about two sentences with a link to the item, or a single card, with no frame, heading or id. A finished outcome is one line of what changed for the person; it begins with "done" only when the done condition is met, it says in a clause what was checked, and the proof is the card. Low-urgency items are batched into one notice; only urgent ones are sent at once. A scheduled check that finds nothing sends nothing, or one line; each exception is its own message of at most three lines. The mode is one of five. The person sets one mode for every agent and may set another for any one agent; a notification shows the face and name of the agent it comes from, when it comes from one.

| Mode | How it reaches the person |
| --- | --- |
| System | The platform's own notifications. The default where the platform has them |
| Terminal | A notification escape sequence the terminal shows, for the TUI in a terminal or over SSH |
| Bell | The terminal bell |
| Channel | A message from the agent's bot in the owner's own chat of an external channel, only where that binding lets the agent write first. It is a notice of a line or two with a link, never the content of an exchange |
| Off | No notification; the item waits in the conversation and on its card |

A surface never notifies about what it is already showing. Opening a notification opens the item.

**Models and processing.** The model of each role, chosen from the accounts above, and, as separate items, the judge (Jev), embeddings and transcription ([runtime.md](runtime.md)). Each of those separate items shows its connection state, its key and its usage. The compaction threshold sits here as well ([conversation-and-memory.md](conversation-and-memory.md)).

- An item that is off shows only its state and what LINA does without it, for example "keyword search in use instead of semantic search".
- No surface asks for any of the non-chat items at first run. The one question of the first run is the ChatGPT sign-in. An agent may suggest a plugin once in a conversation when it would help ([integrations.md](integrations.md)).
- A setting the person edits is a request to LINA Core, like every other edit in the app.

**Cognition.** Each product cognition feature, with a switch that turns it off on its own ([cognition-and-life.md](cognition-and-life.md)).

**Memory.** Importing a Hermes install. The person chooses the install and, if wanted, a Honcho server with a read credential. The person confirms which Hermes profile becomes which agent, which identities are the person, and which peer is which, then reads the rehearsal report before the import runs: counts per source, what will not be imported, the secrets and identifiers found, and whether imported history will be embedded, which happens only if the person agrees on this screen. After the import the screen shows the verification result and the loss report, and offers the promotion batches, grouped and newest first, where a whole group can be accepted. Nothing imported is remembered until the person accepts it. Every action is a request to LINA Core ([conversation-and-memory.md](conversation-and-memory.md#importing-a-hermes-install)).

**Expression.** For each agent, the switch that turns its emotional expression off ([conversation-and-memory.md](conversation-and-memory.md)). For the person, the reply length: the budgets of [Reply shape](conversation-and-memory.md#reply-shape), with a choice of shorter, standard and longer, and the changes LINA made after the person's reactions.

**Developer view.** The switch that adds developer fields to the Details of work cards ([work-and-delegation.md](work-and-delegation.md)). It changes nothing in the default view.

**Report a problem.** An action in the settings and in the about view of the LINA app and the TUI. The agent drafts the report: what happened, the app and Core versions, the operating system and the recent lines of the self-diagnosis log ([self-diagnosis-log.md](self-diagnosis-log.md)), whose events hold no personal content. It strips personal content from anything the person told it, shows the draft, and posts only after the person confirms. The post is a public issue on the tracker of the LINA repository in the person's name, so it is public posting and asks every time under [main-authority.md](main-authority.md#approval); an "always allow" never covers it. Ideas go to the repository's Discussions instead ([product-families.md](product-families.md#repository-artifacts-and-versions)).

## Agents

What an agent is, the persona layers, presets, personal and common knowledge, and deletion are defined in [product-families.md](product-families.md) and [conversation-and-memory.md](conversation-and-memory.md). Creating, changing and deleting an agent is the person's act; every action below is a request to LINA Core and is recorded with the person id. On screen:

- The agents screen lists the person's agents, initially the eight starters, each with its face, its name, its role, its source (the preset it was made from, or "from a description") and its last conversation when one exists. Choosing one opens its main conversation. Kai (investing) is labelled advice only and Kanna (legal) drafts only; these labels describe Core's effect policy, not a voice setting. Display the existing names and personas, not role names in their place. Every starter works with its shipped persona without the private backstory or world source.
- Creating from a preset shows each preset with its form and lets the person name the new agent. Creating from a description takes the person's description and shapes the agent through the same questions as voice tuning ([conversation-and-memory.md](conversation-and-memory.md)). Either way, the new agent's main conversation opens.
- An agent's settings hold its name, its expression switch and its notification mode. Its persona changes by asking the agent in conversation, and each change is a new persona revision ([conversation-and-memory.md](conversation-and-memory.md)). An agent made from a preset shows which layers come from its preset. Its settings also list the channels it is dispatched to, each with its scope, and open the log filtered to its exchanges.
- An agent's settings show what it tracks for the person, the list of record sets it keeps (for example, areas it was told about), which grows when a new area appears ([conversation-and-memory.md](conversation-and-memory.md#preset-methodologies)).
- An agent's settings show what it knows: its personal knowledge and the common knowledge every agent shares, each item marked as one or the other. The person moves an item between the two, and the move is recorded.
- An agent's imported conversations appear in a separate read-only list that is closed by default. A ledger search result from one is labelled imported, and an old id from an earlier transcript can be pasted into search and opens the record it resolves to.
- Deleting an agent shows what goes to the trash for 30 days (its persona revisions, emotional state, conversations and personal knowledge) and offers to move any of its personal items to common knowledge first. Tasks and background runs the agent asked for keep running, and their questions and results move to home cards.

### Agent talk log

Agents talk to each other in the background ([conversation-and-memory.md](conversation-and-memory.md#agent-exchanges)). The person does not read that talk unless they open it.

- The log lists exchanges, newest first, one line each: the face and name of the agent that asked, the face and name of the agent that answered, what it is about in a few words, a state word (waiting, answered, failed, or needs me) and the time.
- A line expands in place to the messages of the exchange, each with the face and name of the agent that wrote it. The log opens from the Agents screen, and from an agent's settings filtered to that agent. No other screen shows the content of an exchange.
- An exchange that needs a decision only the person can make is marked "needs me" and appears as an item in "Waiting for me". Nothing else about exchanges interrupts, notifies or enters a conversation.
- When an exchange that an agent's conversation depends on is answered, that conversation gets a short notice at a turn boundary, never in the middle of a reply ([conversation-and-memory.md](conversation-and-memory.md#agent-exchanges)).
- In an external channel the person can ask for the log; the answer is a short list of the same one-line entries, not the content.
- The log is a view of the ledger. It holds no state of its own, and the TUI shows the same entries as lines with a key to expand.

## Launcher

- On macOS, LINA's launcher does what the person uses Raycast for, without copying Raycast's structure. Which Raycast features it covers is filled in a feature map during implementation.
- The launcher opens from a global shortcut. Whatever the person says from the launcher goes into the main conversation of the agent the person talked with last, or of another agent the person names in the launcher, and the answer continues there.
- The launcher includes clipboard history. Items marked concealed, as password managers mark them, are never stored. The history stays on that Mac. It never goes to LINA Core and never becomes memory or material.
- A clipboard item reaches LINA only when the person explicitly puts it into a message, like typed text.
- The person turns the launcher and clipboard history on. Because the person turns them on, they are the only exception to the clipboard rule in [non-competition.md](non-competition.md); nothing else LINA does reads or writes the person's clipboard.
- The launcher is part of the LINA app on macOS. The app's interface draws it with the app's design tokens. The shell gives it, through the bridge, its global shortcut and its non-activating panel that floats above the person's apps, and a small native module owns its clipboard access, because the history must skip concealed items. The launcher is one of the macOS checks, the first gate of the app milestone (see Platform adaptation); if the shell fails it, the shortcut, panel and clipboard use native code behind the same design tokens.
- On Omarchy, launching LINA belongs to the Omarchy shell's launcher.

## Korean input

Hangul composition input is an acceptance criterion of the LINA app on every supported platform: Omarchy with fcitx5-hangul, macOS, Windows, iOS and Android. An app that fails it is not accepted on that platform. It is the first check of the acceptance in Platform adaptation, and on macOS the first gate of the app milestone.

It applies to every text input: the composer, intent fields, documents, the outlines and drafts edited in the work panel, memos, search and the launcher. Each platform is judged on a real device with these input methods:

| Platform | Engine under test | Input methods |
| --- | --- | --- |
| macOS | The Wails shell on the system WKWebView | The system Korean input, 2-Set |
| Windows | The Wails shell on WebView2 (Chromium) | Microsoft IME in 2-beolsik and 3-beolsik, including fast typing |
| Linux (Omarchy) | The Wails shell on WebKitGTK 6.0 (GTK4), on native Wayland | fcitx5-hangul |
| iOS | The system WebView (WKWebView) | The system Korean input |
| Android | The Android System WebView | Gboard and the Samsung keyboard |

A platform passes when all of the following hold with each of its input methods:

- The syllable being composed shows inline at the caret, and the candidate window opens at the caret.
- No syllable is lost, however fast the person types.
- Sending while a syllable is in composition sends the whole text exactly once. The composing syllable is neither dropped nor duplicated, and Enter to send never breaks the composition.
- Keys the input method consumes never trigger a shortcut or a send.
- Backspace, arrow keys and focus moves during composition behave as in the platform's own text fields.

Every text input of the app follows these field rules, which keep a composition intact:

- A key is consumed by the input method when the event reports composing, its key code is 229, or the field's own composing flag is set. The field clears its flag one tick after the composition ends. Enter sends only when none of these holds.
- On WebKit-based engines (the shell on macOS, iOS and, until a test shows otherwise, WebKitGTK on Linux), Korean composition does not reliably raise composition events. The field drives its state from before-input and input events as well, including the replacement-text input type that the engine sends when a syllable is composed again, and installs no custom input-method bridge over the field.
- While a composition is active, the app writes no state back into the field, does not reconcile its DOM, and applies no length limit, trim or other transformation. A draft is saved by reading the field only, and the app reconciles once after the composition ends.
- "Sends exactly once" is tested at the LINA Core boundary: the conversation ledger shows one message. A last-character fault at the platform's input-method protocol level can affect every client, native ones included, so a test of the screen alone does not prove it.
- These rules apply to the inputs of every component library of the app, the paid ones included (see App front end).

On Linux, LINA OS sets the input-method environment of the app (fcitx5 for GTK) explicitly as a precaution. Which path carries Hangul to the GTK4 shell, the compositor's text-input protocol or the fcitx5 GTK module, is decided by test, and the older protocol version 1 is not forced on Hyprland. The test runs with fcitx5 restarted and with Qt applications running. The app runs a start-up self-check and tells the person when the input-method path is not active.

The TUI accepts the committed text the terminal's input method delivers, renders Korean text at its correct cell width and keeps the cursor on the correct cell.

## Accessibility

- No state is conveyed by color alone, and no agent is told apart by color alone: an agent's color goes with its initial and name. Paused, waiting for me, done and every other state carry a text label and a distinct icon or shape; color only reinforces them. This applies to the TUI as well.
- The working indicator has a text equivalent.
- "Unknown" is a visible state with its own label.
- Text meets WCAG 2.2 AA contrast in the light, dark and high-contrast values. Korean text counts as large text only from 18 point, or 14 point bold.
- A chart and a diagram are never told apart by color alone: series and groups also differ in shape or label. Every result has a plain-text rendering that serves as its text equivalent.
- Text size, reduced motion and high contrast follow the person's settings (see Platform adaptation). With reduced motion on, motion becomes a fade or stops.

## Deferred

- The form and expressions of each preset, the common default form and how its color and initial mark is drawn, the signature interactions and the icon set: set by design exploration and recorded in `DESIGN.md` and the token source.
- How the LINA app joins the Omarchy shell's bar, launcher and notifications: set before the first Omarchy GUI work.
- An Omarchy edition of the LINA app's visual design that matches Omarchy's own identity: a follow-up design task. Until it exists, the app keeps LINA's visual language on Omarchy and follows the active theme as Platform adaptation defines.
- The foundation component, dependency and generation rules for the React app (the npm lockfile and exact versions, the generated TypeScript protocol types and CSS tokens, and the pinned Wails version): added to [foundation.md](../policy/foundation.md) and [dependencies.md](../policy/dependencies.md) by the first app implementation.
- Exact sizes and motion of the screen skeleton: tuned during implementation and recorded in the token source.
- Menu placement of the today feed, and the screen design of the today feed, meeting notes, the plugin catalog, the connect card and the plugin list: set during their implementation.
- The launcher's feature map: filled in during launcher implementation.
- The layout of the agents screen, the agent picker in the conversation header and the creation flow: set during their implementation.
- How often LINA promotes, archives and proposes splitting: set by measurement during development and recorded in the implementation issue.
- The views of the plan map (structure, execution and gap): the UI choice is set during implementation of the plan screen.
- The layouts of the 할 일 views, the quick-add chips and the 메모함 triage: set during their implementation.
- The mobile host: Capacitor by default, confirmed or changed by the checks in Platform adaptation. The Wails v3 release to pin, and whether a platform needs the Electron fallback: set by those checks, because Wails v3 is in beta.
- How a mobile app that is closed receives a notification: it needs a push sender, and LINA has no relay ([runtime.md](runtime.md#remote-access)). It is independent of the shell choice.
- The licence position of the engine and runtime that a shell bundles under the dependency rules of [dependencies.md](../policy/dependencies.md), and where its notice file is listed in `THIRD-PARTY-NOTICES.md`: decided with legal review before the shell is final.
- The layout of the running list on Home, in the sidebar and in the TUI, and the ordering and retention of its landed rows: set during their implementation.
- The panel layout of each result type, the exact size of a result card, and which table and chart interactions go beyond sorting and hiding columns: set during implementation of the work panel.
- The width at which the TUI opens the work panel as a split pane, and whether Bubble Tea and Glamour render tables with Korean double-width text correctly: set by measurement in the TUI spike.
- Whether LINA Core can render a chart or a diagram to an image for channels without a headless browser, and the scheme of the deep link: until Core can, channels receive text and files; set during implementation of the channel adapters.
- The order, grouping and size of "Waiting for me", the wording of the "At a glance" card, and the reply budgets: starting values set in this contract and in Reply shape, tuned by use and recorded in the implementation issue.
- The order of the progress lines, the stage rule for a 목표, the line cap of the digest in a channel and the scheme of its link: starting values set in Progress at a glance and in [work-and-delegation.md](work-and-delegation.md#progress-line), tuned by use.
