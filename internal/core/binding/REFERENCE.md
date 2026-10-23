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
input text, and kind and parent words are mapped to stored values. Only a target
naming no open item, or more than one, returns a correction with the allowed ids.

Target questions store their choice with the existing durable question record.
Their resolution appends the selected or cancelled binding in the same transaction
as the answer inbox event. General skipped-question defaults never resolve them.
The optional Atropos judge receives a closed candidate list and revision; its
accepted choices, probabilities and observed latency are ledger evidence.
