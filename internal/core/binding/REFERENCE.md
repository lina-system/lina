# Input binding

`Resolve` ranks an authorized work snapshot, applies the three explicit-fact rules in order, and records an unresolved default when no rule holds. It does no I/O and makes no execution or permission decision. The historical intake replay calls the same `Rule` evaluator but keeps its original focus fallback.

Core composes the snapshot inside native person admission through the ledger's `AfterAdmission` hook. Transaction-local binding metadata stays inline through `ledger.AppendSystemSnapshot`, preserving full titles even above the ordinary conversation-body threshold without material I/O under the writer. The system `input_binding` event links to the person event through both parent and `view_of`; a unique parent index and deterministic id prevent duplicates. Client retries reuse their inbox receipt. Channel owner input and explicit memory statements use the same hook; forwarded and system events do not.

The snapshot reads the previous binding and the last exchange through `retract_visible_event`, so a withdrawn input neither restores focus nor refreshes its age. A reply to a background notice resolves the run from the durable lane delivery record named by the notice's origin locator; the notice body may be stored as a material and is never read during admission. Bundled deliveries naming several runs stay unresolved.

The current binding enters the mandatory Waiting group of the ContextPacket. Steering carries its own binding at drain. Request evidence retains an `input-binding-v1` source and reconstructs it from the authorized ledger for replay. Record activity is a disposable projection of Git commit times.

`TestM2InputBindingInstalledCore` creates and reads a to-do through record tools, checks an exact reference and an offered option during a live turn, restarts Core, retries the consumed input and binds a card input. Rule tests cover normalization, ambiguity, finished work, aging and candidate priority. Core tests cover transactional rollback, context assembly and the synthetic production replay.

Model choices are append-only `input_binding_choice` events keyed by the actual
call and input. `Choice.Request` addresses a request ordinal; the admission event
is retained. Current choices overlay those events, and a created receipt supplies
the next focus identity. `input-choice-v1` context sources reconstruct immutable
choice bytes for request replay.

`NormalizeChoices` decodes a model's binding arguments before validation:
ordinals are optional and placed in message order, a target may be an object, an
id, an exact title, `new` or `none`, an absent or inexact excerpt becomes the
input text, and kind and parent words are mapped to stored values. An exact
candidate id is bound before a leading kind name and colon are read as new work.
A name that matches no open item and exactly one offered item, including a
finished item, binds that item. A stored call is compared before the current
snapshot can drop a parent that has since completed.
Only a target
naming no open item, or more than one, returns a correction with the allowed ids.
A tied alternative that cannot be resolved returns that correction instead of
being dropped.

A work tool that names its target appends the same kind of choice, so the
message's item follows what the tool acts on. A worker session resolves to the
work item it controls, which keeps a person's chosen target authoritative for
`worker_input` and `worker_interrupt`: a session for another task in the same
conversation is refused while a chosen target is in effect.

A person-selected new target keeps that decider. Once its created id is recorded,
a later read, progress check, or follow-up may name that existing item; any
other item stays pending. A model follow-up to a worker task uses the agent
message path and the requesting conversation. An unrelated conversation is
refused before delivery. `worker_input` and `worker_interrupt` resolve their session to
that task before the person's choice is checked. `worker_interrupt` is a stop:
a retained tie asks which task before cancellation. `submit_step_graph` starts
work, so a retained tie asks which project before the graph is admitted.
`build_mock` starts work, so a retained tie asks which target before the goal
is admitted. A goal correction, a deferred tool call, and a refresh that can
release dependent tasks start work. A remote agent call and an artifact handoff
send. Cancelling a timer stops. Reads stay unclassified. A code goal tool resolves
`goal_id` through the goal package's project record id before the same check.
The binding snapshot includes that record as a project. A goal id that does
not resolve is refused. Steering execution uses the selected input's runtime, so a
channel follow-up keeps that channel source. Result tools rebuilt for that
input keep the turn's prepared reply shapes, so the work-method check commits
with the revision. When the selected input belongs to another conversation, the
result grant uses that conversation's active turn. A retained tie is not a settled
reply target or the next focus. A reply that stays unresolved keeps that tie,
so a later control asks before it runs.

Target questions store their choice with the existing durable question record.
Their resolution appends the selected or cancelled binding in the same transaction
as the answer inbox event. General skipped-question defaults never resolve them.
The optional Atropos judge receives a closed candidate list and revision; its
accepted choices, probabilities and observed latency are ledger evidence.

## Showing and moving a binding

Seen, inferred and unknown, from the reference installs named by the porting
rule (`~/.hermes/hermes-agent`, `~/.hermes/plugins`, `~/code/codex-relay-workflow`):

- **Seen:** none. Those paths do not exist on the development host, so no
  reference behaviour for a message-level binding chip, a move list or effect
  re-attachment could be read.
- **Inferred:** none. Nothing about Hermes, Honcho, LCM-X or CRW is claimed.
- **Unknown:** how a reference surfaces a binding, how a corrected target is
  labelled, and whether it re-attaches effects. LINA therefore implements the
  rule from its own contract: [conversation-and-memory.md](../../../docs/design/conversation-and-memory.md#what-a-message-is-about)
  (Showing and moving), [work-and-delegation.md](../../../docs/design/work-and-delegation.md#messages-about-work)
  (Moving a binding) and [surfaces.md](../../../docs/design/surfaces.md#threads-on-screen).

`ChipFor` shows the chip when the final target is not the fresh focus, which is
exactly "leaves the focus, returns to an aged focus or creates an item".
`Offers` lists the other ranked candidates plus `new` and `none`; `Offered`
re-validates a chosen target before Core writes anything. `Tally` counts moves
per hundred bindings for each decider.

Core stores what the bound turn did in `binding_effects` before a later move
needs it: a created record keeps its id, an edit keeps the bytes and revision it
replaced, a steer keeps its text and target, and work that started from the
turn is marked as having left the person. `binding_moves` holds each move with
its effects and outcomes, the label (the chosen target) and the state snapshot
the replay case is rebuilt from. The counter and the case export read only that
canon, so both survive a restart.
