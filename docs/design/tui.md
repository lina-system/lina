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

`lina tui --socket <path>` is a separate thin-client process. `internal/clientapi`
owns the Unix socket, the generated client 1.0 handshake and the validated
receive/subscription API. The TUI offers the conversation and
status-stream capabilities it renders (`conversation`, `running`, `progress`,
`waiting`); Core grants only installed capabilities. With conversation granted,
it discovers the default agent's main conversation through `history_get`, then
holds `core`, `conversation:<id>` and `results:<id>` plus granted status topics.
Requests, responses and notifications share `clientapi`'s validated correlation
and receive path. The command supplies the product version for diagnosis,
persists no drafts, and never starts Core.

The connection surface uses Lip Gloss and the generated tokens. Usable terminals
show localized states and retained stream content; unset/dumb TERM and pipes keep
the plain `live` status. Terminal input uses raw mode and one cancellable decoded
key reader from negotiation through shutdown. Cleanup joins the reader and restores
terminal settings on every return, including SIGTERM, SIGINT and SIGHUP. There is
no animation timer. The shell model also runs through the Bubble Tea recording
harness; final library acceptance remains the T-08 spike gate.

- It keeps its preferences (theme, glyph set, width settings, notification mode) in one client file, mode 0600, at the location the install fixes. A missing or bad file never blocks start.
- It never opens canon, never runs `lina core` and never holds the lock. Every message it sends is a request that Core accepts or refuses, and when the two disagree Core wins and the TUI refreshes.
- It uses only the client connection. Its types are the Go types generated from the schema ([host-protocol.md](host-protocol.md#schema-and-generated-types)).
- Protocol errors and Core logs go to the status line and a log, never into the conversation ([host-protocol.md](host-protocol.md#client-connection)). `TEA_TRACE` and `TEA_DEBUG` stay unset, and panic output is masked.
- Import rule: `internal/tui/...` imports only `internal/clientapi`, `internal/design`, `kit/protocol` and other `internal/tui` packages. Charm packages are imported only in `tui/term` and `tui/ui`. The Markdown UI adapter may additionally import the already pinned `github.com/yuin/goldmark`, `/ast`, `/text` and `/util` packages; this exception does not extend to `render`, `state`, `layout`, `term` or other Goldmark packages. The one architecture `go test` of [go-core.md](go-core.md) (C-40) checks it.
- Trust: the socket's user check and the rule for who may decide an approval are Core's ([go-core.md](go-core.md), C-42). The TUI shows a refusal as a refusal and adds no rule of its own.
- Parity with the LINA app comes from the shared conformance fixtures ([host-protocol.md](host-protocol.md#conformance-fixtures)). The app is not designed here.

## Connection states (T-05)

`lina tui` never starts a main. The connection is in one of eight states. Each is a screen state, never an empty conversation and never a conversation item.

A validated welcome enters Live. Initial absence shows the Core-absent screen
and exits 69; initial `unsupported_combination` shows Refused and exits 78. Bad
arguments exit 64; malformed handshakes, invalid session frames and other terminal
failures exit 70. Connection and handshake share a five-second timeout. The
refusal screen shows the client's supported protocol (1.0); the Core's supported
range is unknown because the current refusal payload carries no range. Peer
`detail` is never parsed or printed as UI.

After a live connection is lost, the same process enters Retrying with attempt
and delay, retains its window read-only, and opens a new connection. It never
replays application requests. A new epoch produces one local notice; a different
main id or older epoch fails closed. Typed `resync`/`resync_required` and detected
gaps enter Resyncing until the required snapshot arrives. An `unavailable`
subscription response shows Recovering with that cause while history remains
readable; this is unavailable-service presentation, not an inference from peer
text or a claim that reads prove Core mutation readiness. Resync and its fresh
snapshot restore the subscribed view in Core's history order. A durable stopped
outcome stays with its interrupted turn before later inputs and replies, including
after reconnect and terminal resize.

Quit (Ctrl+C with an empty draft or Ctrl+D), input EOF and termination signals close only this
client and exit 0, including during handshake or retry. The raw terminal reader
ignores releases, toggle repeats and pasted quit text. Nonterminal input keeps
the handshake-then-read behavior. No failure path starts Core or creates state.

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

The conversation status row shows the latest local notice before its key hints,
using the same localized notice as the connection shell. It stays outside
conversation items: snapshot replay and redraw do not duplicate it, resync does
not drop it, and a fresh client does not restore it from history. History and
the unsent draft remain usable after reconnect. `e2e/m1/tui-epoch-notice.json`
checks successive graceful and forced restarts of an installed Core through a
120x60 PTY with a loopback Responses fixture.

Connection presentation carries a typed cause separately from diagnostic text.
Refused and Core-absent screens show that closed cause code, its localized label,
and the next action. Unknown causes use the catalog's unknown entry. Retry
presentation takes the attempt number and remaining delay from its connection
owner. A change to either metadata or state refreshes the display; an identical
status writes nothing. The key gate also covers the legacy quit input. Connection-only screens keep
`q` as quit; a conversation composer treats it as ordinary text. A reconnect,
including one in the same epoch, restores an unacknowledged input into the draft
with newer typing after it. The client never automatically resends that input.

Reconnect also retains the selected agent identity. History discovery and the
preserved draft use that agent, regardless of the welcome's agent order. A fresh
agent list validates the selection because welcome may name only the default.
Without the separately negotiated `agents` capability, discovery requests
history by the retained agent id instead. An `unavailable` discovery response
keeps the focus and draft in recovery and reopens the connection to retry these
read-only requests; it does not infer deletion or replay conversation input.
If the selected agent is absent, the TUI drops its unavailable conversation
subscriptions, shows a local notice, and keeps the draft unsent. It does not
select a fallback; sending waits for an explicit `/agents choose <id>`.
`e2e/m1/tui-agent-reconnect.json` checks graceful and forced installed-Core
restarts, the provider request's persona identity and history, and a removed
selection with a retained draft.

Decision overlays retain their readable content while reconnecting or recovering.
A lost connection clears the pending client request and selected approval choice;
it never replays an answer. A new epoch stops pending approval cards. Sending
requires Live again and a fresh selection and confirmation. A correlated refusal
changes the card, without interpreting diagnostic text as a decision.

## Streams on the client (T-03)

The server side is [go-core.md](go-core.md) (C-42): topics, cursors, rings, batching and heartbeat. This section is the client side.

- The TUI holds `core`, `waiting`, `progress` and `running` for the whole connection, the focused conversation with its results, and further conversations as background topics up to the limit Core grants. It opens `exchanges`, `accounts` and `channels` only while their view is open.
- The socket reader feeds a bounded channel that the reducer drains. If it fills, the topic is marked out of sync and a resync is requested. The reader never drops a frame. A slow terminal delays pixels, not state.
- Epochs compare numerically, the form of CP-02 ([host-protocol.md](host-protocol.md#envelope)), without a machine-integer limit. A gap or a newer epoch requests one resync per out-of-sync topic. Duplicate cursors and revisions and older epochs are ignored; a repeated delivery never changes the display. A resync snapshot cannot roll back the accepted cursor within the same epoch and revocation epoch.
- A different revocation epoch ([conversation-and-memory.md](conversation-and-memory.md#terms)) makes the TUI drop cached items before the snapshot applies, so a forgotten item cannot survive a reconnect.
- Replaying a stream or a snapshot twice leaves state unchanged.
- Completed conversation history preserves the live projection's message order
  and content after Core restart. A reply keeps its first streamed position when
  later queue or steering input arrives; a reply with no visible delta first
  appears at completion. Restoration neither groups all replies ahead of later
  input nor invents or drops turns.

The client 1.0 wire carries these mechanics inside payloads as defined by [C-42](go-core.md#client-service): optional per-event `stream` metadata and the `snapshot`, `resync` and `resync_required` kinds. `e2e/m1/topic-streams.json` crosses the production client socket and generated codec, projects decoded frames into the reducer, and checks duplicate snapshots/replay plus independent topic overflow. It proves the protocol and reducer boundary. The connection shell now automatically subscribes to its granted status topics and projects notifications into the interactive view, covered by `e2e/m1/tui-stream.json`; concrete conversation and result subscriptions still require the conversation view's discovery and focus state. The T-04 rule that a replayed approval rings no second bell also relies on this cursor/revision deduplication.

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
6. An interrupt shows "stopping" and converges on the stored partial. Core seals the partial under the streamed block id together with its durable stopped outcome; reopening the TUI or restarting Core restores both without resending the model request.
7. Selection, folds and scroll key on block id and survive a resync.
8. Local notices live outside the items, so a resync never drops them.
9. The client acts on typed fields only. It never parses model, tool, plugin or web text for commands, ids, links, approvals or status.
10. Every string that did not originate in the client passes `sanitize.Display` before layout: control, bidirectional and OSC sequences are removed, and styling follows truncation.
11. A frame that fails schema validation shows a placeholder with a code and is counted.

An accepted snapshot is authoritative for its topic's block order as well as
content. Missing predecessors are inserted before surviving blocks, including a
stopped outcome restored under its ledger id. Existing blocks and local echoes
anchor the merge without overriding that order; other topics, unmatched local
echoes and local notices survive. Selection, folds and scroll retain their block
identities, and an acknowledged echo remaps those identities to its ledger item.
`e2e/m1/tui-stopped-order.json` checks an interrupted turn followed by a successful
reply through an installed Core restart, PTY resize and fresh TUI, and compares
the display order with Core's snapshot without replaying model requests.

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

The screen is a pure function of State and terminal size. Core's validated welcome determines which regions are available ([host-protocol.md](host-protocol.md#versions-and-capabilities)). Running and waiting lists take no rows when empty or not granted. Missing list capabilities produce one localized, plain-language local notice outside conversation history, combining both missing lists into one explanation. Replayed welcomes and reconnects with the same capabilities do not add another notice; a later grant removes the obsolete explanation from the status line. No fallback string or peer diagnostic decides capability availability.

Regions:

- Header: the focused agent's name and mark, and the connection word.
- Conversation.
- Composer, with its attachment strip.
- Card row above the composer, on Home (T-12).
- Status line: connection, agent, the running count ("2 running, 1 waiting for you"), restart pending, and Core messages.

Overlays form a stack and the top one receives keys first: agent list, command palette, pager. The agent list is the agents screen of [surfaces.md](surfaces.md#agents) in text: mark, name, source and last conversation; Enter opens the agent's main conversation.

The decision overlay occupies the conversation surface and keeps the connection
status row. Its targets wrap at cell boundaries and can be read with Up/Down or
Page Up/Page Down. Confirmation stays disabled unless the entire target area
fits. A small terminal retains the size notice and status instead of an actionable
card; enlarging it recomputes visibility.

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
  At usable sizes a granted, populated running strip starts at one to three rows
  and a granted, populated card row at one to five; each keeps one summary row
  when folded. Empty or ungranted lists reserve no rows, even under pressure.
  The attachment strip
  uses one row. After the stated shedding, composer growth yields as necessary
  to preserve the transcript floor. Counts and unsupported flags remain typed
  output even when their region folds.
- Property tests enumerate widths 0–240 and heights 0–100 with empty and crowded
  shapes and granted and missing capabilities, and exercise threshold and
  integer-limit dimensions. The planner itself imposes no maximum size.

The conversation increment uses the planner in the actual command program.
Usable colour terminals use Bubble Tea's cell renderer, alternate screen and
hardware cursor; the single cancellable input decoder supplies key, paste and
resize messages. Monochrome terminals retain the plain display path. Page Up
and Page Down move through the retained conversation. Tab switches composer and
card focus; arrows select a result card. The composer keeps its draft when an
overlay opens or connection status changes. An identical frame and cursor is
not submitted to the renderer, and the plain display writes no duplicate frame.
For the colour conversation screen, each new size reaches the cell renderer
before its reflowed view, even if the view text is unchanged. The connection owner's latest dimensions remain authoritative: late renderer startup size messages are discarded before they can resize the cell buffer. Resizing keeps the
draft and redraws header, status and composer within the current row budget;
cells from the previous dimensions must not survive as stale transcript rows.
The terminal output boundary restores full-height scrolling margins after each
write while preserving the hardware cursor. A frame queued at a smaller size
may arrive after the physical terminal has grown; its cached height must not
leave later frames scrolling inside the old region. The real-model recording
regression holds this ordering across 40x12 → 120x40, starts a streamed reply,
and compares every cell row and cursor with a fresh client during streaming,
after interruption and after a same-epoch reconnect. Writes and cursor updates
establish settlement without sleeps or assertions on UI wording.
Only dimensions forwarded by the owning input decoder may resize the renderer.
The renderer filters the library's asynchronous initial size notification so a
late startup value cannot undo a newer size and clip the composer or status.
The startup-order regression delivers that stale notification explicitly; it
does not rely on scheduler timing or sleeps.

`e2e/m1/tui-resize.json` exercises 80x24 → 40x12 → 120x35 through a real PTY,
compares every row and the cursor with a fresh client at the same size, then
clears the retained draft and completes another exchange.

## Home, waiting for me and progress (T-12)

Contract: [surfaces.md](surfaces.md#waiting-for-me), [surfaces.md](surfaces.md#progress-at-a-glance), [work-and-delegation.md](work-and-delegation.md#progress-line).

Home is the starting state of the conversation. "Waiting for me", "At a glance" and "Today" sit above the composer, with the rows and the order Core sends. This section fixes only how they are drawn and answered as text. The roadmap builds Waiting for me first. "At a glance" and "Today" appear when Core grants their capabilities. The running and waiting lists follow the empty and unavailable presentation of T-07.

- **Waiting for me.** The rows, the five-row limit and the "+N more" count are the contract's. A key switches the order to lightest first; the TUI asks Core for it and never ranks.
- **Answering** is one key. An approval takes the two steps of T-04. When several decisions wait, a key starts the one-at-a-time walk: the TUI shows one decision, sends the answer, and shows the next.
- **At a glance** is one line per running 목표 and 일, as text:

  `name  stage-word  [block-bar]  just finished  next  !`

  The stage word is Core's. A bar of block characters and the word always go together. `!` marks a line that waits on the person. A value that cannot be observed reads "unknown". An 일 of a 목표 is indented below it.
- A key opens the whole progress view: waiting rows first, then the lines, in one scrolling list with no tabs. A key on a line opens its Details in the pager, and a second key shows the running list limited to that line.
- The line "nothing to decide now" appears in the Waiting for me view only, never in a reply.
- A change to a list after an agent's action is a quiet badge. It never moves focus, never interrupts typing and never lands in the middle of a reply being written or read.

The conversation screen projects granted waiting/running topic rows in Core
order and keeps their counts when folded. Ctrl+W and Ctrl+R open their complete
retained text in the pager; Ctrl+P opens the granted progress topic. Missing
capabilities use the single local explanation of T-07 and leave no placeholder
rows. Granted empty lists are hidden without an unavailable notice. Rows appear
when Core sends items and disappear when its snapshot clears the list.
List updates do not focus the pager or
change the draft. Decision answering remains the typed approval/question flow
of T-04; the composer never turns a list row into an approval.

The waiting summary shows the number of unresolved decision cards and the first
card in Core's publication order. F6 opens that card without changing the draft.
New cards update the summary and conversation label without taking keyboard focus.

## Results, cards and the pager (T-13)

Contract: [surfaces.md](surfaces.md#result-surfaces), [host-protocol.md](host-protocol.md#typed-results), [materials-and-knowledge.md](materials-and-knowledge.md#saving-a-result).

A result card is the labeled block of the contract, drawn as a few lines with the key for Open and at most two other actions. While a background run makes the next revision the card shows the working word. Open puts the result in the side pane on a wide terminal (about 120 columns (P)) and otherwise full screen with a key to return.

The TUI declares the result types it draws in `hello`. A type it did not declare arrives without a body and shows as a text card made of the summary and `text`. The text form of each type is the TUI column of the contract's table. This design adds three points:

- The renderers live in `kit/result`, beside the code that derives `text` (C-52 in [go-core.md](go-core.md)). They take the width function of T-09 as an argument.
- A `table` is drawn by our own renderer, not Glamour, with horizontal scroll.
- A `file` export writes through `staging/export/` ([filesystem.md](filesystem.md#import-and-export)). Images appear only where the terminal answers the graphics-protocol query; otherwise the export action is offered.

Edit and save are requests. A direct edit of a `summary` or a `document_draft` sends `result_edit` with the base revision. `stale_revision` reloads the result and keeps the person's text in the edit view. A save names its destination and carries the key of the contract, so a repeated key never makes a second record. A saved result is edited as the saved material.

An approval or form question keeps a short labeled line in the conversation.
Its expanded view shows the requesting agent, recommendation when supplied,
summary, targets and choices. Opaque card references and revision tokens are
routing data and never appear in that line or the waiting summary.

**The pager** shows Details, the activity log and long results: scroll, search, quit. Copy is the person's act in their own terminal: the pager offers a sanitized-text overlay to select with the terminal's own copy. A write by OSC 52 waits for the open point on the clipboard rule below.

Text result cards retain the summary in the conversation and the sanitized
`text` fallback separately for the pager. Ctrl+O opens the selected card (the
latest by default); on wide terminals it occupies the side pane and otherwise
the full screen. The pager supports arrows, Page Up/Down, Home/End, `/` search,
`n` for the next match, and Esc or `q` to return. Its text can be selected with
the terminal's own copy. No host clipboard read or write is installed by the
composer or pager.

## Approvals, questions and sign-in (T-04)

Contract: [main-authority.md](main-authority.md#approval), [integrations.md](integrations.md#questions-from-a-server), [integrations.md](integrations.md#consent-through-the-client).

The approval record is Core's. The TUI shows it and sends the person's decision as a request.

**The card** shows what the contract says an approval request shows, the requesting agent with its recommendation first, and its choices ([main-authority.md](main-authority.md#approval)). Doing nothing means the action will not run. A request that a channel cannot present waits here ([main-authority.md](main-authority.md#channels)).

- Target fields are never truncated. If they do not fit, a details view opens and confirm is disabled while any target field is cut.
- Requests of one scope are one card. A changed group returns stale; the card refreshes and the person selects again.
- A decision is two steps: select, then a separate confirm key. Keys are press-only. Esc closes the view and decides nothing. No free-text path answers an approval, and no `/approve` exists.
- Outcomes the TUI handles: resolved, already resolved elsewhere, withdrawn, expired (not performed), stale epoch or group, and refused. A pending approval is stopped by a Core restart ([runtime.md](runtime.md#recovery)), and the card says so.
- A replayed approval rings no second bell. Two clients racing one approval get exactly one `resolved`.

Conversation input, `/queue` and `/stop` use only input and interrupt requests.
They never dispatch `approval_answer`, `question_answer` or `waiting_answer`.
Opening a text card or closing its pager decides nothing; approval confirmation
continues to require its separate typed card controls.

Channel credentials use the same transient masked-field rule. `/channels`
never places the bot token in a conversation draft, undo entry, echo or history.
Closing the form clears its input; reconnect clears both the input and pairing
code. Responses are matched to their connection generation and view attempt,
so a late reply cannot reopen a closed control view.

The optional `card` field on `approval_request` and `question` carries a stable
reference, an authoritative displayed revision and the presentation fields. The
answer carries that reference and revision independently of the JSON-RPC request
id. Existing payloads remain valid; a legacy payload without a complete card
reference offers no confirmation. Correlated answer responses carry typed
outcomes. Stream cursor/revision replay and repeated publication of the same
approval do not create another notification.

The listener registers decision handlers through the existing operation map.
Approval decisions use Core's authority transaction and authenticated connection
facts; question forms use the owning question source and cannot grant an approval.
Installed Core registers approval operations against its already-open authority
instance and advertises `waiting` alongside its other capabilities. Subscription
admission refreshes that projection from canon; identical records do not advance
the stream. The listener tracks eligible owner connections that negotiated waiting. The
same authority consults that presence before creating a new approval; after a
pending approval or decision commits, its callback publishes to existing
subscriptions outside the canon transaction. Committed publication has its own
five-second context, detached from the requesting client's cancellation. A failed
projection invalidates that topic with `resync_required`, so existing subscribers
refresh through the normal admission path instead of silently keeping a stale
live view. A successful decision publishes the terminal card. No second authority
instance or decision store is created. Question operations are installed only when
the owning question source is supplied.

There is no client decision store. Group membership and its displayed arguments
must be revalidated in the same authority transaction as the decisions, so a
changed group cannot partially approve.

**Questions.** A form question renders its flat schema as prompts, and a URL question follows the contract, opening the URL only on an explicit key. Core refuses a form that asks for a secret, and the TUI shows the refusal. An unknown outcome of an effect is a question card with the dispositions of [main-authority.md](main-authority.md#command-outcomes). The TUI adds no option Core did not list.

**Sign-in.** The TUI is the client side of one relay for plugin and model-provider sign-in. It opens a loopback listener and reports its port, opens the system browser when it runs locally, and relays `code`, `state` and `iss`, or the `error` fields, exactly as received. Over SSH it takes a pasted URL instead. A secret the person types, such as an API key, is a masked field with no undo and no history, and travels once to Core.

The TUI negotiates `accounts` and `sign_in_relay` independently of conversation.
Account listing and sign-out remain available when the relay is not granted;
login and reauthorization then show the unsupported capability.
`/signin` starts a ChatGPT login; `/accounts` opens its labeled account block,
where Up/Down selects a metadata row, `s` reauthorizes it (or logs in when the
list is empty), `a` adds a provider account without changing the owner binding,
and `x` signs out the selected account. List and completion refresh the displayed
account status; ids remain internal request targets. An account-only connection retains a composer and displays the missing
account cause with `/signin`; entering conversation text makes no model request.
The conversation draft survives opening and closing the account block.
An automatic account-list refusal at connection startup retains focus and the
unsent draft. Its typed error stays in account display state; it does not open
the account block over the conversation. A refusal in a block the person opened
keeps that block visible.

The client reports an ephemeral IPv4 loopback port for `/auth/callback`, or
`cannot_listen` over SSH or when binding fails. It opens only an HTTPS
authorization URL, with a single URL argument to the system browser launcher.
The full URL remains available when opening fails. A pasted callback is a masked,
bounded, transient field: it has no conversation echo, draft undo or history.
Both callback paths forward the original URL so dynamic client-registration
parameters reach Core unchanged. Core alone checks state, issuer and identity.
The listener accepts one callback; submit cannot repeat an exchange. Completion,
refusal, cancellation, timeout, disconnect and quit close the listener and join
its tasks. Esc cancels a pending attempt by closing its client connection; normal
connection recovery then obtains a fresh connection. Old connection and attempt
results cannot advance a replacement sign-in.

`e2e/m1/tui-accounts.json` drives the built TUI in a PTY through local loopback
and SSH paste, using the real Core account/relay services and a synthetic signed
issuer. It verifies durable account status and owner binding. This is synthetic
sign-in evidence; the owner's real account and request-surface check remain #81.
`e2e/m1/tui-accounts-installed.json` additionally installs the real binaries,
checks the unconfigured Core's preparation message in a PTY, and runs the same
installed Core composition with the synthetic issuer through sign-in, list and
sign-out. Captured client traffic, terminal output and state outside the secret
store contain no provider tokens. Model routing and live provider acceptance
remain their own contracts.

## Agent surfaces and settings as text (T-14)

Contract: [surfaces.md](surfaces.md#agent-surfaces), [surfaces.md](surfaces.md#settings). Each view is data that Core already computes. The TUI draws it as lines with keys. The roadmap builds the views with the Core feature behind each, and a view whose capability was not granted shows as unsupported (T-07).

- **Board:** three groups of one-line rows (Waiting for me, Running, Done). Enter unfolds a row, with keys for the actions that apply.
- **Steering:** `/note` queues a note for the run's next step, which is the follow-up of the running list. Esc or `/stop` on a row stops it.
- **Agent tree and lanes (developer view):** indented lines (the persona as initial and name, sub-agents as plain indented text, workers as `[name]`), one line per worker for lanes, hand-offs as arrows, folded to one line by default.
- **Usage meter:** one line per connected account: plan, state, a block-character meter or "unknown".

Settings are commands and labeled blocks, all requests to Core:

- Accounts: `/accounts` lists the connected accounts as metadata rows with the one-line summary, add account and sign out. Labels, optional plan names and states come from Core; only the request carries the selected account id. `/signin` opens ChatGPT login even before conversation is available.
- Plugins: the catalog, the connect card as a labeled block, the plugin list and the tools of each plugin.
- Command network: the standing grants, with revoke.
- Channels: `/channels` lists connections and their bindings. `c` opens the masked Telegram token form; `p` requests a single-use pairing code to send to the bot in the platform's private chat; `r` refreshes Core state. `b` selects chat, optional topic, agent and scope before Enter submits the binding. An empty chat uses the paired private chat. Personal memory defaults on only for that private destination; another chat requires explicit opt-in. `u` selects a binding to revoke; `x` disconnects the connection. A token passes to Core once and is never shown.
- Memory import: rehearsal report, run, promotion batches grouped, accept a whole group.
- Memory and agents: [memory and agent commands](#memory-and-agent-command-requests) provide explicit remember, correct, forget, list, create, choose, rename and delete. Tuning remains its own view. The agent talk log: one line per exchange, Enter expands.
- Agent talk: `/talk` opens the agent talk log: one line per exchange, keyed by exchange identity. Up/Down selects and Enter requests that exchange's ledger expansion; When the expansion has more content, Enter requests the next page; Esc returns to the list. `r` requests a fresh list without duplicating rows or losing an existing selected identity. The view shows typed mailbox state, outcome and needs-person state; it derives none of these from text.
- Expression and reply length budgets, command network grants, the developer-view switch and power settings.
- Report a problem: the agent drafts it, the TUI shows the draft, and nothing is posted until the person confirms ([surfaces.md](surfaces.md#settings)).

Agent choice is client focus and survives reconnect (T-05). A missing selection
requires an explicit new choice before conversation input can be sent; listing,
creating or deleting another agent does not redirect the retained draft.

`/help` opens the local command reference, including `/channels` and `/talk`.
These commands are consumed locally even when Core has not granted their
capabilities; they then show unsupported and never become model input.
The TUI owns only transient display state. The `channels` and `exchanges`
capabilities are granted only when their Core services are registered. The
installed-Core acceptance scenarios are `e2e/m1/tui-channels.json` and
`e2e/m1/tui-talk-log.json`; unit projection tests alone do not establish their
pairing, durable binding or real exchange-ledger acceptance.


## Notifications (T-15)

Modes are those of [surfaces.md](surfaces.md#settings): System, Terminal, Bell, Channel, Off. The TUI implements Terminal and Bell. System and Channel are Core's. The TUI never notifies about what it is showing; focus reporting tells it whether the terminal is focused. A notification is one short paragraph with no frame, heading or id, and it never carries an approve action. Nothing steals focus.

Conversation, result and Home updates are quiet display changes. The
conversation increment does not advertise notification delivery and emits no
terminal bell or external notification. Duplicate stream cursors cannot append
a second item or move focus. Notification modes remain capability-gated work.

The card renderer offers ephemeral Terminal and Bell overrides through
`LINA_NOTIFICATIONS=terminal|bell`; without an override it emits no notification.
Focus events suppress notifications while the person is looking. A notification
contains sanitized agent/summary text only, without an answer action, and it
never changes the selected card. This override is not a persisted agent setting.

## Rendering, width and Korean (T-09)

A block-level incremental renderer sits behind `render.Stream` in `tui/render`, which imports no Charm package.

```go
// internal/tui/render
type Measurer interface{ Width(s string) int; Mode() WidthMode } // the one width function
type Painter interface{ Block(markdown string, cells int) []string } // sealed blocks only; may call Glamour
type Key struct{ Block string; Rev uint64; Cells int; Theme uint16; WidthMode uint8 }
type Stream interface {
	Append(delta string)
	Lines(cells int, win Window) []string // closed blocks cached, open tail marker-free, clipped to win
	Seal() []string                       // authoritative final render
}
```

- A closed top-level block (paragraph, closed fence, list, table, heading) is frozen into a stable prefix and cached on (block id, revision, width in cells, theme, width mode). The plain-text foundation closes paragraphs, lists and tables at blank lines, headings at newline and fenced blocks at a matching closing fence. These conservative boundaries keep incomplete markup unstyled.
- The open tail is an unstyled, marker-free preview and wraps incrementally: complete lines freeze and each delta re-wraps the last partial line and its boundary row, because a late combining mark or ZWJ can extend the last cluster. Breaks fall at whitespace, else at a grapheme boundary, never inside a cluster. An open code fence belongs to the tail until it closes. Pending emphasis, inline-code, link and list delimiters and fence headers are withheld across delta boundaries; their text remains readable. Literal markup inside code is preserved.
- A block that closes is painted once, off the UI goroutine. A stale result is dropped. The visible change from plain to styled at close is accepted: rendering the whole tail on every flush grows quadratically.
- `Seal()` at `item_done` replaces the stitched render once.
- The conversation Markdown subset is bold, italic, inline code, ordered and
  unordered lists (including nesting), fenced and indented code blocks, and
  links. The UI adapter uses the pinned Goldmark parser, also used by Glamour,
  to produce semantic spans for closed blocks; `render` receives a parser
  callback and remains free of external dependencies. The shared `Measurer` wraps the visible text before
  the UI adds terminal styles. List continuations align under their content;
  code retains literal text and whitespace. Escaped syntax remains literal.
  Unsupported HTML blocks and inline HTML display their literal source text.
  Tables use our own renderer.
- Conversation spans use theme text, accent and surface tokens: bold and italic
  retain their attributes, code uses the surface background, and links use
  underlined accent text. Lists reuse the theme's action glyph (including the
  ASCII fallback). `NO_COLOR` keeps readable content without terminal escapes.
  `Stream.Markdown` caches closed semantic rows by width, theme and width mode,
  incrementally projects the open tail, and replaces the projection at `Seal`.
  Only visible conversation rows receive terminal styles.
- One width function serves all layout: wcwidth by default, grapheme clusters once mode 2027 is confirmed, ambiguous East Asian width narrow, Hangul syllables two cells. The mode is part of the cache key. The Charm-free `Measurer` uses pinned Unicode 17 data and UAX #29 segmentation for wrapping and clipping in both modes. A cluster wider than the entire row is omitted with a typed clipped flag, never split. Cursor offsets inside a cluster clamp to its start.
- The hardware cursor is placed on every frame at the display width of the text before it on the wrapped row, so IME composition appears in place. The TUI never requests all-keys-as-escape-codes. Committed IME text arrives as UTF-8, one grapheme cluster at a time ([surfaces.md](surfaces.md#korean-input)).
- Only the viewport plus overscan (P) is laid out, so cost follows viewport rows and not history. `Stream` indexes new text as it arrives. `Window` takes a retained byte anchor within a block and stops reflow at the requested row count, including after a width change. `Renderer.Draw` reads and paints only those indexed rows, retains only the current viewport cache, and returns changed-row patches; an empty patch list causes no writes. A trusted UI painter styles only closed rows after sanitizing and clipping.
- Resize events coalesce to one reflow per 32 ms (P).
- Conversation wrapping uses the current width; the indexed row count at the
  old width must not truncate newly wrapped rows or the final reply.
- An unchanged state writes zero bytes. Motion is one status glyph with no timer while deltas stream; elapsed time updates at 1 Hz (P) otherwise.
- OSC 8 links are emitted only for absolute `http`, `https` and `mailto` destinations. Other destinations retain their label without an active hyperlink; decoded text is sanitized before layout.

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

`term.Gate.Admit` takes an injected timestamp. The legacy approval barrier is
250 ms (P), restarted when a card or its group revision changes. Explicit repeat
and release events remain explicit even if the keyboard capability reply has
not arrived. Unknown key classes and event types are rejected.

The terminal input adapter decodes enhanced keys before dispatch and excludes
paste and terminal replies from key actions. Cancellation drains pending decoder
events without dispatching them, joins the input reader, and closes only its own
cancellation resources before the caller restores terminal mode.

**Mid-turn input** is steering by default and applies at the next round boundary ([runtime.md](runtime.md#conversation-loop)). `/queue` makes the input a follow-up. `/stop` cancels.

**The composer** is the Bubbles `textarea`; `textinput` is not used. The
conversation increment sends Enter as one input request, Ctrl+J or Alt+Enter
as a newline, and Esc or `/stop` as an interrupt of the active turn. Ordinary
input uses `mode: steer`; Core decides whether it belongs to a running turn.
`/queue <text>` explicitly uses `mode: queue`. The request carries a fresh
input id for local-echo reconciliation. The acknowledgement means durable
admission, not turn completion; further typing can be sent while the turn runs.
A refused send restores the draft. Connection states other than Live disable
submission without discarding edits.
Resizing preserves the draft and its cursor without submitting it.

`e2e/m1/conversation-history-order.json` exercises queue and ordinary steering
input during a held provider stream through an installed Core and a 100x30 TUI.
It compares live and restored message identities, speakers, text and order,
checks fresh TUI renders, and requires exactly two provider calls. A final-only
response checks that restoration preserves its later first appearance too.

- Paste arrives as one atomic message, is one undo step and has control sequences removed. The TUI never reads the host clipboard; paste is the terminal's.
- Attachments are a strip above the composer and message parts that Core imports ([filesystem.md](filesystem.md#import-and-export)). The draft stays plain text.
- `@` references are completed by Core under its scope. The TUI reads no device file to expand them.
- To-do capture takes the deterministic tokens of [work-and-delegation.md](work-and-delegation.md#the-to-do-record) (`#`, `%`, `!`, `{}`). Core parses the line and shows the chips before it saves. An ambiguous date is never saved silently.
- A failed send restores text and attachments merged with new typing.

**Decision keys.** F6 opens the first unresolved card. An approval selects one
of Core's choices with 1–3 and sends only on a separate Enter press. Esc closes
without deciding. The press-only gate and legacy card-change barrier apply to
both steps. Form prompts use Tab/Shift+Tab to move, Enter to send validated values,
Ctrl+D to decline and Ctrl+X to cancel; Esc only closes. Text, paste and slash
commands never answer an approval. Number values retain their exact JSON value.

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

S5 records whether each synchronized-output and grapheme-mode probe timed out.
A timeout fails the scenario even when the probe's deadline is below 200 ms;
an explicit reply reporting an unsupported mode still resolves the probe.
The evidence validator requires both timeout observations before assigning a result.

S10 selects the physical width mode before the first table paint. Each mode has
an explicit availability observation; a permanently enabled mode 2027 cannot
establish wcwidth alignment. An unavailable mode requires a mitigation and fails
the two-mode comparison. S11 requires every width observation from 100 through
140; a passing threshold is the first usable width and remains usable at every
greater width in that range. SIGINT and SIGTERM cancel the scenario runner through
its terminal-restoration paths.

LN-85 delivers the harness and scenario validation. The confirmed terminal matrix,
owner-run S1–S7 and S9–S11 evidence, and final library decision remain in
[follow-up #371](https://github.com/thisisjun786/lina/issues/371), including #87's
outstanding acceptance. Harness completion does not establish the spike gate.

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
`e2e/m1/tui-approvals-installed.json` installs the real binary, seeds synthetic
producer data after boot recovery, and discovers/answers the card over Core's
socket. A second approval is answered through the built TUI in a real PTY, with
separate selection/confirmation presses, durable attribution and terminal cleanup.
The test creates no additional authority service. `e2e/m1/tui-cards.json` races
two socket clients, changes a group after display, and drives the recording-writer
bell replay assertion.

`e2e/m1/tui-questions.json` exercises a flat question through a separately
running Core listener and the production client adapter. The question source
checks exact typed values, including a large integer, and its accepted answer
is republished so the subscribed card no longer waits. It uses the headless
harness's private state root and process cleanup, with I/O gates instead of sleeps.
The UI reducer also keeps a correlated `already_resolved` result visible when a
resolved stream publication arrives first.

`e2e/m1/tui-state.json` sends duplicate welcomes, reserved conversation frames
and an epoch change over that harness's socket into `state.Reduce`. Package
tests cover projected snapshots, replay, resync, revocation, local echoes,
generation checks, draft recovery, clock offsets and split sanitizer input.
These tests establish the pure reducer and existing receive boundary, without
claiming conversation UI or an unimplemented wire payload.
`e2e/m1/tui-shell.json` exercises the built TUI through the same PTY lifecycle
in English and Korean: connecting, welcome, refusal, absent Core and quit.
It resolves state labels by catalog id and checks that an absent socket leaves
the isolated state directory empty. The separate recovery and stream scenarios
below cover post-handshake behavior.

`e2e/m1/tui-recovery.json` requires a real socket disconnect after the first
welcome has appeared on the PTY, then a second handshake with a changed epoch.
The same TUI process must show retrying, announce the changed epoch once,
return to live and quit. A write-completion gate orders the disconnect after
the displayed welcome; no sleep determines success. The second fake connection
stays open until client quit. This is a separate acceptance test from injecting
local shell messages into the recording harness.

The client adapter retains the handshake reader, so a welcome and immediately
following notification in one socket read lose no bytes. `Subscribe` sends one
request and returns its correlation id; `Receive` yields responses and notifications
in wire order, enforcing Core identity, epoch, framing and correlation. Unknown
kinds use the generated decoder's ignore/count rule. Cancellation during a read
closes the connection rather than preserving an ambiguous partial frame. At most
64 subscription requests may await replies. Tests exercise this API against the
real LN-78 listener, including snapshots, retained replies, overflow isolation,
resubscription, unsubscribe and reads during server recovery.

`e2e/m1/tui-stream.json` drives the built TUI through a real PTY: negotiate the
text-stream capability, subscribe, display an unavailable-service cause, resync,
render snapshot text, and quit with terminal restoration. Its in-memory fixture
variants use generated types; the shared schema and conformance corpus are unchanged.

The shell model retains the reducer's history and draft while connection states
change, ignores asynchronous messages from an older connection generation, and
reserves the layout planner's status row at small terminal sizes. Its recording
harness test checks readable history through retry, resync and recovery, and
terminal restoration on quit. Local epoch notices remain outside history.

The connection-loop tests inject a validated peer, retry wait and bounded message
publisher. Cancellation closes the peer to unblock a pending receive and joins
that close; display errors terminate instead of reconnecting. Initial absence
and refusal terminate immediately. After a live connection is lost, retry delays
start at one second, double to a 32-second cap (P), and remain cancellable. The
loop retries connections only, never application requests. A different main id
or a lower epoch on reconnect fails closed. The production adapter uses
`clientapi.Receive` and `Subscribe`. The PTY recovery scenario establishes
reconnect across the socket; the injected lifecycle
tests cover cancellation and failure boundaries independently.

The shell projects LN-78's generated stream payloads into the existing reducer:
snapshots replace a topic window, output deltas append by revision, and completed
items replace streamed text. Topic/key pairs have distinct internal block ids.
Cursor and revision numbers are decoded exactly, including unsigned 64-bit
limits and JSON integer exponent notation. A payload cursor must match its
envelope epoch. Explicit resync and overflow retain the visible window; overflow
requests a fresh subscription for only the affected topic. Replays do not issue
duplicate requests. A newer observed cursor is a floor for the replacement
snapshot, so an old-epoch response cannot restore Live. Revocation snapshots
drop the reducer's cached content and the shell's rendering cache together.

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

Conversation Markdown golden files in `internal/tui/ui/testdata/` record each
supported element in dark, light and high contrast, including ANSI attributes
and hyperlinks. Regression tests pass snapshots, byte-split deltas and
completion through the conversation model, and run the real Bubble Tea renderer
with the recording terminal. Renderer tests cover nested spans, literal code,
reference links, link scheme filtering, cache reuse, authoritative replacement,
and Korean/emoji wrapping and resize in both width modes. These are automated
rendering checks; they do not replace the physical terminal and IME spike.

**Tokens and theme.** `internal/design` is generated from the DTCG files in `design/tokens/` ([surfaces.md](surfaces.md#one-design-language)). `node scripts/tokens.mjs` writes Go values, CSS, the SVG mark and the token tables in `DESIGN.md`. Its `--check` mode regenerates in memory and fails on drift without changing the checkout. Foundation runs it for TUI/design component changes as well as token or automation changes ([foundation.md](../policy/foundation.md)).

- Raw presentation colours, ANSI colour numbers and glyph literals are banned in production `internal/tui` source. The token checker scans literals and colour constructors; comments, test fixtures and numeric Unicode measurement data are not presentation values. `tui/ui` turns generated semantic colours, spacing and glyphs into Lip Gloss styles.
- Contrast checks require at least 4.5:1 for text, muted text and accent on canvas and surface, in light, dark and high contrast. The source also specifies tested 256-colour and 16-colour mappings. Dark ANSI16 uses bright white for the accent where standard blue cannot pass; user-redefined ANSI palettes can change physical contrast. Hairlines are decorative, never the only state indicator.
- No state is shown by colour alone: each has a mark and a word. Glyph sets are tokens, in plain Unicode and ASCII, and none needs a private-use glyph.
- Connection-screen display overrides are `LINA_THEME=light|dark|high-contrast` and `LINA_GLYPHS=ascii`. Otherwise `COLORFGBG` selects a light or dark background (dark by default); terminal capability selects RGB, 256 or 16 colours. Nonempty `NO_COLOR` preserves the layout, marks, labels and actions without escapes; subsequent states append compact lines. These overrides persist no preferences.
- On LINA OS the complete TUI follows the active Omarchy theme file ([surfaces.md](surfaces.md#platform-adaptation)); theme-file discovery remains part of platform preferences work.
- Every code in the closed lists has an entry in both the Korean and English catalogs, checked by id and never by wording.

The connection shell resolves labels and next actions through
`internal/design` catalog ids. `LC_ALL`, then `LC_MESSAGES`, then `LANG` selects
Korean for `ko` locales; other or missing locales use English. Unsupported ids
render the catalog's unknown label, never peer text. The coverage test walks
every enum in the client schema, including nested enums, and requires both
languages. Repeating the same connection state emits no additional bytes.

`e2e/m1/tui-conversation-stream.json` scripts the fake Core through the built
TUI and a PTY/VT emulator. It asserts input, mid-turn steering, partial text,
authoritative completion, duplicate completion, and terminal restoration.
`e2e/m1/tui-conversation.json` installs the real binaries, starts installed Core
with the fixture Responses provider, types in the real TUI, checks reply cells,
and reopens the conversation. It exercises the existing Core turn, lane and
ledger composition; a direct provider call is not its acceptance proof. The
real-model journey remains #312 and the physical terminal spike remains T-08.
`e2e/m1/conversation-admission.json` verifies large Unicode inputs and replies
through durable materials, secret removal before model input, and restart
readback. `e2e/m1/conversation-failure.json` verifies that a provider item followed
by a failed response is absent from completed history and the next model request.
The installed conversation scenario also restarts Core with its model route
removed and requires connection-only startup.

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

### Memory and agent command requests

T-14 exposes `/memory` and `/agents` in the conversation footer. Each opens a
scrollable text block with its command syntax; Esc returns to the composer.
These commands are intercepted before conversation input, including when their
capability is absent or their arguments are invalid. A refused or unsupported
operation is shown locally and never becomes a model prompt.

- `/memory list [query]` lists current scoped items in pages of at most 1000 rows and 512 KiB of serialized item data, with stable ids,
  content, holder, privacy scope and source event identities. `/memory` is its
  empty-query form. `/memory next` requests the next page with the same agent and query; an invalidated cursor requires listing again.
- `/memory remember [--holder=all|agent] [--scope=local|private|never-leaves] <text>`
  records an explicit person statement through Core's existing memory writer.
  Defaults are common knowledge (`all`) and `local`. `/memory correct <id> <text>`
  supersedes that item with a newly sourced statement, retaining its scope and
  holder. `/memory forget <id>` uses Core's recoverable removal flow; the next
  recall and next model request exclude it. Results acknowledge committed ids.
- `/agents` lists ids and names. `/agents create <name>` uses the LINA preset;
  `/agents choose <id>` changes this client's conversation and memory focus;
  `/agents rename <id> <name>` keeps the id; `/agents delete <id>` uses the
  existing agent trash transition and ends its bindings. Selecting another
  agent drops the old conversation display and subscriptions, while Core keeps
  independent lanes. Deleting the selected agent selects the first remaining
  list entry when that deletion is the client's own action; a selection lost
  across reconnect instead requires an explicit choice (T-05). An empty list
  stays available for creation. Initialization never
  recreates a deleted default agent.

These direct management requests express the person's own action. They do not
answer an approval or question (T-04), bind a channel, or start an agent exchange.
`agents` and `memory` are negotiated separately from conversation and accounts.
Their transient blocks and command drafts are not persisted by the TUI.
`e2e/m1/tui-memory-agents.json` exercises the installed Core, generated client
protocol and real PTY TUI against a loopback Responses fixture, including scoped
recall, correction, forget, agent identities, conversation selection and restart.
