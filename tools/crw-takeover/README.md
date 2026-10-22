# CRW takeover operator tool

This one-time, offline tool maps a verified export into LINA's versioned records.
It is excluded from both binary and source releases, along with its acceptance
scenario. Build it from a repository checkout. It never contacts a tracker,
starts a worker, stops CRW, copies a worktree, or grants authority.

Start the installed Core once, obtain the person's and agent's LINA ids from its
welcome, then stop Core for the import. The same Core lock fences the importer;
a running Core or another importer prevents writes. Keep the original snapshot
and a backup of the destination before the owner-approved operational switch.

```sh
go build -o /tmp/crw-takeover ./tools/crw-takeover
/tmp/crw-takeover --state-root /path/to/lina-state \
  --snapshot /path/to/verified-export.json --sha256 VERIFIED_SHA256 \
  --person LINA_PERSON_ID --agent LINA_AGENT_ID > /path/to/takeover-report.json
```

The hash must come from the independently checked export, not from an unchecked
file accepted just because it hashes consistently. Inputs are read-only and
limited to 64 MiB. The tool verifies the exact bytes and source counts before
opening the destination. It refuses an unknown/trashed agent or different owner.
No real exports or migration reports belong in this repository.

## Export version 1

Export one frozen tracker snapshot, CRW observation records and optional intent
cards into one JSON envelope. The export is an operator format; it does not
claim to be the native schema of any external tracker. Check the source inventory
against the original export before passing its hash. Records not included in the
export cannot be discovered by an offline importer.

```json
{
  "version": 1,
  "counts": {"tracker": 1, "crw": 1},
  "records": [
    {"source":"tracker", "kind":"issue", "id":"example-issue",
     "title":"Write the report", "done_condition":"Evidence checked",
     "payload":{"scope":["report"], "decision":"Use measured evidence"}},
    {"source":"crw", "kind":"receipt", "id":"example-receipt",
     "title":"Prior report receipt", "payload":{"outcome":"ready_for_review"}}
  ]
}
```

Every row needs `source`, `kind`, `id` and a single-line `title`. Optional fields
are `body`, `done` (boolean), `done_condition`, `deadline` (YYYY-MM-DD), `intent`,
`parent`, `project` and `payload`. Bindings are objects with `source`, `kind` and
`id`; forward references work and become LINA ids. Missing, duplicate or cyclic
bindings are reported lost. All original fields, including unmapped fields,
remain in an indented source-observation block. The normalized fields are LINA
front matter; the source key and snapshot digest are provenance, never a product
lookup key. A record is limited to 1 MiB.

| Source | Kind | LINA record |
| --- | --- | --- |
| tracker | initiative | goal, always draft until the person confirms |
| tracker | project | project with a mandatory done condition |
| tracker | issue | todo |
| tracker | milestone | todo with mandatory deadline and done condition |
| tracker | plan, completion_criteria, decision, scope, parent_binding | reference document |
| intent | intent_card | intent card, preserving exact intent text and parent |
| crw | relay_store, scope, assignment, relationship, link, parent_binding, attempt, receipt, verdict, acknowledgement, branch, worktree, completion_criteria, decision | observed reference document with mandatory raw payload |

CRW observations do not populate active runs, permissions, Core verdicts or
worker queues. Historical branches and paths are text, never destinations for
file operations. Tracker done state is retained, except that initiatives remain
drafts. A CRW verdict is still a historical observation. Product code can read
the records through its normal `read_record` tool using the reported LINA ids.
The import does not implement plan confirmation or the operational switch.

## Report and retries

The JSON report contains one row per input position: its source identity when
available, `imported`, `lost` or `unsupported`, a reason for anything not imported,
and the LINA id for an imported record. A malformed record still has its input
position. Unknown kinds are unsupported; invalid or unavailable records are
lost. Loss, an incomplete/duplicate report, a changed snapshot, a storage error,
or an unwritable report exits nonzero. Unsupported kinds are fully accounted for
and do not fail the run. Inspect all outcomes before any switch decision.

`system/crw-takeover-journal.json` in the destination is private operator state.
It binds the single snapshot and actor and durably reserves random ids before
publishing any record. Keep it with the state backup; never delete or replace it
to force another import. There is no product API or schema for this journal.
On retry, reserved ids reconcile against the records repository, including a
crash after publication but before its journal acknowledgement. Already imported
records report `reused: true`; the retry creates no records or commits and does
not overwrite subsequent user edits. Deleted imported records fail visibly.

Records are committed individually; a failed run may have imported earlier rows.
Keep its report, fix transient destination failures, and retry the same verified
snapshot. To undo the import, restore the pre-import backup as an explicit owner
operation; this tool never deletes originals or rolls back unrelated work.

## Verification

```sh
go test ./tools/crw-takeover/... ./internal/archtest/...
go test ./e2e -run '^TestM3CRWTakeoverInstalledCore$' -count=1
node --test scripts/verify/scope.test.mjs
node --test --test-name-pattern='source archives exclude' scripts/release.test.mjs
```

The acceptance scenario is synthetic and credential-free. It imports with the
real operator binary, removes the export, and runs the installed Core's ordinary
model/tool path to read the imported record, reject lookup by source id and save
a new durable result. Restart retains the imported LINA identity.
