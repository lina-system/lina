# File delivery reference

Read-only source: devflow `lib/task.sh`, `task_manifest`, `task_snapshot`, and
`task_land`. No source process or review loop was run.

## Seen

Completion fingerprints all output files and the report, verifies a separate
snapshot, and compares the live result again at landing. Atomic directory
creation claims a fresh versioned bundle. A failed copy retains partial files.

## Inferred

LINA must record a copy intent before touching the destination so an interrupted
copy is discoverable after restart. Snapshot material references and the done
record must commit together. Only a destination read supplies file evidence.

## Unknown

The reference does not establish LINA's ledger, material ownership or tool
authority. The Hermes source was unavailable; no Hermes behavior is inferred.
