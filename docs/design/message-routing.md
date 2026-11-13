# Message routing: one way for a message anywhere to reach the session that carries the work

Design for lina#309. Nothing here is built yet; the follow-up issues build it. Code is cited as `path:line` at dev
`8c88306` for this repository and as `lina-app path:line` at `d3fa8a6` for lina-system/lina-app. Where the install the
agents run today differs from this repository, the text says "local patch, not in the repo" or "ops script, not in the
repo". Counts are values seen in operations over five days, not LINA measurements.

The rule in one sentence: **a message is posted on a place; the place names the session that owns it; the core stores
the message once under its id and then delivers it to that session by the place's rule; the answer comes back on the
same place, once.**

Everything below is that sentence made exact.

## 1. Places

A place is anything the owner (or an agent) can write on. Each place has exactly one owning session at a time, and the
core resolves it when it delivers, not when the message is posted, so a message posted while one run is live and
delivered after it ended still finds the session that owns the place then.

Delivery modes, used in the table:

- **mid-run**: the owning session has a live turn; the message joins it at the next model call (section 3).
- **next turn**: stored and handed to the owning session's next turn as its own user entry.
- **wake**: the owning session is parked (scheduled on a worker, blocked on a question); the message resumes that same
  session id, as the worker-end wake does today (`hermes_cli/worker/kanban_link.py:161-182`).
- **resume**: the session ended but its transcript is kept; a new turn runs in the same session id.
- **start**: no session exists for the place; the core starts one with the place's record as its first context.

| Place | Today | Owning session | Session live | Session parked | No session live | Place finished |
| --- | --- | --- | --- | --- | --- | --- |
| Agent chat (the app's chat with an agent, the agent's Telegram DM) | gateway turn per inbound message; the app has its own API session per agent (`hermes_cli/lina_talk.py:4-11`) | the agent's main session for that owner | mid-run for owner text; next turn otherwise | n/a | resume (a gateway turn in the same conversation) | n/a |
| Goal | goal-mode card and its record goal | the goal's session (the goal card's session) | mid-run | wake | resume; start if the session is gone | new goal linked to it (section 3) |
| Task (to-do) | record to-do, or the work card it spawned | the session of its open work card when one exists; else the owning agent's main session (`hermes_cli/work/board.py:15-20`, `hermes_cli/work/notify.py:44-46`) | mid-run | wake | next turn of the owning agent's main session (started if idle) | new task linked to it (section 3) |
| Run (a worker run) | `lina worker send` / answer (`hermes_cli/worker/hub.py:553-583`) | the run itself while it lives; the card session that started it once it ended | mid-run (the runner's steer; a runner with no channel gets next turn) | queued into the not yet started run (`hermes_cli/worker/hub.py:618-621`) | the card session that started the run, next turn; it decides on a follow-up run | the card session, next turn |
| Inbox decision (a record decision, a card blocked on a question, a run's open question) | three paths (section 9) | the session that asked | mid-run | wake; only that question is released | resume the asking session | the decision is closed: the message becomes a note on the place |
| Reply to a pushed notice (app notification, Telegram reply to a notice message) | lands in the chat session it was typed in (`gateway/run_inbound.py:1579-1588`) | the owner of the place the notice is about | as that place | as that place | as that place | as that place |
| Agent-to-agent chat (one agent writes to another) | a separate Bot Chat session per agent, the only session the tool works in (`tools/bot_mode_dm.py:215`) | the target agent's main session; a card session only when the message names a card that agent owns (section 7) | next turn; mid-run only when it answers a question the target asked | wake only for an answer to the target's question; otherwise next turn | next turn | n/a |
| Owner relay ("tell the investing agent ...", in any channel) | the agent paraphrases the owner into a Bot Chat message, or uses a shell wrapper and a retry queue outside a Bot Chat | one relay record; delivered like agent-to-agent chat, answered to the asking session (section 7) | as agent-to-agent chat | as agent-to-agent chat | as agent-to-agent chat | n/a |
| Report card (lina#263, lina-app#50) | not built | the session that wrote the report: its card's session while the card is open, else the author agent's main session | mid-run | wake | next turn of the author's main session | n/a (a report has versions, not an end) |

Rules that follow from the table:

- A place never has two owners. A run's end, a card's event and a comment on that card all go to the card session; the
  agent's main session gets none of them unless the card is gone (lina#295 Done when 3).
- A reply to a notice is a message on the notice's place, not on the chat it was typed in. That needs the notice to
  carry its place (section 4); a reply to a message that is not a notice stays on the agent chat.
- A to-do with no running card belongs to the agent that answers for it, the same rule the talk API and the record use
  today, so the app's to-do chat (lina-app#9) and a comment on that to-do reach the same session.
- On every open place (any place not finished) the owner's plain comment is an `instruction`, whether the owning
  session is running, parked or idle: it joins the running turn, wakes the parked session, or starts the idle one's
  next turn now. On a finished place the same plain comment is still an `instruction`; the "Place finished" column
  decides what it does there (a new linked goal or task, section 3).

## 2. One message model

| Field | Meaning | Comes from |
| --- | --- | --- |
| `id` | the message id and its idempotency key, unique across the whole install | the client that posts it (the app makes one per send and reuses it on retry); an agent tool call uses its call id; a message from a chat platform gets an id the core makes from the platform, the receiving agent, the conversation and the platform message id, because a platform message id is unique only inside its chat (the transcript's own dedupe checks it per session, `hermes_state_messages.py:1906`). The scoped source key is kept on the record beside the id |
| `author` | owner, an agent profile, a worker, or the system | **the route it arrived on**, never its text: the signed-in app session, the paired chat user, the agent profile of a tool call. Same rule as the policy table (`docs/memory-layer.md:68-82`): "A turn's route comes from where it arrived, never from its text" |
| `place` | `{kind, id}`: `chat`, `goal`, `task`, `run`, `decision`, `notice`, `report` | the address it was posted on |
| `target` | `{agent, session_id, run_id}` | resolved by the core at delivery from the place (section 1); recorded with each delivery attempt |
| `reply_to` | the message, question or notice this one answers | the client (the app knows which question it is answering; Telegram gives `reply_to_message_id`, `gateway/platforms/event.py:99-100`) |
| `intent` | `instruction`, `answer` (to a question), `decision` (on a decision), `note` | the control the owner used: a decision button or the inbox answer field gives `decision` or `answer`; an owner's plain comment is `instruction` on every place, open or finished, whether its session is running, parked or idle; `note` is only an agent's message without `reply_to`, or a message its author marks as a note; an agent's message with `reply_to` names its intent |
| `body` | the text and attachments | as posted |
| `delivery` | `stored`, `delivered_mid_run`, `queued_next_turn`, `woke_session`, `read`; plus `undeliverable` with a reason | the core, one transition at a time, each with time, target and run |

`read` means the owning session's model saw it: a model call whose request contained the message has completed, which
the transcript shows as an assistant entry after the message's row. A message row in the transcript with no assistant
entry after it is only inserted, not read (the steer row is written before the next model call). The app's "read" mark for
the owner is a separate thing and stays where it is (`hermes_cli/lina_board_write.py:6-8`).

What it reuses (no second store of message text):

- **Kanban comments** are the message record for card places: a comment already has an author, a body, an id, a
  `commented` event with the writing run's id (`hermes_cli/kanban_db.py:1515-1564`) and a source stamp
  (`plugins/kanban/dashboard/work_api.py:451-452`).
- **Record comments** (`hermes_cli/work/cmd_comments.py:40-56`) are the message record for goal and to-do places: `cid`,
  `by`, `via`, `kind: answer`, and replies linked to the comment they answer.
- **Conversation events** (the session transcript rows) are the record for agent chat; the gateway's queue
  (`gateway/run_busy.py:117-126`) already keeps busy-session messages in order.
- **Worker receipts** (`hermes_cli/worker/hub.py:563`, `573`, `619`, `633`) are the record of what reached a run.
- **Bot Chat delivery ids and receipts** (`gateway/session_bot.py:25-37`) are the pattern for agent-to-agent delivery.
- **Notice keys and push tags** (`hermes_cli/lina_push_watch.py:13-16`, lina-app `public/sw.js:1-3`) carry read state to
  every device.

What is new: one table, `work.message_delivery`, keyed by message `id`. It lives in the `work` schema because that
schema already holds the routing state it sits beside (`decision`, `event`, `outbox`,
`services/store/migrations/0004_work.sql`), while the text stays where it is stored once (`memory.messages`,
`services/store/migrations/0001_memory_messages.sql`, and the place records below). The table keeps the place record it points to (a
comment id, a record `cid`, a transcript row), `author`, `place`, `reply_to`, `intent`, and one row per delivery state
change with `target`. It holds no copy of the text. It is the only place that answers "where did my message go", and the
app reads it for the line under a sent message (lina-app `src/screens/comments.tsx:14-25` shows "sent to" today, with no
further state).

## 3. Delivery rules

**Joins a running turn** when all of these hold: the owning session has a live turn; the intent is `instruction`,
`answer` or `decision`; the author is the owner, or an agent answering a question this session asked; and the runtime
has a channel into the turn (a worker runner without one answers `refused`, `hermes_cli/worker/hub.py:578-580`, and the
message waits). It is delivered at the next model call: the loop drains steered text before each API call
(`agent/turn_iteration_prep.py:175-181`) and after each tool batch (`agent/tool_executor.py:1232`,
`agent/agent_runtime_helpers.py:3705-3739`), as its own user entry. A long model call delays it to the end of that call;
nothing interrupts a model call for a message, because a cancelled call loses work and the message is not urgent
enough to pay for that by default (the gateway's `redirect` exists for an owner who wants it,
`agent/interrupt_control.py:323-327`).

**Waits for the next turn** when the intent is `note` (an agent's message without `reply_to`, or a message marked as
a note), or when the turn ends before the message reaches a model call. An owner `instruction` to an
idle session does not wait: it starts that session's next turn now. The last case exists
today: steered text left over after the final answer is handed back as `pending_steer`
(`agent/turn_finalizer.py:828-833`). In this design that leftover keeps its message id, its state becomes
`queued_next_turn`, and it is the first user entry of the session's next turn; if the session then parks, the message
is a wake reason by itself.

**Wakes a parked session** for `instruction`, `answer` and `decision`; an owner's plain comment on a parked place is an
`instruction` and so wakes it. A `note` (only an agent's message without `reply_to`, or one marked as a note, can be a
note on an open place) is stored and read at the next wake for another reason.

**An answer releases only its question.** A question has an id: a card question comment, a run's open question
(`hermes_cli/worker/hub.py:553-570`), a record decision (`decide_cid`, `hermes_cli/work/cmd_comments.py:66-71`). An answer
carries `reply_to` that id. The core closes that question only, and unblocks the card only when its block is that
question and no other question on the card is open. Today both card paths release everything: any comment by someone
other than the assignee clears every pending question (`plugins/kanban/dashboard/work_api.py:266-276`), and an answer
unblocks any `needs_input` block whatever it answered (`plugins/kanban/dashboard/work_api.py:453-454`). An answer with no
`reply_to` on a card with two open questions is not guessed: the app asks which one, the same rule as lina#299.

**An inbox decision is a message to the waiting session.** The inbox lists questions; answering one posts a message with
intent `decision` (or `answer`), `reply_to` the question, on the place that asked. There is no separate notify line: the
record's one-line notice to the main chat (`hermes_cli/work/cmd_comments.py:54-56`, `hermes_cli/work/notify.py:26-41`)
is replaced by delivery of the message itself to the asking session.

**A message on a finished goal starts a new goal linked to it** (`--follows`, lina#515), owned by the same agent, with
the message as its first instruction; a message on a finished task starts a new task linked to it, the same way. The
finished item is never reopened, whatever the message says, a `decision` that rejects its last verdict included: done
was decided by gates and a judge (lina#225, lina#295), and reopening would undo a verdict nobody re-judged
([work-and-delegation.md](work-and-delegation.md) section 4). Only a `note` (one its author marked as a note, or an
agent's message without `reply_to`) on a finished place is stored and starts nothing.

## 4. What comes back

- **Where.** The answer is a message on the same place, with `reply_to` the owner's message. The owner sees it where he
  posted: under his comment, in the chat, under the inbox question, on the report card. A reply in Telegram to a
  notice is answered in Telegram and recorded on the place.
- **Who.** Every reply carries the answering agent and the session kind (main session, goal session, run), from the
  delivery record, so "which session answered" is never guessed from text.
- **One notice per event.** Every event that could reach the owner has one delivery id: a reply's id is its message id;
  a run's end is one of lina#223's three kinds with its own delivery id; a card event is the event row. The surface
  for an id is chosen before anything is sent, never by which sender runs first: a reply goes on the channel the owner
  posted on (a Telegram reply to a notice is answered in Telegram), and every other surface skips its id. Each surface
  sends for an id at most once:
  - push sends only lina#244's three kinds, tagged with the place key, and closes on read
    (`hermes_cli/lina_push_watch.py:3-16`, lina-app `src/app.tsx:485-498`, lina-app#53); for a reply, only when the
    owner posted in the app, so a push never takes the place of a chat reply;
  - a chat message goes only when the owner posted the original in chat, or when the event is one lina#261 says the
    owner must hear; it is sent by the agent that owns the place, never by a notifier and the agent both;
  - a card comment for a run's end is the record, not a notice;
  - the agent's main session hears nothing about a run its card owns.
  This is the rule the install already moved to by a local patch, not in the repo: new board subscriptions default to
  `wake` instead of `notify+wake`, so one event reaches the chat once, as the woken agent's own reply (the repo still
  defaults to `notify+wake`, `tools/kanban_tools.py:1277`). Board counts: 81 `wake` and 12
  `notify+wake` subscriptions. lina#223's single delivery id is the key every surface dedupes on.

## 5. Failure and order

- **Retries.** The message `id` (section 2: install-wide, with a platform message id namespaced by platform, receiving
  agent and conversation) is the idempotency key end to end. Posting the same id again returns the stored message
  and its delivery state; nothing is written twice. Delivery into a session is keyed by the same id: the steer carries
  it as its ticket (`agent/interrupt_control.py:310-321` takes a `ticket`), and "delivered" is decided by finding that id
  in the transcript, not by matching the note text as the card receipt does today
  (`tools/kanban_tools.py:629-644`). This is the same shape as lina#219's request keys and the Bot Chat delivery ids.
- **Unknown is not failed.** A delivery whose outcome is unknown (a timeout after the request may have landed) stays
  `stored` with the attempt recorded and is resolved by reading the transcript, never re-sent blindly. The Telegram
  double reply came from treating such a send as failed (lina#214; the fix waits for an in-flight final send,
  `gateway/stream_consumer.py:48-51`).
- **Order.** Per target session, messages are delivered in the order the core accepted them. When a message becomes
  deliverable mid-run (section 3), every earlier undelivered message to that session joins the same model call ahead of
  it, notes included, so nothing is overtaken and nothing holds back a later instruction or answer; several waiting
  messages join one model call together, in order. Different sessions are independent.
- **A session that is gone.** The place picks the next owner (section 1): a card's next run, the goal session resumed or
  started, the chat's conversation. The delivery records the new target and the reason, so the owner sees which
  session answered.
- **A busy agent.** Busy is not a refusal. A message to a busy session is stored and joins the running turn or waits for
  the next one; the sender never holds a turn open waiting for the target. That ends the agent-to-agent busy loop
  (section 9, row 4; section 7).
- **Host restart in the middle.** A message is acknowledged to the client only after it is stored. On start, the core
  reconciles every message not in `read`. One with an assistant entry after its row in the target's transcript
  becomes `read`. One whose row is in the transcript with nothing after it (the host died between inserting the steer
  row and the model call, `agent/agent_runtime_helpers.py:3718-3723`) is not inserted again: the core resumes that
  session with a turn that continues from the existing row, so the model reads it once. One not in the transcript at all,
  in `stored`, `queued_next_turn` or `delivered_mid_run`, is delivered again by its place's rule. Today a planned restart keeps queued
  messages only in `queue` or `steer` mode and drops them in `interrupt`, the gateway's default
  (`gateway/run_busy.py:107-110`, `gateway/run.py:3339`).

## 6. Boundaries

- **Authorship comes from the route.** A message that says "the owner says" is still authored by whoever sent it. A
  message from an agent is never the owner's words in memory, and never the owner's decision (`docs/memory-layer.md:68-82`;
  lina#310 is the open fix for gateway notes stored as the owner's words).
- **Other agents' sessions.** A message is delivered only to the session that owns its place. An agent that posts on a
  place another agent owns writes as itself, and the owner of the place receives it next turn (or mid-run when it
  answers that owner's question). A message never lands in another agent's session unless that agent owns the place.
- **Private work.** A place marked private (private artifacts in lina-app#40, private reports in lina#263, personal
  documents in lina#308) accepts messages only from the owner's own session, delivers only to its owning session, and
  its replies and notices go only to the owner's devices; agents without that scope cannot read its messages. Where
  personal source text may go at all is the general rule in
  [work-and-delegation.md](work-and-delegation.md#11-personal-source-text).
- **Approvals still gate.** A message can ask for anything; the asking does not authorize it. Money, deletion,
  publishing, credentials and sending in the owner's name stay behind approvals (`tools/approval.py:142`,
  `docs/design/hermes-patterns.md` P1; held outbound drafts in lina#301). No message satisfies an approval, the owner's
  included: an approval is given only through lina#322's approval path. An agent's message never approves anything.

### Outbound allowlist (built: lina#638)

A test copy of an agent must not reach real people, whatever its copied jobs, skills or prompts say. The profile's
`config.yaml` key `outbound.allow` (read with the managed overlay applied) lists the only destinations a message may
leave to; unset or an empty list means unrestricted, and any other value that names no usable destination (`false`,
`0`, `{}`, a list of blanks) allows nothing. Entries are `<platform>:<chat id>` (a `:<thread id>` suffix limits it to one thread, parsed like a
`send_message` target) and `agent:<name>` (a local profile, a peer `<peer>` with all its agents, or `<peer>/<agent>`):

```yaml
outbound:
  allow:
    - telegram:123456789
    - agent:owner-bot
```

The check is `agent/outbound_lock.py::check_outbound`, and it runs on the resolved target (after alias and home-channel
resolution). The doors that call it: the `send_message` tool, `hermes send` and the kanban notifier
(`tools/send_message_tool.py`); every gateway adapter content method, defined or inherited (`send*`, `edit_message`,
the exec-approval, update, model and choice prompts, the native task card, played TTS and the draft seal), which covers
replies, streamed edits, notices, webhook cross-platform delivery and cron's live lane
(`BasePlatformAdapter.__init_subclass__`; a refused edit ends the stream and a refused task card stops the card, both
with no fallback send); cron delivery, including
`deliver: <platform>` home channels and `bot-chat:<profile>` (`cron/scheduler_delivery_outbound.py`); `hermes peer
dm`/`run`; `message_agent`; the webhook `github_comment` deliver; and worker end notices.
`tests/agent/test_outbound_lock_doors.py` drives each door with a fake transport: a refused target makes no outbound
call, an allowed one exactly one. A channel-name entry resolves through each profile's own channel directory. The agent's own
local chat, its own Bot Chat and the API server's answer to its caller are not outbound.

A refused send fails and nothing leaves the process. The error reads `Refused by outbound.allow: telegram:222 is not in
the outbound.allow list in <home>/config.yaml, so nothing was sent (<door>).` It is logged once per target, counted in
`<home>/outbound_refusals.json`, and reported by `lina doctor`. A refused cron target is listed in the job's
`last_delivery_refused`, and the run's delivery error names the key. An edit to the list applies to the next send
without a restart.

## 7. Agent to agent

Agents talk to each other through the same model: a message on a place, delivered to the place's owner, answered on
the same place. Today's path is mapped in section 9, row 4; this section closes each gap there.

**Places.** Each agent has one agent-to-agent chat, a place owned by that agent's **main session**. There is no separate
agent-to-agent session: today the tool works only in a dedicated Bot Chat session (`tools/bot_mode_dm.py:203-218`), so a
request the owner makes in a messenger chat has to leave that conversation to be sent. In this design any session of
an agent can send, and the message lands on the target's agent-to-agent chat. When the owner, in any channel, asks one
agent to tell another something, that request becomes **one relay record**:

| Field | Meaning |
| --- | --- |
| `request` | the owner's request as given, kept as provenance: the id of the owner's own turn in the sending agent's conversation and the exact span of the owner's words to pass on |
| `sender` | the agent that relays |
| `target` | the agent the owner named |
| `origin` | the conversation and session the request came from (the asking session) |
| `message` | the relay message: the owner's span, quoted, and the sender's own lines around it, under one message id |
| `delivery` | `queued`, `delivered`, `read`, `answered`, each with its time, from `work.message_delivery` |

A relay record is a message (section 2) with intent `instruction` and place the target's agent-to-agent chat; the
record adds `request` and `origin`.

**Which session receives it.** The target's main session, by default. A card session receives it only when the message
names a card the target owns (it is then a message on that card, section 1). It never lands in a card or session of an
agent that does not own the place (section 6).

**Which session gets the answer.** The asking session: the sender's card session when a card sent the message, else the
sender's main session. The target answers with `reply_to` the message; the answer is delivered to the asking session by
the same rules and never to the target's own chat, the sender's Bot Chat, or the owner. The asking session decides what
the owner hears, once (lina#261).

**Busy target.** Stored, acknowledged to the sender at once, and delivered at the target's next turn; it joins a running
turn only when it answers a question the target asked (section 3). Busy, a session open on another surface and a live
CLI holding the session are not refusals: the message waits on the place, not on the session. The same request sent
twice is one delivery: the message id is the request key, as in lina#219, and the delivery has one id, as in lina#223.
A sender that retries after an unknown outcome reuses the id and gets the stored state back (section 5).

**Speaker provenance.** The route decides whose words are whose (`docs/memory-layer.md:68-82`). In a relay, the owner's
span is stored as the owner's words only when the core can prove it: the `request` names an owner turn in the sending
agent's conversation that arrived on a person route, and the span is found verbatim in that turn. The relay itself, and
anything the sender adds, are the sender agent's words. An agent's text that only claims "the owner said ..." without a
proven span stays the agent's words, in the target's transcript and in memory. Today the opposite happens: the tool asks
the model to paraphrase the owner (`tools/bot_mode_dm.py:86`) and stamps the whole message with a bot author
(`tools/bot_mode_dm.py:248`), which memory stores as the bot's words (`plugins/memory/lina/policy.yaml:23`, `30`). The
proven span needs one new policy route, an owner relay whose quoted span is the person's words; lina#310 keeps gateway
notes out of the owner's words in the same table.

**What the sender sees and says.** The sender reads the delivery record, never guesses: one line "delivered at hh:mm",
then the target's answer when it comes, and "read" in between when the sender is asked. Today's tool returns a dispatch
acknowledgement that it says is not a delivery receipt (`tools/bot_mode_dm.py:79`). The owner who asked for the relay
sees the same line under his request, in the app (a follow-up app issue).

**Long work goes to a card.** A message that asks for more than one reply (it needs tools beyond reading, or more than
one turn) becomes a work card owned by the target, linked to the sender's place; the chat gets the card's link and
the one-line answer, not the work. The rule is enforced in the target's turn: a turn started by an agent message may read
and reply, and its first call to a tool that writes or runs is refused with "make a card" (the tool list stays as it is,
so the cached prefix is untouched). Creating that card is the one write such a turn may make: the card tool is exempt,
and the core links the new card to the sender's place. Results come back as the card's end, to the asking session (section 4).

**Loop cap.** Between two agents, a thread (messages linked by `reply_to`) runs at most three round trips without a
message from the owner. The fourth agent message in that thread is held, and the asking session gets one question to
put to the owner: go on, or stop. A message from the owner on either side resets the count.

**Approvals.** An agent's message never approves anything, relayed or not (section 6). A relayed owner span is the
owner's words for memory and context, not a decision: an approval still takes lina#322's path.

## 8. Context order

The reference is the "Context order" section of `docs/memory-layer.md` (`docs/memory-layer.md:44-60`). This design adds
no new row; it changes two cells of one row and adds one sentence under the table.

**Where a delivered message enters.** A message delivered mid-run is **its own user-role entry appended after the
latest tool result**, part of "recent turns". This is the steer path that exists today: a new `role: "user"` row after
the newest tool result, with the out-of-band marker (`agent/agent_runtime_helpers.py:3705-3739`,
`agent/prompt_builder.py:557-580`). It is **not** a line in the turn note. A message delivered at the next turn, or one
that wakes a session, is that turn's user message (several waiting messages: one user entry each, oldest first). Each
entry starts with one line naming the place and the author, as the app's to-do talk does today
(`hermes_cli/lina_talk.py:45-46`).

**The turn note.** This design puts nothing into the turn note. The note keeps its order from
`agent/turn_context.py:953-956` (gateway notes, plugin notices, surface switch, skill-set delta, date). Agent tastes do
not use the note: core tastes sit in the frozen plugin section and per-turn tastes in the found-memory tool result
(`docs/design/agent-tastes.md` section 9, lina#307).
Messages are not notes because they are someone's words with an author and an id, and the note is one-shot
state that is popped and cleared (`agent/turn_context.py:161-168`). The app-talk carry, which today prepends the app
exchange to the next Telegram message as model-facing text only (`gateway/app_talk_carry.py:88-99`), becomes a delivered
`note` message: its own persisted user entry before the owner's new message.

**Found memory.** A message delivered mid-run does not run recall again; recall stays per turn
(`agent/turn_context.py:192-203`).

**Prefix effect, read from code, not measured:**

- Mid-run delivery: new bytes are the appended user entry only, from the position right after the latest tool
  result; everything before it is the previous request's bytes, and the row is persisted so the next request replays it
  unchanged (`agent/agent_runtime_helpers.py:3718-3723`).
- Next-turn delivery: new bytes start at the new user entry (with its turn note), followed by the recall rows; the
  same as any owner turn.
- Woken or resumed session: the stored system prompt is restored, not rebuilt, so new bytes start at the wake
  message's user entry (plus a date note if the day changed, `agent/date_note.py:36-49`). The bytes before it are
  unchanged, but the provider's cache may have expired while the session was parked; the hit rate of a wake is not
  measured here.
- Started session: everything is new.

**Changes to the table, as changes:**

- Row "Lower summary nodes (depth 0) and the recent turns": the Part cell becomes "Lower summary nodes (depth 0) and the
  recent turns, including delivered messages: a message delivered mid-run is its own user entry after the latest tool
  result; one delivered at the next turn or on a wake is that turn's user message".
- Same row: the Changes cell becomes "appended every turn, and mid-run when a delivered message joins a running turn;
  replaced at a compaction".
- New sentence at the end of the paragraph under the table: "A delivered message is never part of this note: it is a
  user entry of its own (`docs/design/message-routing.md`)."

These changes compose with lina#307's (`docs/design/agent-tastes.md` section 9): lina#307 changes the "System and
persona" row (Part and Changes cells) and the "Found memory" row (Part cell); this design changes only the "Lower
summary nodes (depth 0) and the recent turns" row (Part and Changes cells) and the paragraph under the table. No cell is
changed by both. The table below carries both sets, lina#307's cells copied word for word from its section 9; a marker
after each changed cell names the design it comes from.

The full table after both sets of changes, **proposed**:

| Part | Built in | Changes |
| --- | --- | --- |
| System and persona (identity, SOUL, guidance, context files, skills index, plugin sections (among them the self note and the agent's core tastes, after a line saying tastes never change reporting, honesty, permissions, verification, the owner's instructions or safety), profile line, the session's start date and model lines, runtime environment) (lina#307) | `agent/system_prompt.py` `build_system_prompt_parts` | frozen per session; rebuilt at a compaction; the self note and core tastes change at most daily and weekly and reach a running session only at that rebuild (lina#307) |
| Notes and person card (MEMORY.md, USER.md, the memory provider's block, then its person card: Honcho's representation and peer card from `session_card_block()`), closing the prompt between `# Notes and person card` and its end marker | same, `_memory_parts` | frozen per session; reread at a compaction |
| Upper summary DAG: LCM-X's condensed nodes (depth 1 and up), deepest first | LCM-X `_assemble_context` | appended to or rewritten only when they are themselves condensed |
| Lower summary nodes (depth 0) and the recent turns, including delivered messages: a message delivered mid-run is its own user entry after the latest tool result; one delivered at the next turn or on a wake is that turn's user message (lina#309) | LCM-X, then the transcript | appended every turn, and mid-run when a delivered message joins a running turn; replaced at a compaction (lina#309) |
| Found memory: the turn's recall and up to three of this agent's own tastes picked for the turn, labelled as tastes, as a `memory_recall` tool call and its result, after the user message. `memory_recall` is a real read-only tool (`agent/memory_recall_tool.py`) listed with the provider's tools, so strict providers accept the call (lina#307) | `agent/turn_context.py` `recall_tool_rows` | per turn |

## 9. Today's paths mapped

| # | Path | Code today | Failure seen in operation | What the design changes |
| --- | --- | --- | --- | --- |
| 1 | Agent chat (Telegram DM; the app's to-do talk) | one gateway turn per message; a busy session is interrupted by default, or steered or queued by mode (`gateway/run.py:3339`, `gateway/run_busy.py:680-729`); the app talks in a separate API session per agent because the gateway refuses an API turn into a Telegram conversation (`hermes_cli/lina_talk.py:4-11`, `gateway/session_api_turn.py:144`), and carries the exchange into the next DM turn as model-facing text (`gateway/app_talk_carry.py:88-99`); a busy agent answers 429 in the app (lina-app `src/screens/talk.tsx:17-30`) | one gateway process serves every agent, so a restart drops each agent's turn in the default `interrupt` mode (`gateway/run_busy.py:107-110`); the app and Telegram are two sessions; a busy agent refuses the app message | stored first, then mid-run or next turn; busy is never a refusal; the carry becomes a persisted note message; restart reconciles by message id |
| 2 | Kanban card comment | a comment on a running card is polled on activity ticks at most every 6 s and steered as a user entry after the next tool result (`agent/activity_tracking.py:74-82`, `tools/kanban_tools.py:621-697`); comments on cards that do not run are read at the next run start | same-agent comments were dropped mid-run: the reader skipped every comment authored by the profile name. Fixed in this repository by lina#246 (skip only ids written by this run, `tools/kanban_tools.py:659-679`); the install carried the same fix as a local patch, not in the repo history. Still open: delivery waits for a tool call, a run that ends first never sees the note, and the receipt is matched by note text (`tools/kanban_tools.py:629-644`). Board counts: 149 main-session comments, 140 receipts | the comment is a message with an id; "delivered" is found by id; a note left after the run's last model call is queued for the card's next turn or wakes it |
| 3 | Record comment and decision answer | the comment is stored on the record, then one line (text cut to 80 characters) goes to the answering agent's chat through `work.notice.command` (`hermes_cli/work/cmd_comments.py:54-56`, `hermes_cli/work/notify.py:20-41`); the install's ledger is an ops script, not in the repo, that sends the same line through the bot-to-bot queue | the answer reaches the agent's main chat, not the card or run that asked; when the main session answered a worker question instead of the card, the card did not learn that the scope changed | a decision is a message to the asking session with `reply_to` the question; the main-chat line goes away |
| 4 | Agent-to-agent message and owner relay | `message_agent` works only in a Bot Chat session (`tools/bot_mode_dm.py:203-218`), stamps the sender as a bot author (`tools/bot_mode_dm.py:245-248`) and tells the model to paraphrase, never paste, the owner's words (`tools/bot_mode_dm.py:86`); its answer is a dispatch acknowledgement, not a delivery receipt (`tools/bot_mode_dm.py:79`); Bot Chat admission keeps delivery ids and receipts and refuses on the owner's state, `session_busy` and a live CLI holding the session among them (`gateway/session_bot.py:25-37`, `600-610`); memory stores a bot author's turn as the bot's words (`plugins/memory/lina/policy.yaml:23`, `30`); the desktop relay expires envelopes after 900 s (`tools/bot_relay.py:43`); the install retries a busy target every 2 minutes for 6 hours, 7 days for record and worker notices (ops script, not in the repo) | a busy or held target refuses. Durable queue over 14 days: 1527 messages, 1129 delivered on the first attempt, 236 on the second, 162 needed three or more; median queue-to-delivery 122 s, p90 712 s; 104 never delivered (expired or still pending), most to the development agent. Over five days: 895 delivered, 285 needed two or more attempts, worst 2.1 h. One agent's records over three weeks: 24 relay sends, 8 refused because the target's chat was open on another surface, 4 owner relay requests on 4 separate days. A relayed owner request arrives as the sending agent's paraphrase, and its answer arrives in the sender's Bot Chat, not in the conversation the owner asked in | the message is stored on a place the target owns and delivered once, next turn (mid-run only when it answers that agent's question); the sender sees delivered, read and answered; an owner relay keeps the owner's words with their source turn; the answer goes to the asking session; long work becomes a card (section 7) |
| 5 | Worker end notice | the hub writes the end as a card comment, attaches the report and wakes a parked card (`hermes_cli/worker/kanban_link.py:116-140`, `161-182`), and also sends a notice to the agent's chat (`hermes_cli/worker/notice.py:120-142`); a run waiting on a person sends a notice after 120 s (`hermes_cli/worker/hub.py:31`) | two judges: the card session and the main session both reacted to one end (two pull requests, an overwritten verdict); a card waiting behind an unfinished parent is not woken (`hermes_cli/worker/kanban_link.py:176-182` wakes only a `scheduled` card; the install's wake script moved such a card to `todo`, ops script, not in the repo), so nobody judged the run (lina#295); 31 wake comments in five days, wake log 88 lines with 2 errors | a run's end is an event on the run's place, delivered to the card session only, with lina#223's one delivery id; a parent wait does not hold back a run-end delivery (lina#295 Done when 2) |
| 6 | Card event notice | the notifier sends terminal events to the chat that made the card and/or wakes that chat's session by `delivery_mode` (`gateway/kanban_watchers_notifier.py:47`, `517-519`, `574-600`) | one event reached the owner several times: a passive ping, the woken agent's reply, a worker notice; the install now defaults to `wake` (a local patch, not in the repo) | an event goes once, from the agent that owns the place the owner asked on (section 4); `wake` becomes the rule, not a setting |
| 7 | Telegram reply | the replied-to text is prepended to the new message in the same chat session (`gateway/run_inbound.py:1579-1588`) | a reply to a notice about a card reaches the chat session, not the card; numbered replies bound to the wrong list (lina#299); the same final reply sent twice after a slow acknowledgement (lina#211, lina#214) | every sent notice records its delivery id and platform message id with its place; a reply to it is a message on that place; a reply to an ordinary chat message stays agent chat |

Two operating failures sit beside the paths and are covered by the same rules: a card that fell to triage after two
blocks of the same kind (`hermes_cli/kanban_db.py:118`, 6 cards in five days) is a place with no live owner, so a
message to it is stored and the app says "waiting, the card is in triage" instead of looking delivered; and scope added
to a running card by comment can be missed by a run already finishing, which the delivery state now shows (`read` or
`queued_next_turn`) instead of a receipt comment.

## 10. Comparison

Sources were opened and read for this note unless marked snippet-only.

| | Slack (Events API, agent threads) | Linear (agent sessions) | GitHub (Copilot cloud agent on issues and pull requests) |
| --- | --- | --- | --- |
| How a message finds the session | the thread: `thread_ts` is the thread's id and the app stores context per thread; the user's messages arrive as `message.im` events ([AI apps](https://docs.slack.dev/ai/developing-ai-apps), read) | an `AgentSession` is created when the agent is mentioned or delegated an issue; a follow-up user message arrives as a `prompted` `AgentSessionEvent` on that session ([agent interaction](https://linear.app/developers/agent-interaction), read) | assigning an issue sends its title, description and existing comments; later issue comments are not seen. On the pull request, a comment that mentions `@copilot` starts a new session, marked with an eyes reaction, which remembers earlier sessions on that pull request ([existing PR](https://docs.github.com/en/copilot/how-tos/use-copilot-agents/coding-agent/make-changes-to-an-existing-pr), read) |
| Mid-run or next turn | not defined by Slack; each message is a separate event and the app decides | the docs say to insert a `prompted` message into the conversation history and act on it; whether it joins a running loop is left to the agent | a pull request comment starts a new session; steering a running session is done with follow-up prompts in the session view (same page) |
| How the reply comes back | `chat.postMessage` with `thread_ts`, or streaming; a status ("processing") shows while the agent works and clears on reply ([setStatus](https://docs.slack.dev/reference/methods/assistant.threads.setStatus), read) | the agent emits activities (`thought`, `elicitation`, `action`, `response`, `error`); the session's state follows the last activity; an acknowledgement is expected within 10 s of `created` | a pull request with commits, comments and a session log; commit messages link the session log ([risks and mitigations](https://docs.github.com/en/copilot/concepts/agents/coding-agent/risks-and-mitigations), read) |
| Duplicates and retries | a unique `event_id` per event; three retries (at once, 1 min, 5 min) with `x-slack-retry-num` and `x-slack-retry-reason`; a 3 s acknowledgement limit ([Events API](https://docs.slack.dev/apis/events-api/), read) | the webhook must answer within 5 s; up to 3 retries after 1 min, 1 h and 6 h; signed with `Linear-Signature` and a timestamp ([webhooks](https://linear.app/developers/webhooks), read) | answer within 10 s; `X-GitHub-Delivery` is unique per event and stays the same on redelivery ([handling deliveries](https://docs.github.com/en/webhooks/using-webhooks/handling-webhook-deliveries), [best practices](https://docs.github.com/en/webhooks/using-webhooks/best-practices-for-using-webhooks), read) |
| Approvals | the app's scopes; no per-message approval in the platform | workspace admins install the agent; `elicitation` asks the user for confirmation in the session ([agents](https://linear.app/developers/agents), read); stop and other signals exist, their list was not read | only users with write access can trigger the agent, and comments from others are never shown to it; the agent cannot approve or merge; workflows wait for a person to approve them; the requester's approval does not count ([risks and mitigations](https://docs.github.com/en/copilot/concepts/agents/coding-agent/risks-and-mitigations), [reviewing PRs](https://docs.github.com/en/copilot/how-tos/use-copilot-agents/coding-agent/review-copilot-prs), read) |

**Agent to agent.** None of the three products defines one agent sending a message to another agent in the pages read. Linear's agent session events go only to the agent they are addressed to ([agent interaction](https://linear.app/developers/agent-interaction), read), and agents cannot be project members or leads ([agents](https://linear.app/developers/agents), read); a hand-off between agents is not described. Slack's AI app pages describe one app in a thread with a user ([AI apps](https://docs.slack.dev/ai/developing-ai-apps), read). GitHub's Copilot cloud agent responds only to comments from people with write access ([existing PR](https://docs.github.com/en/copilot/how-tos/use-copilot-agents/coding-agent/make-changes-to-an-existing-pr), read). Section 7 is therefore LINA's own design, built on the same place, id and reply rules as the rest.

**What we take.**

- The place is the address (Slack's thread, Linear's session, GitHub's pull request): a message is routed by where it
  was posted, never by parsing its text. Sections 1 and 2.
- One id per event, stable across redelivery (Slack `event_id`, `X-GitHub-Delivery`): the message id and lina#223's
  delivery id. Section 5.
- A visible state for the work while it runs, cleared by the reply (Slack's status, Linear's session states): the
  delivery state under each message. Section 2.
- Asking back is a typed thing, not prose (Linear's `elicitation`): a question has an id, and an answer replies to it.
  Section 3.
- Only those allowed on the place can drive the agent, and the agent can never approve its own work (GitHub). Section 6.

**What we leave.**

- A new session per comment (GitHub): LINA keeps the place's one owning session, so the owner's context and the agent's
  memory stay in one line of work.
- Short acknowledgement deadlines that push the agent to answer in seconds (Linear's 10 s, Slack's 3 s): in LINA the core
  acknowledges by storing the message; the agent answers when it reaches the message, and the state line says so.
- Ignoring messages that arrive after the start (GitHub issue assignment): every message on a live place reaches its
  session, mid-run or next turn.
