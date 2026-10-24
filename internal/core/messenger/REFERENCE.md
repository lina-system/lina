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

## Imported records

`AdmitImport` takes one chat of a memory importer's run through the same
transaction as a platform page: masking, speaker entries, relations and
materials. A source row's own reference is `messenger_legacy_ref`, keyed
`(system, instance, kind, key)` and pointing at the record; the platform
reference on the event stays the live one, so a source connected later joins the
imported records. One run admits one row per platform key, murmur's archive value
wins over a live value, a changed row without a later edit is reported as a
conflict, and a record whose stored speaker kind differs from the confirmed map
gets a new revision that supersedes it.

## Coverage

`messenger_coverage` records each complete read's range and time. Intermediate
pages advance the cursor only. `Store.Freshness` derives fresh, stale or gap from
the newest read and the adapter's declared interval and read window; nothing
about freshness is stored.

## Live events

`AdmitEvent` admits one pushed message or deletion through the same admission
path as a page, and writes no cursor or coverage: a live event is newer than the
last complete read, and the next page admits it again without adding anything.
`Service` starts one live reader per connected source, after the source's first
sync, and re-joins the platform's session with bounded backoff after a failure.
A source that needs a new sign-in gets no live reader.

`NewServiceWith` resolves an adapter per source, so two accounts on one platform
never share an adapter's read state. `NewService` keeps the map form for
platforms with one account.

## Platform adapters

`internal/core/messenger/discord` reads the servers of a bot the person added
(#1079). It shares the Gateway session with a channel connection on the same
token. Writes are recorded and matched here; the write verbs, grants and
approvals belong to #1084. Learning from the person's own records is #1075,
which waits for the Discord developer policy reading; nothing here learns.
