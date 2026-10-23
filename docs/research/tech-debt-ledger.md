# Tech debt ledger

Audit for [#794](https://github.com/thisisjun786/lina/issues/794), run on `dev` at 3ae37f10 (2026-10-23) after about 2,500 commits from parallel workers in one week. It ranks the debt that costs changes now and plans bounded, behaviour-preserving fixes. It is analysis, not a cleanup: apart from the naming-migration test fix listed under quick wins, it changes no code.

Every number below comes from a command that was run on this tree, and every finding cites `file:line`. Line numbers are at 3ae37f10 and will drift; ids stay stable.

## How to read and rerun

- **Severity.** Critical: active correctness or security risk. High: costs every change in the area. Medium: costs some changes. Low: cosmetic or contained.
- **Effort.** S: one bounded change. M: a few files, one review. L: needs phases.
- **Id.** `<dimension>.<path-slug>.<slug>`. Dimension prefixes: `arch` architectural decay, `cons` consistency rot and duplicated paths, `type` type and contract gaps, `test` test debt, `dep` dependency and configuration, `perf` performance and resources, `err` error handling and observability, `sec` security hygiene, `doc` documentation drift.
- **Rerun.** This is the first run, so every row is NEW with age 0. The next run marks each id RESOLVED (evidence gone at the cited place, with the commit), CARRIED (age plus one) or superseded by a NEW id. A row carried through three audits is itself a finding. Map ids by dimension and path before declaring anything resolved.
- **Never a rewrite.** Each recommendation is one bounded change or a sequence of phases that each ship.

## Orientation

### Stack and checks

- Go 1.27 with toolchain go1.27.3; two modules, the root and `kit`, joined by `go.work`. Node 24.20.0 scripts under `scripts/`. One TypeScript package, `adapters/claude`.
- CI: `.github/workflows/ci.yml` runs `node scripts/foundation.mjs` to select docs, automation, dependencies, build/vet/exhaustive, unit shards and e2e shards, then one `foundation` gate. `internal/archtest` encodes the C-40 import layers.
- Detection tools on the audit host: `gofmt`, `go vet` and `go tool exhaustive` (through foundation). staticcheck, golangci-lint and deadcode are not installed, so dead code was found by reference search only.
- `gofmt -l` printed nothing for `internal/core/boot`, `internal/tui`, `internal/core/clientsrv`, `internal/core/work`, `internal/core/worker`. `go vet` exited 0 for `./internal/core/boot`, `./internal/core/clientsrv`, `./internal/tui/ui` and `./internal/core/canon`.

### Activity

| measure | value | command |
| --- | ---: | --- |
| commits in history | 2,535 | `git log --oneline \| wc -l` |
| commits since 2026-10-16 | 2,479 | `git log --since=2026-10-16 --oneline \| wc -l` |
| non-merge commits since 2026-10-16 | 985 | `--no-merges` |
| merges since 2026-10-16 | 1,494 | `--merges` |
| pull request merges into `dev` since 2026-10-16 | 297 | `--merges --first-parent` |
| open pull requests at audit time | 13 | `gh pr list --state open` |

Per-file churn uses the unfiltered log, `git log --since=2026-10-16 --no-merges --format= --name-only | sort | uniq -c`. A path-filtered `git log -- FILE` undercounts on this history because of history simplification; add `--full-history` when filtering by path.

"Hand-resolved" counts merges since 2026-10-16 where `git show --cc <merge> -- FILE` is not empty, so the result differs from both parents. Most of these are `dev`-into-branch merges, where conflicts are resolved.

### The priority corner: churn, size and tests

Unit coverage is `go test -count=1 -cover` per package, package statements only (e2e not counted). Boot is 51.6% from a rerun with a short `TMPDIR`; the first run failed 24 socket binds (see `test.core-boot.socket-path-length`).

| file | commits since 10-16 | lines | longest function (lines) | package coverage | open PRs touching it | merges touching / hand-resolved |
| --- | ---: | ---: | --- | ---: | ---: | ---: |
| `internal/core/boot/conversation.go` | 74 | 1,358 | `run` (439) | 51.6% | 9 | 157 / 62 |
| `internal/tui/ui/conversation.go` | 46 | 1,048 | `conversationContent` (265) | 72.6% | 1 | 94 / 32 |
| `internal/tui/ui/shell.go` | 30 | 530 | `Update` (249) | 72.6% | 0 | 44 / 15 |
| `internal/core/boot/boot.go` | 27 | 662 | `Run` (443) | 51.6% | 0 | 29 / 4 |
| `internal/tui/ui/program.go` | 26 | 617 | `runClientShell` (502) | 72.6% | 0 | 23 / 8 |
| `internal/core/work/goal/engine.go` | 25 | 1,134 | `report` (211) | 83.3% | 1 | 8 / 7 |
| `internal/core/boot/workers.go` | 20 | 684 | `workerOperation` (250) | 51.6% | 3 | 24 / 14 |
| `internal/tui/state/state.go` | 19 | 735 | `Reduce` (383) | 84.2% | 1 | 15 / 1 |
| `internal/core/work/goal/next.go` | 17 | 692 | `Derive` (342) | 83.3% | 0 | 10 / 2 |
| `internal/core/worker/dispatch/execute.go` | 14 | 452 | `execute` (403) | 70.4% | 1 | 14 / 10 |
| `internal/core/boot/runs.go` | 14 | 608 | `startRuns` (170) | 51.6% | 0 | 9 / 3 |

In the 9 open pull requests that edit `boot/conversation.go`, 34 of 61 hunks are in `conversationRuntime.run` and 11 in `startConversation` (from `gh pr diff` hunk headers). Of the 72 commits to that file with hunks, 19 edited both `startConversation` and `run`, and 18 of those also edited the runtime struct: one feature usually means editing the struct, the constructor and the turn loop.

### Shared lists that every feature appends to

| file | commits since 10-16 | merges touching / hand-resolved | open PRs | why it conflicts |
| --- | ---: | ---: | ---: | --- |
| `docs/design/go-core.md` | 248 | 542 / 82 | 5 | as-built log in a contract; 61 lines over 1,000 characters |
| `scripts/verify/scope.mjs` | 74 | 169 / 126 | 2 | one `internal/core/<pkg>/` line per package |
| `e2e/README.md` | 75 | 196 / 61 | 5 | 54 per-scenario paragraphs |
| `internal/core/canon/migrations.go` | 47 | 80 / 80 | 6 | two hand-kept lists, sequential version numbers |
| `kit/protocol/client.gen.go` | 35 | 60 / 60 | 2 | schema embedded as one 66,578-character line |
| `internal/design/catalog.go` | 55 | 125 / 27 | 3 | one 480-line map literal, gofmt realignment |
| `internal/core/manifest/combinations.go` | 20 | 28 / 28 | 1 | 20 capabilities on one 1,870-character line |
| `protocol/schema/client/1.0/envelope.json` | 35 | 60 / 19 | 2 | each kind edits the enum, the dispatch rules and `$defs` |

### Low unit coverage

From the same coverage run: `cmd/lina` 6.0% (every subcommand has an e2e test), `internal/core/channels` 15.2%, `internal/moirai/jobapi` 32.6%, `internal/core/update` 36.5%, `internal/core/recover` 41.3%, `internal/tui/render` 46.7%, `internal/core/boot` 51.6%, `internal/core/replay` 52.0%, `kit/calendar` 0.0% (no test files).

## Findings

Rows are ranked by severity, then payoff. The **plan** column names the phase issue that fixes the row; an empty cell means not planned yet.

### Core boot (`internal/core/boot`)

68 source files, 12,489 source lines, 9,913 test lines; 149 methods on `*conversationRuntime` across 54 files; 42 `_ =` statements and no logging calls.

| id | dim | file:line | sev | eff | bounded fix | evidence | plan |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `type.core-boot-conversation.scaffold-allowlists` | type | `conversation.go:726`, `conversation_history.go:20`, `goal_code_runtime.go:66`, `record_notice.go:65` | high | S | One Go registry builds both SQL kind lists (pure); adding kinds is a separate decision | The model-context and history lists are separate literals and differ. `goal_result` and `record_change` are admitted and published live but are in neither list, so `seedHistory` does not restore them after a restart. | #825 (step 1) |
| `arch.core-boot-conversation.god-file` | arch | `conversation.go:1-1358` | high | M | Pure moves to sibling files; about 483 lines stay | 74 commits, 8 responsibilities, 59 imports | #813, #819 |
| `test.core-boot-conversation.no-turn-unit-tests` | test | `controls_test.go:49`, `runs.go:57-59`, `worker_dispatch.go:28-30` | high | M | Scripted provider and a `turnRuntime` fixture so `run` runs in unit tests | The fixture passes a nil route; `grep -l '\.run(' *_test.go` finds 0 files. `run` has 41 commits. | #814 |
| `arch.core-boot-conversation.god-run` | arch | `conversation.go:849-1287` | high | L | `turnState`, then one extract per PR | 439 lines, 41 commits, 96 hunks; closures share 11 mutable locals | #828, #831, #833 |
| `arch.core-boot-conversation.order-coupled-registration` | arch | `conversation.go:267-322`, `decisions.go:34`, `growth.go:66,77`, `owner_views.go:55,79` | high | M | One registration helper with duplicate detection, explicit wrap and chained refresh, and a test of the final kinds | Start-call order is load-bearing; `decisions.go:34` replaces the refresh hook that three other sites chain | #826 |
| `cons.core-boot.duplicate-merge-flow` | cons | `github_merge.go:37-140`, `goal_code_effects.go:131-172`, `boot.go:346,359`, `worker_dispatch.go:45` | high | M | One `mergeWithAuthority` helper for both paths; then decide whether one GitHub client and one `Merges` suffice | Two merge-safety sequences, each with its own client and `Merges`, added the same day by LN-141 (5d42b68c) and LN-658 (5a7f9b4d) | |
| `arch.core-boot-conversation.runtime-god-struct` | arch | `conversation.go:75-147`, `conversation_router.go:111` | high | L | Group fields; embed shared services in one pointer; remove `router.root` reach-backs | 71 fields; the view copies 34 of them in one 782-character literal; 13 reach-backs in 8 files | |
| `cons.core-boot-conversation.close-pattern` | cons | `conversation.go:349-482` | medium | M | A loop stop/wait helper and a reverse stack, as `boot.Run` already uses (`boot.go:176-192`) | `Close` hand-codes 7 stop blocks; 20 commits touched it | |
| `cons.core-boot-conversation.tool-wiring-shapes` | cons | `conversation.go:938-982` and 10 family files | medium | M | One prepare signature and a table | 4 signatures; 8 features added a family by editing `run` twice | #832 |
| `cons.core-boot.grant-issuance-dup` | cons | `conversation_records.go:50-65`, `conversation_skills.go:97-122`, `workers.go:260-265` (7 sites), `conversation.go:870-880` | medium | S | `issueGrant` helper; a per-turn set replaces the hard-coded suffix list | Same request, lock, insert and deferred delete 7 times; the per-turn cleanup matches key strings built in other files | |
| `type.core-boot-replay.assembly-literals` | type | `conversation.go:1016,1021,1030,1052`, `replay_assembly.go:24-25,89-98` | medium | S | Constants and one `conversationCore(format)` used by the turn and replay | `"persona-tui-v1"` and `MaxTokens: 128000` written separately in the turn and twice in replay | |
| `err.core-boot.swallowed-no-log` | err | `channels.go:53`, `worker_dispatch.go:132`, `workers_approvals.go:149-156`, `runs.go:220` | medium | S | Return or record these errors; refuse to bind a worker when the parent read fails | A failed bot start shows only "unavailable"; `input, _ :=` gives worker notices an empty parent | |
| `test.core-boot.fixture-rewires-internals` | test | `run_notices_test.go:24-53`, `management_review_test.go:43` | medium | S | Shared `newBackgroundRuntime` used by `startRuns` and the test; deadline from the test context | The fixture rebuilds `runs.Service` and lanes, duplicating `runs.go:60-105`; 15 test files replace runtime fields | |
| `test.core-boot.socket-path-length` | test | `internal/core/jobs/server.go:42`, `channel_controls_test.go:25`, `methodology_policy_test.go:18`, `workers_running_test.go:260` | medium | S | `jobs.Listen` names the error when the path passes the socket limit; boot fixtures use a short root, as `start_test.go:29` does | Tests bind `<t.TempDir()>/state/run/jobs.sock`. With a 23-character `TMPDIR`, 24 binds failed with `bind: invalid argument` (path 113 bytes); with a 12-character one, 6 still failed. CI's `/tmp` fits. | |
| `cons.core-boot-goal.duplicate-goal-rules` | cons | `goal_code_driver.go:107,303-312`, `goal_code_host.go:34-50` | medium | S | Export `goal.DeployRevision` and `goal.NodeFromReport` and call them | Boot re-derives the deploy revision and host node checks that `work/goal/deployment.go:37-55` and `host.go:10-41` own | |
| `arch.core-boot.wiring-functions` | arch | `boot.go:136-578`, `runs.go:56-225` | medium | S | Extract `serveClients` for the two copied serve blocks; name the inline `RunningGet` handler | `Run` 443 lines with identity setup copied (`:443-445`, `:464-466`); `startRuns` 170 lines | |
| `arch.core-boot-workers.worker-operation` | arch | `workers.go:303-552` | medium | M | Per-tool functions behind a map, keeping the shared preamble | 250 lines, 16 commits, about 12 `if name ==` branches, arguments decoded 4 times | |
| `cons.core-boot.work-alias-collision` | cons | `goal_work.go:9`, `work_tools.go:12` | low | S | Import `work/goal` as `goal` everywhere | `work` means two packages in one package | |
| `cons.core-boot-conversation.reply-publish-dup` | cons | `conversation.go:793-803,834-846,1082-1089,1237-1250` | low | S | `publishText` and `publishSystem` helpers | Filter, push and publish inlined 4 times; system notices built in 11 places | |
| `perf.core-boot-conversation.events-n-plus-one` | perf | `conversation.go:723-760`, `ledger/store.go:375-378` | low | S | `ledger.Store.GetMany`; bound `r.positioned` | One query, then up to 200 separate reads per turn; `positioned` never shrinks | |
| `doc.core-boot-conversation.stale-comments` | doc | `conversation.go:67-69,250-254` | low | S | Move the runtime doc comment; drop the nil assignments that `decisions.go:29-33` undoes | The struct comment sits on `acceptedSteering` | |

### Work engine and workers (`internal/core/work`, `internal/core/worker`)

Package totals (source / test lines): goal 3,311 / 5,295, work 3,115 / 2,360, dispatch 1,835 / 1,906, codex 1,463 / 1,239. There is no ACP code yet; PR #790 changed documentation only.

| id | dim | file:line | sev | eff | bounded fix | evidence | plan |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `cons.core-worker-dispatch.worker-registry` | cons | `dispatch/service.go:209,463`, `choice.go:27,33`, `rules.go:49`, `adapter.go:38-53` | high | M | One registry with explicit and chooser flags; derive the allow-lists | About 10 name sites with three sets: explicit {antigravity, claude}, chooser {antigravity, codex}, rules {codex, antigravity}; three registration paths | #834 |
| `type.core-work-goal.kind-behavior-scattered` | type | `goal/engine.go:223-264,380-391`, `revisions.go:102-127`, `input_version.go:27-105` | high | M | `kinds.go` table plus a test that every kind has an entry | 8 exhaustive switches in goal, 5 in boot; the stage and recovery rule is written 4 times with different floors | #820, #829 |
| `arch.core-worker-dispatch.execute-god-function` | arch | `dispatch/execute.go:27-429` | high | M | One `outcome` value and three extracts, defer order kept | 403 lines, 34 hunks, no `execute_test.go`; four flags written from three closures | #835 |
| `cons.core-work-goal.retirement-spread` | cons | `engine.go:203,595-620`, `input_version.go:112-170`, `revisions.go:77-92`, `definition.go:286-317`, `signals.go:88-90` | high | M | Gather in `retire.go`; unify only later | 6 sites; 15 of the last 42 goal commits are about retirement | #815 |
| `cons.core-worker.headless-adapter-copy` | cons | `worker/claudecode/adapter.go:1-233`, `worker/antigravity/adapter.go:1-232` | high | M | Shared headless child lifecycle package; each adapter keeps args, parse and result | 30 of about 190 lines differ; 4 test names copied; `limitedOutput` defined 3 times | |
| `arch.core-work-goal.dead-action-kinds` | arch | `goal/next.go:99,165-169` | medium | S | Remove or fence `RepairQA`, `Observe`, `Decide` and `QARepairs` once the dev-journal policy is known | Derive never produces them; `git log -G'kind = RepairQA'` finds no commit | |
| `arch.core-worker-dispatch.foreign-table-reads` | arch | `dispatch/choice.go:109`, `execute.go:65`, `service.go:425` | medium | S | Small readers in the owning packages | Dispatch reads `work_cards`, `agents` and `turn_calls` (with `json_extract`) directly | |
| `arch.core-worker-antigravity.shared-pr-evidence` | arch | `worker/antigravity/pull_request.go:31-148` | medium | S | Move pull request evidence to a worker-neutral package | Every worker and boot use the review gate in the Antigravity package | |
| `perf.core-work-goal.refresh-every-step` | perf | `goal/engine.go:330`, `definition.go:217-234` | medium | S | Refresh definitions on sweep, startup and revise only | Every step re-reads every goal record ever admitted | |
| `perf.core-work-goal.full-journal-per-transaction` | perf | `goal/journal.go:107-122`, `engine.go:395-421` | medium | M | Reuse decoded actions per goal; later load one goal per update | Each update reads all `work_records`; finished rows are never deleted | |
| `perf.core-worker-dispatch.sync-full-scan` | perf | `dispatch/service.go:173,393-460` | medium | M | Wake on own writes; select non-terminal rows | Every canon commit rescans all `worker_dispatch` rows, which are never deleted | |
| `err.core-worker-dispatch.swallowed-saves` | err | `dispatch/execute.go:41`, `service.go:417-420,455-457` | medium | S | Surface the dropped save and read errors | `_ = s.save(ctx, r)`; a published but unsaved record is published again on each wake | |
| `type.core-work-goal.action-state-string` | type | `goal/next.go:197` | medium | S | `type ActionState string` with constants, same JSON | 68 state literals in 6 files; boot compares `a.State != "finished"` | |
| `type.core-worker-dispatch.record-state-code-string` | type | `dispatch/service.go:71-72` | medium | S | Exported state and code constants | 22 state literal sites, 20 code literals; boot and e2e compare strings | |
| `type.core-work-goal.receipt-rawmessage` | type | `goal/next.go:204` | medium | S | `Receipt *Observation` | Decoded in 3 places, once with the error ignored | |
| `cons.core-worker-dispatch.codex-two-start-paths` | cons | `dispatch/execute.go:284`, `dispatch/adapter.go:75` | medium | M | Decide the contract, then pass tools or document adapter-derived results | Boot passes work tools to Codex; dispatch passes none and gets no result | |
| `test.core-work-goal.fixture-families` | test | `goal/review_regression_test.go:12-90`, `revision_test.go:197-265` | medium | M | One goal test fixture | 61 engine constructions in 21 files; four parallel helper sets | |
| `test.core-worker-codex.helper-merge-churn` | test | `worker/codex/requests_test.go:27-40` | low | S | One peer constructor with options | 4 of the last 10 commits repaired the helper after merges | |
| `type.core-work.event-kind-string` | type | `work/types.go:128`, `project.go:37` | low | S | `type EventKind string` | About 27 literals switched without exhaustive | |
| `doc.core-work-goal.package-name-drift` | doc | `goal/journal.go:3` | low | S | "Package goal" | Package comment says `work` | |
| `cons.core-worker-dispatch.small-dups` | cons | `dispatch/definition.go:79-83`, `execute.go:140-144`, `goal/next.go:578,607,619` | low | S | One repository parser; actions through `add` | Same parse twice; three literals bypass reservations | |

### Other Core packages, design catalog, commands and kit

| id | dim | file:line | sev | eff | bounded fix | evidence | plan |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `test.core-canon.positional-migration-slices` | test | `canon/schema_naming_test.go:9` (and 13 more sites) | high | S | Look migrations up by schema, not position | The naming test used `catalog[:len-1]`, written when naming was last; after renumbering it already applied naming before the "pre-upgrade" insert. Fixed in #794 (red: `the starting catalog already includes the naming migration`). | #794, #839 |
| `arch.core-canon.migration-double-list` | arch | `canon/migrations.go:13-61,82-108` | high | M | Name the table list and test it against the SQL | 80 of 80 merges hand-resolved; e.g. d521e142 renumbered 44 to 46 in both lists | #839 |
| `arch.design-catalog.single-map` | arch | `internal/design/catalog.go:46-526` | high | M | Per-surface files with a duplicate-panicking `register` | 55 commits from 39 issues; a14e61bf rewrote 25 unrelated aligned lines | #840 |
| `arch.core-manifest.capability-one-liner` | arch | `manifest/combinations.go:95` | medium | S | One capability per line | 1,870 characters; 28 of 28 merges hand-resolved; copied in `scripts/build.test.mjs:253` and `tui/ui/program.go:26` | #841 |
| `type.design-catalog.flat-wire-namespace` | type | `catalog.go:487`, `tui/ui/cards.go:175-177,330` | medium | S | Test that fails on a code shared by two schema enums unless allowed | 14 of 232 enum codes appear in more than one enum; bare `stopped` means "Core is unavailable" | |
| `cons.core-replay.owner-predicate-copies` | cons | `replay/source.go:122-124,158-159,211-212`, `ledger/search.go:30-35` | medium | S | One owner predicate exported by ledger | 4 copies; `source.go:123` lacks the agent condition. Masked today by the per-item check in `export.go:21-28` | |
| `arch.core-replay.foreign-table-sql` | arch | `replay/source.go:170` | medium | M | One loader per source; reads through owning packages | `loadSnapshot` is 321 lines; 13 packages outside canon query `conv_event` | |
| `cons.core.hex-id-helpers` | cons | `agents/store.go:99`, `records/store.go:109`, `runs/service.go:197` (16 sites) | medium | M | `internal/core/ids`, already reserved in C-40 | 16 `rand.Read` sites in three error styles; `validID` byte-identical in two packages | |
| `cons.core-skills-agents.versions-copy` | cons | `skills/versions.go:25-165`, `agents/versions.go:25-165` | medium | M | One helper parameterised by root and prefix | Only one line differs after normalising the prefix; only the agents copy has a recovery test | |
| `arch.core-turn.run-god-function` | arch | `turn/store.go:307` | medium | M | Extract the start-state block and the completion closure | `Run` is 180 lines with 22 hunks | |
| `arch.archtest.core-to-tui-unchecked` | arch | `archtest/rules.go:176-194`, `boot/conversation.go:57` | low | S | Rule: Core does not import `internal/tui`, with a named exception | Boot imports `internal/tui/render`; no rule covers it | |
| `doc.archtest.reserved-layer-names` | doc | `archtest/rules.go:22-26` | low | S | Mark `ids`, `plugins`, `review`, `mcphost`, `supervise` as reserved | Five layer names have no package | |
| `cons.core.syncdir-copies` | cons | `ctlfiles/files.go:309` (11 definitions) | low | S | Fold into the versions helper | 11 `syncDir` copies; one drops the close error | |
| `cons.core.canon-interface-copies` | cons | `turn/store.go:36`, `ledger/store.go:122` (14 declarations) | low | S | Export `canon.ReadWriter` | 14 near-identical local interfaces | |
| `test.kit-calendar.tests-outside-module` | test | `kit/calendar/schedule.go` | low | S | Move the tests into `kit/calendar` | 0.0% coverage; tests stayed in `internal/core/timers` after the move | |
| `err.core-records.candidate-read-swallowed` | err | `records/candidate_source.go:29` | low | S | Map only "not found" to pending | A database error shows as "pending" | |
| `test.core-channels.thin-unit-tests` | test | `internal/core/channels/queue.go`, `store.go` | low | M | In-package queue and store tests | 15.2% in-package coverage | |
| `cons.core-boot.migrations-alias` | cons | `boot/migrations.go:9-10` | low | S | One spelling | 116 `canon.Migrations()` vs 25 `ApplicationMigrations()` | |
| `cons.cmd-lina.dispatch-two-lists` | cons | `cmd/lina/main.go:16-51` | low | S | One command table for the switch and usage | 5 of 5 merges hand-resolved | |

### TUI (`internal/tui`)

Package totals (source / test lines): ui 7,288 / 7,248, state 1,436 / 1,159, render 2,816 / 362. No `time.Sleep` in TUI tests.

| id | dim | file:line | sev | eff | bounded fix | evidence | plan |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `arch.tui-ui-program.handle-second-dispatcher` | arch | `ui/program.go:50,145-488` | high | L | Peer seam and a fake-peer test, then reply tracker and discovery session | `runClientShell` 502 lines, 52 hunks (most in the TUI); 7 reply stores; 9 `peer.(clientPeer)` checks; no unit test | #823, #830 |
| `arch.tui-ui-conversation.god-file` | arch | `ui/conversation.go:158,641,783` | high | M | Pure moves to composer, input history, results, pager and view files | 1,048 lines, 46 commits from 39 issues | #817, #821 |
| `arch.tui-ui-shell.update-data-case` | arch | `ui/shell.go:95-343` | high | M | Extract generation, connection, frame and data steps; move view code | `Update` 249 lines, 51 hunks | #818, #822 |
| `cons.tui-ui-program.reconnect-reset-split` | cons | `ui/program.go:146-155`, `ui/shell.go:113-140` | medium | S | One reset owner | `program.go:150` always closes the pager; `shell.go:115` closes it for three views only; unit tests see only the second | #818 |
| `perf.tui-ui-session.envelope-json-roundtrip` | perf | `ui/session.go:178`, `program.go:312-313`, `shell.go:157-158`, `state/state.go:614-615` | medium | M | Decode once and carry the envelope | Up to 4 schema validations per streamed delta on the UI loop | |
| `cons.tui-ui-shell.overlay-precedence-drift` | cons | `ui/activity.go:27-31`, `shell.go:279-312,389,438-455` | medium | S | One `topOverlay` helper and a timer test | Five sites, three orders; the activity check lacks `web.open` | |
| `cons.tui-state-ui.selection-scroll-two-sources` | cons | `state/state.go:88-90,662-670`, `ui/conversation.go:498-508` | medium | M | Store selection as a block id, or drop the unused reducer path | Production never sends `state.Select`, `Fold` or `Scroll`; the UI keeps an index (T-06 rule 7 says block id) | |
| `cons.tui-ui-keys.table-not-dispatch` | cons | `ui/keys.go:24-63`, `conversation.go:168-170` | medium | S | Test that every listed key is handled | `keys.go` feeds only the sheet; quit keys hard-coded in 4 places | |
| `cons.tui-ui-conversation.slash-command-chain` | cons | `ui/conversation.go:261-354` | medium | S | Ordered command table with today's undo flags | 9 copied blocks | |
| `arch.tui-ui-conversation.view-mutates-model` | arch | `ui/conversation.go:437,797,944-945,1020`, `conversation_scroll.go:64-96` | medium | M | Layout pass in Update; View only reads | View writes offsets, overflow and composer height; footer computed twice | |
| `doc.tui-design-t03.reader-backpressure` | doc | `docs/design/tui.md:203`, `ui/program.go:101-112,540-546` | medium | S | Update T-03 or decouple writes from the ack | The doc describes a bounded channel; the code waits for an ack after drawing | |
| `cons.tui-state-ui.card-copy-resolution` | cons | `state/state.go:23,399-402`, `ui/shell.go:347-355` | low | S | `State.CurrentCardFor` | Current card re-resolved by id in two places | |
| `perf.tui-ui-conversation.per-frame-full-rebuild` | perf | `ui/conversation.go:743-755,842-930` | low | S | Cache pager stream by text and width | Full rebuild per View; no benchmarks | |
| `type.tui-ui-conversation.pager-mode-flags` | type | `ui/conversation.go:46-69,641-688` | low | M | One `kind` field | 8 non-exclusive mode fields | |
| `type.tui-ui.stringly-capabilities-topics` | type | `ui/accounts.go:38`, `program.go:26` | low | S | Constants and topic helpers | 32 `hasCapability` calls with 13 string names | |
| `test.tui-render.markdown-indirect-coverage` | test | `render/markdown.go:61` | low | S | Render-level Markdown tests | 46.7% coverage; tested only through `ui` | |
| `dep.tui-term.test-harness-in-prod` | dep | `term/harness.go:25`, `recording.go:16` | low | S | Move to `term/termtest` | The `x/vt` emulator links into `cmd/lina` for tests only | |
| `cons.tui-ui.small-duplicates-and-dead-code` | cons | `ui/board.go:105-110`, `cards.go:336` | low | S | Shared helpers; delete `waitingLine` | Keep-visible code 3 times; `waitingLine` has no caller | |
| `doc.tui-design-t10.minor-drift` | doc | `docs/design/tui.md:1024,1027` | low | S | Remove the duplicate Ctrl+D row | Ctrl+D listed twice | |
| `test.tui-sanitize.seed-gaps` | test | `sanitize/display.go:30-101` | low | S | Seeds for ESC intermediates and C1 | 73.9% coverage; no case for the intermediate branch | |

### Client protocol and client service

| id | dim | file:line | sev | eff | bounded fix | evidence | plan |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `cons.kit-protocol-client-gen.single-line-schema` | cons | `kit/protocol/internal/generate/main.go:285-289` | high | S | Embed the schema verbatim as a multi-line raw string | `client.gen.go:1529` is 66,578 characters; 60 of 60 merges hand-resolved | #838 |
| `arch.clientsrv-server.serve-messages-god-function` | arch | `clientsrv/server.go:201-421` | high | M | Extract hello, admission and dispatch; decider kinds as a table | 220 lines; literal kind special cases at `:351,353`; 5 of 10 merges hand-resolved | #836 |
| `test.clientapi-decode.core-boundary-decoder-unused` | test | `internal/clientapi/decode.go:15-29`, `clientsrv/server.go:310-312` | high | M | Move the decoder next to Core; conformance test through `servePeer` | Only tests use `RequestDecoder`. Core closes the connection on a bad envelope with no `malformed` reply, unlike `host-protocol.md:28-31` and `cases/malformed_server.json` | |
| `cons.clientsrv-operation.decider-gate-split` | cons | `server.go:351`, `clientsrv/channels.go:30`, `boot/channels.go:159` | medium | M | Decider rule on `Operation`, enforced once | 1 listener list plus 9 handler checks; two pairs repeat one rule | #836 (step 1) |
| `doc.host-protocol.client-1-0-edited-in-place` | doc | `protocol/schema/client/1.0/envelope.json:4368`, `docs/design/host-protocol.md:66,132` | medium | S | Say 1.0 is a draft until the first tag, or check additions against a released copy | 35 in-place commits; 6b78e9d6 added an enum value with no gate. No tag exists yet | |
| `cons.fakecore-core.readonly-table-drift` | cons | `internal/fakecore/core.go:159-168`, `clientsrv/operation.go:48-68` | medium | S | One shared read-only predicate and a parity test | The fake lacks `restart_get` and two GitHub reads | |
| `err.clientsrv-server.refusals-unrecorded` | err | `server.go:375-382,393-395` | medium | S | A refusal hook that boot sends to the self-diagnosis log | No logging in clientsrv; `host-protocol.md:140` says refusals are recorded | |
| `cons.protocol-schema-envelope.kind-list-copies` | cons | `kit/protocol/client_test.go:13-30,115`, `kit/protocol/README.md:38` | medium | S | Test iterates the schema enum; README points to the schema | 7 of 11 merges hand-resolved; README misses 10 kinds | #838 |
| `type.kit-protocol-generate.inline-enum-no-constants` | type | `generate/main.go:204-206` | medium | S | Emit constants for inline enums | 29 inline enum properties generate as `*string` | |
| `type.clientsrv-capabilities.stringly-registry` | type | `tui/ui/program.go:26`, `clientsrv/decisions.go:21` | medium | M | Exported capability values in `kit/protocol` | 15 named vars, 4 inline literals, a 14-entry TUI list | |
| `cons.clientsrv-topics.topic-family-registry` | cons | `envelope.json:3860`, `clientsrv/topics.go:123-133`, `boot/conversation_memory.go:96-103` | medium | M | One topic-family table | 4 copies of the topic list; 19 literal sites | |
| `test.kit-protocol-compatibility.hand-ablated-old-schema` | test | `kit/protocol/compatibility_test.go:14-90` | medium | M | Frozen copy of the earlier schema | Old schema built by deleting fields in code; 24 ignored errors | |
| `arch.protocol-schema-envelope.single-file` | arch | `protocol/schema/client/1.0/envelope.json` | medium | M | Family files bundled by the generator, after #838 | 5,613 lines; 30 of the hand-resolved hunks are in `$defs` | |
| `cons.clientsrv-jsonrpc.frame-structs-duplicated` | cons | `server.go:184-195`, `clientapi/client.go:105,120` | low | M | `rpc.Request` and friends in `kit/rpc` | 10 definitions in 4 files with different strictness | |
| `perf.clientsrv-topics.topic-state-never-evicted` | perf | `clientsrv/topics.go:136-141` | low | S | Drop idle topic state | Each topic keeps a 1,024-frame ring for the whole boot | |
| `arch.clientsrv-topics.hub-decodes-payload` | arch | `topics.go:370` | low | S | `Removed` flag on `TopicEvent` | The generic hub decodes one payload kind under its lock | |
| `doc.host-protocol.stale-increment-and-long-lines` | doc | `docs/design/host-protocol.md:250` | low | S | Remove the stale increment paragraph | 18 lines over 600 characters | |
| `sec.clientsrv-websocket.no-hello-deadline` | sec | `clientsrv/websocket.go:32-43` | low | S | Deadline until welcome | Read deadline cleared after the first byte; peers are authenticated first | |

### Scripts, CI and the e2e harness

| id | dim | file:line | sev | eff | bounded fix | evidence | plan |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `cons.scripts-verify-scope.core-directory-registry` | cons | `scripts/verify/scope.mjs:21-66` | high | S | One `internal/core/` prefix | 53 of 74 commits only add a Core line; 126 merges hand-resolved; all 44 entries map to one component | #837 |
| `doc.e2e-readme.scenario-catalogue` | doc | `e2e/README.md:1-208` | high | M | Scenario notes in each test's doc comment | 54 of 68 paragraphs describe one scenario; 61 merges hand-resolved; 49 of 231 tests have doc comments | #845 |
| `test.scripts-verify-check-components.legacy-batching` | test | `check-components.mjs:122-150,164-175` | medium | M | CLI runs through the planned phases; delete the batching branch | All 20 tests in `check-components.test.mjs` use the legacy path; the live path has 3 | |
| `cons.scripts-verify.go-context-duplicated` | cons | `check-dependencies.mjs:261-283`, `check-components.mjs:25-54` | medium | S | One `scripts/verify/go.mjs` | Same toolchain check and env list twice; `goObjects` 3 times | |
| `cons.github-workflows-ci.matrix-inline-js` | cons | `.github/workflows/ci.yml:57-65` | medium | S | `ciMatrix(plan)` in `pipeline.mjs` with a test | Inline JS decides Go and sandbox per job, untested | |
| `cons.scripts-verify-gate.component-name-list` | cons | `scripts/verify/gate.mjs:13` | medium | S | Derive names from the registry | Adding a component needed a follow-up (fc546ec3) | |
| `test.scripts-build-test.capability-golden` | test | `scripts/build.test.mjs:253` | medium | S | Compare with `clientinfo` output | 19 rewrites of the copied list | #841 |
| `dep.scripts-verify-test-times.no-regenerator` | dep | `scripts/verify/test-times.json`, `shard-plan.mjs:161-171` | medium | S | Fold CI shard timings back | 62 of 177 scenario files have no entry | |
| `doc.docs-policy-foundation.stale-budget-table` | doc | `docs/policy/foundation.md:74-89` | medium | S | Current numbers; incident log to evidence | 20 s bound vs a reported 681.4 s run; 900 s vs `1_200_000` ms in code | |
| `doc.e2e-readme.stale-batching` | doc | `e2e/README.md:33-38` | medium | S | Describe scenario-file shards | Text predates sharding (66a19a43) | #845 |
| `cons.e2e-m1.canon-poll-loops` | cons | `e2e/m1/code_goals_test.go:458,505` (8 loops) | medium | S | One harness wait helper and `canonPath` | 8 hand-written loops; the canon path spelled out 50 times | |
| `cons.e2e-root-goal.duplicated-inprocess-setup` | cons | `e2e/run_to_done_test.go:25`, `e2e/goal/verification_test.go:24` | low | S | One setup helper per package | Same setup 9 times | |
| `cons.github-workflows-ci.repeated-toolchain-steps` | cons | `ci.yml:33-40,90-97,176-183` | low | S | `node-version-file`; drop the dead fallback | Same step 3 times | |
| `dep.github-workflows-ci.selection-go-bootstrap` | dep | `ci.yml:21-72` | low | S | Pinned setup-go in selection | Selection runs Go with the runner's Go | |
| `dep.github-workflows-ci.stale-stack-trigger` | dep | `ci.yml:6` | low | S | Remove `codex/ln-369-*` when the stack is done | 4 of 5 branches merged | |
| `cons.scripts-release.tag-pattern` | cons | `scripts/release.mjs:33`, `build.mjs:15` | low | S | Use `releaseVersion` | Release accepts tags the build rejects | |
| `doc.docs-policy-foundation.node-and-registry-drift` | doc | `docs/policy/foundation.md:70,114,118-125` | low | S | Name the exact `.node-version` | Says Node 20 or newer; scripts require 24.20.0 | |
| `doc.docs-policy-integration-releases.cli-flags` | doc | `docs/policy/integration.md:28`, `releases.md:8` | low | S | Document the missing flags | `--remote`, `--wait`, `--attempts`, `--out` missing | |
| `test.scripts-verify-scope-test.per-package-boilerplate` | test | `scripts/verify/scope.test.mjs:25-135,51-67` | low | S | One table test | 15 near-identical tests; one parses the script's text | #837 |

### Design and documentation drift

| id | dim | file:line | sev | eff | bounded fix | evidence | plan |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `doc.design-go-core.status-log-merge-magnet` | doc | `docs/design/go-core.md:3,357,400,606,1124` | high | M | One sentence per line; as-built notes to package docs | 140 of 297 PR merges touched it; 82 merges hand-resolved; 38 issue-tagged status lines | #843, #844 |
| `doc.check-docs.no-anchor-or-id-check` | test | `scripts/verify/check-docs.mjs:3,22` | medium | S | Check local anchors | One broken anchor out of 744: `docs/design/web-tools.md:3` to `runtime.md#tools` | #842 |
| `doc.design-c40.goldmark-rule-stale-in-two-of-three` | doc | `docs/design/go-core.md:75`, `docs/policy/foundation.md:138`, `docs/design/tui.md:77`, `archtest/rules.go:184-187` | medium | S | State the rule once in `tui.md` and link to it | Two documents forbid what the code and `tui.md` allow | |
| `doc.design-go-core.c40-enforcement-overclaim` | doc | `docs/design/go-core.md:75`, `docs/policy/foundation.md:142` | medium | S | Say archtest checks imports only | go-core says archtest checks view and secret rules; it does not | |
| `sec.cmd-lina.hidden-replay-worker-subcommand` | sec | `cmd/lina/main.go:37,51` | medium | S | List `replay-worker` as a helper | Dispatched but absent from usage; C-02 forbids hidden re-exec modes | |
| `arch.core-boot.imports-tui-and-design-unruled` | arch | `boot/conversation.go:57`, `turn/notices.go:69-76`, `exec/persona.go:97` | medium | S | C-40 rows and a rule for the allowed imports | Core imports `internal/tui/render` and `internal/design` with no rule | |
| `doc.design-roadmaps.duplicate-and-status-log` | doc | `docs/design/go-roadmap.md:69-70,135`, `docs/design/roadmap-github.md:3,128` | medium | M | Freeze the GitHub roadmap copy; stop status bullets | 75 of 97 done lines copied, already diverging; no M4 | |
| `doc.internal-design.catalog-not-generated-and-merge-magnet` | doc | `docs/design/tui.md:1376`, `docs/design/go-core.md:41` | medium | S | Say only `tokens_gen.go` is generated | The en/ko catalog is hand-written | |
| `doc.e2e-readme.append-only-scenario-log` | doc | `e2e/README.md:1-8` | medium | S | Harness introduction first | An m3 scenario note sits above the introduction | #845 |
| `doc.lina-command.subcommand-lists-stale` | doc | `docs/design/go-core.md:37`, `docs/design/runtime.md:594` | low | S | Point to the usage line | 8 subcommands missing | |
| `doc.design-go-core.responses-unreachable-stale` | doc | `docs/design/go-core.md:614` | low | S | Delete the sentence | Responses is reachable through Core | |
| `doc.design.verbatim-duplicate-paragraphs` | doc | `docs/design/go-core.md:1164,1636` | low | S | Keep the contract copy; link | Two paragraphs copied from other contracts | |
| `type.host-protocol.records-codes-open-string` | type | `docs/design/host-protocol.md:643-644`, `boot/records_client.go:44-46` | low | S | Enum or full list | Core sends `dependency_cycle`, not listed | |
| `doc.design-go-core.layout-and-kit-table-drift` | doc | `docs/design/go-core.md:41,47,49` | low | S | Mark planned vs built | Several listed packages do not exist; `kit/calendar` missing | |
| `doc.design-go-core.contract-count` | doc | `docs/design/go-core.md:3` | low | S | Drop the count | Says thirteen; 14 documents call themselves normative | |
| `doc.pkg.spdx-as-godoc` | doc | `internal/design/language.go:1-2` (pattern) | low | S | Blank line after the SPDX comment | 59 of 116 packages show the licence line as their doc | |
| `doc.host-protocol.capability-names-only-in-code` | doc | `manifest/combinations.go:95`, `docs/design/host-protocol.md:130-142` | low | S | Capability list in the protocol doc | 5 capabilities not named there | |
| `cons.reference-records.naming-and-location` | cons | `docs/design/tui-shortcuts-reference.md:3` | low | S | One name and location | 25 reference files in 10 spellings | |

## Top fixes

No finding is critical. The high findings, ranked by how many changes they cost now:

1. `arch.core-boot-conversation.god-run` and `arch.core-boot-conversation.god-file`: every conversation feature edits `run`; 9 open pull requests do today.
2. `doc.design-go-core.status-log-merge-magnet`: touched by almost half of all pull request merges.
3. `arch.core-canon.migration-double-list`: every schema change; 80 of 80 merges hand-resolved.
4. `cons.scripts-verify-scope.core-directory-registry`: every new Core package; 126 merges hand-resolved.
5. `cons.kit-protocol-client-gen.single-line-schema`: every protocol change; 60 of 60 merges hand-resolved.
6. `arch.tui-ui-program.handle-second-dispatcher`, `arch.tui-ui-conversation.god-file`, `arch.tui-ui-shell.update-data-case`: every TUI feature, before the work panel lands.
7. `type.core-work-goal.kind-behavior-scattered` and `cons.core-work-goal.retirement-spread`: every goal kind or retirement rule.
8. `cons.core-worker-dispatch.worker-registry`: every worker, before the ACP workers land.
9. `doc.e2e-readme.scenario-catalogue` and `arch.design-catalog.single-map`: every scenario and every new text.
10. `arch.core-worker-dispatch.execute-god-function`, `arch.clientsrv-server.serve-messages-god-function`, `arch.core-boot-conversation.order-coupled-registration`, `test.core-boot-conversation.no-turn-unit-tests`.

High findings not yet planned: `cons.core-boot.duplicate-merge-flow`, `arch.core-boot-conversation.runtime-god-struct`, `cons.core-worker.headless-adapter-copy` and `test.clientapi-decode.core-boundary-decoder-unused` (its fix changes the wire; see below).

## Quick wins

Severity medium or higher and effort S, by payoff per effort:

1. `test.core-canon.positional-migration-slices`: the naming test, done in #794. Red before the fix: `schema_naming_test.go:26: the starting catalog already includes the naming migration`. Green after, and `go test ./internal/core/canon` passes. The other 13 positional slices are #839 and the ledger rows above.
2. `cons.kit-protocol-client-gen.single-line-schema` (#838).
3. `cons.scripts-verify-scope.core-directory-registry` (#837).
4. `arch.core-manifest.capability-one-liner` (#841).
5. `type.core-boot-conversation.scaffold-allowlists`, step 1 (#825).
6. `cons.tui-ui-program.reconnect-reset-split` (#818).
7. `doc.check-docs.no-anchor-or-id-check` (#842).
8. `test.core-boot.socket-path-length`.
9. `err.core-worker-dispatch.swallowed-saves`.
10. `cons.core-replay.owner-predicate-copies`.
11. `cons.tui-ui-keys.table-not-dispatch`.
12. `err.core-boot.swallowed-no-log`.
13. `cons.core-boot-goal.duplicate-goal-rules`.
14. `doc.design-c40.goldmark-rule-stale-in-two-of-three` with `doc.design-go-core.c40-enforcement-overclaim`.
15. `sec.cmd-lina.hidden-replay-worker-subcommand`.

## Behaviour findings for the owner

These are not refactors: fixing them changes what users or peers see, so each needs a decision and a design update in its own pull request.

| id | what happens now | decision |
| --- | --- | --- |
| `type.core-boot-conversation.scaffold-allowlists` | `goal_result` and `record_change` notices show live but are not restored after a Core restart, and the model does not see `worker_result`, `skill_change`, `goal_result` or `record_change` on the next turn | which kinds belong in restored history and in model context |
| `test.clientapi-decode.core-boundary-decoder-unused` | Core closes the connection on a bad envelope after welcome; the design and fixture expect a `malformed` reply | align code or design |
| `cons.tui-ui-shell.overlay-precedence-drift` | the activity timer keeps running under the `/web` overlay; `docs/design/tui.md:980` says covering overlays stop it | fix the timer |
| `doc.host-protocol.client-1-0-edited-in-place` | new enum values land in the 1.0 schema without a gate | draft rule until the first tag, or an additive check |
| `cons.core-worker-dispatch.codex-two-start-paths` | a Codex task started through dispatch gets no LINA work tools and no result | which contract applies |
| `cons.core-replay.owner-predicate-copies` | one replay selection predicate lacks the agent condition; masked by the per-item export check | fix the predicate |
| `arch.core-boot-conversation.order-coupled-registration` | `decisions.go:34` replaces the topic refresh hook that earlier start calls set | keep replacement or chain |
| `err.clientsrv-server.refusals-unrecorded` | refusals are not recorded; `host-protocol.md:140` says they are | record or reword |
| `doc.tui-design-t03.reader-backpressure` | the reader waits for an ack after drawing; T-03 describes a bounded channel | update T-03 or decouple |
| `type.core-work-goal.kind-behavior-scattered` | returning a task restores Assign with `EffectsSettled` true in one path and false in another, with different floors | intended or not |
| `arch.core-work-goal.dead-action-kinds` | three action kinds are never produced | whether dev-build journals must keep loading |

## Looks bad but is fine

- **Generated files.** `kit/protocol/client.gen.go` is checked against the schema in CI and locally (`kit/protocol/generation_test.go:12-17`, `scripts/verify/check-components.mjs:94-98`); only its one-line layout is debt. `internal/tui/render/unicode_tables.go` has a generated header and its generator in `render/internal/unicodegen` (it lacks only a `//go:generate` line). `internal/design/tokens_gen.go` is generated by `scripts/tokens.mjs` and checked.
- **Exhaustive switches over every action kind.** Enforced by `go tool exhaustive`; deliberate. Only the dead kinds inflate them.
- **Legacy shims in the goal engine.** They keep owner dev journals loading and have tests (`deployment_test.go:190`).
- **`boot.Run` reads as one long sequence.** Its steps follow the order that the crash tests kill at (`TestSIGKILLAtEveryStartStep`). Extract helpers only; keep the order visible.
- **`Close` returns early on timeout without closing stores.** Documented at `conversation.go:379-381`.
- **`_ = json.Unmarshal` after a strict schema decode** (boot `workers.go`, `internal/core/exec`) and `_, _ = rand.Read` under Go 1.27, where it cannot fail. Only the mixed styles are debt.
- **Commented best-effort publication.** `runs.go:99,213`, `timers.go:36-38` and `clientsrv/decisions.go:166,235` retry from durable state on purpose.
- **Renumbering development migrations at merge.** The documented pre-release rule (`migrations.go:64-66`); nothing is released. The debt is the double list and the positional tests.
- **All DDL in canon.** C-40 assigns migrations to canon.
- **Callback fields in layer-4 configs.** C-40 forbids imports between layer-4 families.
- **`internal/tui/spike` has no production importer.** It is the T-08 evidence harness that `docs/design/tui.md:1201` and `evidence/tui-spike/README.md` run.
- **Three TUI output paths** (renderer, monochrome, plain): intended by `docs/design/tui.md:436`.
- **fakecore replays fixtures and runs no effects.** Documented in `host-protocol.md:69-77`; only its read-only table drifts.
- **The e2e harness avoids timing luck.** No `time.Sleep` in e2e or TUI tests; every `t.Skip` has a reason; waits poll under bounded contexts. The 1-second activity deadline in `e2e/m1/tui_activity_test.go:68` is contracted by `docs/design/tui.md:386`.
- **Pinned CI.** Every action is pinned to a commit SHA; actionlint is checksum-verified.
- **Low test ratios with good coverage.** `internal/addons/importer/hermes` 85.8%, `kit/result` 82.5%.
- **Catalog keys no literal references.** Built dynamically (for example `"running.state."+state`).
- **No dangling contract ids.** Every C, T, CP and G id cited in code or docs is defined or listed as ended.
- **`work/dispatch.go` is not a second dispatcher.** It holds directory reservations; only the name misleads.
- **Two append-only ledgers in work** (`work_ledger` and the goal journal) have different contracts.

## Open questions

- Has a renumbered migration ever hit `ErrDevelopmentSchema` on a development database?
- Is a second `ghclient.Client` instance harmful (rate limits, leases)? This decides how far `cons.core-boot.duplicate-merge-flow` goes.
- Is Core's import of `internal/tui/render` for reply line counting meant to stay?
- Does any installed TUI older than 6b78e9d6 still connect to current Core?
- Should finished `work_records`, `worker_dispatch` rows and the goal definitions cache ever be compacted?
- Which mutating client kinds lack `message_id` deduplication?
- Are the `exchanges`, `accounts` and `channels` topics reserved or forgotten? They validate but nothing publishes them.
- Is the 1-second activity deadline flaking under `-parallel=4` in CI?
- Is `origin/codex/ln-369-relationship` still an active stack?

## Refactor plan

The plan covers the top findings. Hot files go first: boot conversation, the goal engine, and TUI conversation and shell. Each phase is one pull request that keeps behaviour and is small enough to open and merge within a few hours.

### Map

- **Ownership.** All moved symbols in boot, goal, TUI `ui`, dispatch and clientsrv are unexported; outside importers of `boot` (`cmd/lina`, 36 e2e files, backup, replay) use only exported names. Exported symbols that change: `goal.WorkerAction` (new, #820) and the `dispatch.Factory` fields (#834), used by boot and `e2e/m1/worker_quota_window_test.go`.
- **Hidden coupling.** In boot: grant key suffixes built in other files and released per turn; the two SQL kind lists; `"persona-tui-v1"` shared with replay; `view.positioned` read by skill maintenance under `publicationMu`; operation registration order. In goal: map tables are not seen by the exhaustive analyzer, so #820 adds a coverage test. In the TUI: `program.go` intercepts messages before `Update`; View order sets overflow before the second footer. In canon: `Migration` must stay comparable.
- **Blast radius.** No phase changes a public API, CLI output, wire format or persisted schema. #838 regenerates a generated file with identical parsed content. #843 changes `AGENTS.md` by one sentence.

### Rules for every phase

- Same tests green: `node scripts/foundation.mjs` on the pull request selects and passes the same tests as `dev`.
- e2e unchanged: `git diff --stat origin/dev...HEAD -- e2e` prints nothing. #845 changes only comments in e2e Go files and checks that with a diff filter.
- Pure moves are reviewed with `git diff --color-moved=dimmed-zebra`; moved blocks carry no edits.
- Area hold: while a phase is open, dispatch keeps other jobs out of the paths in its `## Area` section. Start a phase on a hot file when the open pull requests on that file have merged or are parked, then merge it quickly; open branches re-apply their edits in the new location.
- Rollback: each phase is one merge commit; revert it. No phase deletes anything another phase needs, so a revert never cascades.
- Interfaces-first order from the method maps to: guard tests first (#814), pure moves (callers unchanged), extracts behind the same callers, then tables that replace scattered lists. No phase removes an old path; the dead-kind removal stays a behaviour decision.

### Phases

| phase | issue | change | area | depends on | focused check |
| ---: | --- | --- | --- | --- | --- |
| 1 | [#813](https://github.com/thisisjun786/lina/issues/813) | boot: move client, steering and publication code | `internal/core/boot/conversation*.go` | | `go test ./internal/core/boot` |
| 2 | [#814](https://github.com/thisisjun786/lina/issues/814) | boot: full-turn unit fixture (tests only) | new boot test files | | `go test -run Turn ./internal/core/boot` |
| 3 | [#815](https://github.com/thisisjun786/lina/issues/815) | goal: gather retirement in `retire.go` | `internal/core/work/goal` | | `go test ./internal/core/work/goal` |
| 4 | [#817](https://github.com/thisisjun786/lina/issues/817) | TUI: move composer, input history, results | `internal/tui/ui/conversation*.go` | PR #735 merged | `go test ./internal/tui/ui` |
| 5 | [#818](https://github.com/thisisjun786/lina/issues/818) | TUI: one reconnect reset owner | `ui/shell*.go`, `ui/program.go` | | `go test ./internal/tui/ui` |
| 6 | [#819](https://github.com/thisisjun786/lina/issues/819) | boot: move turn loop and history readers | `internal/core/boot/conversation*.go` | #813 | `go test ./internal/core/boot` |
| 7 | [#820](https://github.com/thisisjun786/lina/issues/820) | goal: action-kind table, `WorkerAction` | `work/goal`, `boot/goal_code_*.go` | #815 | goal and boot tests |
| 8 | [#821](https://github.com/thisisjun786/lina/issues/821) | TUI: move pager and conversation view | `internal/tui/ui/conversation*.go` | #817 | `go test ./internal/tui/ui` |
| 9 | [#822](https://github.com/thisisjun786/lina/issues/822) | TUI: split shell data handling, move view | `ui/shell*.go` | #818 | `go test ./internal/tui/ui` |
| 10 | [#823](https://github.com/thisisjun786/lina/issues/823) | TUI: client shell peer seam and routing test | `ui/program.go`, `ui/ui.go` | #818 | `go test ./internal/tui/ui` |
| 11 | [#825](https://github.com/thisisjun786/lina/issues/825) | boot: one notice-kind registry (byte-identical SQL) | boot history and memory files | #819 | `go test ./internal/core/boot` |
| 12 | [#826](https://github.com/thisisjun786/lina/issues/826) | boot: one client operation registration helper | `startConversation` and the start files | #819 | registration test |
| 13 | [#828](https://github.com/thisisjun786/lina/issues/828) | boot: `turnState` for shared locals | `boot/conversation_turn.go` | #814, #819 | turn tests |
| 14 | [#829](https://github.com/thisisjun786/lina/issues/829) | goal: counters and stage restore through the table | `work/goal` | #820 | `go test ./internal/core/work/goal` |
| 15 | [#830](https://github.com/thisisjun786/lina/issues/830) | TUI: reply tracker and discovery session | `ui/program.go`, talk, web, channels | #823 | routing test |
| 16 | [#831](https://github.com/thisisjun786/lina/issues/831) | boot: extract turn request preparation | `boot/conversation_turn.go` | #828 | turn tests |
| 17 | [#832](https://github.com/thisisjun786/lina/issues/832) | boot: tool families behind one signature | turn file and 10 family files | #831 | turn tests, tool order |
| 18 | [#833](https://github.com/thisisjun786/lina/issues/833) | boot: extract completion, observation, publication | `boot/conversation_turn.go` | #832 | turn tests |
| 19 | [#834](https://github.com/thisisjun786/lina/issues/834) | dispatch: one worker registry, before ACP (#747) | `internal/core/worker/dispatch` | | worker and boot tests |
| 20 | [#835](https://github.com/thisisjun786/lina/issues/835) | dispatch: split `execute` | `dispatch/execute.go` | #834 | dispatch tests |
| 21 | [#836](https://github.com/thisisjun786/lina/issues/836) | clientsrv: split `serveMessages` | `clientsrv/server.go`, `operation.go` | | clientsrv and clientapi tests |
| 22 | [#837](https://github.com/thisisjun786/lina/issues/837) | foundation: one `internal/core/` prefix | `scripts/verify/scope*` | | script tests; same selection |
| 23 | [#838](https://github.com/thisisjun786/lina/issues/838) | protocol: multi-line generated schema | `kit/protocol` | | `go test ./kit/protocol/...` |
| 24 | [#839](https://github.com/thisisjun786/lina/issues/839) | canon: checked table list, lookup by schema | `canon/migrations.go`, canon tests | #794 | `go test ./internal/core/canon` |
| 25 | [#840](https://github.com/thisisjun786/lina/issues/840) | design: catalog split by surface | `internal/design` | | catalog tests; key dump identical |
| 26 | [#841](https://github.com/thisisjun786/lina/issues/841) | manifest: one capability per line | `manifest/combinations.go`, `scripts/build.test.mjs` | | manifest and build tests |
| 27 | [#842](https://github.com/thisisjun786/lina/issues/842) | docs check: local anchors | `scripts/verify/check-docs*` | | check-docs tests |
| 28 | [#843](https://github.com/thisisjun786/lina/issues/843) | go-core.md: one sentence per line; where notes go | `docs/design/go-core.md`, `AGENTS.md` | #842 | word diff empty |
| 29 | [#844](https://github.com/thisisjun786/lina/issues/844) | go-core.md: status notes to package docs | `docs/design/go-core.md`, package docs | #843 | comment-only Go diff |
| 30 | [#845](https://github.com/thisisjun786/lina/issues/845) | e2e README: scenario notes to test comments | `e2e/README.md`, e2e doc comments | | comment-only e2e diff |

Waves: phases 1 to 5 start together (disjoint areas). Phases 6 to 10 follow their predecessors, with 19 beside them. Phases 11 to 15 and 20 to 21 come next; 16 to 18 are sequential in one file. Phases 22 to 30 touch other files and can run beside the boot, goal and TUI tracks whenever a worker is free.

### Files

| path | action | phase |
| --- | --- | --- |
| `internal/core/boot/conversation.go` | modify | 1, 6, 12 |
| `internal/core/boot/conversation_client.go`, `conversation_steering.go`, `conversation_publish.go` | create | 1 |
| `internal/core/boot/conversation_memory.go` | modify | 1, 6, 11 |
| `internal/core/boot/conversation_turn.go` | create 6; modify 13, 16, 17, 18 | 6, 13, 16-18 |
| `internal/core/boot/conversation_history.go`, `conversation_assembly.go`, `conversation_persona.go` | modify | 6, 11 |
| `internal/core/boot/conversation_scaffolds.go` | create | 11 |
| `internal/core/boot/conversation_operations.go` | create | 12 |
| `internal/core/boot/growth.go`, `owner_views.go`, `decisions.go`, `runs.go`, `work_waiting.go`, `boot.go` | modify | 12 |
| `internal/core/boot/turn_fixture_test.go`, `conversation_turn_test.go` | create | 2 |
| boot tool family files (10, listed in #832) | modify | 17 |
| `internal/core/boot/goal_code_driver.go`, `goal_code_effects.go` | modify | 7 |
| `internal/core/boot/worker_dispatch.go` | modify | 19 |
| `internal/core/work/goal/retire.go` | create | 3 |
| `internal/core/work/goal/kinds.go` | create 7; modify 14 | 7, 14 |
| `internal/core/work/goal/engine.go`, `input_version.go`, `revisions.go`, `definition.go`, `signals.go`, `next.go` | modify | 3, 7, 14 |
| `internal/tui/ui/conversation.go` | modify | 4, 8, 9 |
| `internal/tui/ui/composer.go`, `input_history.go`, `results.go` | create | 4 |
| `internal/tui/ui/pager.go`, `conversation_view.go` | create | 8 |
| `internal/tui/ui/accounts.go` | modify | 4 |
| `internal/tui/ui/shell.go` | modify | 5, 9 |
| `internal/tui/ui/shell_view.go` | create | 9 |
| `internal/tui/ui/program.go` | modify | 5, 10, 15 |
| `internal/tui/ui/ui.go`, `program_test.go` | modify / create | 10 |
| `internal/tui/ui/talk.go`, `web.go`, `channels.go` | modify | 15 |
| `internal/core/worker/dispatch/service.go`, `adapter.go`, `choice.go`, `rules.go` | modify | 19 |
| `internal/core/worker/dispatch/execute.go` | modify | 20 |
| `internal/core/clientsrv/server.go`, `operation.go` | modify | 21 |
| `scripts/verify/scope.mjs`, `scope.test.mjs`, `docs/policy/foundation.md` | modify | 22 |
| `kit/protocol/internal/generate/main.go`, `client.gen.go`, `client_test.go` | modify | 23 |
| `internal/core/canon/migrations.go` and canon test files | modify | 24 |
| `internal/design/catalog.go`; `catalog_*.go` | modify; create | 25 |
| `internal/core/manifest/combinations.go`, `scripts/build.test.mjs` | modify | 26 |
| `scripts/verify/check-docs.mjs`, `check-docs.test.mjs`, `docs/design/web-tools.md` | modify | 27 |
| `docs/design/go-core.md` | modify | 28, 29 |
| `AGENTS.md`, `docs/design/go-roadmap.md` | modify | 28 |
| package `doc.go` or `REFERENCE.md` files named in #844 | modify or create | 29 |
| `e2e/README.md` and e2e test doc comments | modify | 30 |
