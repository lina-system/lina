# Roadmap for the Go core and TUI

This document is informative. It orders the Go design of LINA Core and the terminal UI into three phases by what becomes usable, and lists the epics and issues of each phase with their done conditions and dependencies. The roadmap is issue #1 of this repository, and the plan of record is the GitHub milestones and issues listed in [roadmap-github.md](roadmap-github.md). The earlier Linear project is a read-only record. This file is a derived reading of the contracts in this directory, [go-core.md](go-core.md) and [tui.md](tui.md). It decides nothing that they decide, and where it differs from them they win. Issue ids such as `G1-01` are the tracker's stable names, and an id is never reused. Listing an issue here does not mean it is filed, started, implemented or accepted.

## Conventions

- `Gp-nn` issue ids, where `p` is the phase digit. An id is never changed or reused. A new issue takes the next free id of its phase and may be depended on by lower-numbered issues.
- "Done when" lines are observable: ids, codes, states, counts, bytes. Never prose, wording or timing luck ([AGENTS.md](../../AGENTS.md)).
- "Design refs" are `C-nn` of go-core.md, `T-nn` of tui.md, `CP-nn` of the change proposals in go-core.md, and contract sections.
- (P) means proposed: written down before the measurement that uses it. A miss selects a fallback and never loosens the bound.

## Phase rules

1. Phases are defined by what becomes usable. The file has no dates.
2. A phase is done when a fresh install passes real use, failure, restart and backup restore on one dev revision.
   Fresh install: built artifacts on a clean state root of a supported host. Real use: the phase's acceptance checks through the TUI (and Telegram from LINA 1) against the real Core. Failure: the phase's injections, including `kill -9` at stated boundaries. Restart: one planned and one crash restart, each with a strictly higher epoch and no effect run again. Backup restore: a whole generation restored into a different state root, then the zero-exposure test. All four run on one dev commit; a fix after a failure reruns the set.
3. An earlier phase works without any later one. A package belongs to the phase of its first consumer. A later capability is absent from `welcome`, and the TUI shows it as unsupported, never as empty. A migration creates no table that only a later phase uses.
4. The next phase starts when the previous one is done.
5. A measurement named in an issue is its first commit, with its bounds written in the issue first.
6. A released row of the supported-combination table needs its fixtures and failure injections to pass ([host-protocol.md](host-protocol.md#supported-combinations)).
7. Fixtures, corpora and evidence records in the repository are synthetic or hold only ids, codes, counts and hashes.
8. The checks of [runtime.md](runtime.md#runtime-acceptance) land as follows: V1 in G1-31, V2 in G1-40, V3 in G1-09, V4 in G2-26 to G2-28, V5 in G2-04, V6 and V7 in G1-01.
9. The specification comes first. Each issue names the design sections it implements, and those sections are written before the code. A pull request that changes behavior updates those sections in the same pull request. Keeping the documents in step with the code is part of done, not a later task ([work-and-delegation.md](work-and-delegation.md#work-definition)).
10. Each phase has a headless end-to-end suite that runs in `foundation` ([foundation.md](../policy/foundation.md)). The suite drives the TUI from a scripted terminal and LINA Core through its protocol. It runs every acceptance check that a program can observe. The person's part of a phase's acceptance is a check of UI and UX only. The harness is M0-14, and the suites are M1-80, M2-50 and M3-45 of [roadmap-github.md](roadmap-github.md).
11. One review per pull request. A high-risk contract change may get one extra independent read, and there is no standing gate of several verifiers.
12. Design documents are written in English. Summaries for the person are in Korean.

## Phases

Development starts with LINA Core and the TUI. The LINA app follows once they are usable and does not block Core work.

The phases LINA 1 to LINA 3 are the milestones M1 to M3 of [roadmap-github.md](roadmap-github.md). Their names say what becomes usable. They are not versions: finishing a phase does not change the version ([versioning.md](versioning.md)).

| Phase | Name | Usable at the end |
| --- | --- | --- |
| LINA 1 | LINA 1 · 나를 기억하는 리나와 대화 | Install; sign in with ChatGPT; talk with Lina in the TUI and one owner Telegram chat; short replies, long results as cards; Lina remembers, forgets and recalls with evidence; heavy work runs in the background; skills grow with undo; agents ask each other in the background; a Hermes install can be imported; Core restarts only when idle; backup and restore. |
| LINA 2 | LINA 2 · 일 하나 맡기기 | Memos and 할 일 in LINA's own records; one 일 handed to a worker under a work definition, reviewed once against the design, merged or closed; progress at a glance; more accounts with quota-aware routing; Discord; update with rollback. |
| LINA 3 | LINA 3 · 큰 일을 계획하고 여러 작업·기기로 끝내기 | A plan for a 목표; work split into tasks that run in parallel across workers and devices; plugins and MCP servers; Nodes with grants and a seat lease; the switch from CRW. |

## LINA 1

Order follows the Depends-on lines. 1.A comes first and 1.H last.

**Acceptance (LINA 1).**
1. A fresh install reaches READY with a strictly higher epoch after `kill -9` at each start step. Corrupt canon halts writes; `doctor` answers.
2. With no account, preparation fails with one plain line and the sign-in offer. After sign-in a conversation streams; a second ChatGPT account does not change the owner binding.
3. A scripted over-long reply is cut at the last sentence and becomes a card; the ledger keeps it. A turn at the hard budget starts a background run from the gathered state; the run lands as a notice at a turn boundary, never while a reply streams.
4. A forgotten item has zero hits in search, summaries and the ContextPacket, also after restoring an older generation. Korean fixtures pass by id. `expand` returns the exact range of a fact stated before compaction.
5. An instruction-only skill write is adopted with a notice and undone in one action. A skill with executable code is not adopted this way.
6. A synthetic Hermes import reconciles row for row; a second run adds nothing.
7. One owner Telegram chat reaches the right agent; cursor and inbox row commit together under `kill -9`; a button from anyone but the paired owner changes nothing; two agents exchange a typed hand-off in the background with one log line.
8. The sandbox probe passes or shell is off with a cause. Planted instructions in tool output are data. A restart request waits for an in-flight fake effect.
9. The TUI shows Waiting for me first, the running count and cards from fixtures; an unchanged state writes zero bytes; spike records S1 to S11 exist.

Failure injections: `kill -9` at each start step and turn phase; corrupt canon; a write fault on `canon/`; disk full; an unusable sandbox; a slow client and dropped frames; a replayed channel update; provider unavailable, expired token and usage cap; `kill -9` with a background run working; restore of an older generation.

- #358 covers provider unavailable and truncated SSE through the installed Core and real TUI in a headless PTY. Failure notices and streamed partials remain visible after restart, accepted requests are not replayed, and a new request excludes failed partials from completed assistant context. Turn-store tests inject a notice write failure and verify atomic terminal state, recovery and notice deduplication.

### 1.A Foundation and control plane

**G1-01 Go skeleton, modules and import rules.** Import rules as one `go test`; V6 and V7 baselines.
- Done when: a seeded import that breaks a rule fails `go test`; the foundation script passes with the components registered; the toolchain lines of both `go.mod` files and `go.work` match; `go.sum`, module count and the edit-to-test and full foundation times are recorded.
- Depends on: none. Design refs: C-02, C-40; [foundation.md](../policy/foundation.md#go-components).

**G1-02 Control files, epoch, start order and idle registry.** Start order, RECOVERING, tri-state idle.
- Done when: `kill -9` at each start step is followed by a start with a strictly higher epoch; a second `lina core` exits 69; a non-local state root exits 78; an external call without a lease does not compile; an unreadable registry reads as busy.
- Depends on: G1-01. Design refs: C-01, C-03 to C-05; CP-01 to CP-03.
- #36 implements control files, the lock, the durable epoch and the ten startup boundaries. Its subprocess tests kill Core after each acknowledged boundary and compare the next epoch against all previously durable epochs; before step 5 a new run has no epoch. The `e2e/m1/` startup protocol scenario remains pending the #34 harness.
- #37 implements the lease API, its missing-lease compile-fail fixture and the idle registry. Hard and Live leases block idle until their calls finish; Reported activity does not. Unreadable, invalid or canceled activity reads yield Unknown and count as busy. Package tests cover these rules, release and cancellation, notifications and concurrent activity. There is no TUI or protocol operation for this increment; restart and its observable fence remain G1-24 work.

**G1-03 Canon writer, epoch fence, halt and SQLite binding.** One writer, halt marker, SQLite binding.
- Done when: a write under a lower epoch fails `stale_epoch` and latches the writer; a write on a reader fails with a closed code; a store newer than the binary exits 78 unchanged; injected corruption writes the halt marker and reads still work; the binding is recorded in the build manifest.
- Depends on: G1-02. Design refs: C-16; [runtime.md](runtime.md#storage).

**G1-04 Diagnosis emitter, event store and citation check.** Closed enums, twelve fields, chain roots, agent filter.
- Done when: no emitter parameter is a free string; stored keys equal the twelve names; the model, interruption and storage injections are each explained by event id, class and cause code; returned events hold no secret, prompt, provider body or absolute home path.
- Depends on: G1-02. Design refs: C-33; [self-diagnosis-log.md](self-diagnosis-log.md).
- #41 implements the typed emitter, root constructors, strict codec, `database/sql` event store, intersecting filters and citation oracle. Package injections cover model failure, cancellation and a failed write; scripted SQL tests cover transaction calls and error handling. The caller supplies #39's binding. Binding-dependent persistence, crash/full-disk durability and running-install diagnosis remain unverified. No TUI or protocol operation exposes this package yet.

**G1-29 Asset store and staging.** Asset ids, revisions, hashes, staging, trash.
- Done when: a rename keeps asset id and hash; an edit keeps the id, changes the hash and advances the revision; identical content gets two ids; an import writes only under staging until handed over; an export leaves a canon record and no staged copy; a backup detects a hash mismatch.
- Depends on: G1-03. Design refs: C-16, C-39; [filesystem.md](filesystem.md#asset-identity).
- #42 implements the materials store and its canon migration, staged handover and delivery, recoverable trash, and verified copying of every recorded revision for backup. Package tests use real files and SQLite; no asset operation is exposed through the TUI or Core protocol yet. Whole-generation coordination and permanent-deletion propagation remain with their owning implementations.

### 1.B Authority, tools and sandbox

**G1-05 Approvals, Dispatch, Gate and effect ledger.** The approval rule, grants, approval records, effect states.
- Done when: a plugin tool that no service profile marks runs without asking; an approval holds what, where and undoable computed by Core; a standing grant for network access revokes in one action; one idempotency key runs once; an approval pending at restart is stopped and a late button answers stale; a write on a stale revision is refused.
- Depends on: G1-02, G1-03, G1-04. Design refs: C-12, C-13; [main-authority.md](main-authority.md#approval).

**G1-06 Speaker kinds, masking and the secret store.** Entry-path speaker kind, masking, external evidence, secret store.
- Done when: a seeded token is masked and its span is in no index, summary or export; a formatter prints a placeholder for a secret; speaker kind never comes from text; planted instructions in a tool result arrive marked as external evidence and cannot answer an approval.
- Depends on: G1-01, G1-05, G1-29. Design refs: C-15; [conversation-and-memory.md](conversation-and-memory.md#event-fields).
- #48 supplies the masking/projection and opaque-secret APIs, credential-store backend seam and private-file fallback. Conversation/protocol admission, OS-keychain adapters and account forms still need their owning flows; this increment does not establish speaker-kind or external-evidence acceptance.

**G1-07 Sandbox, tool executor and trash.** Sandbox wrapper, network ask, file tools, recoverable deletion.
- Done when: the start probe passes or shell and file tools turn off with a cause while conversation continues; a canary cannot read canon, secrets or the client socket; network waits for once, always allow or deny; a patch with stale context fails; a trash delete restores identical bytes.
- Depends on: G1-02, G1-05, G1-06. Design refs: C-14; [runtime.md](runtime.md#sandbox).

### 1.C Model path, accounts and loop

**G1-08 Provider interface and the Responses adapter.** `kit/provider`, typed failures, usage, budget.
- Done when: scripted-wire tests cover each failure kind by code, then type, then status; no request is sent after the first output or after a failure; a cut-off response runs no tool; unreported usage is stored unknown; a field outside the manifest list is not sent.
- Depends on: G1-02, G1-03, G1-04. Design refs: C-10, C-43, C-44.
- #53 implements provider types, the closed failure vocabulary and fenced budget reservations/settlements. SQLite reopen tests retain unknown usage for all seven run kinds. These packages have no TUI or protocol entry point yet.
- #54 adds the standard-library Responses HTTP/SSE adapter and build-manifest settings. Scripted wires cover code/type/status failures, zero retries, truncated responses with no tool release, opaque state, unknown usage, and field filtering; build tests verify the adapter identity and retry value. Live V1 and conversation integration remain unverified.

**G1-09 Loop core and run durability.** `kit/loop`, steering, cancellation, intent and result commits.
- Done when: a scripted wire shows no tool from a cut-off response, one result per call in call order, an aborted result for each open call, and steering applied at the next round; no effect is applied twice across a restart.
- Depends on: G1-05, G1-08. Design refs: C-08, CP-04; [runtime.md](runtime.md#conversation-loop).
- #55 implements the loop and turn commit boundaries, ordered parallel tool results, steering, cancellation, and recovery without replay. Scripted Responses-wire and SQLite reopen tests cover V3 at the package boundary; no conversation capability is exposed through the client protocol yet.

**G1-10 Ledger, request assembly and preparation.** Ledger fields, pure Assemble, ContextPacket, preparation steps.
- Done when: an update of a ledger row is denied; identical input gives identical bytes; the layout order matches the contract; each failing preparation step shows one plain line and a stub provider sees zero calls; the ContextPacket is never stored.
- Depends on: G1-03, G1-06, G1-08. Design refs: C-11, C-17; [conversation-and-memory.md](conversation-and-memory.md#request-layout).
- #58 implements pure deterministic assembly, bounded optional ContextPacket collection and the four-step preparation runner with commit-before-dispatch evidence callbacks. Package tests cover ordered layout, budget and scope exclusions, one-line failures with zero provider calls, follow-up reuse and absence of packet bodies after SQLite reopen. It adds no client-protocol or TUI conversation endpoint.
- #57 implements the immutable ledger tables and native admission, using the fenced canon writer, entry-derived speaker kinds and masking before storage. SQLite and real material-store scenarios cover mutation refusal, atomic admission, restart readback and unavailable bytes. Preparation and request assembly remain with their own issues; no ledger operation is exposed through the TUI or Core protocol in this increment.

**G1-11 Inbox, lanes, steering and deadlines.** Inbox, lanes, steering, stop, expiry timers.
- Done when: a duplicate key inserts one row and preserves its original bytes across restart; the acknowledgement follows the commit and no failed commit is acknowledged; each conversation runs one turn at a time while other conversations can proceed; a message during a turn is applied as steering at the next round; stop ends open calls as aborted; after `kill -9` mid-turn the input is not replayed and the conversation shows one plain line.
- Depends on: G1-03, G1-09, G1-10. Design refs: C-09; [runtime.md](runtime.md#conversation-loop).

**G1-12 Agents, presets, persona layers and first run.** Persona layers, presets, deletion, first run.
- Done when: a new revision applies from the next turn and the preparation acknowledgement names it; a preset update changes only core and default voice; one agent's change never alters another's; a deleted agent sits in the trash 30 days and its channel bindings end at once.
- Depends on: G1-03, G1-05, G1-29. Design refs: C-24; [conversation-and-memory.md](conversation-and-memory.md#agents-and-personas).
- #62 implements the agent store, immutable Git persona revisions, durable per-turn persona preparation, preset fan-out, first-run initialization and recoverable 30-day trash with immediate binding termination. Package scenarios use real canon and files; account sign-in, conversation preparation and channel transport flows have no client endpoint in this increment.

**G1-31 Accounts and Sign in with ChatGPT.** Single refresh actor, sign-in relay, owner binding, V1 record.
- #81 implements the client relay and immutable owner binding with recorded issuer wires. Live V1 remains `pending_owner_signin`; the [owner runbook](../../evidence/v1-chatgpt.md) states the command, observations and outstanding remote-client proof.
- Done when: one refresh runs at a time per account and a rotated refresh token is stored before use; a failed refresh shows needs sign-in and uses no other account; a second ChatGPT account never changes the binding; no token appears in a log, request, sandbox or client; the V1 record states what the request surface accepted.
- Depends on: G1-06, G1-08, G1-18. Design refs: C-47; [runtime.md](runtime.md#chatgpt).
- #80 implements the account store, serialized refresh and idle keep-alive, durable token rotation and account states. Package tests cover persistence, concurrent calls, interruption, failed refresh, turn exclusion and secret projections. The sign-in relay, issuer exchange, owner binding, protocol/TUI exposure and V1 record remain with their owning increments.

### 1.D Memory, context and skills

**G1-13 Memory items, recall, person model and Korean search.** Items with holder, recall with evidence, `recall_person`, Korean search.

The Korean acceptance corpus is `testdata/korean/search.json`: both stores assert returned source ids for attached particles, two-syllable words, imported text and literal queries. Store tests also check index maintenance and recall scope.
- Done when: fixtures find a particle-attached three-syllable word and a two-syllable word in ledger and memory; recall for agent A returns no id held by B; a person-subject item exists only from person events; "remembered" is said only after the commit.
- Depends on: G1-03, G1-05, G1-10, G1-12. Design refs: C-20 to C-22; [conversation-and-memory.md](conversation-and-memory.md#memory).
- #64 implements the memory table and revision snapshots, holder/person scope checks, native person-event provenance, committed memory tool receipts, ephemeral standing profiles and bounded `recall_person`. Acceptance uses real SQLite and ledger admission; there is no client memory endpoint yet. #65 supplies the Korean indexes and fixtures.

**G1-14 Retraction, trash and the removal list.** Revocation epoch, trash, tombstones, removal list.
- Done when: after a forget, search, summaries, the ContextPacket and answers give zero hits in the next request, also after restoring an older generation; a fault leaves no half state; a tombstone never exists without its removal-list line; a forgotten item is not extracted again.
- Depends on: G1-10, G1-13. Design refs: C-19; [filesystem.md](filesystem.md#backup-generations).
- #66 implements the durable removal journal, canon recovery barrier, memory trash/recovery/expiry, content-free tombstones, extraction exclusions and preparation snapshots. Real-store scenarios cover recall, ledger fallback, profiles, summaries, ContextPackets and scripted answers after forgetting and after restoring an older SQLite snapshot with the latest removal list; process-death scenarios cover the journal/transaction boundary. The packages expose no TUI or Core client operation yet; whole-generation restore remains G1-22.

**G1-15 Compaction, summary tree and raw recovery.** Compaction steps, continuation turn, summary tree, `expand`.
- Done when: a failed summarizer publishes no node and the events stay in the request; a stale-frontier publish fails; `expand` returns the exact range of a fact stated before compaction; a continuation that does not fit sends nothing and logs sizes; a retraction rebuilds the node.
- Depends on: G1-08, G1-10, G1-13, G1-14. Design refs: C-18; [conversation-and-memory.md](conversation-and-memory.md#compaction-and-resume).
- #70 supplies the request-fitting pass, auxiliary summary validation, continuation dispatch guard and scoped `expand` tool boundary. Package scenarios verify failure preservation, exact evidence recovery and no dispatch above the threshold; client-protocol and TUI wiring remain in their consuming flows.
- #69 implements summary tables, exact coverage and single-parent checks, frontier/epoch-fenced publication, and atomic bottom-up rebuilding after retraction. Real SQLite scenarios cover concurrent changes, summarizer/storage failures, migration and reopen. This is a package API; request compaction, provider wiring, continuation and `expand` remain with their consuming flows, and no client/TUI scenario is introduced here.

**G1-16 Skills written by agents, adopted at once, undone in one action.** Reversible-write check, notice, undo, index.
- Done when: an instruction-only patch is adopted as a new version with a one-line notice after the commit; undo restores the previous text as a new version; a skill with executable code is not adopted this way; the index holds name and description and bodies load only when needed.
- Depends on: G1-05, G1-10, G1-12, G1-29. Design refs: C-23; [conversation-and-memory.md](conversation-and-memory.md#skills).
- #71 supplies the versioned skill store, reversible-write classification, durable undo suppression, lazy catalog/read handles, embedded meta-skill and guarded registry tools. Real canon/Git tests cover publication, failure, reopening, concurrency and undo; authority tests cover receipts and replay. The client service has no skill endpoint yet; presentation, context selection and explicit proposal acceptance remain with their consuming flows.

**G1-27 Hermes install import.** Rehearsal, import, verification and promotion, as a worker job.
- Done when: on synthetic fixtures counts reconcile row for row; every source key has one legacy reference in both directions; the reverse mapper hash equals the source hash; a second run adds nothing; no write reaches a source; an unknown schema is refused; recall returns no imported observation before promotion.
- Depends on: G1-13, G1-14, G1-22, G1-29, G1-36. Design refs: C-28; [conversation-and-memory.md](conversation-and-memory.md#importing-a-hermes-install).

### 1.E Client protocol and TUI

**G1-17 Schema, generators and fake Core.** Schema, generators, fixtures, fake Core with fault switches.
- Done when: regenerated types equal the checked-in files; every fixture round-trips; the server rejects an unknown request field and the client counts an unknown kind; a fixture replay through the fake Core is deterministic with no sleep.
- Depends on: G1-01. Design refs: [host-protocol.md](host-protocol.md#schema-and-generated-types); T-11.

**G1-18 Client service.** Unix socket listener (the tailnet listener is G3-06), handshake, topics with cursors, resync, decider rule.
- Done when: an unlisted pair is refused `unsupported_combination`; a gap yields resync and a snapshot and replaying twice changes nothing; an overflowing topic closes alone; mutations in RECOVERING return unavailable; a connect from inside the sandbox fails; an approval answer from a Core descendant is refused.
- Depends on: G1-05, G1-06, G1-07, G1-11, G1-12, G1-17. Design refs: C-42, CP-02; [host-protocol.md](host-protocol.md#client-connection).

**G1-19 TUI state, layout and renderer.** Charm-free `state`, `layout`, `render`, `sanitize.Display`, Measurer.
- Done when: duplicates and a double snapshot change nothing; layout property tests show disjoint regions and the floor at every size; fuzzing `sanitize.Display` finds no control, bidirectional or OSC output; renderer calls are proportional to viewport rows.
- Depends on: G1-17. Design refs: T-01, T-03, T-06, T-07, T-09, T-11.

**G1-20 TUI library spike.** Spike S1 to S11 of tui.md; fallbacks F1 to F3 on a miss.
- Done when: the terminal list is recorded; S1 to S11 each have an evidence record with every field; the recording writer runs, quits and parses with no sleep; the chosen library step is recorded.
- Depends on: G1-01, G1-19. Design refs: T-08, T-09.

**G1-21 TUI screens and input.** Home, conversation, composer, approvals, cards, pager.
- Done when: a PTY run against the fake Core covers start, connect, refuse, recover and quit; two fake clients racing one approval give exactly one `resolved`; an unchanged state writes zero bytes; with no socket the Core-absent screen appears and no Core is started; every closed code has a catalog entry by id.
- Depends on: G1-12, G1-18, G1-19, G1-20, G1-35. Design refs: T-04, T-05, T-07, T-10, T-12, T-13, T-15.

**G1-30 TUI memory, agent, account, channel and talk-log commands.** Memory, agent, account, import, channel and talk-log commands.
- #112 connects installed Core's existing account service to the TUI. Synthetic PTY scenarios cover loopback and pasted redirects, account list and sign-out, and the no-account sign-in offer. Owner real-account validation remains #81.
- Done when: a forget from the TUI gives zero hits in the next request; agent create, choose, rename and delete round-trip by id; a ChatGPT sign-in completes through the relay from the TUI, including a pasted URL; a channel connection is paired and bound from the TUI, and the agent talk log lists one line per exchange and expands it.
- Depends on: G1-12, G1-13, G1-14, G1-16, G1-21, G1-25, G1-26, G1-31. Design refs: T-04, T-14, C-25, C-27, C-47.

### 1.F Backup, install and restart

**G1-22 Backup generations and restore.** `lina backup` and `lina restore`; removal list applied first.
- #89 implements whole-generation capture, removal-list replay before copying, online SQLite snapshots, verified file/material inventories and manifest-last publication. Package failure scenarios include SIGKILL during copying; `e2e/m1/backup.json` drives a synthetic conversation and backup through the installed executable. Restore and its zero-exposure acceptance remain with #90.
- Done when: a kill during copy leaves no generation; a hash mismatch fails the backup; the zero-exposure test passes also into another root; after restore non-terminal external-effect rows are unknown, leases are void and accounts show needs sign-in; the epoch never falls.
- Depends on: G1-03, G1-11, G1-14, G1-15, G1-29. Design refs: C-32; [filesystem.md](filesystem.md#backup-generations).
- #90 supplies the stopped-main restore CLI, independent generation checks, replay before first read, authority invalidation and recoverable publication. Package tests cover zero exposure after losing canon in the original or a second root, source removals during interruption, and process death. The M1 installed-binary scenario restores conversation history through the Core protocol and proves subsequent epoch advancement and new input.

**G1-23 Install.** `lina install`: state root, services, adapter files, tool discovery.
- Done when: install succeeds with no account and the first preparation fails with a plain cause and the sign-in offer; a foreign unit or an occupied port is refused and nothing is overwritten; versions of discovered tools are recorded and unverified ones listed.
- Depends on: G1-02, G1-06, G1-08. Design refs: C-01, C-02; [runtime.md](runtime.md#the-lina-command).
- #91 adds verified bundle installation, private state and identity creation, systemd user units, optional adapter/worker artifacts and bounded tool discovery. The install scenario launches the actual installed Core command, checks its identity through the protocol and connects the TUI. A package integration exercises G1-10 preparation after account-free installation: the account step fails with its sign-in offer and no provider request. The current client protocol still reserves conversation/account operations; this is not evidence of a completed sign-in or model conversation. Automated service registration uses a scripted manager; the real systemd parser validates the generated units.

**G1-24 Restart request and fence.** Durable request, Fence, `when_idle`, `drain`, exit 75.
- #92 implements the durable control-file receipt, shared admission fence and lifecycle client capability. Tests cover in-flight effects, Unknown, SIGKILL boundaries, restore isolation and hot reload. `e2e/m1/restart.json` drives real Core requests, exit 75, cleanup and discharge at the replacement's higher READY epoch.
- Done when: a request does not exit while a fake effect is in flight; unknown idle blocks; a request survives a crash and reports owed until discharged; a restore cannot resurrect it; a settings, persona or binding change mints no epoch.
- Depends on: G1-02, G1-18, G1-23. Design refs: C-05 to C-07; [runtime.md](runtime.md#updates).

**G1-40 Packaging and residency check.** Static binaries, sqlite-vec, 24-hour residency on Linux and macOS.
- Done when: sqlite-vec loads after its digest check; services start; size, start time and resident memory and CPU stay within the budget written before the run on both platforms; the binding is recorded in the manifest.
- Depends on: G1-03, G1-23. Design refs: [runtime.md](runtime.md#runtime-acceptance) (V2).
- #94 supplies digest-checked embedded sqlite-vec, platform build manifests and Linux amd64/macOS arm64 release archives, with service-start acceptance on the install path. It does not substitute cross-builds for the separate native-platform and 24-hour residency measurements.

### 1.G Channels and agent exchanges

**G1-25 Channel gateway and Telegram.** Pairing, bindings, scopes, long poll, outbound queue, buttons.
- Done when: `kill -9` between receipt and reply leaves one inbox row, the matching cursor and no repeat; an unpaired sender yields no turn; the token is in no event, ledger row or child environment; READY is reached with the platform unreachable; a button from anyone but the paired owner changes nothing; a send of unknown outcome is not repeated.
- Depends on: G1-06, G1-11, G1-12, G1-18, G1-29. Design refs: C-27; [integrations.md](integrations.md#channels).
- #97 implements the gateway adapter contract, owner pairing, dispatch scope, atomic inbox/cursor admission and durable per-chat outbound queue. Real canon scenarios use SIGKILL at admission and send boundaries, prove no duplicate dispatch or uncertain resend, and exercise pairing, retry-after and agent-deletion binding removal. There is no channel TUI/protocol endpoint in this increment; Telegram transport, credential wiring and approval interactions remain separate work.
- #98 adds Telegram long polling, secret-store token injection, current-owner approval interactions, and the adapter's send/edit/file boundary. Package tests cover masked inbox and ledger input, child environment isolation and non-owner/foreign-bot rejection. `e2e/m1/telegram.json` exercises real Core startup and socket requests while the platform transport is unreachable. These checks use synthetic credentials; live account setup and public channel settings are not established by this adapter increment.

**G1-26 Agent exchanges.** Mailbox, typed hand-off, round limit, one-line log, needs-person flag.
- Done when: a request delivers once across `kill -9`; an acknowledgement ends the exchange without a model turn; no exchange message enters a person's conversation as a person turn; an exchange turn reads no personal knowledge; the flag is set only when the person owns the decision.
- Depends on: G1-11, G1-12, G1-33. Design refs: C-25; [conversation-and-memory.md](conversation-and-memory.md#agent-exchanges).

### 1.H Phase acceptance

**G1-28 Phase 1 acceptance run.** Run script and evidence record for LINA 1.
- Done when: one record holds every field of the phase rules and a result per acceptance check; epochs rise strictly across the matrix; a failed check reruns the whole record on the fixing commit.
- Depends on: G1-21, G1-22, G1-24, G1-25, G1-26, G1-27, G1-30, G1-32, G1-34, G1-37, G1-38, G1-39, G1-40. Design refs: Phase rules; [self-diagnosis-log.md](self-diagnosis-log.md).

### 1.I A conversation that never waits

**G1-32 Turn budget and hand-off to the background.** Meter, soft and hard budget, handles, promotion, quality counters.
- Done when: at the soft budget every call except `start_run` and `dispatch_work` gets `budget_reached` and does not run; at the hard budget with no effect a run starts from the gathered state; with an effect, further effects wait on a question; a report turn makes no tool round trip.
- Depends on: G1-09, G1-11, G1-33. Design refs: C-49; [runtime.md](runtime.md#turn-budget).
- #101 implements the meter and durable turn outcomes at the turn/run package boundary. Scripted-provider tests cover soft refusals, hard promotion with gathered state, retained effect receipts, report isolation, and continuation budgets. Quality counters persist without automatic tuning.

**G1-33 Background runs.** Run record, start tool, scope, limits, states, landing.
- Done when: start returns an id before the run's first model call; tools outside the run scope are not declared; no time or call cap exists; a second interruption lands the run failed or partial; a cancelled run stays cancelled; an unresolved citation lands the run partial.
- Depends on: G1-05, G1-09, G1-10, G1-29. Design refs: C-50; [work-and-delegation.md](work-and-delegation.md#background-runs).

**G1-34 Reply shape and typed results.** Reply budgets, streaming filter, `present_result`, `read_result`, `save_result`, text rendering.
- Done when: a chat reply over 5 lines or 300 characters is cut at the last sentence and a card is made while the ledger keeps the reply; a body that fails the schema is refused `malformed` to the agent only; a stale revision is refused; a double save makes one record.
- Depends on: G1-10, G1-17, G1-29. Design refs: C-51, C-52, CP-05; [conversation-and-memory.md](conversation-and-memory.md#reply-shape).

The #104 typed-result increment supplies all eight closed body schemas, deterministic rendering, ledger revisions and Core result-tool bindings. Its tests cover malformed bodies without conversation events, stale edits/saves, scoped reads, one versioned record for a repeated save, and unknown effects without redispatch. Client-service/TUI delivery and C-51's streaming filter remain separate increments.

**G1-35 Waiting list, running list and notices.** Waiting rows, running list, notices at turn boundaries.
- The Core implementation supplies canonical approval and run-input producers, run/task projections, attributed answers, and a durable lane outbox that writes system evidence at delivery. Its isolated scenarios are `e2e/m1/waiting-running-notices.json` and `e2e/m1/run-landing-notice.json`; client waiting/running payloads and TUI rendering remain reserved for their surface implementation.
- Done when: rows rank in the contract order on a fixture; the source is re-read before a row shows; a notice is never delivered while a reply streams; notices within 10 s coalesce; answering a row records the decision with person and agent.
- Depends on: G1-05, G1-11, G1-33. Design refs: C-55; [surfaces.md](surfaces.md#waiting-for-me).

### 1.J Background memory

**G1-36 Moirai worker and the Core job API.** Separate binary, read-only canon, job API (Aux, Apply, Report).
- Done when: a worker killed mid-job leaves canon unchanged and resumes from its watermark; the worker holds no credential; every write goes through Apply; with the worker absent only background memory and planning are unsupported.
- Implementation evidence: `cmd/moirai-worker`, the read-only worker layers, and `internal/core/jobs` provide the service boundary; `e2e/m1/moirai-worker.json` exercises a real worker killed during Aux, unchanged canon at the kill boundary, reopening canon and resuming from its watermark, and the independent Core client protocol. Concrete cognition jobs remain their own increments.
- Depends on: G1-03, G1-05, G1-08. Design refs: C-56; [runtime.md](runtime.md#processes).

**G1-37 Extraction, maintenance pass and person brief.** Extraction, one maintenance pass, person brief.
- Done when: a person-subject item comes only from person events; a fan-out fixture yields one item with two evidences; a chain of three statements returns only the newest; the pass applies only pre-authorized actions and is silent when nothing changed.
- Depends on: G1-13, G1-14, G1-36. Design refs: C-21; [cognition-and-life.md](cognition-and-life.md#memory-jobs).

**G1-38 Prepared summaries and higher tree levels.** Prepared summaries, higher tree levels.
- Done when: a prepared summary is used only if the covered history is unchanged and its epoch holds; no always-on rollup job exists; a retraction rebuilds the node and its ancestors.
- Depends on: G1-15, G1-36. Design refs: C-18; [conversation-and-memory.md](conversation-and-memory.md#compaction-and-resume).

### 1.K Replay and export for development

**G1-39 Replay command and episode export.** `lina replay` and `lina export` for the development loop.
- Done when: a replay writes no ledger event and no effect row; an effect call is answered by the recorded result or the turn is excluded; retracted and forgotten items are left out; an export selects nothing by default and masks secrets; the manifest counts left-out turns.
- Depends on: G1-10, G1-15. Design refs: C-59; [runtime.md](runtime.md#replay-command).

## LINA 2

**Acceptance (LINA 2).**
1. A 일 is handed to Codex under a work definition with a write area. The completion event is a row, the first pull request read follows it, one review returns P0 and P1 once, the merge is pinned to the head last read, and dependents are released after one confirming read.
2. Overlapping write areas never run together; disjoint areas run at once; a worker with a blocked quota row is not chosen; a quota stop hands the same packet once to the next worker.
3. Killing Core mid-task leaves the effect unknown; the thread snapshot settles it; a second failure becomes a decision row.
4. The waiting list orders by the contract rules on a fixture; no progress line holds an id; the digest has at most five lines and a count; a card says done only with a verified verdict.
5. The records index rebuilt from files equals the live index; an outside edit is detected and not merged; a recurring 할 일 advances only to a future occurrence.
6. With two providers connected, a subscription near its limit moves new requests to the other with one line, never to pay-per-use beyond the setting.
7. A Discord binding behaves as the Telegram one.
8. An update runs preflight and rolls back before READY; the failure injections of runtime.md pass.

Failure injections: `kill -9` at every statement boundary of task, completion event and review; a killed worker; a lost event; a malicious pull request description; every LINA 1 injection.

### 2.A Work ledger, completion events and timers

**G2-17 CRW schema pin and fixture runner.** Pin record and fixture runner.
- Done when: the pin record names the CRW commit, a hash for each adopted schema document and fixture file, and the adopted fixture domains; the runner takes expected values from the fixture; a changed hash fails; the pin appears in the build manifest.
- Depends on: G1-01. Design refs: C-57; [work-and-delegation.md](work-and-delegation.md#start-gate).

**G2-01 Task records and work area.** Work ledger, card projection, `work/` directories, protected refs.
- Done when: a second active run on a task is refused; a task worktree is created from the base commit under `work/tasks`; the projection rebuilt from the ledger equals the live one; a result without a pull request is copied into the generation; a receipt for a changed artifact without provenance is rejected.
- Depends on: G1-05, G1-11, G1-12, G2-17. Design refs: C-29, C-30, C-39; [work-and-delegation.md](work-and-delegation.md#work-area).

**G2-02 Completion events, reaction path and reconcile sweep.** Event rows, idempotent claims, one reaction path, sweep.
- Done when: a completion event is a row before anything reacts; the same task, attempt and head collapse to one row; an event with a head that is not current is ignored; `kill -9` between a state change and its event leaves both or neither; a lost event is recreated once.
- Depends on: G1-11, G2-01. Design refs: C-58; [work-and-delegation.md](work-and-delegation.md#event-driven-flow).

**G2-03 Timers, reminders and sensors.** Durable timers, reminders, deterministic sensors.
- Done when: a nonexistent local time and a repeated hour each fire once; `kill -9` at claim gives one fire per instant; an unchanged sensor signature spends no model call.
- Depends on: G1-11, G2-02. Design refs: C-26.

### 2.B Delegation

**G2-04 Worker adapter boundary and the Codex adapter.** `worker.Adapter`, generated types, dynamic tools, approvals, quota rows. It runs first and does not wait for the pin.
- Done when: the round trip completes with the installed Codex and records its version; an unverified version is reported and still used; a worker approval reaches the originating conversation and the answer returns; no sandbox, credential or setting is imposed on the worker.
- Depends on: G1-05, G1-18. Design refs: C-29; [work-and-delegation.md](work-and-delegation.md#codex-adapter).

**G2-05 Claude Code adapter.** Headless child, final result as signal.
- Done when: a final result message ends the turn; exit without one is a failure; a receipt built from the final result and the pull request is marked adapter-derived; nothing is installed into the user's tool.
- Depends on: G2-04. Design refs: C-29; [work-and-delegation.md](work-and-delegation.md#completion-signals).

**G2-07 Design-conformance review, one return, merge or close.** One review, P0 to P3, one return, merge or close.
- Done when: a second review request is refused; P2 and P3 are absent from the return by finding id; a failing required check and a change outside the write area are each a P1 finding; a residual P0 or P1 closes the pull request with the findings as the reason and leaves the task blocked without a verified verdict.
- Depends on: G2-02, G2-18, G2-19. Design refs: C-58; [work-and-delegation.md](work-and-delegation.md#judgment-and-delivery).

**G2-08 Worker stop, reconcile and one dispatch again.** Interrupts, snapshot reconcile, one dispatch again.
- Done when: `kill -9` of Core mid-task leaves the effect unknown and the thread snapshot settles it without a new turn; dispatch again happens once and only when no unknown effect remains; a second failure becomes a decision row; an interrupt is sent only for the listed reasons.
- Depends on: G1-02, G2-04, G2-05. Design refs: C-29, C-58; [work-and-delegation.md](work-and-delegation.md#stall-and-failure).

**G2-18 Work definition and worker choice.** `dispatch_work`, task packet, chooser, quota fallback.
- Done when: a definition without a done condition or write area is refused; two tasks with overlapping write areas are never in flight together; a worker whose quota row is blocked is not chosen before its reset; unknown quota is usable; a quota stop hands the same packet once to the next worker.
- Depends on: G2-01, G2-04. Design refs: C-57; [work-and-delegation.md](work-and-delegation.md#work-definition).

**G2-19 GitHub reads, CI watch and merge.** `gh` as Core's child, check watcher, head-pinned merge, observed merges.
- Done when: no webhook or events feed is used; one watcher per pull request ends at the conclusion; a rate limit shows CI pending with its cause; a moved head is read again and not merged; a protected-ref mismatch blocks the merge; a person's merge is observed only after the merge commit and target ref are checked.
- Depends on: G1-05, G1-07. Design refs: C-58; [integrations.md](integrations.md#github).

### 2.C Memory and growth upkeep

**G2-12 Skill pruning and merging job.** Prune and merge stale or duplicate skills.
- Done when: each prune or merge is a versioned skill write with a one-line notice and one-action undo; built-in skills are untouched; a merge the person undid is not made again.
- Depends on: G1-16, G1-36. Design refs: C-23; [cognition-and-life.md](cognition-and-life.md#skill-suggestions).

**G2-20 Persona growth step with undo.** Adopted at once into the growth snapshot layer; undo.
- Done when: a step is a new revision applied from the next turn; undo restores the previous text as a new revision; an undone step is not proposed again; only the growth snapshot layer changes.
- Implementation: Core's deterministic Moirai growth definition publishes the revision and one-line notice with the job receipt. Agent-bound notice/undo tools retain other layers and reject undone candidate ids and content hashes across restart. `e2e/m2/persona-growth.json` covers the real worker and installed Core, including the growth capability, next-turn model instructions, conversation notice, one-action undo, migration upgrades and restart suppression using synthetic candidates.
- Depends on: G1-12, G1-37. Design refs: C-23; [cognition-and-life.md](cognition-and-life.md#growth).

### 2.D Owner views

**G2-13 Progress view, full waiting list and digest.** Progress lines, digest, full waiting list.
- Done when: a progress line holds no id or pull request and reads unknown for an unobserved value; the digest has at most five lines and a count; a waiting row whose source shows it done is dropped; the order switch is asked of Core; stop, stop all and a follow-up have the same effect from the TUI and from a channel.
- Depends on: G1-18, G1-21, G1-35, G2-02, G2-23. Design refs: C-55; [surfaces.md](surfaces.md#progress-at-a-glance).

**G2-21 Discord adapter.** Gateway adapter with interactions and buttons.
- Done when: a repeated interaction is handled once; a press by anyone but the paired owner changes nothing; text over 2,000 characters splits on paragraph boundaries; a blocked chat marks the binding unreachable.
- Depends on: G1-25. Design refs: C-27; [integrations.md](integrations.md#channels).

### 2.E Update and phase acceptance

**G2-14 Update flow with rollback.** Digest, preflight, gate cells, lifecycle record, swap, rollback.
- Done when: each failure injection of runtime.md leaves the previous composition running or the install held with a closed cause; an old binary against a newer store leaves the store byte-identical.
- Depends on: G1-22, G1-23, G1-24. Design refs: C-38; [runtime.md](runtime.md#updates).

**G2-15 Phase 2 acceptance run.** Run script and evidence record for LINA 2.
- Done when: one record holds every field of the phase rules and a result per check; the interruption injection now stops a running task and its citations pass; Codex and Claude Code versions are recorded.
- Depends on: G2-05, G2-07, G2-08, G2-12, G2-13, G2-14, G2-20, G2-21, G2-24, G2-27, G2-28, G2-29. Design refs: Phase rules.

### 2.F Records and to-dos

**G2-22 Records repository and index.** Git repository, index, rebuild, outside-change detection.
- Done when: an index rebuilt from the files equals the live index; an outside change is detected and shown and not merged; a write on a stale revision is refused; moving a record keeps its id; behavior with thousands of files is recorded.
- Depends on: G1-03, G1-05, G1-29. Design refs: C-53; [filesystem.md](filesystem.md#records-repository).

**G2-23 To-do record, views, recurrence and candidates.** Fields, today rule, views, recurrence, candidates, memo promotion.
- Done when: the today rule passes its fixture; a recurring item advances only to a future occurrence; a dependency cycle is rejected; an ambiguous date is never saved silently; a candidate is added on its own only when the deterministic test passes.
- Depends on: G2-22, G2-03. Design refs: C-54; [work-and-delegation.md](work-and-delegation.md#the-to-do-record).

**G2-24 TUI records, progress and agent screens.** Views, memo inbox, progress view, quick add, board, steering, usage meter.
- Done when: a quick add shows chips before it saves; every view is a Core query result drawn without client ranking; a progress view line holds no id-shaped field; the board groups Core's rows into Waiting for me, Running and Done; a follow-up note reaches a running background run at its next round trip; the usage meter reads "unknown" for an unreported quota.
- Depends on: G1-21, G2-13, G2-23, G2-25. Design refs: T-12, T-13, T-14.

### 2.G Accounts and routing

**G2-25 Connected accounts, plan catalog and automatic setup.** Plan catalog, Setup as a pure function, summary and override.
- Done when: setup is the same for the same inputs; the catalog version and digest are in the build manifest; an API-key account serves a role only where no subscription covers it; the summary line changes in one action; general settings show no model id.
- Depends on: G1-30, G1-31. Design refs: C-48; [runtime.md](runtime.md#connected-accounts-and-automatic-setup).

**G2-26 Claude adapter.** External Node.js adapter, isolation checks, idle stop.
- Done when: a turn runs with built-in tools off and LINA tools over MCP; a runtime that loaded anything unlisted is refused; competing credential variables are absent; quota from rate-limit events is recorded; the SDK is in no LINA artifact.
- Depends on: G1-05, G1-08, G1-31. Design refs: C-45; [runtime.md](runtime.md#claude).

**G2-27 Gemini adapter.** `genai` client, key header, stateless requests.
- Done when: a turn returns provider state items as received; a missing key is needs sign-in with no fallback; quota comes from error codes and LINA's own count.
- Depends on: G1-08, G1-31. Design refs: C-44; [runtime.md](runtime.md#gemini).

**G2-28 Grok adapter.** ACP client over the installed `grok` command, approvals routed to Core, telemetry off.
- Done when: a turn runs with the command's tools restricted and LINA tools offered; a permission request reaches Core's approval path and returns; telemetry switches are set off; a missing command makes only this provider unsupported.
- Depends on: G1-05, G1-08, G1-31. Design refs: C-46; [runtime.md](runtime.md#grok).

**G2-29 Quota-aware routing.** Router over quota rows; no pay-per-use beyond the setting.
- Done when: a new request goes to another connected account when a subscription is near its limit and a one-line event is written; a started request is never resent; unknown quota is not treated as zero; pay-per-use is not started beyond the set budget.
- Depends on: G2-25, G2-26. Design refs: C-48; [runtime.md](runtime.md#connected-accounts-and-automatic-setup).

## LINA 3

**Acceptance (LINA 3).**
1. A large 목표 is split into dependent tasks; a dependent is released only after its predecessor is confirmed merged; independent tasks go at once up to quota and capacity.
2. A Node executes nothing without a grant; a reboot lands BLOCKED; revoke settles in-flight effects cancelled or unknown; a remote worker's pull request is read from GitHub.
3. A catalog file with a bad signature is refused; a command server runs in a sandbox; a changed tool definition is marked on the plugin card.
4. A seat lease ends at the person's input, a lock or a resume.
5. The CRW takeover reports every record imported, lost or unsupported.

Failure injections: a Node disconnect, a silent partition, a Node reboot, a revoke mid-command, a stale-epoch command, a plugin crash loop, a bad catalog signature, every earlier injection.

### 3.A Plugins

**G3-01 MCP host and plugin registry.** Official Go SDK, era fallback, tool snapshots, sandbox for command servers.
- Done when: a crash loop never blocks conversation; a command server with no sandbox does not start; a changed tool definition is marked on the plugin card; a name over 64 characters is shortened with a stored map; a metadata fetch to a private, loopback or link-local address is refused; an earlier-era server connects by fallback.
- Depends on: G1-05, G1-06, G1-07. Design refs: C-34; [integrations.md](integrations.md#host).

**G3-02 Outward-effect marks, tool annotations and on-demand tools.** The service profile mark, declaration order, on-demand loading.
- Done when: an annotation never marks a tool as outward; a call to a tool that a service profile marks asks every time and a call to any other tool runs; the always-declared tool list is byte-identical before and after a plugin is enabled.
- Depends on: G1-05, G1-10, G3-01. Design refs: C-34, C-12; [integrations.md](integrations.md#tool-classification).

**G3-15 Plugin catalog, connect card and OAuth.** Signed catalog, registration order, PKCE, connect card.
- Done when: a catalog file with a bad signature or an older version is rejected; each registration mode is exercised by a fixture in order; the first request asks the scopes the manifest or the 401 challenge names, and an `insufficient_scope` answer asks the earlier scopes together with the new ones; a 401 refreshes once; `invalid_grant` shows needs sign-in; the connect card is a labeled block in the TUI and a question from a plugin appears as prompts.
- Depends on: G1-18, G1-21, G1-31, G3-01. Design refs: C-34, T-14; [integrations.md](integrations.md#oauth-for-mcp-servers).

### 3.B Node and devices

This epic also folds in the owner's Node direction, which is not yet in the contracts: Core runs on the person's own computer by default; a device joins as a Node in one action; Nodes bid for work by capacity, load, readiness and capabilities, and work of a failed Node is offered to another; repositories are only cached on a Node; non-coding work gets read-only file copies and returns artifacts to the one canonical store on the Core machine. The contract changes come with this epic ([go-core.md](go-core.md) C-35).

**G3-03 Node gate, receipt journal and transport.** `cmd/lina-node`, state machine, receipt journal, expiry.
- Done when: a Node with no valid grant is BLOCKED after install, reboot, revoke and disconnect; an earlier-epoch command is rejected; a replayed journal entry applies once; a simulated suspend revokes the grant; a journal newer than the binary exits 78; a receipt for a changed artifact without provenance is rejected.
- Depends on: G1-05, G1-07. Design refs: C-35; [main-authority.md](main-authority.md#node-states).

**G3-04 Device registration, Node host and reconcile.** Device identity, registration, grants per task, RECONCILING.
- Done when: an unregistered device gets no grant; registration grants nothing; a Core restart takes each Node through BLOCKED and RECONCILING to GRANTED only with a fresh-epoch grant; READY is reached with a Node absent; an unlisted pair is refused and reported.
- Depends on: G1-18, G2-08, G3-03. Design refs: C-35; [runtime.md](runtime.md#remote-access).

**G3-05 Remote delegation.** Workers as Node children; pull request read from GitHub.
- Done when: a task runs on the chosen device and its receipts match the grant by id; revoke or disconnect settles in-flight work unknown and reconcile settles it; no commit travels between Node and main; identical bytes on two devices get two asset ids.
- Depends on: G2-08, G2-18, G3-04. Design refs: C-29, C-35; [work-and-delegation.md](work-and-delegation.md#remote-devices).

**G3-06 Remote clients and mobile transport.** Tailnet listener, WebSocket framing for a mobile host.
- Done when: a tailnet client gets the same welcome; no listener binds a public interface; a WebSocket client passes the conformance fixtures; stop returns a typed result and needs no approval.
- Depends on: G1-18, G2-13, G3-04. Design refs: C-42; [host-protocol.md](host-protocol.md#client-connection).

**G3-07 macOS Node and seat lease.** launchd Node, Seatbelt, seat lease.
- Done when: the person's input ends the lease with a receipt id; a session lock ends it and an unlock does not revive it; a resume revokes it; no recording or keep-awake call exists; the macOS cells stay unverified until evidence.
- Depends on: G3-04. Design refs: C-35; [non-competition.md](non-competition.md#seat-lease).

### 3.C Large work

**G3-09 Splitting, dependencies and parallel hand-off.** Dependency graph, write-area overlap, event-driven release, one waiting reason.
- Done when: overlapping areas are never in flight together; a dependent is released only after the predecessor is confirmed merged; a task nothing depends on never waits; a waiting task shows exactly one reason; an unmergeable pull request goes back once to its worker; the developer view shows one lane for each worker, folded to one line by default.
- Depends on: G2-02, G2-07, G2-18. Design refs: C-57, T-14; [work-and-delegation.md](work-and-delegation.md#parallel-hand-off).

**G3-10 Devin adapter.** Hosted session API; polling only in flight.
- Done when: polling starts at dispatch and ends at a terminal state; a waiting-for-user state is blocked; a suspended state with a quota reason parks the worker; the adapter is an opt-in account of the person.
- Depends on: G2-04. Design refs: C-29; [work-and-delegation.md](work-and-delegation.md#completion-signals).

**G3-16 Antigravity adapter.** Headless child; success from the pull request.
- Done when: the result event ends the turn; success is confirmed from the pull request and never from the exit code; an unapproved tool soft-denial is reported.
- Depends on: G2-04. Design refs: C-29; [work-and-delegation.md](work-and-delegation.md#completion-signals).

**G3-17 Plan drafting.** Klotho drafts 일 and 할 일 for an accepted 목표 as a `plan_view`; confirmation is the only way to plan objects.
- Done when: no plan object exists before confirmation; a draft is replaced completely on each update; splitting into tasks is not a second confirmation.
- Depends on: G1-34, G2-23. Design refs: C-52, C-57; [cognition-and-life.md](cognition-and-life.md#klotho-project-planning).

**G3-18 CRW takeover.** One-time import tool outside every release, single-owner switch with rollback.
- Done when: every fetched record is reported imported, lost or unsupported; each is mapped to a LINA id once; a failed switch is rolled back explicitly; no product path resolves a source id.
- Depends on: G2-01, G2-17, G2-22. Design refs: [work-and-delegation.md](work-and-delegation.md#operational-switch).

### 3.D Evidence and phase acceptance

**G3-12 Non-competition evidence on a Linux box.** Tiers 1 and 2 of the evidence ladder.
- Done when: one record per applicable column at each tier with every field, counting agents and background runs; zero focus steals, injected input, clipboard access, out-of-scope file access and lost edits; a failing tier stops the ladder.
- Depends on: G3-05. Design refs: [non-competition.md](non-competition.md#evidence-ladder).

**G3-13 Phase 3 acceptance run.** Run script and evidence record for LINA 3.
- Done when: one record holds every field of the phase rules and a result per check; every Node transition is one the state table allows; given-back slices are listed as not delivered.
- Depends on: G3-02, G3-06, G3-07, G3-09, G3-10, G3-12, G3-15, G3-16, G3-17, G3-18. Design refs: Phase rules.

## Ended ids

| Id | Ended as | Reason |
| --- | --- | --- |
| G2-06 Verification pipeline | cancelled | Withdrawn: the development role is a method and the rest relies on GitHub review and CI ([work-and-delegation.md](work-and-delegation.md#development-method)). |
| G2-09 Derivation job runner and usage reconciler | cancelled | Moved to G1-36; the usage reconciler went with the withdrawn model proxy. |
| G2-10 Observation deriver and model-written brief | cancelled | Moved to G1-37. |
| G2-11 Summary condensation and `conv_describe` | cancelled | Moved to G1-38; `conv_describe` is not in the contracts. |
| G2-16 Review run | duplicate of G2-07 | A worker review thread is withdrawn; the one review is Core's call in G2-07. |
| G3-14 Forge connector and CI gate | cancelled | Withdrawn: LINA has no GitHub identity. The merge guard and CI watch survive in G2-19. |
| G3-08 Machine-specific placement spike | cancelled | Machine-specific placement is not a contract. |
| G3-11 Skill curator | duplicate of G2-12 | Pruning and merging is one job. |

Ids G1-27 and G2-07 keep their number with a changed subject. No other id changed meaning.

## Cut lines

A given-back slice moves to the start of the next phase and is listed as not delivered. The phase rules never weaken.

| Phase | Given back, in order | Never given back |
| --- | --- | --- |
| LINA 1 | Telegram (G1-25) and exchanges (G1-26), which then start LINA 2; the Hermes import (G1-27); prepared summaries (G1-38) | Epoch and recovery (G1-02), approvals (G1-05), ledger (G1-10), retraction (G1-14), the turn budget and background runs (G1-32, G1-33), restart fence (G1-24) |
| LINA 2 | Discord (G2-21), Gemini and Grok adapters (G2-27, G2-28), skill pruning (G2-12), persona growth (G2-20), the progress digest for channels (part of G2-13) | The delegation core (G2-01, G2-02, G2-04, G2-07, G2-08, G2-18, G2-19), records (G2-22, G2-23), update (G2-14) |
| LINA 3 | Antigravity and Devin (G3-16, G3-10), the macOS seat lease (G3-07), plan drafting (G3-17), the CRW takeover (G3-18), mobile transport (part of G3-06) | Node gate and reconcile (G3-03, G3-04), outward-effect marks (G3-02), splitting and parallel hand-off (G3-09) |

## Numbers to set by measurement

A number marked (P) is a starting value of this design. The others come from the contract named in the row, and the contracts also call most of them starting values. None is a LINA measurement.

| Quantity | Starting value or source | Set in |
| --- | --- | --- |
| Turn budget: acknowledgement, soft, hard, tool deadline | [runtime.md](runtime.md#turn-budget) | G1-32, tuned on waiting time and completion quality together |
| Compaction start | [conversation-and-memory.md](conversation-and-memory.md#compaction-and-resume) | G1-15 |
| Reply budgets | [conversation-and-memory.md](conversation-and-memory.md#reply-shape) | G1-34 |
| Background runs: concurrency, depth, no time or call cap | [work-and-delegation.md](work-and-delegation.md#limits) | G1-33 |
| Exchange rounds | 4, from [conversation-and-memory.md](conversation-and-memory.md#agent-exchanges) | G1-26 |
| Ring and snapshot window of a stream topic | 1,024 frames; 200 items (P) | G1-18 |
| Coalescing of progress and waiting changes | 250 ms (P) | G2-13 |
| Tasks in flight per repository | 4, from [work-and-delegation.md](work-and-delegation.md#parallel-hand-off) | G3-09 |
| Stall cap; reconcile sweep | 3 hours; 5 minutes, from [work-and-delegation.md](work-and-delegation.md#stall-and-failure) | G2-02 |
| TUI bounds | S1 to S11 (P) | G1-20 |
| Claude adapter idle stop; concurrent turns per account | 10 minutes; 2 (P) | G2-26 |
| Records repository with thousands of files | none yet | G2-22 |
| Non-competition bounds | none yet; declared per test | G3-12 |

## Not scheduled here

The LINA app (a private repository), the library viewers and meeting notes, the Google plugin, Jev, Klotho supervision and the QA product map, the news feed, LIFE, the launcher, OpenViking and the mobile host. The development-time improvement loop is not a product feature; only G1-39 serves it.
