# GitHub milestones and issues

This document lists the GitHub milestones and issues that carry the roadmap. Once they are created, the issues on GitHub are the plan of record: the state of a piece of work is its issue, and this list is a reading aid that is not kept in step with it. [go-roadmap.md](go-roadmap.md) stays the engineering breakdown, with the phases, the epics and the `Gp-nn` ids that each issue below names as its roadmap reference. The earlier Linear project is a read-only record. An issue is one pull request with one done condition, and big work is a parent issue with sub-issues. Each issue on GitHub also names its file areas and its test command, and they are not repeated here. A sub-issue's done condition is its part of its parent's, so only the other issues list one here. Versions are set by [versioning.md](versioning.md), and no milestone changes the version.

Labels: one or more area labels (`core`, `tui`, `node`, `protocol`, `docs`, `ci`) and one type label (`feature`, `bug`, `decision`). In an entry, "after" lists the issues that must be closed first; they are in the same or an earlier milestone.

Every issue names, under Design, the spec sections it implements; a pull request that changes behavior updates those sections in the same pull request, so documents and code stay in sync. M0-14 builds a headless end-to-end harness: a scripted terminal drives the TUI, and LINA Core is driven through its protocol. M1, M2 and M3 each have a suite issue that runs in `node scripts/foundation.mjs`. A feature issue whose behavior the TUI or the protocol can observe adds its scenario to its milestone's suite. The person checks only UI and UX. One review per pull request; a high-risk contract change may get one extra independent read, never a standing gate.

## Milestones

| Key | Name | Goal |
| --- | --- | --- |
| M0 | 첫 스캐폴드 | A repository skeleton that builds, passes the foundation checks for its Go components, and has an empty LINA Core and TUI that start and stop, with `lina version` showing the version and commit. |
| M1 | 나를 기억하는 리나와 대화 | Install LINA, sign in with ChatGPT and talk with Lina in the TUI and one Telegram chat, with memory that remembers, forgets and recalls with evidence, heavy work in the background, and backup and restore. |
| M2 | 일 하나 맡기기 | Hand one 일 to a worker under a work definition, review its pull request once, merge or close it, see progress at a glance, route across more accounts, and update with rollback. |
| M3 | 큰 일을 여러 작업·기기로 | Split a large 목표 into tasks that run in parallel across workers and devices: Nodes with grants bid for work, repositories are only cached on Nodes, plus plugins and the switch from the earlier work system. |
| Later | Later | The LINA app (its own public repository, `thisisjun786/lina-app`) after M1 to M3, and decisions that wait for real use, such as when LINA leaves alpha. |

## M0 첫 스캐폴드

15 issues (parents 1, sub-issues 6, standalone 8).

- **M0-01** Align the release script with the version policy\
  `ci` `feature`\
  Done: The usage text, the error message and the examples of `scripts/release.mjs` use an alpha tag, and its test accepts `v0.0.1-alpha.20261020.1` and rejects a tag with `+` build metadata. `node scripts/foundation.mjs` passes.
- **M0-02** Go skeleton, modules and import rules\
  `core` `node` `ci` `feature` · G1-01\
  Done: A seeded import that breaks a rule fails `go test`. The foundation script passes with the components registered. The toolchain lines of both `go.mod` files and `go.work` match. `go.sum`, the module count and the edit-to-test and full foundation times are recorded. Every sub-issue is closed.
- **M0-03** Create the root module, the kit module and go.work\
  `core` `feature` · parent M0-02\
- **M0-04** Add the lina and lina-node command skeletons\
  `core` `node` `feature` · parent M0-02 · after M0-03\
- **M0-05** Register the Go components in foundation and add the components check\
  `ci` `feature` · parent M0-02 · after M0-03, M0-04\
- **M0-06** Add the module checks of the dependencies check\
  `ci` `feature` · parent M0-02 · after M0-05\
- **M0-06b** Add the vulnerability and licence checks to the dependencies check\
  `ci` `feature` · parent M0-02 · after M0-06\
- **M0-07** Add the import rules test and record the V6 and V7 baselines\
  `core` `ci` `feature` · parent M0-02 · after M0-04, M0-06, M0-06b · G1-01\
- **M0-08** Print the version and commit from lina and lina-node\
  `core` `node` `feature` · after M0-01, M0-04, M0-07\
  Done: `lina version` and `lina-node version` print `<version> (commit <short sha>, built <date>)`, with the three values set by `-ldflags -X` on `internal/buildinfo`. A tagged build passes `0.0.1-alpha.YYYYMMDD.N`, and the date is UTC `YYYY-MM-DD`. A build without ldflags prints `0.0.1-alpha.dev (commit <sha>)`, with the commit read from the VCS data Go embeds, or `unknown` outside a repository. A test builds each binary both ways and checks the printed fields. No `+sha` build metadata appears in any version, and the source holds no release number.
- **M0-09** Write a build manifest next to the binaries\
  `core` `ci` `feature` · after M0-01, M0-05, M0-08\
  Done: `node scripts/build.mjs --out <dir>` builds `lina` and `lina-node` with `-trimpath`, `CGO_ENABLED=0` and the ldflags of `lina version`. It writes `<dir>/manifest.json` with the version, commit, date, Go toolchain, kit version, and the name, size and SHA-256 of each binary. The kit version is the integer in `kit/VERSION`. From a work tree with uncommitted changes it builds and writes no manifest. `internal/core/manifest` reads and validates the file. A test checks that the commit in the manifest equals the commit printed by `lina version` from the same build.
- **M0-10** Add the protocol schema skeleton for the client connection\
  `protocol` `feature` · after M0-03, M0-05\
  Done: Every schema file declares JSON Schema draft 2020-12. The envelope schema has exactly the fields of the envelope section of host-protocol.md, and `hello`, `welcome` and the closed error codes are defined for `client` 1.0. The fixtures for a hello and welcome pair and for an `unsupported_combination` refusal decode with `kit/protocol`, an unknown envelope field is rejected, and re-encoding gives the same JSON. The Go types are hand-written here; G1-17 replaces them with generated types. `protocol/schema/` and `protocol/fixtures/` are registered in `scripts/verify/scope.mjs`.
- **M0-11** Start and stop an empty LINA Core\
  `core` `feature` · after M0-04, M0-07, M0-10\
  Done: Run as a child process, `lina core --socket <path>` creates the socket at mode 0600 in a 0700 directory. It answers a `hello` with a `welcome` that carries a fixed placeholder main id and epoch, and answers a `hello` whose protocol versions it does not list with `unsupported_combination` and closes. SIGTERM or SIGINT ends it with exit 0 and removes the socket file. The test waits for readiness by connecting, never by a fixed sleep. It holds no state root, lock, epoch or database yet.
- **M0-12** Connect and quit with an empty TUI\
  `tui` `feature` · after M0-07, M0-10, M0-11\
  Done: Against the empty Core, `lina tui --socket <path>` completes the handshake, reports the connection as live and exits 0 on `q` or end of input. With no socket at the path it exits 69 and starts no Core. When the Core refuses the combination it exits 78. It imports no terminal UI library yet.
- **M0-13** Generate release notes from merged pull request titles and labels\
  `ci` `feature`\
  Done: `node scripts/release-notes.mjs --from <tag or root> --to <sha> --out <file>` lists each pull request merged in the range once, as `title (#number)`, grouped by its type label (feature, bug, decision). A title with `!` before the colon is listed first under Breaking changes, and a pull request with no type label is listed under Other. An empty range exits non-zero. The output is accepted by `scripts/release.mjs --notes-file`. The tests use a stub `gh` and no network.
- **M0-14** Headless end-to-end harness for the TUI and LINA Core\
  `ci` `tui` `core` `feature` · after M0-11, M0-12\
  Done: A scripted terminal drives the TUI and a protocol client drives LINA Core, with no window and no person; one scenario starts Core and the TUI, sends a message, sees the reply state and quits; the suite runs as part of `node scripts/foundation.mjs`. When the change alters behavior, the spec sections listed under Design are updated in the same pull request.


## M1 나를 기억하는 리나와 대화

88 issues (parents 22, sub-issues 48, standalone 18).

- **M1-01** Control files, epoch, start order and idle registry\
  `core` `feature` · after M0-02, M0-11 · G1-02\
  Done: `kill -9` at each start step is followed by a start with a strictly higher epoch; a second `lina core` exits 69; a non-local state root exits 78; an external call without a lease does not compile; an unreadable registry reads as busy.
- **M1-01a** Control files, lock, epoch and start order\
  `core` `feature` · parent M1-01 · after M0-02, M0-11 · G1-02\
- **M1-01b** Lease kit and the idle registry\
  `core` `feature` · parent M1-01 · after M1-01a · G1-02\
- **M1-02** Canon writer, epoch fence, halt and SQLite binding\
  `core` `feature` · after M1-01 · G1-03\
  Done: A write under a lower epoch fails `stale_epoch` and latches the writer; a write on a reader fails with a closed code; a store newer than the binary exits 78 unchanged; injected corruption writes the halt marker and reads still work; the binding is recorded in the build manifest.
- **M1-03** Choose the SQLite binding and record it in the build manifest\
  `core` `decision` · parent M1-02 · after M0-09, M1-01 · G1-03\
- **M1-04** Canon writer, epoch fence and halt marker\
  `core` `feature` · parent M1-02 · after M1-03 · G1-03\
- **M1-05** Diagnosis emitter, event store and citation check\
  `core` `feature` · after M1-01 · G1-04\
  Done: No emitter parameter is a free string; stored keys equal the twelve names; the model, interruption and storage injections are each explained by event id, class and cause code; returned events hold no secret, prompt, provider body or absolute home path.
- **M1-06** Asset store and staging\
  `core` `feature` · after M1-02 · G1-29\
  Done: A rename keeps asset id and hash; an edit keeps the id, changes the hash and advances the revision; identical content gets two ids; an import writes only under staging until handed over; an export leaves a canon record and no staged copy; a backup detects a hash mismatch.
- **M1-07** Approvals, Dispatch, Gate and effect ledger\
  `core` `feature` · after M1-01, M1-02, M1-05 · G1-05\
  Done: A plugin tool that no service profile marks runs without asking; an approval holds what, where and undoable computed by Core; a standing grant for network access revokes in one action; one idempotency key runs once; an approval pending at restart is stopped and a late button answers stale; a write on a stale revision is refused.
- **M1-08** Authorization check, tool dispatch and the effect ledger\
  `core` `feature` · parent M1-07 · after M1-01, M1-02, M1-05 · G1-05\
- **M1-09** Approval records and standing grants\
  `core` `feature` · parent M1-07 · after M1-08 · G1-05\
- **M1-10** Speaker kinds, masking and the secret store\
  `core` `feature` · after M0-02, M1-06, M1-07 · G1-06\
  Done: A seeded token is masked and its span is in no index, summary or export; a formatter prints a placeholder for a secret; speaker kind never comes from text; planted instructions in a tool result arrive marked as external evidence and cannot answer an approval.
- **M1-10a** Speaker kinds and external evidence\
  `core` `feature` · parent M1-10 · after M0-02, M1-07 · G1-06\
- **M1-10b** Masking and the secret store\
  `core` `feature` · parent M1-10 · after M0-02, M1-06 · G1-06\
- **M1-11** Sandbox, tool executor and trash\
  `core` `feature` · after M1-01, M1-07, M1-10 · G1-07\
  Done: The start probe passes or shell and file tools turn off with a cause while conversation continues; a canary cannot read canon, secrets or the client socket; network waits for once, always allow or deny; a patch with stale context fails; a trash delete restores identical bytes.
- **M1-12** Sandbox wrapper, process groups and the start probe\
  `core` `feature` · parent M1-11 · after M1-01, M1-07, M1-10 · G1-07\
- **M1-13** File tools, apply_patch, network ask and trash\
  `core` `feature` · parent M1-11 · after M1-12 · G1-07\
- **M1-14** Provider interface and the Responses adapter\
  `core` `feature` · after M1-01, M1-02, M1-05 · G1-08\
  Done: scripted-wire tests cover each failure kind by code, then type, then status; no request is sent after the first output or after a failure; a cut-off response runs no tool; unreported usage is stored unknown; a field outside the manifest list is not sent.
- **M1-15** Provider interface, usage and budget types\
  `core` `feature` · parent M1-14 · after M1-01, M1-02, M1-05 · G1-08\
- **M1-16** Responses adapter with scripted-wire tests\
  `core` `feature` · parent M1-14 · after M0-09, M1-15 · G1-08\
- **M1-17** Loop core and run durability\
  `core` `feature` · after M1-07, M1-14 · G1-09\
  Done: A scripted wire shows no tool from a cut-off response, one result per call in call order, an aborted result for each open call, and steering applied at the next round; no effect is applied twice across a restart.
- **M1-18** Ledger, request assembly and preparation\
  `core` `feature` · after M1-02, M1-10, M1-14 · G1-10\
  Done: An update of a ledger row is denied; identical input gives identical bytes; the layout order matches the contract; each failing preparation step shows one plain line and a stub provider sees zero calls; the ContextPacket is never stored.
- **M1-19** Ledger tables and the admit path\
  `core` `feature` · parent M1-18 · after M1-02, M1-10 · G1-10\
- **M1-20** Request assembly, the ContextPacket and preparation\
  `core` `feature` · parent M1-18 · after M1-14, M1-19 · G1-10\
- **M1-21** Inbox, lanes, steering and deadlines\
  `core` `feature` · after M1-02, M1-17, M1-18 · G1-11\
  Done: A duplicate key inserts one row; the acknowledgement follows the commit; a message during a turn is applied as steering at the next round; stop ends open calls as aborted; after `kill -9` mid-turn the input is not replayed and the conversation shows one plain line.
- **M1-21a** Inbox and lanes\
  `core` `feature` · parent M1-21 · after M1-02 · G1-11\
- **M1-21b** Steering, stop and deadlines\
  `core` `feature` · parent M1-21 · after M1-17, M1-18, M1-21a · G1-11\
- **M1-22** Agents, presets, persona layers and first run\
  `core` `feature` · after M1-02, M1-06, M1-07 · G1-12\
  Done: A new revision applies from the next turn and the preparation acknowledgement names it; a preset update changes only core and default voice; one agent's change never alters another's; a deleted agent sits in the trash 30 days and its channel bindings end at once.
- **M1-23** Memory items, recall, person model and Korean search\
  `core` `feature` · after M1-02, M1-07, M1-18, M1-22 · G1-13\
  Done: Fixtures find a particle-attached three-syllable word and a two-syllable word in ledger and memory; recall for agent A returns no id held by B; a person-subject item exists only from person events; "remembered" is said only after the commit.
- **M1-24** Memory items, recall scope and the person model\
  `core` `feature` · parent M1-23 · after M1-02, M1-07, M1-18, M1-22 · G1-13\
- **M1-25** Korean search over the ledger and memory\
  `core` `feature` · parent M1-23 · after M1-24 · G1-13\
- **M1-26** Retraction, trash and the removal list\
  `core` `feature` · after M1-18, M1-23 · G1-14\
  Done: After a forget, search, summaries, the ContextPacket and answers give zero hits in the next request, also after restoring an older generation; a fault leaves no half state; a tombstone never exists without its removal-list line; a forgotten item is not extracted again.
- **M1-27** Compaction, summary tree and raw recovery\
  `core` `feature` · after M1-14, M1-18, M1-23, M1-26 · G1-15\
  Done: A failed summarizer publishes no node and the events stay in the request; a stale-frontier publish fails; `expand` returns the exact range of a fact stated before compaction; a continuation that does not fit sends nothing and logs sizes; a retraction rebuilds the node.
- **M1-28** Summary tree tables and publication\
  `core` `feature` · parent M1-27 · after M1-18, M1-26 · G1-15\
- **M1-29** Compaction passes, continuation turn and expand\
  `core` `feature` · parent M1-27 · after M1-14, M1-23, M1-28 · G1-15\
- **M1-30** Skills written by agents, adopted at once, undone in one action\
  `core` `feature` · after M1-06, M1-07, M1-18, M1-22 · G1-16\
  Done: An instruction-only patch is adopted as a new version with a one-line notice after the commit; undo restores the previous text as a new version; a skill with executable code is not adopted this way; the index holds name and description and bodies load only when needed.
- **M1-31** Schema, generators and fake Core\
  `tui` `protocol` `feature` · after M0-02, M0-10 · G1-17\
  Done: Regenerated types equal the checked-in files; every fixture round-trips; the server rejects an unknown request field and the client counts an unknown kind; a fixture replay through the fake Core is deterministic with no sleep.
- **M1-32** Full client schema, type generator and generated Go types\
  `protocol` `feature` · parent M1-31 · after M0-02, M0-10 · G1-17\
- **M1-33** Conformance fixtures and runner\
  `protocol` `feature` · parent M1-31 · after M1-32 · G1-17\
- **M1-34** Fake Core with fault switches\
  `tui` `protocol` `feature` · parent M1-31 · after M1-33 · G1-17\
- **M1-35** Client service\
  `core` `protocol` `feature` · after M1-07, M1-10, M1-11, M1-21, M1-22, M1-31 · G1-18\
  Done: An unlisted pair is refused `unsupported_combination`; a gap yields resync and a snapshot and replaying twice changes nothing; an overflowing topic closes alone; mutations in RECOVERING return unavailable; a connect from inside the sandbox fails; an approval answer from a Core descendant is refused.
- **M1-36** Client listener, handshake and the approval decider rule\
  `core` `protocol` `feature` · parent M1-35 · after M0-11, M1-07, M1-10, M1-11, M1-22, M1-31 · G1-18\
- **M1-37** Topics, cursors, resync and overflow\
  `core` `protocol` `feature` · parent M1-35 · after M1-21, M1-36 · G1-18\
- **M1-38** Accounts and Sign in with ChatGPT\
  `core` `feature` · after M1-10, M1-14, M1-35 · G1-31\
  Done: One refresh runs at a time per account and a rotated refresh token is stored before use; a failed refresh shows needs sign-in and uses no other account; a second ChatGPT account never changes the binding; no token appears in a log, request, sandbox or client; the V1 record states what the request surface accepted.
- **M1-39** Account store, single refresh actor and account status\
  `core` `feature` · parent M1-38 · after M1-10, M1-14 · G1-31\
- **M1-40** Sign in with ChatGPT: relay, owner binding and the V1 record\
  `core` `feature` · parent M1-38 · after M1-35, M1-39 · G1-31\
- **M1-41** TUI state, layout and renderer\
  `tui` `feature` · after M1-31 · G1-19\
  Done: Duplicates and a double snapshot change nothing; layout property tests show disjoint regions and the floor at every size; fuzzing `sanitize.Display` finds no control, bidirectional or OSC output; renderer calls are proportional to viewport rows.
- **M1-42** TUI state reducer and display sanitizer\
  `tui` `feature` · parent M1-41 · after M1-31 · G1-19\
- **M1-43** TUI layout plan and incremental renderer\
  `tui` `feature` · parent M1-41 · after M1-42 · G1-19\
- **M1-44** TUI library spike\
  `tui` `decision` · after M0-02, M1-41 · G1-20\
  Done: The terminal list is recorded; S1 to S11 each have an evidence record with every field; the recording writer runs, quits and parses with no sleep; the chosen library step is recorded.
- **M1-45** Spike harness: recording writer, terminal list and evidence format\
  `tui` `decision` · parent M1-44 · after M0-02, M1-41 · G1-20\
- **M1-46** Run S1 to S11 and record the chosen library step\
  `tui` `decision` · parent M1-44 · after M1-45 · G1-20\
- **M1-47** Backup generations and restore\
  `core` `feature` · after M1-02, M1-06, M1-21, M1-26, M1-27 · G1-22\
  Done: A kill during copy leaves no generation; a hash mismatch fails the backup; the zero-exposure test passes also into another root; after restore non-terminal external-effect rows are unknown, leases are void and accounts show needs sign-in; the epoch never falls.
- **M1-47a** Backup generations\
  `core` `feature` · parent M1-47 · after M1-02, M1-06, M1-26, M1-27 · G1-22\
- **M1-47b** Restore from a backup generation\
  `core` `feature` · parent M1-47 · after M1-21, M1-47a · G1-22\
- **M1-48** Install\
  `core` `feature` · after M1-01, M1-10, M1-14 · G1-23\
  Done: Install succeeds with no account and the first preparation fails with a plain cause and the sign-in offer; a foreign unit or an occupied port is refused and nothing is overwritten; versions of discovered tools are recorded and unverified ones listed.
- **M1-49** Restart request and fence\
  `core` `feature` · after M1-01, M1-35, M1-48 · G1-24\
  Done: A request does not exit while a fake effect is in flight; unknown idle blocks; a request survives a crash and reports owed until discharged; a restore cannot resurrect it; a settings, persona or binding change mints no epoch.
- **M1-50** Packaging and residency check\
  `core` `ci` `feature` · after M1-02, M1-48 · G1-40\
  Done: sqlite-vec loads after its digest check; services start; size, start time and resident memory and CPU stay within the budget written before the run on both platforms; the binding is recorded in the manifest.
- **M1-51** Static binaries, sqlite-vec and service start\
  `core` `ci` `feature` · parent M1-50 · after M1-02, M1-48 · G1-40\
- **M1-52** 24-hour residency run on Linux and macOS\
  `core` `ci` `feature` · parent M1-50 · after M1-51 · G1-40\
- **M1-53** Channel gateway and Telegram\
  `core` `feature` · after M1-06, M1-10, M1-21, M1-22, M1-35 · G1-25\
  Done: `kill -9` between receipt and reply leaves one inbox row, the matching cursor and no repeat; an unpaired sender yields no turn; the token is in no event, ledger row or child environment; READY is reached with the platform unreachable; a button from anyone but the paired owner changes nothing; a send of unknown outcome is not repeated.
- **M1-54** Channel gateway core: bindings, pairing, inbox cursor and outbound queue\
  `core` `feature` · parent M1-53 · after M1-06, M1-10, M1-21, M1-22 · G1-25\
- **M1-55** Telegram adapter\
  `core` `feature` · parent M1-53 · after M1-35, M1-54 · G1-25\
- **M1-56** Background runs\
  `core` `feature` · after M1-06, M1-07, M1-17, M1-18 · G1-33\
  Done: Start returns an id before the run's first model call; tools outside the run scope are not declared; no time or call cap exists; a second interruption lands the run failed or partial; a cancelled run stays cancelled; an unresolved citation lands the run partial.
- **M1-57** Agent exchanges\
  `core` `feature` · after M1-21, M1-22, M1-56 · G1-26\
  Done: A request delivers once across `kill -9`; an acknowledgement ends the exchange without a model turn; no exchange message enters a person's conversation as a person turn; an exchange turn reads no personal knowledge; the flag is set only when the person owns the decision.
- **M1-58** Turn budget and hand-off to the background\
  `core` `feature` · after M1-17, M1-21, M1-56 · G1-32\
  Done: At the soft budget every call except `start_run` and `dispatch_work` gets `budget_reached` and does not run; at the hard budget with no effect a run starts from the gathered state; with an effect, further effects wait on a question; a report turn makes no tool round trip.
- **M1-59** Reply shape and typed results\
  `core` `feature` · after M1-06, M1-18, M1-31 · G1-34\
  Done: A chat reply over 5 lines or 300 characters is cut at the last sentence and a card is made while the ledger keeps the reply; a body that fails the schema is refused `malformed` to the agent only; a stale revision is refused; a double save makes one record.
- **M1-60** Reply budgets and the streaming filter\
  `core` `feature` · parent M1-59 · after M1-18 · G1-34\
- **M1-61** Typed results: validation, rendering and the result tools\
  `core` `feature` · parent M1-59 · after M1-06, M1-18, M1-31 · G1-34\
- **M1-62** Waiting list, running list and notices\
  `core` `feature` · after M1-07, M1-21, M1-56 · G1-35\
  Done: Rows rank in the contract order on a fixture; the source is re-read before a row shows; a notice is never delivered while a reply streams; notices within 10 s coalesce; answering a row records the decision with person and agent.
- **M1-63** TUI screens and input\
  `tui` `feature` · after M1-22, M1-35, M1-41, M1-44, M1-62 · G1-21\
  Done: A PTY run against the fake Core covers start, connect, refuse, recover and quit; two fake clients racing one approval give exactly one `resolved`; an unchanged state writes zero bytes; with no socket the Core-absent screen appears and no Core is started; every closed code has a catalog entry by id.
- **M1-64** TUI program shell, connection states and catalogs\
  `tui` `feature` · parent M1-63 · after M0-12, M1-35, M1-41, M1-44 · G1-21\
- **M1-65** Conversation, composer, home rows, cards and pager\
  `tui` `feature` · parent M1-63 · after M1-22, M1-62, M1-64 · G1-21\
- **M1-66** Approval and question cards\
  `tui` `feature` · parent M1-63 · after M1-64 · G1-21\
- **M1-67** TUI memory, agent, account, channel and talk-log commands\
  `tui` `feature` · after M1-22, M1-23, M1-26, M1-30, M1-38, M1-53, M1-57, M1-63 · G1-30\
  Done: A forget from the TUI gives zero hits in the next request; agent create, choose, rename and delete round-trip by id; a ChatGPT sign-in completes through the relay from the TUI, including a pasted URL; a channel connection is paired and bound from the TUI, and the agent talk log lists one line per exchange and expands it.
- **M1-68** TUI memory and agent commands\
  `tui` `feature` · parent M1-67 · after M1-22, M1-23, M1-26, M1-30, M1-63 · G1-30\
- **M1-69** TUI account commands and the sign-in relay\
  `tui` `feature` · parent M1-67 · after M1-38, M1-63 · G1-30\
- **M1-70** TUI channel, import and agent talk-log commands\
  `tui` `feature` · parent M1-67 · after M1-53, M1-57, M1-63 · G1-30\
- **M1-71** Moirai worker and the Core job API\
  `core` `feature` · after M1-02, M1-07, M1-14 · G1-36\
  Done: A worker killed mid-job leaves canon unchanged and resumes from its watermark; the worker holds no credential; every write goes through Apply; with the worker absent only background memory and planning are unsupported.
- **M1-72** Hermes install import\
  `core` `feature` · after M1-06, M1-23, M1-26, M1-47, M1-71 · G1-27\
  Done: On synthetic fixtures counts reconcile row for row; every source key has one legacy reference in both directions; the reverse mapper hash equals the source hash; a second run adds nothing; no write reaches a source; an unknown schema is refused; recall returns no imported observation before promotion.
- **M1-73** Hermes snapshot readers, schema gate and secret scan\
  `core` `feature` · parent M1-72 · after M1-06, M1-71 · G1-27\
- **M1-74** Staged import with legacy references and the reverse mapper\
  `core` `feature` · parent M1-72 · after M1-23, M1-26, M1-73 · G1-27\
- **M1-75** Verification, promotion and the import seal\
  `core` `feature` · parent M1-72 · after M1-47, M1-74 · G1-27\
- **M1-76** Extraction, maintenance pass and person brief\
  `core` `feature` · after M1-23, M1-26, M1-71 · G1-37\
  Done: A person-subject item comes only from person events; a fan-out fixture yields one item with two evidences; a chain of three statements returns only the newest; the pass applies only pre-authorized actions and is silent when nothing changed.
- **M1-77** Prepared summaries and higher tree levels\
  `core` `feature` · after M1-27, M1-71 · G1-38\
  Done: A prepared summary is used only if the covered history is unchanged and its epoch holds; no always-on rollup job exists; a retraction rebuilds the node and its ancestors.
- **M1-78** Replay command and episode export\
  `core` `feature` · after M1-18, M1-27 · G1-39\
  Done: A replay writes no ledger event and no effect row; an effect call is answered by the recorded result or the turn is excluded; retracted and forgotten items are left out; an export selects nothing by default and masks secrets; the manifest counts left-out turns.
- **M1-80** M1 headless end-to-end suite\
  `ci` `tui` `core` `feature` · after M0-14, M1-47, M1-49, M1-50, M1-53, M1-57, M1-58, M1-59, M1-63, M1-67, M1-72, M1-76, M1-77, M1-78\
  Done: Every acceptance check of LINA 1 that a program can observe runs headless through the harness of M0-14 (the TUI by scripted terminal, LINA Core through its protocol) as part of `node scripts/foundation.mjs`; the person's part of the acceptance is a UI and UX check only. When the change alters behavior, the spec sections listed under Design are updated in the same pull request.
- **M1-79** M1 acceptance run (LINA 1)\
  `core` `tui` `ci` `feature` · after M1-47, M1-49, M1-50, M1-53, M1-57, M1-58, M1-59, M1-63, M1-67, M1-72, M1-76, M1-77, M1-78, M1-80 · G1-28\
  Done: One record holds every field of the phase rules and a result per acceptance check; epochs rise strictly across the matrix; a failed check reruns the whole record on the fixing commit.

## M2 일 하나 맡기기

50 issues (parents 12, sub-issues 25, standalone 13).

- **M2-01** CRW schema pin and fixture runner\
  `core` `feature` · after M0-02, M1-79 · G2-17\
  Done: The pin record names the CRW commit, a hash for each adopted schema document and fixture file, and the adopted fixture domains; the runner takes expected values from the fixture; a changed hash fails; the pin appears in the build manifest.
- **M2-02** Task records and work area\
  `core` `feature` · after M1-07, M1-21, M1-22, M2-01 · G2-01\
  Done: A second active run on a task is refused; a task worktree is created from the base commit under `work/tasks`; the projection rebuilt from the ledger equals the live one; a result without a pull request is copied into the generation; a receipt for a changed artifact without provenance is rejected.
- **M2-03** Work ledger and card projection\
  `core` `feature` · parent M2-02 · after M1-07, M1-21, M1-22, M2-01 · G2-01\
- **M2-04** Work area, task worktrees and protected refs\
  `core` `feature` · parent M2-02 · after M2-03 · G2-01\
- **M2-05** Completion events, reaction path and reconcile sweep\
  `core` `feature` · after M1-21, M2-02 · G2-02\
  Done: A completion event is a row before anything reacts; the same task, attempt and head collapse to one row; an event with a head that is not current is ignored; `kill -9` between a state change and its event leaves both or neither; a lost event is recreated once.
- **M2-06** Timers, reminders and sensors\
  `core` `feature` · after M1-21, M2-05 · G2-03\
  Done: A nonexistent local time and a repeated hour each fire once; `kill -9` at claim gives one fire per instant; an unchanged sensor signature spends no model call.
- **M2-07** Worker adapter boundary and the Codex adapter\
  `core` `ci` `feature` · after M1-07, M1-35, M1-79 · G2-04\
  Done: The round trip completes with the installed Codex and records its version; an unverified version is reported and still used; a worker approval reaches the originating conversation and the answer returns; no sandbox, credential or setting is imposed on the worker.
- **M2-08** Worker adapter interface and generated Codex protocol types\
  `core` `ci` `feature` · parent M2-07 · after M1-07, M1-35, M1-79 · G2-04\
- **M2-09** Codex adapter session over codex app-server\
  `core` `feature` · parent M2-07 · after M2-08 · G2-04\
- **M2-49** Codex dynamic tools, approvals and quota rows\
  `core` `feature` · parent M2-07 · after M2-09 · G2-04\
- **M2-10** Claude Code adapter\
  `core` `feature` · after M2-07 · G2-05\
  Done: A final result message ends the turn; exit without one is a failure; a receipt built from the final result and the pull request is marked adapter-derived; nothing is installed into the user's tool.
- **M2-11** Worker stop, reconcile and one dispatch again\
  `core` `feature` · after M1-01, M2-07, M2-10 · G2-08\
  Done: `kill -9` of Core mid-task leaves the effect unknown and the thread snapshot settles it without a new turn; dispatch again happens once and only when no unknown effect remains; a second failure becomes a decision row; an interrupt is sent only for the listed reasons.
- **M2-12** Work definition and worker choice\
  `core` `feature` · after M2-02, M2-07 · G2-18\
  Done: A definition without a done condition or write area is refused; two tasks with overlapping write areas are never in flight together; a worker whose quota row is blocked is not chosen before its reset; unknown quota is usable; a quota stop hands the same packet once to the next worker.
- **M2-39** Work definition validation and the task packet\
  `core` `feature` · parent M2-12 · after M2-02, M2-07 · G2-18\
- **M2-40** Worker chooser, area leases and quota fallback\
  `core` `feature` · parent M2-12 · after M2-39 · G2-18\
- **M2-13** GitHub reads, CI watch and merge\
  `core` `feature` · after M1-07, M1-11, M1-79 · G2-19\
  Done: No webhook or events feed is used; one watcher per pull request ends at the conclusion; a rate limit shows CI pending with its cause; a moved head is read again and not merged; a protected-ref mismatch blocks the merge; a person's merge is observed only after the merge commit and target ref are checked.
- **M2-14** GitHub reads and the CI watcher\
  `core` `feature` · parent M2-13 · after M1-07, M1-11, M1-79 · G2-19\
- **M2-15** Head-pinned merge and observed merges\
  `core` `feature` · parent M2-13 · after M2-14 · G2-19\
- **M2-16** Design-conformance review, one return, merge or close\
  `core` `feature` · after M2-05, M2-12, M2-13 · G2-07\
  Done: A second review request is refused; P2 and P3 are absent from the return by finding id; a failing required check and a change outside the write area are each a P1 finding; a residual P0 or P1 closes the pull request with the findings as the reason and leaves the task blocked without a verified verdict.
- **M2-41** Design-conformance review and its findings\
  `core` `feature` · parent M2-16 · after M2-05, M2-12, M2-14 · G2-07\
- **M2-42** One return of findings, merge or close\
  `core` `feature` · parent M2-16 · after M2-15, M2-41 · G2-07\
- **M2-17** Skill pruning and merging job\
  `core` `feature` · after M1-30, M1-71, M1-79 · G2-12\
  Done: Each prune or merge is a versioned skill write with a one-line notice and one-action undo; built-in skills are untouched; a merge the person undid is not made again.
- **M2-18** Persona growth step with undo\
  `core` `feature` · after M1-22, M1-76, M1-79 · G2-20\
  Done: A step is a new revision applied from the next turn; undo restores the previous text as a new revision; an undone step is not proposed again; only the growth snapshot layer changes.
- **M2-19** Discord adapter\
  `core` `feature` · after M1-53, M1-79 · G2-21\
  Done: A repeated interaction is handled once; a press by anyone but the paired owner changes nothing; text over 2,000 characters splits on paragraph boundaries; a blocked chat marks the binding unreachable.
- **M2-20** Update flow with rollback\
  `core` `feature` · after M1-47, M1-48, M1-49, M1-79 · G2-14\
  Done: Each failure injection of runtime.md leaves the previous composition running or the install held with a closed cause; an old binary against a newer store leaves the store byte-identical.
- **M2-21** Update digest, preflight and gate cells\
  `core` `feature` · parent M2-20 · after M1-47, M1-48, M1-49, M1-79 · G2-14\
- **M2-22** Update swap, rollback and failure injections\
  `core` `feature` · parent M2-20 · after M2-21 · G2-14\
- **M2-23** Records repository and index\
  `core` `feature` · after M1-02, M1-06, M1-07, M1-79 · G2-22\
  Done: An index rebuilt from the files equals the live index; an outside change is detected and shown and not merged; a write on a stale revision is refused; moving a record keeps its id; behavior with thousands of files is recorded.
- **M2-47** Records Git repository and write path\
  `core` `feature` · parent M2-23 · after M1-02, M1-06, M1-07, M1-79 · G2-22\
- **M2-48** Records index, rebuild and outside-change detection\
  `core` `feature` · parent M2-23 · after M2-47 · G2-22\
- **M2-24** To-do record, views, recurrence and candidates\
  `core` `feature` · after M2-06, M2-23 · G2-23\
  Done: The today rule passes its fixture; a recurring item advances only to a future occurrence; a dependency cycle is rejected; an ambiguous date is never saved silently; a candidate is added on its own only when the deterministic test passes.
- **M2-25** To-do record, views and the today rule\
  `core` `feature` · parent M2-24 · after M2-23 · G2-23\
- **M2-26** Recurrence, candidates and memo promotion\
  `core` `feature` · parent M2-24 · after M2-06, M2-25 · G2-23\
- **M2-27** Progress view, full waiting list and digest\
  `core` `tui` `feature` · after M1-35, M1-62, M1-63, M2-05, M2-24 · G2-13\
  Done: A progress line holds no id or pull request and reads unknown for an unobserved value; the digest has at most five lines and a count; a waiting row whose source shows it done is dropped; the order switch is asked of Core; stop, stop all and a follow-up have the same effect from the TUI and from a channel.
- **M2-43** Progress lines and the full waiting list\
  `core` `feature` · parent M2-27 · after M1-35, M1-62, M2-05, M2-24 · G2-13\
- **M2-44** Digest and the same controls from the TUI and a channel\
  `core` `tui` `feature` · parent M2-27 · after M1-63, M2-43 · G2-13\
- **M2-28** Connected accounts, plan catalog and automatic setup\
  `core` `ci` `feature` · after M1-38, M1-67, M1-79 · G2-25\
  Done: Setup is the same for the same inputs; the catalog version and digest are in the build manifest; an API-key account serves a role only where no subscription covers it; the summary line changes in one action; general settings show no model id.
- **M2-45** Plan catalog and its digest in the build manifest\
  `core` `ci` `feature` · parent M2-28 · after M1-38, M1-67, M1-79 · G2-25\
- **M2-46** Automatic setup, summary line and override\
  `core` `feature` · parent M2-28 · after M2-45 · G2-25\
- **M2-29** TUI records, progress and agent screens\
  `tui` `feature` · after M1-63, M2-24, M2-27, M2-28 · G2-24\
  Done: A quick add shows chips before it saves; every view is a Core query result drawn without client ranking; a progress view line holds no id-shaped field; the board groups Core's rows into Waiting for me, Running and Done; a follow-up note reaches a running background run at its next round trip; the usage meter reads "unknown" for an unreported quota.
- **M2-30** TUI record views, memo inbox, quick add and progress view\
  `tui` `feature` · parent M2-29 · after M1-63, M2-24, M2-27 · G2-24\
- **M2-31** TUI board, steering and usage meter\
  `tui` `feature` · parent M2-29 · after M2-28, M2-30 · G2-24\
- **M2-32** Claude adapter\
  `core` `feature` · after M1-07, M1-14, M1-38, M1-79 · G2-26\
  Done: A turn runs with built-in tools off and LINA tools over MCP; a runtime that loaded anything unlisted is refused; competing credential variables are absent; quota from rate-limit events is recorded; the SDK is in no LINA artifact.
- **M2-33** Claude adapter program and its lockfile\
  `core` `feature` · parent M2-32 · after M1-14, M1-79 · G2-26\
- **M2-34** Claude provider in Go: start, init check and idle stop\
  `core` `feature` · parent M2-32 · after M1-07, M1-14, M1-38, M2-33 · G2-26\
- **M2-35** Gemini adapter\
  `core` `feature` · after M1-14, M1-38, M1-79 · G2-27\
  Done: A turn returns provider state items as received; a missing key is needs sign-in with no fallback; quota comes from error codes and LINA's own count.
- **M2-36** Grok adapter\
  `core` `feature` · after M1-07, M1-14, M1-38, M1-79 · G2-28\
  Done: A turn runs with the command's tools restricted and LINA tools offered; a permission request reaches Core's approval path and returns; telemetry switches are set off; a missing command makes only this provider unsupported.
- **M2-37** Quota-aware routing\
  `core` `feature` · after M2-28, M2-32 · G2-29\
  Done: A new request goes to another connected account when a subscription is near its limit and a one-line event is written; a started request is never resent; unknown quota is not treated as zero; pay-per-use is not started beyond the set budget.
- **M2-50** M2 headless end-to-end suite\
  `ci` `tui` `core` `feature` · after M0-14, M2-10, M2-11, M2-16, M2-17, M2-18, M2-19, M2-20, M2-27, M2-29, M2-35, M2-36, M2-37\
  Done: Every acceptance check of LINA 2 that a program can observe runs headless through the harness of M0-14 (the TUI by scripted terminal, LINA Core through its protocol) as part of `node scripts/foundation.mjs`; the person's part of the acceptance is a UI and UX check only. When the change alters behavior, the spec sections listed under Design are updated in the same pull request.
- **M2-38** M2 acceptance run (LINA 2)\
  `core` `tui` `ci` `feature` · after M2-10, M2-11, M2-16, M2-17, M2-18, M2-19, M2-20, M2-27, M2-29, M2-35, M2-36, M2-37, M2-50 · G2-15\
  Done: One record holds every field of the phase rules and a result per check; the interruption injection now stops a running task and its citations pass; Codex and Claude Code versions are recorded.

## M3 큰 일을 여러 작업·기기로

45 issues (parents 11, sub-issues 25, standalone 9).

- **M3-01** Fold the Node direction into the contracts\
  `node` `docs` `decision` · after M2-38\
  Done: The contracts state that LINA Core runs on the person's own computer by default and that adding a Node is one action; They state that Nodes bid for work by free capacity, load, readiness and capabilities, and that the work of a failed or vanished Node is offered to another Node; They state that a Node keeps repositories only as a cache: clone on demand, worktree removed after merge; They state that other work gets read-only file copies fetched by content hash, and that its outputs return as result artifacts that Core files into the one canonical store; They state that credentials are granted per job and are never copied into a Node's files; The `node` connection in `docs/design/host-protocol.md` lists the offer and bid message kinds; The bullet of C-35 in `docs/design/go-core.md` that says these rules are not yet in the contracts is replaced by a pointer to the contract text; `node scripts/foundation.mjs` passes.
- **M3-02** MCP host and plugin registry\
  `core` `feature` · after M1-07, M1-10, M1-11, M2-38 · G3-01\
  Done: A crash loop never blocks conversation; A command server with no sandbox does not start; A changed tool definition is marked on the plugin card; A name over 64 characters is shortened with a stored map; A metadata fetch to a private, loopback or link-local address is refused; An earlier-era server connects by fallback.
- **M3-03** MCP host, SDK and era fallback\
  `core` `feature` · parent M3-02 · after M1-07, M1-10, M1-11, M2-38 · G3-01\
- **M3-30** Plugin registry, tool snapshots and stable names\
  `core` `feature` · parent M3-02 · after M3-03, M1-02, M1-09 · G3-01\
- **M3-04** Plugin servers: sandbox and crash loop\
  `core` `feature` · parent M3-02 · after M3-03 · G3-01\
- **M3-05** Outward-effect marks, tool annotations and on-demand tools\
  `core` `feature` · after M1-07, M1-18, M3-02 · G3-02\
  Done: An annotation never marks a tool as outward; A call to a tool that a service profile marks asks every time, and a call to any other tool runs; The always-declared tool list is byte-identical before and after a plugin is enabled.
- **M3-06** Plugin catalog, connect card and OAuth\
  `core` `tui` `feature` · after M1-35, M1-38, M1-63, M3-02 · G3-15\
  Done: A catalog file with a bad signature or an older version is rejected; Each registration mode is exercised by a fixture in order; The first request asks the scopes the manifest or the 401 challenge names, and an `insufficient_scope` answer asks the earlier scopes together with the new ones; A 401 refreshes once; `invalid_grant` shows needs sign-in; The connect card is a labeled block in the TUI and a question from a plugin appears as prompts.
- **M3-07** Signed plugin catalog\
  `core` `feature` · parent M3-06 · after M3-30, M0-09 · G3-15\
- **M3-31** OAuth for MCP servers\
  `core` `feature` · parent M3-06 · after M3-03, M1-10, M1-39 · G3-15\
- **M3-08** TUI connect card and plugin prompts\
  `tui` `feature` · parent M3-06 · after M1-63, M3-07, M3-31 · G3-15\
- **M3-09** Node gate, receipt journal and transport\
  `node` `protocol` `feature` · after M1-07, M1-11, M2-38 · G3-03\
  Done: A Node with no valid grant is BLOCKED after install, reboot, revoke and disconnect; An earlier-epoch command is rejected; A replayed journal entry applies once; A simulated suspend revokes the grant; A journal newer than the binary exits 78; A receipt for a changed artifact without provenance is rejected.
- **M3-10** Node connection schema and fixtures\
  `node` `protocol` `feature` · parent M3-09 · after M1-33, M2-38 · G3-03\
- **M3-32** Node state machine, grant check and binary\
  `node` `feature` · parent M3-09 · after M1-07, M1-01, M1-11, M3-10 · G3-03\
- **M3-11** Receipt journal and expiry\
  `node` `feature` · parent M3-09 · after M3-32 · G3-03\
- **M3-12** Device registration, Node host and reconcile\
  `core` `node` `feature` · after M1-35, M2-11, M3-09 · G3-04\
  Done: An unregistered device gets no grant; Registration grants nothing; A Core restart takes each Node through BLOCKED and RECONCILING to GRANTED only with a fresh-epoch grant; READY is reached with a Node absent; An unlisted pair is refused and reported.
- **M3-33** Device identity and registration\
  `core` `node` `feature` · parent M3-12 · after M1-36, M3-10, M0-09 · G3-04\
- **M3-34** Node host and reconcile in Core\
  `core` `node` `feature` · parent M3-12 · after M3-33, M3-11, M2-11, M1-01, M1-02 · G3-04\
- **M3-13** Remote delegation\
  `node` `core` `feature` · after M2-11, M2-12, M3-12 · G3-05\
  Done: A task runs on the chosen device and its receipts match the grant by id; Revoke or disconnect settles in-flight work unknown and reconcile settles it; No commit travels between Node and main; Identical bytes on two devices get two asset ids.
- **M3-35** Worker relay on the Node\
  `node` `feature` · parent M3-13 · after M3-32, M2-07 · G3-05\
- **M3-36** Dispatch a task to a chosen device\
  `core` `node` `feature` · parent M3-13 · after M2-11, M2-12, M3-34, M3-35 · G3-05\
- **M3-14** Node bidding for work\
  `node` `protocol` `core` `feature` · after M2-12, M3-01, M3-12, M3-13\
  Done: An offer reaches every connected Node, each Node answers with its free capacity, load, readiness and capabilities, and Core awards the task to one Node; The other Nodes receive a lost-bid notice; Work awarded to a Node that fails or vanishes is offered again to another Node once; The offer, bid, award and re-offer fixtures pass; The person is never asked to pick a device.
- **M3-37** Offer and bid on the node connection\
  `node` `protocol` `feature` · parent M3-14 · after M3-01, M3-10, M3-32, M3-34\
- **M3-38** Award, lost bid and re-offer\
  `core` `node` `feature` · parent M3-14 · after M2-12, M3-37, M3-36\
- **M3-15** Repositories as a cache on a Node\
  `core` `node` `feature` · after M3-01, M3-13\
  Done: A Node clones a repository on demand into its cache; The task worktree on the Node is removed after the pull request merges; No commit travels between the Node and the Core machine except through GitHub; Credentials are granted per task and are in no file of the Node; Killing the Node mid-task leaves the cache usable and the task unknown until reconciled.
- **M3-16** Read-only file copies and returned artifacts for non-coding work\
  `core` `node` `protocol` `feature` · after M1-06, M3-01, M3-12\
  Done: A Node receives only the files its grant names, as read-only copies fetched by content hash and verified before use; Its outputs return as result artifacts that Core files as new assets with provenance; A file outside the grant is refused; The file-copy and artifact fixtures pass.
- **M3-39** Read-only file copies for a Node\
  `core` `node` `protocol` `feature` · parent M3-16 · after M1-06, M3-01, M3-10, M3-34\
- **M3-40** Returned artifacts filed as assets\
  `core` `node` `protocol` `feature` · parent M3-16 · after M3-39, M1-06\
- **M3-17** Remote clients and mobile transport\
  `core` `protocol` `feature` · after M1-35, M2-27, M3-12 · G3-06\
  Done: A tailnet client gets the same welcome; No listener binds a public interface; A WebSocket client passes the conformance fixtures; Stop returns a typed result and needs no approval.
- **M3-18** Tailnet listener for remote clients\
  `core` `protocol` `feature` · parent M3-17 · after M1-35, M2-27, M3-12 · G3-06\
- **M3-19** WebSocket framing for a mobile host\
  `protocol` `feature` · parent M3-17 · after M3-18 · G3-06\
- **M3-20** macOS Node and seat lease\
  `node` `feature` · after M3-12 · G3-07\
  Done: The person's input ends the lease with a receipt id; A session lock ends it and an unlock does not revive it; A resume revokes it; No recording or keep-awake call exists; The macOS cells stay unverified until evidence.
- **M3-41** macOS Node: launchd job and Seatbelt sandbox\
  `node` `feature` · parent M3-20 · after M3-32, M1-12 · G3-07\
- **M3-42** Seat lease\
  `node` `feature` · parent M3-20 · after M3-41, M3-34 · G3-07\
- **M3-21** Splitting, dependencies and parallel hand-off\
  `core` `tui` `feature` · after M2-05, M2-12, M2-16, M2-38 · G3-09\
  Done: Overlapping areas are never in flight together; A dependent is released only after the predecessor is confirmed merged; A task nothing depends on never waits; A waiting task shows exactly one reason; An unmergeable pull request goes back once to its worker; The developer view shows one lane for each worker, folded to one line by default.
- **M3-22** Dependency graph, write-area checks and event-driven release\
  `core` `feature` · parent M3-21 · after M2-05, M2-12, M2-16, M2-38 · G3-09\
- **M3-23** Developer view: one lane per worker\
  `tui` `feature` · parent M3-21 · after M3-22 · G3-09\
- **M3-24** Cursor adapter\
  `core` `feature` · after M2-07, M2-38 · G3-10\
  Done: Find the installed `cursor-agent` and record version and sign-in state; Not signed in is unsupported delegation; Run headless per task in its worktree with `stream-json` progress; Process exit plus the final stream event ends the run, and exit 0 without a final event is unknown; Interrupt stops the run; Once-only recovery resumes the same chat id in the same work area; Quota or rate-limit replies park the worker until reset without spending a retry; The adapter is an opt-in account of the person. Exact flags, stream events and control behavior are to be confirmed by [#210](https://github.com/lina-system/lina/issues/210).
- **M3-25** Antigravity adapter\
  `core` `feature` · after M2-07, M2-38 · G3-16\
  Done: The result event ends the turn; Success is confirmed from the pull request and never from the exit code; An unapproved tool soft-denial is reported.
- **M3-26** Plan drafting\
  `core` `feature` · after M1-59, M1-71, M2-24, M2-38 · G3-17\
  Done: No plan object exists before confirmation; A draft is replaced completely on each update; Splitting into tasks is not a second confirmation.
- **M3-27** CRW takeover\
  `core` `feature` · after M2-01, M2-02, M2-23, M2-38 · G3-18\
  Done: Every fetched record is reported imported, lost or unsupported; Each is mapped to a LINA id once; A failed switch is rolled back explicitly; No product path resolves a source id.
- **M3-43** CRW import tool and report\
  `core` `feature` · parent M3-27 · after M2-01, M2-02, M2-23, M2-38 · G3-18\
- **M3-44** CRW operational switch\
  `core` `feature` · parent M3-27 · after M3-43, M2-02 · G3-18\
- **M3-28** Non-competition evidence on a Linux box\
  `core` `node` `ci` `feature` · after M3-13 · G3-12\
  Done: One record per applicable column at each tier with every field, counting agents and background runs; Zero focus steals, injected input, clipboard accesses, out-of-scope file accesses and lost edits; A failing tier stops the ladder.
- **M3-45** M3 headless end-to-end suite\
  `ci` `tui` `core` `feature` · after M0-14, M3-05, M3-06, M3-14, M3-15, M3-16, M3-17, M3-20, M3-21, M3-24, M3-25, M3-26, M3-27, M3-28\
  Done: Every acceptance check of LINA 3 that a program can observe runs headless through the harness of M0-14 (the TUI by scripted terminal, LINA Core through its protocol) as part of `node scripts/foundation.mjs`; the person's part of the acceptance is a UI and UX check only. When the change alters behavior, the spec sections listed under Design are updated in the same pull request.
- **M3-29** M3 acceptance run (LINA 3)\
  `core` `tui` `node` `ci` `feature` · after M3-05, M3-06, M3-14, M3-15, M3-16, M3-17, M3-20, M3-21, M3-24, M3-25, M3-26, M3-27, M3-28, M3-45 · G3-13\
  Done: One record holds every field of the phase rules and a result per check, and it names the dev commit and the `lina version` output of the build that was run; The record has a result for each acceptance check of LINA 3 and for each failure injection: a Node disconnect, a silent partition, a Node reboot, a revoke mid-command, a stale-epoch command, a plugin crash loop, a bad catalog signature, and every earlier injection; Every Node transition in the record is one the state table allows; Given-back slices are listed as not delivered; The record also covers the offer, award and re-offer injections of Node bidding, the repository cache and the read-only file copies.

## Later

5 issues (parents 1, sub-issues 2, standalone 2).

- **Later-01** Decide when LINA leaves alpha\
  `docs` `decision` · after M0-01\
  Done: The decision is written in `docs/design/versioning.md`: what comes after alpha (beta tags or 1.0.0), the trigger for it (for example the first personal-edition release), and who decides. The tag form that follows the decision is written next to it, and `docs/policy/releases.md` does not contradict it. Until the decision is merged, the document still says the version stays 0.0.1-alpha and that no milestone changes it.
- **Later-02** Generate TypeScript types and run the conformance fixtures in TypeScript\
  `protocol` `feature` · after M1-31, M2-33\
  Done: TypeScript types are generated from `protocol/schema/` with a pinned generator, and `foundation` fails when the checked-in files differ from a regeneration. The TypeScript implementation decodes and re-encodes every conformance fixture of client 1.0 without loss. `npm ci --ignore-scripts` passes the npm rows of the `dependencies` check.
- **Later-03** Generate TypeScript types from the protocol schema\
  `protocol` `ci` `feature` · parent Later-02 · after M1-32, M2-33\
- **Later-04** Run the conformance fixtures in TypeScript\
  `protocol` `feature` · parent Later-02 · after Later-03, M1-33\
- **Later-05** Decide a report route for people without a GitHub account\
  `docs` `decision`\
  Done: The chosen route (a relay, e-mail or none) and its privacy limits are written in `docs/design/product-families.md` and `docs/design/surfaces.md`. If no route is chosen, both documents say that no route exists and that the first release has none. The report-a-problem action still posts only after the person confirms the draft.
