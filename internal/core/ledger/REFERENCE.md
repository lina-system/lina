# internal/core/ledger implementation notes

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-22 ledger search implementation

The ledger store exposes `Search` with a required agent and person scope, optional origin, speaker and occurrence-time filters, and an explicit replay opt-in.
A person event establishes conversation ownership; conversations with another person or no established person are excluded, including their agent/tool replies.
The same scope gates pending backfill before any source bytes are read.
The default limit is 20 hits and the maximum is 100.
The migration durably queues existing events; an insert trigger also queues imported rows.
Native admission writes the secret-free text and both indexes in its event transaction.
Search drains the requesting person's and agent's pending rows in batches of 128 before reading hits.
It reads material bytes outside canon transactions, and a missing or unverifiable material fails the search instead of returning cached text as a complete event.
Index metadata records `trigram` and `hangul_bigram`.
One-character queries and two-character queries outside composed Hangul have no postings in this increment.
Ledger search is a package boundary; it adds no TUI or Core client capability.

### C-17 ledger increment

The #57 increment installs all three tables through `canon.Migrations`.
SQLite triggers refuse updates, ordinary deletes and replacement inserts, including on relations and sensitivity spans.
The explicit person-authorized erasure path remains a separate operation to implement; this store exposes no deletion bypass.
`ledger.Store.Admit` commits the event, its relations and its spans in one fenced transaction and returns an event only after commit.
A repeated event id fails without changing the first event.
Parent references must exist in the same conversation; replay, scaffold and relation targets must already exist.
Scaffold references name an event that stores the scaffold once, with the same scaffold kind.

### C-17 material integration

Core's composition layer supplies the ledger's material interface from the assets store, without an import between L3 stores.
It opens one `assets.MaterialWriter` before admission, which reconciles markers from interrupted preparations.
Preparation syncs a marker and the masked body without retaining asset metadata; `PreparedMaterial.Insert` inserts that metadata in the same canon transaction as the event.
Closing the preparation after the transaction checks committed canon, removing uncommitted bytes and preserving committed revisions.
An interrupted cleanup leaves its durable marker for the next opening.
Cleanup cannot revoke an event that committed.
The threshold applies to masked UTF-8 bytes.
Reads verify the immutable material revision; missing or corrupt bytes return event metadata marked `bytes_unavailable`, with no complete message.
Real SQLite and asset-store scenarios, including abrupt process exits at publication boundaries, cover these behaviors; the increment adds no conversation capability to Core's protocol or the TUI.

### C-11 assembly boundary

The #57 store supplies the immutable history records for assembly; preparation, request hashes and assembly lists belong to the consuming preparation/assembly implementation.
Ledger admission neither constructs nor stores a ContextPacket.
