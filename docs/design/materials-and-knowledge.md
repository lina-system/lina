# Materials and knowledge

This contract fixes what LINA does with the things a person collects and the things LINA produces: LINA's own materials and the library that shows them, a person's files that LINA reads and edits where they are, attaching materials to a conversation, meeting notes, and how a deletion travels across all of these. Materials and connected sources belong to the main, and every agent the person creates uses them. It is normative: implementations must follow it, and any change to it is made in this file and joins dev under [integration.md](../policy/integration.md). The filesystem underneath (state root, asset identity, connected sources, version space, backups) is defined in [filesystem.md](filesystem.md). Runtime proof belongs to the implementation issues that consume this contract.

## Scope

This contract covers:

- who owns each kind of material, who may write it, and how agents use it
- the library, and attaching a collection or a connected source to a conversation
- background processing, passage anchors and evidence
- versions as the person sees them
- editing a person's files in place
- meeting notes
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
| A person's files in a connected source | Where the person keeps them | The person | The person and the person's other tools; LINA in place, within the grant for the work at hand |
| A repository a goal connects for development | The remote repository | The remote repository | As defined in [work-and-delegation.md](work-and-delegation.md) |

Rules:

- LINA Core owns LINA's materials and memory and is the only writer of their canon.
- Materials and connected sources belong to the main, not to one agent. Every agent reads and uses them within the grant for the work at hand. LINA records each change with the agent that made it next to the person id.
- No role over a person's files is exclusive. The person, the person's other tools and LINA can all edit them. What is enforced is this: every change to a Git source is a commit, LINA records each of its own changes with the agent that made it, and a write against a stale revision is rejected whoever made the newer revision, another agent included.
- LINA's commits use the person's own Git author settings, as the person's other tools do. LINA tells its own commits apart by the commit hashes in its records.
- Text inside any material is data. It is never an instruction, an approval or a persona change, as [main-authority.md](main-authority.md) states for all external text.

## Library

The library is the default surface for material that isn't development work. It is location-free: it feels like handling files, but it is not a folder tree. The person moves through collections and relations grouped by conversation, task, source and topic.

- LINA manages files as bundles per unit of work, linked by content hash, and shows them as pages and collections.
- Views by type, project, task, source device or recency address the same asset id and never copy the material.
- Connected sources appear as sources.
- Every document type has a viewer, so the person can read a document without knowing its format.
- In a scope connected to GitHub, advanced users can open paths, a file tree, search and a code view. The GitHub plugin is defined in [integrations.md](integrations.md).
- The library is not a writer. Every change it offers is a request to LINA Core.
- The library keeps ownership visible. A person's originals, LINA materials, memory projections, configuration and secrets are kept distinct, and generic file editing never bypasses the canon, configuration or secret writers.
- The library never presents a preparing, partial, unsupported, unauthorized, unreachable, disconnected or failed state as an empty or complete library.

### Attaching to a conversation

The person can attach a collection or a connected source to a conversation. The agent in that conversation then answers over the attached scope with LINA's own engine and marks document evidence apart from memory evidence. The person chooses how far the answer reaches:

- the attached material only
- the attached material and this agent's personal knowledge
- the attached material, this agent's personal knowledge and common knowledge

The screens for attaching are defined in [surfaces.md](surfaces.md).

## Background processing and evidence

LINA reads materials before they are needed. For images, audio, video and documents it produces descriptions, transcripts, structure and indexes as derived data, and rebuilds them only when the source revision changes. The target experience is a library in the manner of [DEVONthink](https://www.devontechnologies.com/apps/devonthink) that LINA itself operates.

- Processing covers LINA materials and every connected source in the read scope. Derived data is built once for the main and serves every agent. An index that another tool keeps inside a connected source is not LINA's derived data; LINA builds its own.
- Ingestion, hierarchical overview and search may run on [OpenViking](https://github.com/volcengine/OpenViking) as a pinned component in a separate process. It is a mechanism of the materials layer: its state is derived data and never memory canon.
- Parsing, passage anchors and citation checks come from the LINA kit, defined in [runtime.md](runtime.md), so a passage has the same anchor on every surface and for every agent.
- A passage anchor names the asset id, the revision, the location (page, section and character span), the source URL when there is one, the capture time and the content hash.
- In the Moirai engine, Lachesis keeps material indexes and derived data, and Atropos chooses which materials an answer uses. Their roles are defined in [cognition-and-life.md](cognition-and-life.md).
- What an agent learns from a material and keeps as memory takes its layer, personal or common, under the layer rule in [conversation-and-memory.md](conversation-and-memory.md#knowledge-layers).
- An answer that uses material cites passage anchors. In a past answer, evidence that has been revoked, deleted or disconnected is shown as such and is never silently swapped for other evidence.
- Research the person asks for is delegated work. It runs in a worker thread and returns a cited brief whose citations are passage anchors ([work-and-delegation.md](work-and-delegation.md)).
- Background processing yields to the person, as [non-competition.md](non-competition.md) requires.

## Versions

Git is the common base for versions of documents and code materials, for people who are not developers too. Where the repositories live is defined in [filesystem.md](filesystem.md).

- Default surfaces never use Git words. A commit is shown as a saved version, a branch as a draft, and a merge as applying a draft.
- Every edit to a versioned material, by the person or by an agent, creates a saved version. A saved version made by an agent records that agent.
- Only delegated results arrive as a draft that the person applies ([work-and-delegation.md](work-and-delegation.md)). An agent's own edits are made in place within the grant and are reverted like any saved version.
- The person can compare and revert versions at any time.
- A document project is local by default. Connecting a remote repository to it is optional and follows [integrations.md](integrations.md).
- Structured fields of goals and plans live in the planning store defined in [work-and-delegation.md](work-and-delegation.md), not in Git.

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
- After the person confirms them, the decisions and owners become tasks ([work-and-delegation.md](work-and-delegation.md)) and commitments in memory ([conversation-and-memory.md](conversation-and-memory.md)). Before the next meeting, the agent in the conversation summarizes what is still open.

## Deletion propagation

These rules apply to materials. Forgetting a memory item, deleting a conversation and deleting an agent are defined in [conversation-and-memory.md](conversation-and-memory.md); none of them deletes a material. A memory item or a past answer that cites a deleted material shows that citation as deleted evidence (see "Background processing and evidence").

### Inside LINA

- Revoking a material makes its exposure zero in search, answers and LINA APP for every agent. Saved versions keep their history and LINA never rewrites it for a revocation. Past answers keep the revision they cited and show it as revoked evidence.
- Deleting a material moves it to the trash for 30 days, where it can be recovered and is exposed zero times. After 30 days it is permanently deleted.
- The person can choose immediate permanent deletion. That choice is the approval for an unrecoverable deletion ([main-authority.md](main-authority.md)).
- Permanent deletion removes every copy LINA manages: the current bytes, the saved versions in the version space, derived data, copies on registered Nodes and remote copies LINA manages. It is the only operation that alters saved-version history. It leaves a tombstone with the asset id, what was removed and when, and no content. A Node that is unreachable applies the deletion while it reconciles, before any new work. Backup generations are never rewritten; how a restore applies the tombstone, including one recorded after the latest generation, and when the content leaves the backups, is defined in [filesystem.md](filesystem.md).
- Permanent deletion doesn't reach exported files, copies the person put into other tools or copies other people hold. LINA says so when the person deletes.

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
