# internal/core/messenger implementation notes

Design authority: [integrations.md](../../../docs/design/integrations.md#messengers) and
[conversation-and-memory.md](../../../docs/design/conversation-and-memory.md#messenger-records).
These notes record how this package meets them; they do not restate the contract.

## Holder of a record

`conv_event.agent_id` is not nullable, and a messenger conversation has no
participating agent. Admission therefore stores the source's holder id,
`messenger:<platform>:<account>`, as the holder of every record the source
produced. It is not an agent id and never matches one, so no agent reads a
messenger conversation as its own. A message LINA wrote in the person's name
carries the requesting agent's id instead, because that row is that agent's.

## One transaction per page

`AdmitPage` reads the chat's existing records and the chat's effects first, then
admits the page through `ledger.Store.AdmitBatch` with an `AfterAdmission` hook.
The hook writes the record row, its revision row and, on the batch's last event,
the chat's cursor and coverage. The ledger prepares oversized bodies before the
canon transaction, so material bytes never extend the writer's hold. A page that
adds nothing still commits its cursor and coverage in one transaction.

The page is the unit of resumption: a page that never commits leaves the cursor
where it was, so the next read sees it again.

Reply and thread targets are resolved before that batch is written, by the
same placement an import run uses. A target already committed in the chat uses
that record's event. A target on the same page is written first, and the reply
names that event. Each target names one revision: when a committed target is
edited on the same page, the relation names the new revision only. A target that is neither committed nor on this page adds no
relation, and the message is still kept. A cycle keeps the relations that can
name an event already written.

## Speaker kind

The adapter's `Route` is the only input: `person`, `automation`, `system` and
`external_participant` map to their messenger entries in `internal/core/guard`.
`agent` is decided by Core: a own-account message whose id matches a recorded
effect that is `sent` or `unknown` is admitted as an agent record with that
effect's agent. While a matching effect is still `pending`, the page is held
back (`ErrPageWaiting`) instead of being attributed to the person, so the cursor
does not pass a message whose author is still unknown.

Non-person bodies carry the `third_party` tag; text is masked at admission by
the ledger, including murmur's URL rules in `kit/secret`.

## Edits and deletions

An edit is a new event id per revision that `supersedes` the previous revision
and is linked by `messenger_record_revision`. The record row names the newest.

A platform-reported deletion is applied through the one retraction path
(`retract.Store`), which hides the event at once from every evidence reader,
purges its document from the one index, and keeps it in the trash for 30 days.
The removal-list line keeps `messenger/<platform>/<account>` and
`<chat>/<message>`; the record row stays as a `deleted` tombstone, so a later
read or import adds nothing. A message absent from a later read is not a
deletion and stays visible.

`retract.Open` now also resolves event targets. It accepts only events whose
origin is `messenger`, because deletion of a native conversation has its own
owner; the event row itself is never rewritten, since the ledger is append-only.

## Which adapter a source uses

The service selects the adapter by the name stored on the source, not by the
platform. Discord's bot and personal account are two names. Every call also
receives the source account, so one adapter implementation can serve several
accounts without writing one account's chats under another.

## Imported records

`AdmitImport` takes one chat of a memory importer's run through the same
transaction as a platform page: masking, speaker entries, relations and
materials. A source row's own reference is `messenger_legacy_ref`, keyed
`(system, instance, kind, key)` and pointing at the record; the platform
reference on the event stays the live one, so a source connected later joins the
imported records. One run admits one row per platform key, murmur's archive value
wins over a live value, a changed row without a later edit is reported as a
conflict, and a record whose stored speaker kind differs from the confirmed map
gets a new revision that supersedes it. A record whose reply or thread target is
in the same run is admitted after that target, whatever their source order, so
its relation survives; a cycle of replies keeps the relations it can.

## Coverage

`messenger_coverage` records each complete read's range and time. Intermediate
pages advance the cursor only. `Store.Freshness` derives fresh, stale or gap from
the newest read and the adapter's declared interval and read window; nothing
about freshness is stored.

## Live events

An adapter's pushed event reaches `Service.Apply`: an edit or new message is
admitted with `AdmitMessages` without moving a cursor or coverage. Deletions
travel through the retraction path. A deletion without a chat is resolved by
`DeleteSourceMessages` across chats where the id is live. `Service.RunLive`
subscribes connected sources. One per-source adapter serves sync and events.

## Adapters

The [Telegram adapter](telegram/REFERENCE.md) ships with Core, using its secret
store through an account-specific factory. The [Teams adapter](teams/README.md)
polls Graph through its installed service. Other platforms are tracked by #1070
and the per-platform issues. Writes are recorded and matched here; write verbs,
grants and approvals belong to #1084. Learning from the person's records is #1075.

Sender profile emails, when supplied by an adapter, are stored as evidence in
`messenger_author` with the source account and platform author ID. They do not
change ledger attribution or create an automatic person merge.

Live events and history pages serialize revision planning and commit through a
cancellable, per-source admission lock. Platform requests happen outside that
lock, so waiting for a provider does not hold up live admission.
