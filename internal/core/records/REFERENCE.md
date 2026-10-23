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
