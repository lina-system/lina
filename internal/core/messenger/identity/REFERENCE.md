# internal/core/messenger/identity implementation notes

Design authority: [conversation-and-memory.md](../../../../docs/design/conversation-and-memory.md#sender-identity),
[materials-and-knowledge.md](../../../../docs/design/materials-and-knowledge.md#knowledge-documents-and-people-records)
and the evaluation in [messengers.md](../../../../docs/research/messengers.md#identity-evaluation).
These notes record how this package meets them; they do not restate the contract.

## Reference: murmur's people model

The port keeps murmur's shape and adds links and identifiers. Murmur was read
read-only in `~/code/murmur` (schema, `reads.py`, README) on 2026-10-23.

| Item | Seen in murmur | Inferred for LINA | Unknown |
| --- | --- | --- | --- |
| Person key | `people(source, account, author_id)` with `author_name`; one row per author per account | The identity key is `(platform, scope, user)`, where scope is the workspace or tenant a platform scopes ids by and `user` is the id or the handle | How Slack and Teams spell a workspace id in every payload |
| Names | `author_name` is the latest value seen; `people` reports past names with room counts | Names are kept per time seen and never used as an identifier | Whether a platform reports a rename as an edit or a new author |
| Merging | Never merges by name, nickname or photo; `people` returns identities as they are | Two records merge only on an exact identifier or the person's answer, with both ids kept | Whether murmur's operator ever merged rows by hand |
| Cross-platform linking | None: identities stay per `(source, account, author_id)` | A shared phone or email, or a contact card, links on its own; the person's answer links anything else | Which of the person's platforms show an email for other people today |
| Ambiguity | Not modelled | A shared family phone, a team mailbox, a contact card with two numbers, two links to one identity, or a reused number make a link unusable and ask the person once | How often each case occurs in the person's own sources |
| Repair | Not modelled, because nothing is learned from senders | Removing or correcting a link retracts what cites it and requeues only that identity | How many items one identity produces before the gate is on |

Hermes, Honcho, LCM-X and CRW were named in the porting rule but are absent in
this environment (`~/.hermes` does not exist; `~/crw` holds briefs, reports and
an unrelated workflow repository). Nothing was taken from them, and no behaviour
here is inferred from them.

## Identity key

An identity is `(platform, scope, user)`. `scope` is the messenger source's
workspace when the platform scopes its user ids by one; Core records it on
`messenger_source.workspace` and passes it to every observation. iMessage and
WhatsApp key senders by handle, so the handle is both the `user` and an
identifier of the identity.

The same identity seen in two chats or two sources is one identity. Every
admission of a person's or an external participant's record observes the author
inside the page's transaction (`messenger.Store.WithIdentities`); bots and
platform notices are not observed. The person's own account (`Self`) is recorded
so it is known, and is never linked as another person.

## Identifiers

Phone numbers are E.164 and emails are lower case; a number without a country
code is refused rather than guessed, and an identifier that does not normalize
is dropped instead of failing the page. Each identifier keeps its source and its
first and last sighting. A link made on an identifier is valid from the earliest
sighting of that identifier.

## Matching

The matcher is pure over a people snapshot read before the transaction; the
write happens inside it. It runs on every observation and again for every known
identity at Core start (`Rematch`).

1. Candidate records are the people records that list one of the identity's
   identifiers. More than one candidate is ambiguous (`shared_identifier`): a
   family phone, a team mailbox, or a contact card listing two people's numbers.
2. An identity with an active exact or confirmed link to another record is
   ambiguous (`two_links`).
3. An identifier another identity already showed is a move. Both platforms
   keyed by that number (iMessage and WhatsApp) keep linking on their own; a
   handle that reuses an identifier only an identifier link held (a recycled
   number) is ambiguous (`identifier_reused`) and ends the earlier link's
   validity at the new sighting.
4. Otherwise exactly one candidate makes an `exact` link with every shared
   identifier as a signal; a contact-card source adds a `contact_card` signal.

Weak signals never make a usable link. With no exact candidate, a single record
whose name or alias matches a display name makes a `proposed` link; two records
with the same name propose nothing. A rejection or a removal is kept and that
proposal never returns. Proposals are offered in one batch of at most five.

A link is usable when it is `confirmed`, or `exact` and unambiguous, and its
validity covers the time asked about. Ambiguity is derived, never stored.

## Person actions and repair

`Confirm` writes a confirmed link with the person's words as the answer signal
and answers the open question. `Reject` keeps the link. `Remove` marks it
removed, retracts the items it produced through `retract.Store`, and queues only
that identity. `Correct` moves the identity to the right record, retracts what
the old link produced and requeues that identity.

`Merge` records both record ids, moves the links and the items of the first
record to the second, and `UndoMerge` puts each link and each item that still
exists back on the record it came from. A forgotten item moves back still
forgotten. A permanently deleted item stays gone, and neither blocks the undo.
`Split` moves the identities the person chooses and the items their evidence
produced. Item moves run through `memory.ReassignSubject` in the same
transaction, so an item keeps its evidence, holder and scope.

What a link produced is recorded in `identity_artifact` (memory items, and the
kind reserved for people-record entries) and a citation of the link as external
evidence counts too. The removal list is durable and idempotent, so it runs after
the link's own transaction; a failure surfaces and the link is already unusable.

A repair leaves one row per identity in `identity_requeue`, from the source's
learning watermark or, without one, the identity's first sighting. Extraction
about matched senders (#1077) reads it; nothing else is queued.

## People records

A people record is a record of kind `person` under an area or a resource. Its
aliases, kept when two records merge, are the front matter's `names`; its contact
data sits in its own `## Contacts` section with the source of each item
([materials-and-knowledge.md](../../../../docs/design/materials-and-knowledge.md#knowledge-documents-and-people-records)):

```
## Contacts

- phone +82 10-1234-0001 — iMessage profile 2026-01-02
- email anna@example.com — Slack profile 2026-01-02
```

`records.Store.People` returns only the owner's active records; the title takes
part in name proposals. An archived record is not matched, and a malformed line
inside the contact section is refused rather than guessed at.

## Evaluation

`testdata/identity/synthetic.json` holds the hard cases; `lina identity-eval`
runs it and prints the measures. The synthetic gate is no wrong usable link and
every planted ambiguity. `--private <dir>` runs the owner's labelled export and
writes `counts.json` with counts and rates only. Its `gate` is the owner-sample
gate: the synthetic conditions plus at least 300 usable links. A shorter sample
records `gate` false and the command exits unsuccessfully. The files are
`people.json` (the same shape as the set's `people`), `identities.json` (`SetIdentity`
without `expect`) and `labels.json` (`{"<platform>/<scope>/<user>": "<record id>"|"unknown"|"self"}`).

`Evaluate` replaces the store's people source with the set's, observes every
identity in time order, and then measures. It removes one link as its repair
check, so a store is measured once.

## Not in this increment

Learning about matched senders, including writing items and `identity_artifact`
rows, is #1077 and stays off until the gate holds. Matching a newly created
people record without restarting Core is not wired: Core rematches at start and
on every observation. The person's actions reach no client surface here.
