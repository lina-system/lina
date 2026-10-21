# Terminal UI

This document fixes how the LINA terminal UI is built in Go on Bubble Tea where the design contracts leave a point open: its state model, layout, rendering, input, spike gate and tests. It is a design document, subordinate to the thirteen contracts of this directory, above all [surfaces.md](surfaces.md#tui) and [host-protocol.md](host-protocol.md#client-connection). It cites contract sections instead of restating them. Where this document and a contract differ, the contract wins and this document is corrected. The Core side of the same design is in [go-core.md](go-core.md), and the order of work is in [go-roadmap.md](go-roadmap.md). Finishing this document does not mean any screen, package or library choice is implemented or accepted; runtime proof belongs to the implementation issues that consume it.

## North star

Short replies, waiting for me first, progress as lines, detail in a pager. The TUI follows [product-families.md](product-families.md#north-star) and [surfaces.md](surfaces.md#default-view): the person sees the design at a glance with how far each piece of work has come, and the list of what they must do or decide. The TUI never prints a long form into the scrollback. A card line stays there, and a key opens the rest.

Numbers marked (P) are starting values to measure. A number without (P) comes from a contract or a protocol.

## Terms

| Term | Meaning |
| --- | --- |
| Agent | A persona the person creates ([product-families.md](product-families.md#agents)); LINA is the name of the default agent. The TUI shows its name and a mark, its initial and name, never colour alone ([surfaces.md](surfaces.md#accessibility)). |
| Worker | A coding tool the person installed. It is never an agent and never a speaker. The TUI shows a worker as its name in brackets, such as `[Codex]`, and a sub-agent as plain indented text under its persona; marks never rely on colour alone. |
| Card | A labeled block in the conversation that stands for one thing: a result, a question, an approval, a background notice. |
| Result | A typed result of Core ([host-protocol.md](host-protocol.md#typed-results)), shown as a card and opened on request. |
| Running list | What LINA does in the background: in progress, waiting, landed ([surfaces.md](surfaces.md#running-list)). |
| Waiting list | What only the person can decide or do, ranked by Core ([surfaces.md](surfaces.md#waiting-for-me)). |
| Details (자세히) | The audit trail behind a card, a result or an answer, opened only on request. |

## Decision index

Ids are stable. Section headings carry the ids they specify. T-02 is withdrawn: [host-protocol.md](host-protocol.md) decides the envelope, the handshake and the versions.

| Id | Decision | Status |
| --- | --- | --- |
| T-01 | Boundaries and trust | changed |
| T-03 | Streams on the client | changed |
| T-04 | Approvals, questions and sign-in | changed |
| T-05 | Connection states | changed |
| T-06 | State model and presentation kinds | kept |
| T-07 | Layout, tiers and row budget | changed |
| T-08 | Library choice, risks and spike gate | kept |
| T-09 | Rendering, width and Korean | changed |
| T-10 | Input and commands | changed |
| T-11 | Tests, tokens and theme | kept |
| T-12 | Home, waiting for me and progress | new |
| T-13 | Results, cards and the pager | new |
| T-14 | Agent surfaces and settings as text | new |
| T-15 | Notifications | new |

## Boundaries and trust (T-01)

`lina tui` is a subcommand of the `lina` binary and runs as its own process ([runtime.md](runtime.md#the-lina-command)). It holds display state only, under the rules of [surfaces.md](surfaces.md#the-lina-app-is-a-thin-client): what is on screen, layout, reading position and unsent drafts. Drafts live in memory and never on disk, because a file of drafts would be personal content outside canon.

The empty TUI increment (#32) runs as `lina tui --socket <path>` with plain text input and output. `internal/clientapi` owns the Unix connection and the generated client 1.0 handshake types; `internal/tui/ui` owns the connection display and quit input. The command supplies the product version for diagnosis. This increment declares no capabilities and writes no preferences or drafts. The connection surface now uses Lip Gloss v2.0.6 to style usable terminals from generated tokens (T-11); pipes and terminals with unset or `dumb` TERM retain plain connection codes. Terminal input uses raw mode for single-key quit, with one cancellable reader from negotiation through the connected state. The TUI stops and joins that reader and restores the original terminal settings on return, including termination by SIGTERM, SIGINT or SIGHUP. It installs no animation timer. Bubble Tea, conversation input and the remaining screens still follow the T-08 spike gate. The pure `state`, `sanitize`, `layout` and `render` packages are independently testable foundations; these pure packages add no terminal I/O or conversation wire payload.

- It keeps its preferences (theme, glyph set, width settings, notification mode) in one client file, mode 0600, at the location the install fixes. A missing or bad file never blocks start.
- It never opens canon, never runs `lina core` and never holds the lock. Every message it sends is a request that Core accepts or refuses, and when the two disagree Core wins and the TUI refreshes.
- It uses only the client connection. Its types are the Go types generated from the schema ([host-protocol.md](host-protocol.md#schema-and-generated-types)).
- Protocol errors and Core logs go to the status line and a log, never into the conversation ([host-protocol.md](host-protocol.md#client-connection)). `TEA_TRACE` and `TEA_DEBUG` stay unset, and panic output is masked.
- Import rule: `internal/tui/...` imports only `internal/clientapi`, `internal/design`, `kit/protocol` and other `internal/tui` packages. Charm packages are imported only in `tui/term` and `tui/ui`. The one architecture `go test` of [go-core.md](go-core.md) (C-40) checks it.
- Trust: the socket's user check and the rule for who may decide an approval are Core's ([go-core.md](go-core.md), C-42). The TUI shows a refusal as a refusal and adds no rule of its own.
- Parity with the LINA app comes from the shared conformance fixtures ([host-protocol.md](host-protocol.md#conformance-fixtures)). The app is not designed here.

## Connection states (T-05)

`lina tui` never starts a main. The connection is in one of eight states. Each is a screen state, never an empty conversation and never a conversation item.

For plain output, a validated `welcome` prints `live` on standard output. On a usable terminal, the brand header and a labelled Connecting state appear before the handshake; a validated welcome draws Connected with a quit hint. A refusal, absent Core or other handshake failure draws the corresponding marked state. Quit and EOF draw Disconnected. Only these existing transitions are wired; the theme also supplies presentation data for the future states in the table. Reading `q`, Ctrl+C or Ctrl+D, or reaching end of input, closes only the client connection and exits 0; other input is ignored. Terminal input enters raw mode before negotiation, so quit needs no Enter and remains available while a Core handshake is pending. Quit cancels that handshake instead of waiting for its five-second timeout. While raw mode is active, SIGTERM, externally sent SIGINT and SIGHUP also cancel negotiation or the wait for input and exit through the same normal-termination path with status 0. One reader owns terminal input throughout; cleanup interrupts and joins it before restoring the original terminal settings and releasing signal handling. Nonterminal input keeps the handshake-then-read behavior. Failure to connect to the supplied socket reports `unavailable` on standard error and exits 69 without starting Core or creating state. A typed `unsupported_combination` refusal reports that code and exits 78. Bad arguments exit 64; malformed handshakes and other connection or input/output failures exit 70. Connection and handshake share a five-second timeout. Peer diagnostic text is not printed as terminal output. Reconnect, streaming disconnect notices, supported-range presentation and the remaining screens below belong to later increments.

| State | Screen |
| --- | --- |
| Connecting | The status the TUI can read |
| Live | Normal |
| Resyncing | The window stays; the status line says so |
| Retrying | The window stays, read-only; attempt and next try |
| Refused | The cause code; for `unsupported_combination`, the supported ranges |
| Core recovering | History is readable; mutations are disabled and show their `unavailable` cause |
| Core stopped | The Core-absent screen: cause code and next action only |
| Failed | A cause code and the next action |

A changed epoch is announced once as a local notice. Remote use is `lina tui` connecting inside the tailnet ([runtime.md](runtime.md#remote-access)). A TUI run in an SSH session on the host is a local client.

## Streams on the client (T-03)

The server side is [go-core.md](go-core.md) (C-42): topics, cursors, rings, batching and heartbeat. This section is the client side.

- The TUI holds `core`, `waiting`, `progress` and `running` for the whole connection, the focused conversation with its results, and further conversations as background topics up to the limit Core grants. It opens `exchanges`, `accounts` and `channels` only while their view is open.
- The socket reader feeds a bounded channel that the reducer drains. If it fills, the topic is marked out of sync and a resync is requested. The reader never drops a frame. A slow terminal delays pixels, not state.
- Epochs compare numerically, the form of CP-02 ([host-protocol.md](host-protocol.md#envelope)), without a machine-integer limit. A gap or a newer epoch requests one resync per out-of-sync topic. Duplicate cursors and revisions and older epochs are ignored; a repeated delivery never changes the display. A resync snapshot cannot roll back the accepted cursor within the same epoch and revocation epoch.
- A different revocation epoch ([conversation-and-memory.md](conversation-and-memory.md#terms)) makes the TUI drop cached items before the snapshot applies, so a forgotten item cannot survive a reconnect.
- Replaying a stream or a snapshot twice leaves state unchanged.

## State model and presentation kinds (T-06)

Every change of UI state goes through one pure function.

```go
// internal/tui/state: no I/O, no clock, no goroutines, no Charm import.
type Msg interface{ isMsg() }
type Cmd func(ctx context.Context) Msg // the result carries the Gen that issued it

func Reduce(s State, m Msg) (State, []Cmd)
```

Messages are frames from Core, keys, paste, resize, ticks from an injected clock, and async results. Effects leave the reducer only as `Cmd` values that `tui/ui` runs. Reducer rules:

1. A block id is the ledger item id. A local echo of a sent message uses its idempotency key until the item arrives, then is replaced in place.
2. A delta applies by (block id, revision) and is idempotent. `item_done` replaces the streamed text.
3. An async result carries the generation that launched it. An older generation is dropped.
4. Durations come from server timestamps and the offset measured at the last snapshot, never from arrival time.
5. Busy is set in the same call that issues a send. A failed send restores the draft merged with anything typed since.
6. An interrupt shows "stopping" and converges on the stored partial.
7. Selection, folds and scroll key on block id and survive a resync.
8. Local notices live outside the items, so a resync never drops them.
9. The client acts on typed fields only. It never parses model, tool, plugin or web text for commands, ids, links, approvals or status.
10. Every string that did not originate in the client passes `sanitize.Display` before layout: control, bidirectional and OSC sequences are removed, and styling follows truncation.
11. A frame that fails schema validation shows a placeholder with a code and is counted.

The reducer accepts typed internal `Snapshot`, `Delta` and `ItemDone` projections.
They do not define a wire format: client 1.0 still reserves the conversation
payload fields, and the conversation implementation supplies their adapter.
`Frame` uses the generated schema validator, admits welcome identity and
capabilities, counts unknown and malformed envelopes separately, and keeps
error codes outside conversation blocks. Reserved empty payloads create no
content. An epoch change invalidates pending effects and restores a pending
send's draft; an authoritative echo reconciles the local block in place.

`sanitize.Display` preserves structural newlines, expands tabs to four spaces,
replaces invalid UTF-8, and removes other controls, bidi controls and terminal
escape strings, including unterminated strings. Unsealed snapshots and deltas
retain a value-type sanitizer continuation, so instructions and UTF-8 split
across frames have the same filtering as contiguous input. A completed item is
sanitized independently and replaces that continuation.

The client computes no state of work. It shows the words and marks Core sends ([work-and-delegation.md](work-and-delegation.md#progress-display)), and "unknown" is shown as "unknown", never as none, zero or done.

**Presentation kinds** are the kinds of version 1.0 of the client connection ([host-protocol.md](host-protocol.md#client-connection)): output delta, item done, turn state, approval request, question, result card, running-list update, background notice, progress view, waiting list, Details, readiness failure, and the results of agent, work-record, account, memory, channel and exchange requests, and error. An unknown kind is ignored and counted. The wire carries codes and parameters, and the TUI owns the Korean and English catalogs. Display strings travel only for text the person or an agent wrote. A secret placeholder from Core renders as a placeholder, and the TUI has no way to reveal one.

## Layout, tiers and row budget (T-07)

The screen is a pure function of State and terminal size. A region whose capability was not granted shows as unsupported, never as empty ([host-protocol.md](host-protocol.md#versions-and-capabilities)).

Regions:

- Header: the focused agent's name and mark, and the connection word.
- Conversation.
- Composer, with its attachment strip.
- Card row above the composer, on Home (T-12).
- Status line: connection, agent, the running count ("2 running, 1 waiting for you"), restart pending, and Core messages.

Overlays form a stack and the top one receives keys first: agent list, command palette, pager. The agent list is the agents screen of [surfaces.md](surfaces.md#agents) in text: mark, name, source and last conversation; Enter opens the agent's main conversation.

Width tiers (P):

| Tier | Columns | Arrangement |
| --- | --- | --- |
| Wide | 120 and up | Conversation and an optional side pane |
| Medium | 80 to 119 | Conversation; everything else as overlays |
| Narrow | below 80 | One region at a time, switched by key |
| Too small | below 40 columns or 10 rows | A size notice and the status line |

```
 Lina                         live                      2 running, 1 waiting for you
+--------------------------------------------+---------------------------------------+
| you   Compare the two quotes.              | Quote comparison           preview     |
| lina  B is cheaper by a tenth.             | table                                  |
|       [table] Quote comparison             | item       A          B                |
|       o open   s save                      | price      ...        ...              |
|                                            | delivery   ...        ...              |
+--------------------------------------------+---------------------------------------+
| WAITING FOR ME  1   Send the signed form to the supplier?  Recommended: send      |
| AT A GLANCE         Move  [###--]  building  just done: ...  next: ...  !         |
+-----------------------------------------------------------------------------------+
| > composer                                                                        |
+-----------------------------------------------------------------------------------+
 live  Lina  2 running, 1 waiting for you
```

The diagram is schematic: it fixes the regions and their order, not glyphs or widths.

**Row budget** is a pure function in `tui/layout`.

```go
func Plan(size Size, caps Caps, st Shape) Layout // regions disjoint, sum fits, floor holds
```

- The header, the status line, one composer row and a transcript floor of four rows (P) always survive.
- The composer grows with the draft to six rows (P), then scrolls inside itself.
- What remains is shared in a stated order of shedding: the side pane closes first, then the running strip folds to a count, then the card row folds to one summary line. A folded part keeps its count, so a count on screen always equals the list.
- `Plan` treats negative dimensions as zero. At too-small sizes it reserves one
  status row when a cell exists and uses the remaining rows for the size notice.
  At usable sizes the running strip starts at one to three rows and the card row
  at one to five; each keeps one summary row when folded. The attachment strip
  uses one row. After the stated shedding, composer growth yields as necessary
  to preserve the transcript floor. Counts and unsupported flags remain typed
  output even when their region folds.
- Property tests enumerate widths 0–240 and heights 0–100 with empty and crowded
  shapes and granted and missing capabilities, and exercise threshold and
  integer-limit dimensions. The planner itself imposes no maximum size.

## Home, waiting for me and progress (T-12)

Contract: [surfaces.md](surfaces.md#waiting-for-me), [surfaces.md](surfaces.md#progress-at-a-glance), [work-and-delegation.md](work-and-delegation.md#progress-line).

Home is the starting state of the conversation. "Waiting for me", "At a glance" and "Today" sit above the composer, with the rows and the order Core sends. This section fixes only how they are drawn and answered as text. The roadmap builds Waiting for me first. "At a glance" and "Today" appear when Core grants their capabilities, and until then the TUI shows them as unsupported (T-07).

- **Waiting for me.** The rows, the five-row limit and the "+N more" count are the contract's. A key switches the order to lightest first; the TUI asks Core for it and never ranks.
- **Answering** is one key. An approval takes the two steps of T-04. When several decisions wait, a key starts the one-at-a-time walk: the TUI shows one decision, sends the answer, and shows the next.
- **At a glance** is one line per running 목표 and 일, as text:

  `name  stage-word  [block-bar]  just finished  next  !`

  The stage word is Core's. A bar of block characters and the word always go together. `!` marks a line that waits on the person. A value that cannot be observed reads "unknown". An 일 of a 목표 is indented below it.
- A key opens the whole progress view: waiting rows first, then the lines, in one scrolling list with no tabs. A key on a line opens its Details in the pager, and a second key shows the running list limited to that line.
- The line "nothing to decide now" appears in the Waiting for me view only, never in a reply.
- A change to a list after an agent's action is a quiet badge. It never moves focus, never interrupts typing and never lands in the middle of a reply being written or read.

## Results, cards and the pager (T-13)

Contract: [surfaces.md](surfaces.md#result-surfaces), [host-protocol.md](host-protocol.md#typed-results), [materials-and-knowledge.md](materials-and-knowledge.md#saving-a-result).

A result card is the labeled block of the contract, drawn as a few lines with the key for Open and at most two other actions. While a background run makes the next revision the card shows the working word. Open puts the result in the side pane on a wide terminal (about 120 columns (P)) and otherwise full screen with a key to return.

The TUI declares the result types it draws in `hello`. A type it did not declare arrives without a body and shows as a text card made of the summary and `text`. The text form of each type is the TUI column of the contract's table. This design adds three points:

- The renderers live in `kit/result`, beside the code that derives `text` (C-52 in [go-core.md](go-core.md)). They take the width function of T-09 as an argument.
- A `table` is drawn by our own renderer, not Glamour, with horizontal scroll.
- A `file` export writes through `staging/export/` ([filesystem.md](filesystem.md#import-and-export)). Images appear only where the terminal answers the graphics-protocol query; otherwise the export action is offered.

Edit and save are requests. A direct edit of a `summary` or a `document_draft` sends `result_edit` with the base revision. `stale_revision` reloads the result and keeps the person's text in the edit view. A save names its destination and carries the key of the contract, so a repeated key never makes a second record. A saved result is edited as the saved material.

**The pager** shows Details, the activity log and long results: scroll, search, quit. Copy is the person's act in their own terminal: the pager offers a sanitized-text overlay to select with the terminal's own copy. A write by OSC 52 waits for the open point on the clipboard rule below.

## Approvals, questions and sign-in (T-04)

Contract: [main-authority.md](main-authority.md#approval), [integrations.md](integrations.md#questions-from-a-server), [integrations.md](integrations.md#consent-through-the-client).

The approval record is Core's. The TUI shows it and sends the person's decision as a request.

**The card** shows what the contract says an approval request shows, the requesting agent with its recommendation first, and its choices ([main-authority.md](main-authority.md#approval)). Doing nothing means the action will not run. A request that a channel cannot present waits here ([main-authority.md](main-authority.md#channels)).

- Target fields are never truncated. If they do not fit, a details view opens and confirm is disabled while any target field is cut.
- Requests of one scope are one card. A changed group returns stale; the card refreshes and the person selects again.
- A decision is two steps: select, then a separate confirm key. Keys are press-only. Esc closes the view and decides nothing. No free-text path answers an approval, and no `/approve` exists.
- Outcomes the TUI handles: resolved, already resolved elsewhere, withdrawn, expired (not performed), stale epoch or group, and refused. A pending approval is stopped by a Core restart ([runtime.md](runtime.md#recovery)), and the card says so.
- A replayed approval rings no second bell. Two clients racing one approval get exactly one `resolved`.

**Questions.** A form question renders its flat schema as prompts, and a URL question follows the contract, opening the URL only on an explicit key. Core refuses a form that asks for a secret, and the TUI shows the refusal. An unknown outcome of an effect is a question card with the dispositions of [main-authority.md](main-authority.md#command-outcomes). The TUI adds no option Core did not list.

**Sign-in.** The TUI is the client side of one relay for plugin and model-provider sign-in. It opens a loopback listener and reports its port, opens the system browser when it runs locally, and relays `code`, `state` and `iss`, or the `error` fields, exactly as received. Over SSH it takes a pasted URL instead. A secret the person types, such as an API key, is a masked field with no undo and no history, and travels once to Core.

## Agent surfaces and settings as text (T-14)

Contract: [surfaces.md](surfaces.md#agent-surfaces), [surfaces.md](surfaces.md#settings). Each view is data that Core already computes. The TUI draws it as lines with keys. The roadmap builds the views with the Core feature behind each, and a view whose capability was not granted shows as unsupported (T-07).

- **Board:** three groups of one-line rows (Waiting for me, Running, Done). Enter unfolds a row, with keys for the actions that apply.
- **Steering:** `/note` queues a note for the run's next step, which is the follow-up of the running list. Esc or `/stop` on a row stops it.
- **Agent tree and lanes (developer view):** indented lines (the persona as initial and name, sub-agents as plain indented text, workers as `[name]`), one line per worker for lanes, hand-offs as arrows, folded to one line by default.
- **Usage meter:** one line per connected account: plan, state, a block-character meter or "unknown".

Settings are commands and labeled blocks, all requests to Core:

- Accounts: the connected-accounts view with the one-line setup summary, add account, sign out.
- Plugins: the catalog, the connect card as a labeled block, the plugin list and the tools of each plugin.
- Command network: the standing grants, with revoke.
- Channels: connections, pairing code, bindings and scopes. A token passes to Core once and is never shown.
- Memory import: rehearsal report, run, promotion batches grouped, accept a whole group.
- Agents: list, create, tune, delete. The agent talk log: one line per exchange, Enter expands.
- Expression and reply length budgets, command network grants, the developer-view switch and power settings.
- Report a problem: the agent drafts it, the TUI shows the draft, and nothing is posted until the person confirms ([surfaces.md](surfaces.md#settings)).

## Notifications (T-15)

Modes are those of [surfaces.md](surfaces.md#settings): System, Terminal, Bell, Channel, Off. The TUI implements Terminal and Bell. System and Channel are Core's. The TUI never notifies about what it is showing; focus reporting tells it whether the terminal is focused. A notification is one short paragraph with no frame, heading or id, and it never carries an approve action. Nothing steals focus.

## Rendering, width and Korean (T-09)

A block-level incremental renderer sits behind `render.Stream` in `tui/render`, which imports no Charm package.

```go
// internal/tui/render
type Measurer interface{ Width(s string) int; Mode() WidthMode } // the one width function
type Painter interface{ Block(markdown string, cells int) []string } // sealed blocks only; may call Glamour
type Key struct{ Block string; Rev uint64; Cells int; Theme uint16; WidthMode uint8 }
type Stream interface {
	Append(delta string)
	Lines(cells int, win Window) []string // closed blocks cached, open tail plain, clipped to win
	Seal() []string                       // authoritative final render
}
```

- A closed top-level block (paragraph, closed fence, list, table, heading) is frozen into a stable prefix and cached on (block id, revision, width in cells, theme, width mode). The plain-text foundation closes paragraphs, lists and tables at blank lines, headings at newline and fenced blocks at a matching closing fence. These conservative boundaries keep incomplete markup plain.
- The open tail is plain text and wraps incrementally: complete lines freeze and each delta re-wraps the last partial line and its boundary row, because a late combining mark or ZWJ can extend the last cluster. Breaks fall at whitespace, else at a grapheme boundary, never inside a cluster. An open code fence belongs to the tail until it closes.
- A block that closes is painted once, off the UI goroutine. A stale result is dropped. The visible change from plain to styled at close is accepted: rendering the whole tail on every flush grows quadratically.
- `Seal()` at `item_done` replaces the stitched render once.
- Markdown blocks may use Glamour one block at a time with wrap width 0, and every line it returns is re-wrapped with the `Measurer`. Tables use our own renderer.
- One width function serves all layout: wcwidth by default, grapheme clusters once mode 2027 is confirmed, ambiguous East Asian width narrow, Hangul syllables two cells. The mode is part of the cache key. The Charm-free `Measurer` uses pinned Unicode 17 data and UAX #29 segmentation for wrapping and clipping in both modes. A cluster wider than the entire row is omitted with a typed clipped flag, never split. Cursor offsets inside a cluster clamp to its start.
- The hardware cursor is placed on every frame at the display width of the text before it on the wrapped row, so IME composition appears in place. The TUI never requests all-keys-as-escape-codes. Committed IME text arrives as UTF-8, one grapheme cluster at a time ([surfaces.md](surfaces.md#korean-input)).
- Only the viewport plus overscan (P) is laid out, so cost follows viewport rows and not history. `Stream` indexes new text as it arrives. `Window` takes a retained byte anchor within a block and stops reflow at the requested row count, including after a width change. `Renderer.Draw` reads and paints only those indexed rows, retains only the current viewport cache, and returns changed-row patches; an empty patch list causes no writes. A trusted UI painter styles only closed rows after sanitizing and clipping.
- Resize events coalesce to one reflow per 32 ms (P).
- An unchanged state writes zero bytes. Motion is one status glyph with no timer while deltas stream; elapsed time updates at 1 Hz (P) otherwise.
- OSC 8 links are emitted for allowed schemes only.

## Input and commands (T-10)

Keys are a proposal and data in `tui/ui`. No contract fixes them.

| Key | Action |
| --- | --- |
| Enter | Send the draft |
| Alt+Enter, Ctrl+J | Newline (Ctrl+J because Alt may not reach the application) |
| Esc | Close the top overlay; with none, interrupt the turn |
| Ctrl+C | Clear the draft; with an empty draft, quit the TUI only, never Core |
| Tab, Shift+Tab | Move focus |
| Ctrl+K | Command palette |
| Keys for Waiting for me, progress, running list and Open on a card | Open those views |

Key classes are Toggle, Edit and Approve. Every key event passes one `Admit` gate: releases are ignored, repeats are dropped for Toggle and Approve, and Edit keys may repeat. Without key events from the terminal every key counts as a press, and approval cards add a dead time after any change of card (P).

**Mid-turn input** is steering by default and applies at the next round boundary ([runtime.md](runtime.md#conversation-loop)). `/queue` makes the input a follow-up. `/stop` cancels.

**The composer** is the Bubbles `textarea`; `textinput` is not used.

- Paste arrives as one atomic message, is one undo step and has control sequences removed. The TUI never reads the host clipboard; paste is the terminal's.
- Attachments are a strip above the composer and message parts that Core imports ([filesystem.md](filesystem.md#import-and-export)). The draft stays plain text.
- `@` references are completed by Core under its scope. The TUI reads no device file to expand them.
- To-do capture takes the deterministic tokens of [work-and-delegation.md](work-and-delegation.md#the-to-do-record) (`#`, `%`, `!`, `{}`). Core parses the line and shows the chips before it saves. An ambiguous date is never saved silently.
- A failed send restores text and attachments merged with new typing.

**Slash commands** share one registry with channels: name, summary key, aliases, arguments, surfaces, busy policy and executor. Core owns it, so both surfaces list the same commands, and the TUI keeps a small client table only for commands whose executor is the client. The contracts define no capability for the registry and [go-core.md](go-core.md) does not design it, so how the listing travels is a schema matter of the client connection. A command never queues behind a running turn. Slash text never approves anything.

## Library choice, risks and spike gate (T-08)

**Chosen:** Bubble Tea v2 with Bubbles (`textarea` only), Lip Gloss and Glamour (sealed blocks only). Versions are pinned together, exactly, by the first implementation issue ([dependencies.md](../policy/dependencies.md)). The October 2026 candidates are Bubble Tea 2.1.0, Lip Gloss 2.0.6, Bubbles 2.2.1 and Glamour 2.0.1, with Ultraviolet at an exact revision. All are permissive licences.

The UI needs a cell-diffing renderer with synchronized output, the alternate screen, progressive keyboard events, bracketed paste as one message, correct Hangul width, an application-placed hardware cursor, OSC 8 links, and a way to run without a terminal in tests.

| Candidate | Licence | Verdict |
| --- | --- | --- |
| [Bubble Tea](https://github.com/charmbracelet/bubbletea) v2 with Bubbles, Lip Gloss, Glamour | MIT | Chosen. An Update and View loop fits a pure reducer, and `View` carries content, cursor, title and keyboard flags. |
| tview on tcell | MIT | Rejected. A retained widget tree owns state, so the reducer cannot be the one state. |
| gocui | BSD-3-Clause | Rejected. Pseudo-versions only. |
| tcell v3 alone | Apache-2.0 | Fallback F3. Alone it means writing the renderer, widths and input. |
| Ultraviolet directly | MIT | Fallback F2. The cell buffer, reader and renderer under Bubble Tea; its API may change. |
| termdash | Apache-2.0 | Rejected for the main UI: gauges and charts, no conversation or composer model. |

The reducer, render, layout and `clientapi` packages are Charm-free, so a fallback touches `term` and `ui` only.

**Known risks**, found by reading the module sources and not yet measured:

- Every row is repainted after a clear, and a row with a wide cell is rewritten in full, so a Hangul screen costs bytes on every changed frame.
- The `textinput` cursor ignores display width. Only `textarea` is used.
- A fixed 50 ms wait separates Esc from an escape sequence.
- Terminal probes are skipped over SSH and in Apple Terminal. LINA sends its own DECRQM probe with a deadline, so a late reply cannot leak to the shell.
- Tab stops are reset on start, paste buffering has no cap, and the flush ticker runs when idle.

**The spike gate** runs on real terminals: at least one on macOS, the default terminal of LINA OS, and one reached over SSH. The owner confirms the list. The bounds below are LINA's proposals (P), derived from perception limits (one 60 Hz frame, key echo, first paint). They are written into the spike issue before the run. A miss selects a fallback and never loosens a bound.

| Id | Question | Proposed bound (P) |
| --- | --- | --- |
| S1 | Hangul IME cursor and composition | Cursor column equals the expected width in composed, mid-line and wrapped cases; IME text appears in place |
| S2 | Bytes per status tick on a full Hangul screen at 140x45 | At most 2 KiB |
| S3 | Long streams: 100 KB prose; 300 KB with no blank line and with no newline; a 1,000-line code fence; a 1,200-row table; a 300-item list | Build and flush p50 at most 4 ms, p95 at most 16 ms; flat per-flush cost; peak heap at most 64 MiB; key-to-byte p95 at most 50 ms at 250 events per second |
| S4 | Resize with 200 history messages; tab-stop alignment | Reflow at most 100 ms after the coalesce; no misalignment |
| S5 | Own sync-output and grapheme probes, OSC 52 write, OSC 8 link, over SSH and Apple Terminal | Each resolves within 200 ms; no reply leaks to the shell; behavior recorded per terminal |
| S6 | Key events, press-only gate, Esc latency | Gate correct; Esc to action at most 80 ms |
| S7 | Paste whose end marker is lost | Later keys work within 1 s, or the gap is recorded with a mitigation |
| S8 | Recording-writer harness | Runs, quits and parses with the emulator without any sleep |
| S9 | Idle CPU and memory | At most 1 percent of one core; no bytes written; resident at most 80 MiB with a 200-item window |
| S10 | Korean tables and Glamour | A Hangul, Latin and emoji table aligns under both width modes; otherwise our own renderer draws all tables |
| S11 | Side-pane threshold | The width between 100 and 140 columns at which the pane stays usable; default 120 |

Each run writes an evidence record: terminal and version, `TERM`, SSH or not, library revisions, declared bounds, measured values, and the result per scenario. A record missing a field is not evidence.

Fallbacks, in order. F1: Bubble Tea with its own probe, a quiet spinner, a lower frame rate while streaming and a frame memo. F2: a row-string renderer on Ultraviolet with changed rows only, in synchronized output. F3: tcell v3. The reducer, render, layout and `clientapi` do not change in any fallback.

## Tests, tokens and theme (T-11)

**Tests.** The fake Core, `internal/fakecore`, is built from the conformance fixtures ([host-protocol.md](host-protocol.md#conformance-fixtures)) with an injected clock and fault switches: drop, slow reader, epoch change, duplicate delivery, disconnect mid-batch, recovering and refusal.

Each script step optionally consumes a request and names fixture envelopes to
emit. Faults are per step, except an epoch change persists through later steps
and updates the welcome payload too. Drop suppresses that step's output; slow
reader waits on the injected clock before consuming the request. Duplicate sends
each envelope twice with one logical id and distinct attempt ids. Disconnect
closes after the specified number of delivered frames in that step, including
duplicates (zero closes before output). Refusal replaces the response with a
typed `refused` error. Recovering supplies `unavailable` for mutations while
handshakes and read-only queries, including history, retain their scripted
responses. Clearing it on the next step permits that response again. Explicit
refusal takes precedence over recovering; drop precedes duplicate delivery.

`e2e/m1/fakecore.json` runs through the headless harness of #34: a protocol client
observes every fault, including recovery followed by another response, and the
built TUI connects, quits and rejects an unsupported combination through a real
PTY. The fake owns each accepted stream, bounds request frames to 1 MiB, and
closes on completion, cancellation or failure. The harness owns its listener and
joins the peer. Clock gates and I/O observations, never sleep intervals, establish
ordering. This scenario covers the current connection screen and wire behavior;
rendering and application effects still require their own tests.
`e2e/m1/tui-state.json` sends duplicate welcomes, reserved conversation frames
and an epoch change over that harness's socket into `state.Reduce`. Package
tests cover projected snapshots, replay, resync, revocation, local echoes,
generation checks, draft recovery, clock offsets and split sanitizer input.
These tests establish the pure reducer and existing receive boundary, without
claiming conversation UI or an unimplemented wire payload.

The schema and receive-boundary corpus runs independently through
`kit/protocol/conformance`. It checks that an unknown kind yields no admitted
message, increments the client's unknown-kind count once per frame, and does not
prevent the next valid frame from being received. Malformed envelopes increment
a separate count. The server adapter checks typed `malformed` refusals for unknown
request fields. These checks establish the boundary behavior used by T-06; they
do not establish rendering, live Core behavior or the end-to-end scenarios that
depend on the headless harness.

| Layer | Runs | Asserts |
| --- | --- | --- |
| Reducer | Fixtures replayed through `Reduce` | Unique block ids; duplicates change nothing; a resumed state equals a run that never disconnected |
| Layout | `layout.Plan` over all sizes | Regions disjoint; floor holds; counts conserved |
| View to cells | `View` drawn into a cell buffer in test code | Cell contents, cursor position, widths |
| End to end | The program with a recording writer and an emulator | Zero bytes on an unchanged state; bounds on writes; final cells |
| PTY | The built binary against the fake Core | Start, connect, refuse, recover, quit |

No test sleeps. Assertions check ids, codes, counts and bytes, never prose or rendered strings, and no golden file pins renderer bytes ([AGENTS.md](../../AGENTS.md)). Wall-clock bounds are measured in the spike and in benchmark runs, and recorded as evidence. The PTY layer does not replace S1 to S11.

Invariants that need a test:

- Two fake clients racing one approval give exactly one `resolved`.
- A group changed after display returns stale.
- A replayed approval rings no second bell.
- After forget and reconnect, the forgotten item is absent.
- No line of the default view carries an id-shaped field, checked on the typed view model.
- `sanitize.Display` is fuzzed with control, bidirectional and OSC input.
- A width corpus (Hangul, mixed ASCII, emoji sequences, combining marks) guards cell widths and cursor columns without pinning rendered prose. The Unicode 17 grapheme conformance corpus guards segmentation; chunked-stream fuzzing compares arbitrary splits with complete input. Renderer tests count measurements and paint calls across large histories, verify stable prefix caches and changed-row application, and establish zero patches on unchanged state. Real terminal writes, IME behavior, resize scheduling and the T-08 spike remain the consuming UI's acceptance work.

**Tokens and theme.** `internal/design` is generated from the DTCG files in `design/tokens/` ([surfaces.md](surfaces.md#one-design-language)). `node scripts/tokens.mjs` writes Go values, CSS, the SVG mark and the token tables in `DESIGN.md`. Its `--check` mode regenerates in memory and fails on drift without changing the checkout. Foundation runs it for TUI/design component changes as well as token or automation changes ([foundation.md](../policy/foundation.md)).

- Raw presentation colours, ANSI colour numbers and glyph literals are banned in production `internal/tui` source. The token checker scans literals and colour constructors; comments, test fixtures and numeric Unicode measurement data are not presentation values. `tui/ui` turns generated semantic colours, spacing and glyphs into Lip Gloss styles.
- Contrast checks require at least 4.5:1 for text, muted text and accent on canvas and surface, in light, dark and high contrast. The source also specifies tested 256-colour and 16-colour mappings. Dark ANSI16 uses bright white for the accent where standard blue cannot pass; user-redefined ANSI palettes can change physical contrast. Hairlines are decorative, never the only state indicator.
- No state is shown by colour alone: each has a mark and a word. Glyph sets are tokens, in plain Unicode and ASCII, and none needs a private-use glyph.
- Connection-screen display overrides are `LINA_THEME=light|dark|high-contrast` and `LINA_GLYPHS=ascii`. Otherwise `COLORFGBG` selects a light or dark background (dark by default); terminal capability selects RGB, 256 or 16 colours. Nonempty `NO_COLOR` preserves the layout, marks, labels and actions without escapes; subsequent states append compact lines. These overrides persist no preferences.
- On LINA OS the complete TUI follows the active Omarchy theme file ([surfaces.md](surfaces.md#platform-adaptation)); theme-file discovery remains part of platform preferences work.
- Every code in the closed lists has an entry in both the Korean and English catalogs, checked by id and never by wording.

## Earlier decisions and open points

| Earlier id | Status |
| --- | --- |
| T-01 | Changed. Preferences moved to one client file; the client report request is withdrawn. |
| T-02 | Withdrawn. [host-protocol.md](host-protocol.md) decides envelope, handshake and versions. |
| T-03 | Changed. The server side moved to C-42. |
| T-04 | Changed. The approval rule of [main-authority.md](main-authority.md#approval) replaces the four effect classes. |
| T-05 | Changed. Unread counts and presence are withdrawn; they are not client capabilities. |
| T-06 | Kept. |
| T-07 | Changed. The work-panel phases are replaced by the surfaces contract. |
| T-08 | Kept. The bounds are LINA's own. |
| T-09 | Changed. |
| T-10 | Changed. Redirect is withdrawn in favour of steering and follow-up. |
| T-11 | Kept. |

Withdrawn without an id: the hand-off pane, which the agent talk log replaces, and the work-panel worker mirror, which no contract defines.

Open points, each decided by its owner or by measurement: the side-pane width (S11); Korean table rendering (S10); the terminal list of the spike gate; image paste over SSH; how the shared list of slash commands travels (a schema matter); whether a copy by OSC 52 on an explicit key fits the clipboard rule of [surfaces.md](surfaces.md#launcher) and [non-competition.md](non-competition.md), which S5 measures for support; mouse support; locales beyond Korean and English; and non-competition evidence for the TUI on a device-OS install ([non-competition.md](non-competition.md)).
