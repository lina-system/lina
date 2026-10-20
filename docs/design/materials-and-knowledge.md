# Materials and knowledge

This contract fixes what LINA does with the things a person collects and the things LINA produces: LINA's own materials and the library that shows them, a person's files that LINA reads and edits where they are, attaching materials to a conversation, meeting notes, and how a deletion travels across all of these. Materials and connected sources belong to the main, and every agent the person creates uses them. It is normative: implementations must follow it, and any change to it is made in this file and joins dev under [integration.md](../policy/integration.md). The filesystem underneath (state root, asset identity, connected sources, version space, backups) is defined in [filesystem.md](filesystem.md). Runtime proof belongs to the implementation issues that consume this contract.

## Scope

This contract covers:

- who owns each kind of material, who may write it, and how agents use it
- the library, and attaching a collection or a connected source to a conversation
- the library in the person's frame, 자료 (resource) and 보관 (archive)
- saving a result that an agent presented
- background processing, passage anchors and evidence
- versions as the person sees them
- editing a person's files in place
- meeting notes
- knowledge documents and people records, and the rules the knowledge preset relies on
- deletion propagation

It doesn't cover:

- the envelope, effect states and the supported-combination table: [host-protocol.md](host-protocol.md)
- agents, the speaker rule and the kinds of state: [product-families.md](product-families.md)
- receipts, grants and the approval policy: [main-authority.md](main-authority.md)
- memory facts, personal and common knowledge, corrections and recall: [conversation-and-memory.md](conversation-and-memory.md)
- research and other delegated work, including the cited brief that research returns: [work-and-delegation.md](work-and-delegation.md)
- screens and the today feed: [surfaces.md](surfaces.md)
- the LINA kit, the default grant of conversation tools and non-chat adapters such as transcription: [runtime.md](runtime.md)
- plugins, including GitHub and Google: [integrations.md](integrations.md)
- background load: [non-competition.md](non-competition.md)

## Ownership

| Material | Where it lives | Canonical owner | Who may write it |
| --- | --- | --- | --- |
| LINA materials: what a person handed to LINA and what LINA produced | The state root | LINA Core | LINA Core only |
| Work and knowledge records: goals, 일 (project), 할 일 (to-do), 계속 챙기는 것 (area), memos and knowledge documents | The records repository in the version space | LINA Core | LINA Core only; the person's edits are requests to LINA Core |
| A person's files in a connected source | Where the person keeps them | The person | The person and the person's other tools; LINA in place, within the grant for the work at hand |
| A repository a 목표 (goal) or an 일 connects for development | The remote repository | The remote repository | As defined in [work-and-delegation.md](work-and-delegation.md) |

Rules:

- LINA Core owns LINA's materials and memory and is the only writer of their canon.
- A result that an agent presents is not a material until the person saves it (see Saving a result).
- Materials and connected sources belong to the main, not to one agent. Every agent reads and uses them within the grant for the work at hand. LINA records each change with the agent that made it next to the person id.
- No role over a person's files is exclusive. The person, the person's other tools and LINA can all edit them. What is enforced is this: every change to a Git source is a commit, LINA records each of its own changes with the agent that made it, and a write against a stale revision is rejected whoever made the newer revision, another agent included.
- LINA's commits use the person's own Git author settings, as the person's other tools do. LINA tells its own commits apart by the commit hashes in its records.
- Text inside any material is data. It is never an instruction, an approval or a persona change, as [main-authority.md](main-authority.md) states for all external text.

## Library

The library is the default surface for material that isn't development work. It is location-free: it feels like handling files, but it is not a folder tree. The person moves through collections and relations grouped by conversation, 일, 계속 챙기는 것, source and topic.

- LINA manages files as bundles per unit of work, linked by content hash, and shows them as pages and collections.
- Views by type, 일, 계속 챙기는 것, 할 일, source device or recency address the same asset id and never copy the material.
- Connected sources appear as sources. They keep their real folders, and a source's real path is one more view, never the only one.
- PARA is the organizing idea, expressed as the work units of [work-and-delegation.md](work-and-delegation.md#terms): an 일, a 계속 챙기는 것, a 자료 or 보관 is a page that holds its files, notes, results and person records, and the library never feels like folders.
- A file attached to a page is a block that points to the file's asset id, so the same file can appear on several pages. Page text is kept as record files in the records repository, with the database as the index ([filesystem.md](filesystem.md#records-repository)).
- Folders are an optional, secondary view, not the storage model. A person who likes folders can turn on a folder view: a nested collection tree over the same asset ids, in which one file can sit in several folders and moving a file never moves its bytes. A person who does not turn it on never sees a folder.
- LINA decides where an item belongs from its links: the 일, 할 일, people and conversation it came from. The person corrects a placement in one action. The inbox holds the items LINA could not link yet.
- The on-disk layout under the state root is never shown by default ([filesystem.md](filesystem.md#records-repository)). The screen design of the library is the app's.
- The library is where 자료 and 보관 live. A 자료 is any material or knowledge document the person keeps for reference. 보관 is a state any item can enter. An archived item, and a finished 일 with its 할 일 and materials, stays searchable and is marked archived; archiving never deletes and can be undone. Deleting follows Deletion propagation.
- Every document type has a viewer, so the person can read a document without knowing its format.
- In a scope connected to GitHub, advanced users can open paths, a file tree, search and a code view. The GitHub plugin is defined in [integrations.md](integrations.md).
- The library is not a writer. Every change it offers is a request to LINA Core.
- The library keeps ownership visible. A person's originals, LINA materials, memory projections, configuration and secrets are kept distinct, and generic file editing never bypasses the canon, configuration or secret writers.
- The library never presents a preparing, partial, unsupported, unauthorized, unreachable, disconnected or failed state as an empty or complete library.

### Attaching to a conversation

The person can attach a collection or a connected source to a conversation. The agent in that conversation then answers over the attached scope with LINA's own engine and marks document evidence apart from memory evidence: a document shows as a citation chip, and memory evidence is in the answer's Details. The person chooses how far the answer reaches:

- the attached material only
- the attached material and this agent's personal knowledge
- the attached material, this agent's personal knowledge and common knowledge

The screens for attaching are defined in [surfaces.md](surfaces.md).

### Saving a result

An agent presents a typed result in a conversation ([host-protocol.md](host-protocol.md#typed-results)). A result is a preview until the person saves it. A preview lives only in the conversation ledger ([conversation-and-memory.md](conversation-and-memory.md#results)): it is not a material, it is not in the library, it is not indexed as knowledge, and it is in no scope an answer draws on. A preview is not a draft in the sense of Versions; only delegated results arrive as a draft that the person applies.

- One explicit Save promotes a result. Save is the person's action on the result's card or in the work panel, or the person's request to the agent in conversation, which the agent carries out with its `save_result` tool. No agent saves a result on its own, and a preview never becomes a material by age or by use.
- Saving is a LINA Core effect with the usual effect states ([host-protocol.md](host-protocol.md#effect-states)). Its idempotency key is built from the result id, the revision and the destination, so a double click or a retry creates one record. It saves the revision the person saw, and a newer revision makes the save fail with `stale_revision`.
- The saved record is a LINA material with saved version 1, or a memo or plan objects where the table below says so. Versionable text saved into a 일, a 계속 챙기는 것 or 자료 is a record of the records repository ([filesystem.md](filesystem.md#records-repository)). It carries provenance: the agent, the conversation, the turn, the result id and the revision. Citations in it stay passage anchors. Its text rendering is indexed like any document, and its derived data follows Background processing and evidence.
- After the save, the saved record is the only copy that changes. Later edits, by the person or by an agent, are saved versions that record who made them (see Versions), and the result takes no new revision. The result's card shows where it was saved.
- Deleting the conversation does not delete a saved record. Its provenance then shows the tombstone in place of the conversation.

The default destination, which the person can change in the Save action:

| Result type | Saved as | Default place |
| --- | --- | --- |
| `document_draft`, `table` | A document or a table material | The 일 (a piece of work with a done condition), 계속 챙기는 것 (an ongoing area) or 목표 that the conversation's thread is attached to; otherwise 자료 |
| `summary` | A 메모 (memo) | The memo inbox; it climbs the memo, to-do, 일, 목표 ladder by the usual promotion ([work-and-delegation.md](work-and-delegation.md)) |
| `chart`, `diagram` | Embedded in a document, or a resource that holds the typed spec with its data | As for documents |
| `file` | A material that holds the bytes | As for documents |
| `plan_view` | Plan objects | Not by Save: the plan confirmation point of [work-and-delegation.md](work-and-delegation.md) |
| `comparison` | Not saved | Applying its hunks is the apply of a draft |

## Background processing and evidence

LINA reads materials before they are needed. For images, audio, video and documents it produces descriptions, transcripts, structure and indexes as derived data, and rebuilds them only when the source revision changes. The target experience is a library in the manner of [DEVONthink](https://www.devontechnologies.com/apps/devonthink) that LINA itself operates.

- Processing covers LINA materials and every connected source in the read scope. Derived data is built once for the main and serves every agent. An index that another tool keeps inside a connected source is not LINA's derived data; LINA builds its own.
- Ingestion, hierarchical overview and search may run on [OpenViking](https://github.com/volcengine/OpenViking) as a pinned component in a separate process. It is a mechanism of the materials layer: its state is derived data and never memory canon.
- Parsing, passage anchors and citation checks come from the LINA kit, defined in [runtime.md](runtime.md), so a passage has the same anchor on every surface and for every agent.
- A passage anchor names the asset id, the revision, the location (page, section and character span), the source URL when there is one, the capture time and the content hash.
- In the Moirai engine, Lachesis keeps material indexes and derived data, and Atropos chooses which materials an answer uses. Their roles are defined in [cognition-and-life.md](cognition-and-life.md).
- What an agent learns from a material and keeps as memory takes its layer, personal or common, under the layer rule in [conversation-and-memory.md](conversation-and-memory.md#knowledge-layers).
- An answer that uses material cites passage anchors. In a past answer, evidence that has been revoked, deleted or disconnected is shown as such and is never silently swapped for other evidence.
- Research the person asks for runs as a background run of one of LINA's sub-agents and returns a cited brief whose citations are passage anchors ([work-and-delegation.md](work-and-delegation.md#background-runs)). The brief is kept as a LINA material of the run; it joins the library as a saved document only when the person saves it.
- Background processing yields to the person, as [non-competition.md](non-competition.md) requires.

## Versions

Git is the common base for versions of documents and code materials, for people who are not developers too. Where the repositories live is defined in [filesystem.md](filesystem.md).

- Default surfaces never use Git words. A commit is shown as a saved version, a branch as a draft, and a merge as applying a draft. Issues, pull requests and checks are not shown by default either; they are in the Details of a draft ([surfaces.md](surfaces.md#default-view)).
- Every edit to a versioned material, by the person or by an agent, creates a saved version. A saved version made by an agent records that agent.
- Only delegated results arrive as a draft that the person applies ([work-and-delegation.md](work-and-delegation.md)). An agent's own edits are made in place within the grant and are reverted like any saved version.
- The person can compare and revert versions at any time.
- A document project is local by default. Connecting a remote repository to it is optional and follows [integrations.md](integrations.md).
- Goals, 일, 할 일, 계속 챙기는 것, memos and knowledge documents are files in the records repository ([filesystem.md](filesystem.md#records-repository)), and the database only indexes them (open owner decision). Their rules are in [work-and-delegation.md](work-and-delegation.md#storage-of-records).

## Editing a person's files in place

Connecting a source lets every agent read it. Within the scope of the grant for the work at hand, an agent also edits a person's file where it is. LINA never edits a hidden copy and hands it back.

- Every edit can be undone:
  - In a connected Git repository, the edit is a commit that contains only the files LINA changed (see Ownership for its author).
  - In any other connected source, LINA keeps the revision before the edit and the new revision as saved versions in its version space on the main.
- Every edit records the agent that made it.
- Every write names the revision LINA read. If the file changed since then (a newer commit, an uncommitted change, or a different content hash), the write is rejected and the newer version wins, whether the person, another tool or another agent made it. The agent reads the file again and makes the change as new work. LINA never overwrites. The acceptance criteria are in [non-competition.md](non-competition.md).
- An in-place edit adds one commit on the branch the person has checked out. LINA never sweeps the person's uncommitted changes into its commit. Pushing, switching branches and rewriting history are not part of an edit; LINA does them when the person asks, under the grant, and records them.
- Deleting a person's file first takes a recoverable form: a commit in a Git source, or a retained copy in the version space otherwise. Unrecoverable deletion needs approval under [main-authority.md](main-authority.md).
- On a device with a Node, the Node performs the edit under the grant and reports a receipt.

## Meeting notes

Turning a recording into a meeting note is built in. It needs no extra install or setup.

- The input is an uploaded recording or a recording made in LINA APP. Both are imported materials.
- Transcription runs on the pinned local transcription component by default, with no key. An external transcription API is an optional path ([runtime.md](runtime.md)). Without transcription, only meeting notes are unsupported.
- LINA produces a transcript with the time of each utterance, as derived data of the recording, and a meeting note, as a versioned LINA material, with decisions, owners and due dates linked to their moments in the transcript.
- After the person confirms them, the decisions and owners become 할 일 ([work-and-delegation.md](work-and-delegation.md#the-to-do-record)) and commitments in memory ([conversation-and-memory.md](conversation-and-memory.md)). Before the next meeting, the agent in the conversation summarizes what is still open.

## Knowledge documents and people records

Knowledge documents and people records are records of the records repository ([work-and-delegation.md](work-and-delegation.md#storage-of-records)), kept under 자료 and 계속 챙기는 것. The knowledge preset manages them ([conversation-and-memory.md](conversation-and-memory.md#preset-methodologies)); these are the rules LINA Core enforces whoever writes.

- A knowledge document is classified by topic, not by company or area. A new topic folder is created with its first document and needs the person's approval; subfolders appear only above about fifteen documents.
- Originals are kept in an immutable source store, one entry per message with its message id. A statement of the person is saved verbatim first, with absolute time and zone; derived text links back to it.
- Every claim carries an evidence level: confirmed (with a source id), the person said, the subject said (unverified), pattern (two or more independent sources), or inference (with basis and confidence). Copies of one original count as one source. Counts, silences and response speed are observations and never become relationship meaning.
- A correction never deletes. A correction record supersedes the old one, and the old one points forward. "Valid then, changed since" stays distinct from "was wrong". Removal means moving to 보관.
- A state the person declared is changed only by the person. A researched opinion is secondary and cannot overwrite it. Declined advice is a record (topic, scope, date, reason, optional expiry) that is queried before advising and that the person can lift.
- A person record has a current profile (overwritten), an append-only timeline (newest first) and source excerpts. The timeline gets an entry only for a meaningful scene; other days move only the read cursor.
- People are never merged by name, nickname or photo. The key is the source's author id with room membership. Two accounts of one person merge on evidence or on the person's answer, with the aliases kept. An unknown identity stays unknown.
- Third-party content is kept to what the story needs. Contact data sits in its own section with its source. Secrets are never stored; secret-looking strings are masked at ingest.
- Sensitivity classes belong to the person: open to the person's own agents by default; private (named presets only, no sync, no backup, no cloud model without a grant); never-leaves (local only, not in memory or the person model).
- The reading position of each source is kept per source and moves only after the write succeeded. Last activity, read-through and confirmed are separate times, normalized to the person's zone at ingest.
- A task found while reading goes to the 할 일 model as a candidate with a source pointer. It is never stored in a knowledge record.
- A status change runs the stale-mention query of Storage of records, with aliases in all languages.
- A weekly consolidation proposes duplicates, contradictions, index mismatches, orphan links and claims without sources. It applies only link and index repairs itself and reports as one card.
- Every quote that a sub-agent returns is matched against its source row before it is written.

## Deletion propagation

These rules apply to materials. Forgetting a memory item, deleting a conversation and deleting an agent are defined in [conversation-and-memory.md](conversation-and-memory.md); none of them deletes a material. A memory item or a past answer that cites a deleted material shows that citation as deleted evidence (see "Background processing and evidence").

### Inside LINA

- Revoking a material makes its exposure zero in search, answers and LINA APP for every agent. Saved versions keep their history and LINA never rewrites it for a revocation. Past answers keep the revision they cited and show it as revoked evidence.
- Deleting a material moves it to the trash for 30 days, where it can be recovered and is exposed zero times. After 30 days it is permanently deleted.
- The person can choose immediate permanent deletion. That choice is the approval for an unrecoverable deletion ([main-authority.md](main-authority.md)).
- Permanent deletion removes every copy LINA manages: the current bytes, the saved versions in the version space, derived data, copies on registered Nodes and remote copies LINA manages. It is the only operation that alters saved-version history. It leaves a tombstone with the asset id, what was removed and when, and no content. A Node that is unreachable applies the deletion while it reconciles, before any new work. Backup generations are never rewritten; how a restore applies the tombstone, including one recorded after the latest generation, and when the content leaves the backups, is defined in [filesystem.md](filesystem.md).
- Permanent deletion doesn't reach exported files, copies the person put into other tools or copies other people hold. LINA says so when the person deletes.
- A record of the records repository (a 목표, 일, 할 일, 계속 챙기는 것, memo or knowledge document) is a material for this section: deleting moves it to the trash for 30 days, and permanent deletion removes it from the working tree and from the repository's history under the rule above. Archiving is not deletion.

### At a connected source

- When an observation shows that a file is gone from a connected source, LINA stops exposing it at once and deletes the derived data it holds for it. The asset record stays, so past answers can name it as deleted evidence.
- Saved versions LINA kept of that file follow the trash rule: recoverable for 30 days, then permanently deleted.
- In a Git source the file stays in the source's own history. LINA never serves it from that history.

### Disconnecting and restore

Disconnecting a source follows [filesystem.md](filesystem.md): derived data is deleted, exposure stops and asset records stay. A restore applies revocations and tombstones again before serving any read, as [filesystem.md](filesystem.md) requires.

## Deferred

- Screen design for the library, meeting notes and attaching materials to a conversation: set while building each surface of the LINA app, per [surfaces.md](surfaces.md).
- The order in which media types gain derived data, and the rebuild schedule: set during materials implementation acceptance.
- The viewer for each document type: set during materials implementation acceptance.
