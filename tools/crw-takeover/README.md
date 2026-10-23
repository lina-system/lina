# CRW takeover operator tools

The offline importer and the owner-approved coordination switch are separate
operator commands. Neither is included in LINA releases.

## One-time import

This one-time, offline tool maps a verified export into LINA's versioned records.
It is excluded from both binary and source releases, along with its acceptance
scenario. Build it from a repository checkout. It never contacts a tracker,
starts a worker, stops CRW, copies a worktree, or grants authority.

Start the installed Core once, obtain the person's and agent's LINA ids from its
welcome, then stop Core for the import. The same Core lock fences the importer;
a running Core or another importer prevents writes. Keep the original snapshot
and a backup of the destination before the owner-approved operational switch.
An interrupted restore must be completed first: under the lock, the importer
checks the restore publication marker before inspecting canon or opening records.

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

### Export version 1

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
`id`; forward references work and become LINA ids, including native `belongs_to` edges
for Core's relationship index. Missing, duplicate or cyclic
bindings are reported lost. All original fields, including unmapped fields,
remain in an indented source-observation block. The normalized fields are LINA
front matter; the source key and snapshot digest are provenance, never a product
lookup key. A record is limited to 1 MiB.

| Source | Kind | LINA record |
| --- | --- | --- |
| tracker | initiative | goal, always draft until the person confirms |
| tracker | project | project with a mandatory done condition |
| tracker | issue | todo: `open` when incomplete, `completed` when done |
| tracker | milestone | todo: `open` when incomplete, `completed` when done, with mandatory deadline and done condition |
| tracker | plan, completion_criteria, decision, scope, parent_binding | reference document |
| intent | intent_card | intent card, preserving exact intent text and parent |
| crw | relay_store, scope, assignment, relationship, link, parent_binding, attempt, receipt, verdict, acknowledgement, branch, worktree, completion_criteria, decision | observed reference document with mandatory raw payload |

CRW observations do not populate active runs, permissions, Core verdicts or
worker queues. Historical branches and paths are text, never destinations for
file operations. The export's `done: true` completes tracker issues and
milestones; `false` or an omitted `done` leaves them `open`. Initiatives always
remain `draft`; CRW records always remain `observed`. Other tracker records and
intent cards retain their `active`/`done` mapping. A CRW verdict is still a historical observation. Product code can read
the records through its normal `read_record` tool using the reported LINA ids.
The import does not implement plan confirmation or the operational switch.

### Report and retries

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

Earlier imports may store to-dos as `active` or `done` without
`deadline_source`. Core recognizes their tracker issue/milestone provenance and
snapshot digest and projects `open`/`completed` and the deadline source without
changing their files or revisions. A later planning edit takes precedence over
the original snapshot. No reimport or bulk rewrite is needed.

If the earlier importer stopped after saving a to-do but before acknowledging
its journal entry, retry accepts its exact earlier bytes as well as the current
encoding. Both are checked against the same verified snapshot and reserved id.
Different content is reported lost and left untouched. Preserve the journal and
inspect that conflict; do not remove the reservation to force a new import.

Records are committed individually; a failed run may have imported earlier rows.
Keep its report, fix transient destination failures, and retry the same verified
snapshot. To undo the import, restore the pre-import backup as an explicit owner
operation; this tool never deletes originals or rolls back unrelated work.

### Verification

```sh
go test ./tools/crw-takeover/... ./internal/archtest/...
go test ./e2e/m1 -run '^TestM3CRWTakeoverInstalledCore$' -count=1
node --test scripts/verify/scope.test.mjs
node --test --test-name-pattern='source archives exclude' scripts/release.test.mjs
```

The acceptance scenario is synthetic and credential-free. It imports with the
real operator binary, removes the export, and runs the installed Core's ordinary
model/tool path to find and read the imported record, reject lookup by source id and save
a new durable result. Restart retains the imported LINA identity.

## Coordination switch

This operator command is built from source and is **not in a LINA release**:

```sh
go build -o /tmp/crw-takeover-switch ./tools/crw-takeover/cmd
/tmp/crw-takeover-switch approve CORE_SOCKET MANIFEST JOURNAL 2m
/tmp/crw-takeover-switch apply CORE_SOCKET MANIFEST JOURNAL 2m
/tmp/crw-takeover-switch status CORE_SOCKET MANIFEST JOURNAL 2m
/tmp/crw-takeover-switch rollback CORE_SOCKET MANIFEST JOURNAL 2m
```

Use one owner and one durable journal for the whole procedure. `approve` is an
explicit owner action; `apply` cannot manufacture approval. The Core socket must
belong to that owner. Worker descendants cannot approve or operate a switch.

The manifest is JSON, with these fields:

| Field | Meaning |
| --- | --- |
| `switch_id` | New LINA switch identity |
| `tasks` | Sorted, unique LINA work-task identities from the owner-verified inventory |
| `import_snapshot_sha256` | SHA-256 of the verified import snapshot (#214) |
| `quiesced_directors` | Owner's inventory and confirmation that all independent parent sessions, flow engines and other hosts have stopped directing these tasks |
| `executable` | Absolute path to the pinned external relay executable |
| `state_directory` | Absolute path to the single production coordination scope |
| `app_server_socket` | Absolute path to that scope's App Server socket |

The switch requires existing LINA work tasks with `external_control: true` and
the correct person identity. They cannot run in LINA even before approval.
The offline importer above writes versioned records; its record ids and historical
observations do not by themselves register executable work tasks. The operator
checks the work-task inventory, import report and snapshot before approving.
Imported source ids are not accepted as task identities.

The manifest's canonical JSON digest binds the approval to its exact task set,
source snapshot, scope, executable and quiescence declaration. Changing any field
requires a new approval. Never put private operational manifests in this repo.
Use a private journal directory outside the original store and worktrees.

`apply` first checks Core's matching approval and records the original service
state in a synced journal. It invokes the pinned service's `disable` and separately
reads `status`; enabled intent, a held daemon lock, unknown scope, conflicts or
unreadable output refuse activation. The readback must show disabled intent and
no running service. Independently running parent sessions and flow engines are
outside that service: the owner must stop and inventory them before approval.
The tool never guesses their absence from a stopped daemon. The owner must not
re-enable or launch any external director while LINA owns the tasks.

Failures after stopping begins explicitly request rollback in Core. Core first
blocks new runs and waits for all active or unknown executions to be accounted
for. Only its durable release lets the tool restore the prior service intent
and running state. The final independent status read must confirm restoration.
An interrupted process, expired deadline, active execution or failed restoration
returns an error and keeps the journal. Run `status`, reconcile outstanding
executions, then use the same manifest and journal with `rollback`; never delete
the journal or start another coordinator to bypass an unresolved switch.

Rollback retains originals, worktrees, branches, receipts, output files and both
journals. It does not reverse completed work or delete imported tasks. A rolled
back task remains externally controlled in LINA. This procedure never removes a
plugin, tracker record, branch or runtime. Normal LINA operation uses no external
coordination executable, relay or state path.

The synthetic tests exercise refusal, successful handoff, failures, interrupted
execution and explicit recovery. They do not execute an installed CRW service or
prove an owner's real multi-host quiescence inventory.
