# Common filesystem

This contract fixes the filesystem under the main LINA Core: the state root and its layout, where every agent's records live, who writes each area, how a material keeps its identity, where versions and derived data live, how LINA reaches a person's folders and repositories without copying them, how material enters and leaves LINA, and what a backup generation holds. It is normative: implementations must follow it, and any change to it is made in this file and joins dev under [integration.md](../policy/integration.md). Runtime proof belongs to the implementation issues that consume it.

## Scope

The main LINA Core keeps its state on a Linux filesystem, whether LINA OS is the device operating system, LINA OS runs in a VM, or LINA Core runs on a general Linux system. Every install uses the same relative layout. The installation mode changes where the layout is mounted, never its shape. Installation modes are defined in [product-families.md](product-families.md).

A Node keeps only its own execution state: the grants it holds and the receipts it has not yet delivered. It never holds canon, materials or a version space. Grants, receipts and provenance are defined in [main-authority.md](main-authority.md).

This contract covers:

- the state root, its areas, and the writer and backup unit of each area
- where agents' records live, and what agents share
- the rules for every write outside the state root
- asset identity: asset id, revision, content hash, observed path and declared external keys
- connected sources: how LINA reaches a person's folders and repositories without a copy
- the version space
- the split between original bytes and derived data
- import and export
- backup generations and restore

What LINA does with materials, how the library presents them, editing a person's files in place and deletion propagation are defined in [materials-and-knowledge.md](materials-and-knowledge.md).

## State root and layout

There is one state root per main, written here as `<state-root>`. Every path below is relative to it. One state root holds the whole main: every agent, the people it serves, the common knowledge and everything the agents share.

| Area | Relative path | Holds | Writer | Backup unit |
| --- | --- | --- | --- | --- |
| Canon | `canon/` | LINA Core's SQLite canon: agents, the people it serves, ledgers (conversation events, with the bodies of results), the summary trees, memory items with their holder (an agent id or `all`), the legacy references and import batches of imports, materials metadata, the connected-source registry, the plugin registry, the channel connections, bindings, scopes and gateway cursors, standing grants, machine state (queues, cursors, leases, claims, idempotency keys), and derived indexes including the index of the records repository | LINA Core | Backup generation |
| Version space | `versions/` | Local Git repositories for versionable text and metadata, including each agent's persona and personal text under `versions/agents/<agent-id>/` | LINA Core | Backup generation |
| Records repository | `versions/records/` | The person's human-readable work and knowledge records (goals, 일 (project), 할 일 (to-do), 계속 챙기는 것 (area), memos, knowledge documents) as files under a PARA layout | LINA Core | Backup generation |
| Imported materials | `materials/user/<asset-id>/<revision>/` | Bytes of material a person handed to LINA, when they are not kept in the version space | LINA Core | Backup generation |
| LINA materials | `materials/lina/<asset-id>/<revision>/` | Bytes of LINA's own outputs, when they are not kept in the version space, including the large bodies of ledger events and the large outputs of an imported install | LINA Core | Backup generation |
| Derived data | `derived/<asset-id>/<revision>/` | Regenerable descriptions, transcripts, structure and indexes | LINA Core | Backup generation, recorded by source revision |
| Conversation workspace | `workspace/` | The working directory of conversation tools, shared by every agent | LINA Core conversation tools, inside the sandbox | Backup generation |
| Parent clones | `work/clones/<repository-id>/` | Clones of the repositories that work runs against, one per repository | LINA Core | Not in a backup generation |
| Task workspaces | `work/tasks/<task-id>/` | The worktree or the non-Git work directory of each task | The worker of that task, inside its grant | Not in a backup generation |
| Run workspaces | `work/runs/<run-id>/` | The scratch directory and transcript of each background run | LINA Core creates it; the run writes inside it under its run scope | Not in a backup generation |
| Plugin data | `plugins/<plugin-id>/` | The data directory of a local plugin server that runs in the sandbox | That server, inside its grant | Not in a backup generation |
| Import staging | `staging/import/` | Material on its way in, and the snapshots of a memory system being imported | LINA Core | Not backed up |
| Export staging | `staging/export/` | Material on its way out | LINA Core | Not backed up |
| Installation state | `system/` | Install configuration and component events; LINA Core's control files under `system/core/` (lock, epoch record, run sentinel, halt marker, restart request, main id, local config including the persona source path), written by LINA Core, never in a backup generation and preserved by a restore except for clearing a verified recovery's halt latch | The install and each component's event writer; LINA Core for `system/core/` | OS root snapshot, where the install has one |
| Secrets | `secrets/` | LINA's secret store when no OS keychain is available: tokens, API keys, client secrets, request header values and server environment variables of plugins, the credentials of model-provider accounts, bot tokens of channel connections, Google client credentials and LINA's other secrets, each file with mode 0600 and each plugin, account or channel secret keyed by the id of the person it belongs to ([integrations.md](integrations.md), [runtime.md](runtime.md#accounts-and-sign-in)) | LINA Core | Not backed up |
| Backup generations | `backups/<generation-id>/` | Backup generations of the main | The backup procedure | Is the unit |
| Removal list | `backups/removals` | Every retraction, trash recovery, tombstone and source disconnection of the main, appended as each happens | LINA Core | Beside the generations, in none of them |

Rules for the layout:

- Only LINA Core writes canon, the version space, the materials areas and derived data. LINA APP, Node, the library view and generic file tools never write there.
- The records repository is canon in files: only LINA Core writes it, and the database index is derived from it and can be rebuilt at any time. Machine state stays in canon.
- An agent is data in this layout: its row and records in canon, keyed by its agent id, and its repository `versions/agents/<agent-id>/`. Every other area is shared by all agents, and every record of a write names the agent that made or asked for it next to the person id.
- Deleting an agent moves its persona revisions, emotional state, conversations and personal knowledge to the trash for 30 days, and then deletes them permanently with tombstones ([conversation-and-memory.md](conversation-and-memory.md)). Common knowledge the agent wrote stays, with its agent id as the source.
- Conversation tools write only in the conversation workspace and in the scope of their grant. The sandbox that enforces this is defined in [runtime.md](runtime.md).
- Every agent's conversation tools work in the same `workspace/`. Overlapping writes there are handled and recorded as [runtime.md](runtime.md#tools-and-sandbox) defines.
- The work area is everything under `work/`. Every task record in canon names the person who owns the task and the agent that asked for it. What clones, task workspaces and run workspaces contain, and how long they are kept, is defined in [work-and-delegation.md](work-and-delegation.md).
- Backups live under `backups/`, beside the areas they capture and never inside one of them. A generation never captures `backups/`.
- Configuration and secrets have their own writers. They never live under `canon/`, `versions/`, `materials/` or `derived/`. LINA's secret store is the OS keychain where one is available, and `secrets/` otherwise.
- Renaming any directory in this table is a contract change, made by a change to this file.

## Writes outside the state root

Every write LINA makes outside `<state-root>` is an effect under a grant whose scope names the target, checked before the effect as [main-authority.md](main-authority.md) requires. On top of that:

- Export leaves only through `staging/export/`, to a destination the person chooses.
- An edit to a file in a connected source follows the in-place editing rules in [materials-and-knowledge.md](materials-and-knowledge.md).
- LINA's own secrets in the OS keychain are its secret store, not a write to a person's files.

## Asset identity

Every material carries an asset id. The asset id, the revision, the content hash and the observed path are distinct values, and the system never derives one from another.

- A rename or move changes only the observed path. The asset id and the content hash stay the same.
- An edit keeps the asset id. The revision advances and the content hash changes.
- Two files with identical content get two asset ids. A matching hash never merges them, and a matching name or path never merges them either.
- After a move or a rename the material is still found by its asset id, so collections, relations and citations don't break.

An asset id is assigned when LINA first records a file: at import, when LINA produces it, or when LINA first observes it in a connected source.

For materials-area bytes, the asset store assigns a random 128-bit id independently of the SHA-256 content hash. Revisions are increasing positive integers, starting at 1; a write of identical bytes is a no-op. Edits require the current base revision and retain earlier bytes. The observed path is metadata, separate from the internal byte path `materials/<user|lina>/<asset-id>/<revision>/content`. A rename changes that metadata without moving revision bytes. Existing revision files are never overwritten; an interrupted write can leave a gap in revision numbering.

An identical-byte edit still checks the epoch fence, current base revision and active (not trashed) state in a canon write transaction after consuming the input. It returns the same authority errors as a changed edit and removes its temporary copies. `assets.OpenRead` streams revision bytes without holding the store mutex during consumption; size and SHA-256 are verified at EOF, with a mismatch returned as `asset_hash_mismatch`. The caller must read through that terminal result and close the stream; an early close does not verify it. The byte-slice convenience `Read` has an explicit 8 MiB limit (`MaxReadBytes`) and returns `asset_too_large` for larger recorded or actual content, which must use the stream API.

The store records the person and asking agent with each metadata change, revision, export and trash transition. Trashing hides every revision from normal reads and export while preserving its bytes for recovery and backup; restoring keeps the id, revisions and hashes. The store's durable trash transitions are inputs to the removal-list and permanent-deletion coordinator, which owns the 30-day expiry and deletion propagation described in [materials-and-knowledge.md](materials-and-knowledge.md#inside-lina).

The revision depends on where the bytes live:

| Where the bytes live | Revision |
| --- | --- |
| Version space | The commit that holds that version |
| Imported or LINA materials area | The revision directory |
| Connected source that is a Git repository | The commit, together with the file's blob hash. Working-tree content that differs from the commit is observed by content hash and observation time and marked uncommitted. |
| Connected source that is not a Git repository | The content hash and the observation time |

### Renames

LINA treats two paths as the same asset only on one of these grounds:

- a move LINA itself performed
- a move event the filesystem reported to the observer, which is LINA Core for local sources and the Node of the device for sources on that device
- a declared external key, under the rule below

Similarity-based rename detection, such as Git's, matching hashes and matching names never establish that two paths are the same asset. When none of the grounds applies, the asset at the old path is recorded as gone and the file at the new path gets a new asset id.

### Declared external keys

A connected source may declare a key that its owner keeps stable across renames, such as an id that the person's note tool keeps in each note's front matter.

- LINA records the declared key with the asset.
- LINA follows a rename by the key only when exactly one file in the same connected source carries that key at the observed revision.
- When two or more files carry the same key, LINA ignores the key for all of them and identifies each file by the other grounds until the duplication is resolved by the source's owner.
- Keys are never compared across connected sources.
- LINA never assigns, edits or removes a declared key.

### Files on Node devices

A reference to a file on a Node device carries the provenance fields of [main-authority.md](main-authority.md): the asset id as the artifact id, the task id, the node id, the observed path, the revision and content hash, and the observation time. Such a reference is valid only under a current grant from the main. A stale grant makes the reference unusable until the file is observed again. Name, path or identical content alone never prove that two files on different devices are the same file.

## Connected sources

A connected source is a folder or repository that stays where the person keeps it and is visible to LINA without a copy. There are two kinds:

- folder
- Git repository: a folder whose root is a Git work tree

A repository that a 목표 (goal) or an 일 connects for development work is not a connected source. It follows [work-and-delegation.md](work-and-delegation.md) and [integrations.md](integrations.md).

The library scope is the set of connected sources. It is the scope that conversation tools read by default, as defined in [runtime.md](runtime.md).

### Connecting

- Nothing becomes a connected source unless a person connects it. LINA never connects a folder on its own. A folder being visible to the machine or to a Node doesn't make it a connected source.
- The act of connecting is the approval. Connecting adds the source to the default read grant of every agent. There is no separate import step and no per-file approval. The connected-source registry records the person who connected each source.
- On LINA OS, the person's data area on that device is offered as one connected source when the person sets up the device. On a general Linux main and on every device with a Node, the person connects the folders they choose.
- Connected sources do not overlap. Connecting a folder that lies inside an existing connected source makes it its own source, and the outer source excludes it. A file belongs to at most one connected source.
- Nothing under `<state-root>` is ever a connected source.

### Reach

| Where the source lives | Who reads and writes it |
| --- | --- |
| The LINA OS main device | LINA Core, locally |
| The machine of a general Linux main | LINA Core, locally |
| A device with a Node: a desktop install on Linux, macOS or Windows, or the host of a VM install | The Node of that device, under a grant from the main |

Whatever device holds the files, the materials metadata, observed revisions and derived data of every connected source live on the main.

### No copy

- LINA never copies a connected source into a materials area. The bytes stay with the person.
- LINA keeps on the main only asset records, observed revisions, derived data and the saved versions of its own edits.
- When the source is a Git repository, its commits are the revisions. LINA keeps no second history of it.
- When the source is not a Git repository, the saved versions of LINA's own edits live in the version space. LINA never creates a repository inside a person's folder.

### Observation and state

LINA observes each connected source to learn about new, changed, moved and deleted files. Every observation records the revision and the observation time.

A connected source is in exactly one state:

| State | Meaning |
| --- | --- |
| connected | LINA can observe the source now |
| unreachable | The source exists but LINA can't observe it now: its Node is disconnected, its path is missing, or the observer lacks permission |
| disconnected | The person disconnected it |

While a source is unreachable, LINA may answer from derived data of the last observed revision and marks that evidence with its observation time. It never presents that evidence as current. How the library shows each state is defined in [materials-and-knowledge.md](materials-and-knowledge.md).

### Disconnecting

The person can disconnect a source at any time. On disconnect, LINA:

- stops reading and writing the source at once and cancels writes to it that haven't started
- deletes the derived data it built from the source
- stops exposing the source in search, answers and LINA APP
- keeps the asset records in canon, marked disconnected, so past answers can still name the evidence they used

Reconnecting the same folder is a new connection. Asset identity carries over only through declared external keys.

## Version space

The version space is a set of local Git repositories under `versions/`. It holds every versionable text and metadata of LINA: documents and pages, document projects, notes, meeting notes, imported text, the skills that the person or an agent created, each agent's persona revisions and personal text, the records repository, and the saved versions of LINA's edits to connected sources that are not Git repositories. Skills live in the repository `versions/skills/`, one folder per skill, and an agent's persona and personal text live in the repository `versions/agents/<agent-id>/` ([conversation-and-memory.md](conversation-and-memory.md)).

- Large binaries and media stay outside Git, in the materials areas, linked by content hash.
- The SQLite canon and fast-changing state stay outside Git.
- Each material has exactly one byte store: the version space for versionable text, or a materials area for everything else. Canon records which one.
- Machine state and fast-changing state stay in canon. The structured fields of goals, 일, 할 일 and the other work and knowledge records are front matter of record files in the records repository, not rows in canon; the rules are in [work-and-delegation.md](work-and-delegation.md#storage-of-records).
- The version space never holds the history of a connected Git repository. It refers to that repository's commits.

How versions appear to the person is defined in [materials-and-knowledge.md](materials-and-knowledge.md).

### Records repository

The records repository `versions/records/` is one Git repository for the whole main. Every agent reads the same records ([product-families.md](product-families.md)).

- The four PARA folders are at the top level: the 일 and the 목표 they belong to, 계속 챙기는 것, 자료 (resource) and 보관 (archive), with an inbox for memos. An 일 is a folder that holds its 할 일 and its materials. A 계속 챙기는 것 is a folder. A 목표 is a file that the 일 refer to by id. A record's folder is fixed when the record is created; finishing an 일 into 보관 or promoting a 자료 to an 일 changes fields of the records, and no file moves. A folder is created only when something needs it.
- A record is one Markdown file with the front matter defined in [work-and-delegation.md](work-and-delegation.md#storage-of-records). The id never appears in the path, so a move never changes it.
- Large binaries and media stay outside Git in the materials areas, linked by content hash. 자료 holds the knowledge documents and the links to materials.
- LINA Core commits each change as one saved version and records the person id and the agent that asked in the commit message. Recurring completions are commits, not files.
- Another tool may read the repository. A change made to it by another tool is detected at the next read and handled as [work-and-delegation.md](work-and-delegation.md#storage-of-records) states.
- The canon index has a separate derived head, title/note search and relation edges. Missing/stale derived data is rebuilt only from validated files matching the last Core-published head. Outside edits, additions, deletions and head changes remain untouched and are reported through record tools with change kinds and validation outcomes; outside file content and physical paths are not returned. An outside change also blocks explicit rebuild, so rebuild cannot silently accept it. Core startup remains available to report such changes.
- The repository lives at `versions/records/`, the inbox holds one file per memo, and each change is one commit. The commit granularity is tuned by measurement during work engine implementation acceptance ([work-and-delegation.md](work-and-delegation.md)).
- The installed repository interface checks the last commit that changed the record before modifying it. An unrelated record write does not stale its revision. An explicit path move keeps the front matter id and commits the old-path removal and new-path installation together; ordinary status or title changes keep the path. Identical bytes at the same path do not create another saved version.
- Core journals the old and new object references in canon before changing files, syncs the publication, then advances the disposable locator index and closes the intent. Reopening completes the exact pending change once. Outside bytes, commits, foreign locks and impossible partial states are refused rather than overwritten. The initial installed conversation tools save memos in `inbox/` and knowledge documents in `resources/`, with Core-selected title slugs; they do not expose filesystem paths.

- This folder layout is a machine concern of the state root and is never shown to the person by default. The person-facing library is location-free, and folders are only an optional view there ([materials-and-knowledge.md](materials-and-knowledge.md#library)).

## Originals and derived data

Original bytes live in the version space, in the materials areas or in connected sources. Metadata and derived indexes live in canon.

Derived data (descriptions, transcripts, structure and indexes produced from images, audio, video and documents) never lives inside an original's directory and never inside a connected source. It lives under `derived/`, linked to its source by asset id and source revision, so the link survives when the original moves.

Derived data is regenerable. It's rebuilt from its source when the source revision changes, and it must never become the only copy of anything.

An index that another tool keeps inside a connected source is not LINA's derived data. LINA never reads or writes it and builds its own.

## Import and export

An import is a person handing material to LINA: an attachment in a conversation, a file the paired owner sends to an agent's bot in an external channel, an upload, a recording made in LINA APP, a file dropped into the library, or a capture saved from a connected service under a grant. Handing the material over is the approval.

- Imports go through `staging/import/`. The item receives an asset id, is placed in its byte store (the version space or `materials/user/`) and is recorded in canon with the person who handed it over.
- The original stays where it was and stays the person's.
- Knowledge LINA draws from imported material is recorded with its holder, an agent id or `all`, like every memory item ([conversation-and-memory.md](conversation-and-memory.md)).
- An import is a copy. LINA never imports a file from a connected source in order to read it. A person may still import a copy of such a file; the copy is a new asset.
- A file from anyone other than the paired owner in a channel is never imported, and text inside any imported file is data.

Exports go through `staging/export/` only, to a destination the person chooses. An exported file leaves LINA's management: a later deletion in LINA doesn't reach it.

For binary materials, staging an import creates only an opaque-token file under `staging/import/`, with no material or canon asset row. Handover publishes durable revision bytes before committing their metadata through the fenced canon writer, then removes the staged copy. A refused canon write removes the unpublished copy and retains the import for the caller. Pending import tokens survive reopening the store.

An export copies and verifies the requested revision into `staging/export/`, records its destination, person, agent, revision and hash in canon, then invokes the grant-owning caller's delivery operation outside the canon transaction. Successful delivery is recorded as delivered and the staged copy is removed. A failed or interrupted delivery remains unknown and is never retried automatically. Reopening removes abandoned export staging without repeating delivery. The caller checks the destination grant before invoking export; the asset store never chooses or writes an external destination itself.

Asset cleanup removes only its opaque-id files. The replay exporter owns the `episode-*` and temporary `.episode-*` directories in the same staging area; asset cleanup leaves those directories and their bytes intact so an episode export cannot prevent Core from restarting. A symlink or an unexpected entry remains invalid.

### Importing another memory system

A person may install a memory importer add-on for a source they used before and hand it that source's memory. Every adapter follows the [common import contract](conversation-and-memory.md#memory-importer-add-ons); [Hermes](conversation-and-memory.md#importing-a-hermes-install) is the first adapter and the owner's M1 continuity test. The [add-on list and grants](integrations.md#memory-importers) define what each adapter may read. Core, onboarding and the default install have no dependency on an importer. Ordinary material import above remains a Core operation. For memory imports:

- Installing an add-on grants no source access by itself. The person chooses the source and confirms its identity maps for a run. The adapter checks the registered schema or record format before reading records, refuses unknown layouts, and offers a rehearsal that writes nothing to canon. Import runs in the background with no model call; source bytes are stored before deterministic interpretation, and speaker identity comes from the source route and confirmed maps, never message text.
- The source is read and never written. A SQLite source is copied with the online backup API through a read-only connection into `staging/import/`; a live database file is never copied as a file. A server source is read with read and query calls only, and its answers are stored as received. Sidecar files are copied and their hashes checked. LINA never modifies, moves or deletes anything in the person's install.
- A snapshot in staging is the input of one import run. It is not a material and has no asset id. It is deleted when the run ends.
- The large outputs that the source kept as sidecar files become LINA materials under `materials/lina/`, referenced by the event that produced them. LINA assigns their asset ids.
- The adapter submits the records created, their legacy references and the import batch through Core's write paths, in the same transaction, so they are in every backup generation. It never becomes a canon writer of its own. An import ends by taking a backup generation.
- A legacy reference is canon. It changes only when its record is deleted, and a tombstone keeps it (see Backup generations).
- Removing or disabling the importer leaves the imported records, materials, legacy references and backups usable by Core. Reading, recalling, restoring or deleting that content does not require the source adapter or another source read. A later import is a new requested run under the same schema, conflict and legacy-reference rules.
- Deleting imported content reaches every copy LINA manages. The install and any server it was read from are outside LINA's erasure scope, and LINA says so.

## Backup generations

A backup generation captures the whole main at one point in time: canon, version space, materials areas, derived data and the conversation workspace, for every agent and the common knowledge together, so that a restore never leaves them out of step.

Connected sources are not in a generation. They belong to the person and have their own recovery. The generation holds their registry and observed revisions as part of canon, never their bytes. The work area is not in a generation either; the output files of a task without a pull request, and the branch bundle of a repository without a remote, are kept in the generation as LINA materials once the result is ready ([work-and-delegation.md](work-and-delegation.md)).

A generation manifest records:

- the schema version of the manifest itself
- the generation id and the main id
- the created-at time
- the artifact versions and schema versions in use when the generation was taken
- the SQLite snapshot: relative path, hash and revision
- every version-space repository: relative path and head commit
- every asset in a materials area: asset id, revision, relative path, hash and size
- the source revisions of the derived data included
- the revocation watermark at capture time

The backup procedure must:

1. Quiesce writers.
2. Take a consistent SQLite snapshot with the online backup API. Copying database and WAL files is never a backup.
3. Record the head of every version-space repository, copy the repositories and the asset bytes, and verify every hash against the manifest.
4. Publish the manifest last. A generation without a published manifest doesn't exist.

`lina backup --state-root <path>` requires Core to be stopped, holds the stable exclusive Core lock through publication, and refuses a running Core without writing state while explaining how to stop it (`systemctl --user stop lina-core.service`, or Ctrl+C for a foreground Core).

The coordinator replays and validates `backups/removals` before taking the SQLite snapshot or copying any files. A missing acknowledged line, torn record or invalid chain aborts the backup. The captured revocation watermark is the replayed watermark. Capture holds the main's writer exclusion through publication; a SQLite snapshot alone does not quiesce material or workspace writers.

The schema-1 `manifest.json` includes the capture epoch, artifact versions, SQLite schema version and content revision (its SHA-256), repository heads, material revisions, derived source revisions and a complete file/directory inventory. Source revisions for `derived/<asset-id>/<revision>/` follow that source-revision path. For agent and skill repositories, the published head comes from the captured canon snapshot; their immutable Git HEAD anchors are not current revision pointers. The selected commit object must exist and match its Git hash, declared length and commit type in both the source and copy. Verification streams commit bodies with bounded buffering and cancellation checks, without imposing an additional content-size limit on accepted persona or skill revisions. Other repositories use their Git HEAD. Copy hashes are checked against their captured inputs and material hashes against canon. The coordinator rereads the destination before publication. Empty directories and safe relative workspace symbolic links are retained; links are copied as links without reading their targets. Absolute, dangling, cyclic or chained links that cannot resolve entirely inside the captured workspace are refused, so inspection cannot reach excluded state. Database/WAL file copying, linked protected paths and special files are refused. Secrets, installation/control state, work areas, staging and connected-source bytes are excluded.

Each attempt uses a fresh private `backups/generation-<id>/` directory. Copied data and directories are synced before an atomic rename publishes the synced manifest, followed by directory sync. An error removes only that attempt's unpublished directory. A process killed during copying can leave an incomplete directory without a manifest; it is never a generation and cannot be verified. A later backup uses a new id and leaves published generations unchanged. Verification compares the complete inventory, SQLite and material digests, repository heads and derived source revisions with the manifest, including detecting missing or extra files and directories.

The asset store supplies the materials portion of this procedure: it copies every recorded revision, including recoverable trash, and checks size and SHA-256 against canon both during copying and by reading the copy back. Verification of an existing copy uses the captured manifest's hashes. A mismatch fails the operation and yields no successful manifest; the backup coordinator discards the unpublished partial generation. Staging and unrecorded files are excluded. This package operation does not replace the whole-generation coordinator or the SQLite online snapshot.

Restore rules:

`lina restore --generation <generation-path> --state-root <target-root>` restores with Core stopped; missing arguments and `--help` show restore-specific usage. The generation travels with its sibling removal list. The target root must already have the same installed main identity and compatible artifacts. It uses its own control files, which are neither copied from the generation nor replaced. After the restored canon is verified and reconciled, the valid halt latch is durably cleared; identity, epoch and lifecycle records remain untouched. Compatibility requires the captured installed-artifact versions (binaries, kit and SQLite) to match the installation and the snapshot schema to be supported by the command's registered migrations. The backup command's build string remains provenance and is not a compatibility requirement.

- Restore operates on whole generations. Restoring the SQLite snapshot without its version space and assets, or those without the snapshot, is forbidden.
- Restore uses a compatible release and schema pair. Rolling back binaries alone over state they can't read is forbidden ([runtime.md](runtime.md)).
- Agents, people, receipts, artifacts and ledgers are restored first. Then the latest revocation watermark, and every retraction and tombstone recorded in canon, in any newer generation or in the removal list, are applied again before any read is served. A tombstone is the record a permanent deletion leaves: ids, the legacy references of an imported record, the holder (an agent id or `all`), the agent the item belonged to when it had one, and time, never content. Revoked and deleted items are exposed zero times in search or answers, and a memory tombstone keeps the restored conversation ledger from yielding the forgotten item again ([conversation-and-memory.md](conversation-and-memory.md)).
- Engine thread history and work-area state that the generation does not hold are marked as not restored.
- Before publication, non-terminal external effects lose their captured authority. Channel deliveries captured as queued or sending become unknown, including deferred retries and partially delivered replies, so a generation cannot resend a reply delivered after capture. Completed outcomes and partial-delivery receipts are retained.
- Restore checks the freshness of the generation and an inventory of what it restored that is independent of the manifest.
- The records index in the SQLite snapshot is checked against the head of the records repository in the same generation. A mismatch is rebuilt from the files, because the files are canon.
- After a restore, LINA observes every connected source again before serving from it, except one the removal list shows as disconnected. Observations newer than the generation win.

Restoration is prepared under private staging before any restored read is possible. The source and target removal lists must be prefixes of one append-only history; the longer list wins and a divergent or truncated history is refused. Replay validates acknowledged removals against the copied canon, the available current canon and published generations at both the source and target before it can be published. Canon asset references supply an independent inventory check in addition to the manifest hashes. A durable pending publication blocks Core startup until all areas have been installed; repeating restore finishes it. Resumption locks an installed source main as well as the target and reapplies any newer removal entries before reopening access. Neither a validation failure nor an interrupted copy admits a partial generation. A target canon that SQLite identifies as corrupt or not a database can be replaced when a valid, nonzero durable control epoch survives. Its unreadable acknowledgments are not treated as an empty history: the complete removal list is still validated against every readable current canon and published source/target generation before replay. Missing or malformed epoch controls and non-corruption inspection errors fail closed.

A published generation is never changed, and a permanent deletion never rewrites one. A generation captured before the deletion still holds the deleted content. A restore from it applies the tombstone again before serving any read, as above, so the content is never exposed, and the content is gone once every such generation has passed its retention and been removed.

Every retraction, every recovery from the trash, every permanent deletion and every source disconnection is also appended to the removal list at `backups/removals`, beside the generations and outside every one of them. The list is append-only and holds ids, kinds and times, never content. When a replacement memory is forgotten or explicitly permanently deleted, the same operation records every superseded predecessor and its evidence identities. Automatic trash expiry respects each target’s own deadline. Restore replays those targets before reads even when the generation predates the replacement and contains none of its supersession links. A restore reads the list with the generation, so an item forgotten or deleted after the latest generation stays hidden when canon is lost, including an item still in its 30 days in the trash, an item recovered from the trash comes back, and a source disconnected after the latest generation stays disconnected and is not observed again. Generations move between machines only whole, together with the removal list.

Removal records are versioned JSON Lines with an operation id, sequence, previous digest and SHA-256 digest. One line covers an atomic operation and contains only the action, time, person, target identities and provenance needed to preserve extraction exclusions. The file is private; creation syncs its parent directories as well as the file. The coordinator syncs the line before canon acknowledges it. If that acknowledgement fails, replay finishes the durable intent before any read is served. A malformed or torn line, broken digest chain, or missing or changed line already acknowledged in canon prevents reads; recovery never silently discards an uncertain removal. Replaying the list against an older generation advances its watermark without duplicating operations already present there. A recovery line reverses recoverable trash; permanent deletion cannot be reversed by a later recovery request.

On LINA OS, the OS root snapshot, the boot configuration, the person's home data and LINA's backup generations are distinct recovery units. An OS root snapshot doesn't include the person's home data and is never a LINA Core restore. `system/` follows the OS root snapshot; the areas a backup generation captures follow the backup generation. Neither stands in for the other. The update and rollback procedure that uses these units is defined in [runtime.md](runtime.md).

## Deferred

- The location of a Node's execution state on each OS: set during packaging implementation acceptance, together with the absolute `<state-root>` and the other paths deferred in [runtime.md](runtime.md).
- Which text formats count as versionable, the file size above which bytes stay outside Git, and how the records repository behaves with thousands of small files and rename detection on archive moves: set by measurement during materials implementation acceptance.
- The observation cadence for connected sources, and which move events each OS reports to the observer: set during Node and materials implementation acceptance.
- Backup cadence, retention and size limits: set during implementation acceptance.
- Fault injection during SQLite writes and backups, and the restore measurements that prove hash and reference integrity of one generation: set during implementation acceptance.
