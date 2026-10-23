# internal/core/canon implementation notes

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-16 published migration order

The published development order is retraction at version 11, turn records/control/outcomes at versions 12/13/14, compaction at version 15, and account sign-in at version 16.

### C-16 development catalog compatibility

No release has shipped: incompatible older development layouts are refused with a published-catalog error before writer initialization, without a compatibility shim, renumbering or normalization.

### C-16 initial catalog versions

The registry retains assets at version 1 and appends `InboxSchema` at version 2.

### Citation migration

Migration 54 adds `cite` beside the old canonical pointers. View kinds are
`summary`, `memory`, `person_brief` and `record`; evidence kind `item` maps to
`memory`. Revisions are decimal strings for canon views and opaque Git hashes
for records. An unknown target revision is `0`. Numeric view history uses
`added_revision <= revision < dropped_revision` after integer conversion;
a null drop means the pointer is current. Ordinals are zero-based: summary
children precede direct coverage, and brief sentences flatten in sentence order.
Brief IDs are JSON arrays of person and agent IDs.

The fill groups consecutive historical snapshots of an identical pointer into
one interval, including removals and re-additions. Existing event evidence
quotes hold the resolved full derived text; their UTF-8 byte length supplies the
span, without storing the quote. Other references have no inferred span.
The target index supports later retraction work; no reader switches here.

`WriteEvidenceCitations` and `WriteSummaryCitations` run in the existing writer
transaction. The memory revision helper also covers corrections and maintenance.
Summary rebuild writes citations after all changed children, retaining the
revisions the parent consumed. A citation error rolls back the old rows too.
Deleting an erased memory removes its own citation history. Record citations
are disposable: projection replacement removes them and reads the current file's
`source`. A source matching a retained event ID cites that event; other opaque
sources remain external references with precision `none`. File contents do not
change. The migration clears the projection head to force the initial refill.

The migration also installs the formerly composition-owned person-brief table
when absent. Existing tables and briefs are retained. This does not schedule a
new background job or change brief readers.

The generated store benchmark corpus from #867 (seed 851, 1,094 turns,
20,020 events, 43,818,394 text bytes and 18 summary nodes) produced 1,498
citation rows. Rebuilding this migration from its retained old pointers took
71.945 ms median across three runs (68.663–78.662 ms), including transaction
commit and excluding startup preflight. The table occupied 135,168 bytes;
table plus its four indexes occupied 364,544 bytes by `dbstat`. The source
benchmark commit was `469337d78c8dbc1b1dce9355a9fec34e4647a704`; its three forgets
had already run, retaining the old pointers. These are observed measurements,
not performance thresholds or recall-quality evidence.

### Recall conversation scope

Migration 60 stores one inferred person per conversation: the only person who
has spoken there, or empty when none or more than one have. A person event
updates indexed documents only when that value changes. `recall_conversation`
indexes `(conversation, person)` for the lookup. Repeat messages from the same
person do not rewrite documents or their scope index entries. The person value
on a document stays the one `recall_source` already derived.

### Citation retraction

Migration 63 gives the shared index one retraction path. A removal walks `cite`
from the target to the views that cite it through `cite_target`, so its cost is
the number of documents it hides rather than the size of the store. The walk
replaces the shadow-index bridges that reacted to the old memory and ledger
indexes; those indexes keep their own triggers until the contract step.

A memory removal hides the item and the items whose item evidence reaches it.
An erased or excluded event hides the event and the summaries that cover it;
memory items resting on it stay, as [the contract](../../../docs/design/conversation-and-memory.md#correction-retraction-and-deletion)
requires. Recovery queues only the documents the source view admits again, and
the view itself now walks citation ancestors instead of decoding record JSON.
A document's own deletion also drops its queue entry, so a late drain cannot
publish a hidden source.

The scope check across holders (`memory.Store.RestrictedEvents`) reads protected
event ids from `cite`, so it no longer decodes the JSON of every private or
`never-leaves` item on each call.
