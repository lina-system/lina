# Recurrence and candidate reference observations

Design authority: [C-54](../../../docs/design/go-core.md#records-and-memory) and
[the to-do record](../../../docs/design/work-and-delegation.md#the-to-do-record).

## Seen

Hermes commit `e06df9c2eae6901f4c88f464e0ae6e753c0428b8`,
`cron/occurrences.py`, distinguishes completed scheduled occurrences from
unproven attempts and journals pending dispatch slots. `tools/todo_tool.py`
tracks worker checklist steps, rather than LINA's personal records.

CRW commit `821d6453aacfc9053b17c3b24a134c0ce4077f80`,
`plugins/crw/skills/crw-canon/references/schedule-contract.md`, explicitly
computes no dates and defines no schedule database.

## Inferred

LINA should keep one record, preserve its recurrence anchor and use the existing
Core civil-time calculator. Record changes and their undo receipts need the same
publication intent and recovery boundary. A worker's success claim is not
completion evidence, and a proposal's confidence is not an admission grant.

## Unknown

No upstream candidate admission or undo implementation was observed. No live
upstream recurrence or DST behavior was exercised. LINA's deterministic
candidate rule comes from its own design; no upstream calendar code is copied.

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-53 repository implementation

The installed write path uses `internal/core/records.Repo`, with go-git's pure-Go object/hash and index codecs behind a confined local filesystem adapter.
Result saving uses the same repository implementation.
Core opens the repository with the published canon migration catalog; `records_state` journals the pending publication and `records_index` holds disposable id/path/blob/revision locators.
Backup reads the published records head from its canon snapshot.
Before the first saved version (also after an interrupted first attempt), it copies the repository files and intent without inventing a commit entry in the generation manifest.
The adapter writes loose objects and a linear local history; it has no Git subprocess, remote, transport or credential dependency.

### C-53 outside-change acceptance status

Outside-change acceptance is not automatic and no accept/merge operation is supplied by this increment.

### C-54 to-do projection migration

`records_todo` is a disposable projection of typed front matter, appended at migration 38.
Migration marks the index stale; startup rebuilds it from published files.
Missing to-do rows also trigger repair even when the saved index head matches.
No canonical record is moved or deleted by the migration.
Legacy tracker imports with verified-snapshot provenance project `active`/`done` to `open`/`completed` and derive a missing deadline source from that provenance, without rewriting the published files.

### C-54 to-do acceptance scenarios

The today fixture includes overdue starts, deadlines, current recurring occurrences, undated tasks, terminal states and zone boundaries.
The installed Core scenario `e2e/m2/todos.json` exercises saves, date and cycle refusals, today/upcoming/anytime queries, rescheduling and restart after index loss.
`e2e/m2/todo-completed-planning.json` covers editing a completed delegated to-do through Core and retaining its fields and completion after restart.
`e2e/m2/todo-timestamps.json` covers timestamp refusals in all date fields, an invalid edit, and valid timed tasks in board/list and dated views.
`e2e/m2/record-recurrence-promotion.json` exercises same-record recurrence completion, model-free advancement at Core restart, memo promotion and undo.

### C-54 operation migration and recovery

Migration 45 adds the canonical `record_operations` and `record_candidates` machine-state tables, without modifying existing files.
Operation membership, undo handles and candidate admission results travel in the records publication intent and commit with its index checkpoint.
Recovery therefore cannot publish a promotion or automatic addition without its undo receipt.
Durable operation notices are projected into the owning agent's conversation independently of a model reply and recovered on startup; existing notices are not republished.
