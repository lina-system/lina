# One-time coordination switch

This operator command is built from source and is **not in a LINA release**:

```sh
go build -o /tmp/crw-takeover ./tools/crw-takeover/cmd
/tmp/crw-takeover approve CORE_SOCKET MANIFEST JOURNAL 2m
/tmp/crw-takeover apply CORE_SOCKET MANIFEST JOURNAL 2m
/tmp/crw-takeover status CORE_SOCKET MANIFEST JOURNAL 2m
/tmp/crw-takeover rollback CORE_SOCKET MANIFEST JOURNAL 2m
```

Use one owner and one durable journal for the whole procedure. `approve` is an
explicit owner action; `apply` cannot manufacture approval. The Core socket must
belong to that owner. Worker descendants cannot approve or operate a switch.

The manifest is JSON, with these fields:

| Field | Meaning |
| --- | --- |
| `switch_id` | New LINA switch identity |
| `tasks` | Sorted, unique LINA task identities from the verified import |
| `import_snapshot_sha256` | SHA-256 of the verified import snapshot (#214) |
| `quiesced_directors` | Owner's inventory and confirmation that all independent parent sessions, flow engines and other hosts have stopped directing these tasks |
| `executable` | Absolute path to the pinned external relay executable |
| `state_directory` | Absolute path to the single production coordination scope |
| `app_server_socket` | Absolute path to that scope's App Server socket |

The import must create tasks with `external_control: true` and the correct person
identity. They cannot run in LINA even before approval. Imported source ids are
not accepted as task identities. The operator checks the import report and its
snapshot before approving; this command does not implement the importer (#214).

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
