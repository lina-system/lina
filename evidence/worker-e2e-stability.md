# Worker e2e scheduling regressions (#812)

This investigation covers `TestM2WorkerQuotaWindows`, `TestM2WorkerChoice`,
and every other e2e test that failed on at least two distinct branches in a
snapshot of the last 100 pull-request CI runs taken on 2026-10-23.
Subtest failures count toward their top-level test, not as independent runs.
The snapshot contains 62 successful, 33 failed, four unfinished and one
cancelled workflow runs. All 33 failed-run logs were inspected. The sample was selected with
`gh run list --workflow ci.yml --event pull_request --limit 100`.
The sample spans [39887424342](https://github.com/thisisjun786/lina/actions/runs/39887424342)
(02:03:01 UTC) through [39903513854](https://github.com/thisisjun786/lina/actions/runs/39903513854)
(05:02:00 UTC).

## Reproduced CPU budget loss

The initial dev baseline passed, but repeating the same four-shard workload
on the updated dev source (`a76f888b`, plus the evidence-only commit `2f0b14fa`)
reproduced startup/readiness failures: **331/360 top-level runs passed**.
The shared harness cleared the child environment without passing the parent's
Go CPU budget. Although every test process ran with `GOMAXPROCS=2`, each Core
independently used the host's default instead.

During the failures, 15 test-owned Core processes had no `GOMAXPROCS` setting,
CPU affinity spanning all 16 host CPUs, and 12–20 OS threads each. Host load was
37.27, CPU pressure averaged 31.52% over 60 seconds, and I/O pressure averaged
13.47%; available memory was 23 GiB with no measured memory pressure. Tests
failed at their existing Core connection or readiness deadlines. These resource
observations accompany the failures; the deterministic regression below directly
proves the CPU-budget loss.

`TestHarnessCommandsShareCPUBudget` starts a real Go child through the shared
harness with parent runtime budgets of 1 and 3. Before the fix, both children
reported 16:

```text
child CPU budget = "16\n", want 1: <nil>
child CPU budget = "16\n", want 3: <nil>
```

The fix passes the parent's current numeric `runtime.GOMAXPROCS(0)` value into
the otherwise isolated command environment. Both regression cases then passed.
The test checks the child's actual runtime setting, without sleeps or a timing
assertion. The command was:

```sh
dv heavy -- go test ./e2e/m1 -run '^TestHarnessCommandsShareCPUBudget$' -count=1 -v
```

It exited 1 before the implementation change and 0 afterwards. Go reported
package execution times of 0.119 and 0.040 seconds respectively, excluding build
and heavy-slot waiting time. Scenario deadlines, parallelism and assertions are unchanged.
In the fixed workload, all 12 sampled Core processes received `GOMAXPROCS=2`
and used 6–9 OS threads while retaining the same 16-CPU affinity. CPU pressure
was still 39.94% over 60 seconds at that observation. The fixed branch's repeated
workload is measured separately below.

## Historical failure causes

These logs describe the historical heads; they are not evidence that the
current branch fails or passes. The starting `origin/dev` commit is
`6b6824a3a04ff842afc5d8647388c8e8eef4ccd9`.

| Failure | Evidence | Cause and existing correction |
| --- | --- | --- |
| Worker quota windows and choice: `codex: start_failed` | [39888324468](https://github.com/thisisjun786/lina/actions/runs/39888324468), [39887884907](https://github.com/thisisjun786/lina/actions/runs/39887884907) | A terminal notification could arrive before the `turn/start` reply, leaving the returned turn binding empty. `0e022945` preserves the binding on notification and `03c44008` preserves it on reply without reviving the completed turn. Both are already in the starting dev commit. The failing heads lack `03c44008`. |
| Partial quota recovery: `missing independent initial quota` | [39900094625](https://github.com/thisisjun786/lina/actions/runs/39900094625), [39897264365](https://github.com/thisisjun786/lina/actions/runs/39897264365) | Background `thread/read` could release the recovery notification before the model fixture had observed the initial blocked window. `9e226e5a` gates recovery on that observation. Both failing heads lack this correction; starting dev includes it. |
| Antigravity: `worker result missing EOF` / `Core exited before listening: signal: killed` | [39900558256](https://github.com/thisisjun786/lina/actions/runs/39900558256), [39893950131](https://github.com/thisisjun786/lina/actions/runs/39893950131), [39893322258](https://github.com/thisisjun786/lina/actions/runs/39893322258) | Bubblewrap's parent-death signal followed the launching Go OS thread, which could retire while the test process lived. `2d13d810` adds a single-threaded shell parent and a regression that explicitly retires the launching thread. Starting dev includes it. |
| Worker fixture: `invalid_arguments`, `shell_file_tools_unsupported: probe_failed` | [39889712774](https://github.com/thisisjun786/lina/actions/runs/39889712774) | The `--bind / /` wrapper introduced by `7c1bb824` did not preserve device semantics. `bb9aa26f` uses `--dev-bind` and sends graceful shutdown to Core's PID. Starting dev includes the correction. |
| Worker tools unavailable in the LN-613 branch | [39895653176](https://github.com/thisisjun786/lina/actions/runs/39895653176), [39897822831](https://github.com/thisisjun786/lina/actions/runs/39897822831) | That branch changed tool loading and dispatch policy while fixtures still expected the original directly exposed tools and call sequence. `4f3132c7` introduces the policy change; `a0ca738f` updates its acceptance integration. This branch-specific change is separate from the timing failures. |
| Replay/export on LN-617: `missing_assembly`, `export missing real conversation text` | [39892408741](https://github.com/thisisjun786/lina/actions/runs/39892408741) | The exchange declaration's literal `<id>` changed bytes when durably JSON-encoded. `1d7cd788` corrects the declaration and tests byte-preserving round trips. The failing head lacks the correction; starting dev includes it. |
| The same replay/export symptoms on LN-615 | [39890100135](https://github.com/thisisjun786/lina/actions/runs/39890100135) | `9ebf9f6d` added a `working-preference` context source without its replay mapping. `1cdaa2af` adds that mapping in replay and boot. This is a separate deterministic branch regression, not evidence that the exchange-declaration fault crossed branches. |
| Backup: `development schema does not match the published migration catalog` | [39892449532](https://github.com/thisisjun786/lina/actions/runs/39892449532), [39890100135](https://github.com/thisisjun786/lina/actions/runs/39890100135) | The fixture downgrades a database to v14 by removing later tables. It retained the new question tables on LN-615 and completion/result tables on LN-616. `1cdaa2af` and `7830426b` update those branches' downgrade fixtures. These deterministic branch changes produced the same validation error; they do not establish a shared timing fault. |

The worker checks exercise the existing [worker choice](../docs/design/work-and-delegation.md#choosing-the-worker)
and [Codex adapter](../docs/design/work-and-delegation.md#codex-adapter) contracts.
This investigation does not change those contracts.

## Repetition protocol

Four independent Go processes run concurrently on a 16-CPU Linux host, each
with `GOMAXPROCS=2`, Go 1.27.3 and Node.js 24.20.0.
Each runs the following command with the test names from its row joined by
`|` inside the anchored expression:

```sh
go test -json -count=30 -parallel=4 -timeout=60m ./e2e/m1 -run '^(TEST_NAMES)$'
```

The four-process invocation runs under one `dv heavy` slot. The one-hour
package watchdog accommodates 30 repetitions; the existing operation and
scenario deadlines are unchanged. Each process retains the original fixtures,
assertions, per-scenario state and subprocess cleanup. There are no retries.

| Shard | Top-level tests |
| --- | --- |
| 0 | `TestM2WorkerQuotaWindows`, `TestM2WorkerChoice`, `TestM2WorkerQuotaPartialRecovery` |
| 1 | `TestM3AntigravityThroughInstalledCore`, `TestM2ClaudeWorkerThroughInstalledCore` |
| 2 | `TestM2DispatchedWorkArea`, `TestM2WorkCompletions`, `TestM2WorkDefinition` |
| 3 | `TestM1BackupInstalledConversation`, `TestM1ReplayExportInstalledCore`, `TestM1TUIToolRows`, `TestPersonaReplayExactSources` |

## Measured results

Baseline at the starting dev commit: **360/360 top-level runs passed**.
All 43 distinct test/subtest names ran 30 times; none failed, skipped or lacked
a terminal observation. Shards 0–3 exited 0 in 816.84, 953.14, 906.91 and 631.76
seconds respectively.

| Test | Initial dev baseline | Updated dev before fix | Fixed branch |
| --- | --- | --- | --- |
| `TestM1BackupInstalledConversation` | 30/30 (100%) | 25/30 (83.3%) | 30/30 (100%) |
| `TestM1ReplayExportInstalledCore` | 30/30 (100%) | 27/30 (90.0%) | 30/30 (100%) |
| `TestM1TUIToolRows` | 30/30 (100%) | 27/30 (90.0%) | 30/30 (100%) |
| `TestM2ClaudeWorkerThroughInstalledCore` | 30/30 (100%) | 29/30 (96.7%) | 30/30 (100%) |
| `TestM2DispatchedWorkArea` | 30/30 (100%) | 26/30 (86.7%) | 30/30 (100%) |
| `TestM2WorkCompletions` | 30/30 (100%) | 29/30 (96.7%) | 30/30 (100%) |
| `TestM2WorkDefinition` | 30/30 (100%) | 27/30 (90.0%) | 30/30 (100%) |
| `TestM2WorkerChoice` | 30/30 (100%) | 27/30 (90.0%) | 30/30 (100%) |
| `TestM2WorkerQuotaPartialRecovery` | 30/30 (100%) | 30/30 (100.0%) | 30/30 (100%) |
| `TestM2WorkerQuotaWindows` | 30/30 (100%) | 29/30 (96.7%) | 30/30 (100%) |
| `TestM3AntigravityThroughInstalledCore` | 30/30 (100%) | 29/30 (96.7%) | 30/30 (100%) |
| `TestPersonaReplayExactSources` | 30/30 (100%) | 26/30 (86.7%) | 30/30 (100%) |

The updated-dev trial completed all 12 top-level tests 30 times with no skips.
Three startup failures prevented `TestM1ReplayExportInstalledCore` from reaching
its `export` and `replay` subtests, so those subtests ran only 27 times; the three
parent failures remain in the denominator. Shards 0–3 exited 1 in 1108.16,
1370.99, 1309.53 and 952.42 seconds respectively. These failures are retained in
the before-fix column.

The fixed branch at `04472d1067bbcaed98311cac5f7866fce2238887` passed **360/360**
top-level runs. All 43 test/subtest names ran and passed 30 times, without skips
or missing observations. Shards 0–3 exited 0 in 1049.21, 1375.40, 1323.76 and
940.47 seconds respectively. Both repetition runs use the same updated dev
source plus this harness change; the earlier initial-dev baseline is retained
as a separate observation.

Final integration uses `node scripts/foundation.mjs --full --no-note` after
merging `origin/dev` into the PR branch. The result for that final candidate,
GitHub CI and the current-head bot review are reported on the pull request.

## Historical Codex control

After completing the unchanged-dev baseline, a temporary diagnostic removed
only the two existing early-completion binding fixes from
`internal/core/worker/codex/events.go` and `session.go`. The existing
`TestStartRetainsCompletionBeforeTurnReply` then failed both its `completed`
and `failed` cases with:

```text
early completion lost the returned turn binding: {ID:thread TurnID: TaskID:task AttemptID:attempt ProcessGeneration:1 BindingEpoch:1}
```

Restoring the exact original files made both cases pass. The command was
`go test ./internal/core/worker/codex -run '^TestStartRetainsCompletionBeforeTurnReply$' -count=1 -v`
(exit 1 for the control; exit 0 after restoration). No sleeps, deadline changes
or additional attempts were used to produce either result. This is a controlled
removal of an existing fix, not a failure of the unchanged starting dev commit.

The existing declaration round-trip regression also passed:
`go test ./internal/core/boot -run '^TestExchangeDeclarationsSurviveEvidenceEncoding$' -count=1`.

The historical failures include timing races already fixed in dev and distinct
branch regressions. The later loaded run exposed the additional harness CPU
budget fault addressed here. The original passing baseline and the failing
updated-dev trial are both retained; a finite repetition run does not establish
that every possible schedule is free of failures.

## Integration fixture correction

The full gate after the final dev merge also exposed a management fixture whose
`testing.T.TempDir` path grew beyond the Unix socket limit when `jobs.sock` was
added. `TestGrowthCommittedAdoptionSurvivesNoticeFailure` failed both cases under
a long temporary base with `bind: invalid argument`. The fixture now uses a short
private directory prefix and removes it after its stores and growth service close.

The same focused command and long temporary base failed before that change and
passed afterwards (Go package execution: 8.864 seconds red, 1.773 seconds green):

```sh
go test ./internal/core/boot -run '^TestGrowthCommittedAdoptionSurvivesNoticeFailure$' -count=1 -v
```

The first full run also exhausted the boot package's six-minute watchdog after
165 top-level tests had passed; the currently running test had run for only one
second and was synchronizing an agent directory. The entire first run also reached the 1200-second shared limit before its final
two e2e shards completed; this run is retained as a failure. The subsequent full
gate uses a private temporary directory on a local data volume to avoid root-volume
I/O contention, retaining the package, scenario and global deadlines. Its result
is recorded for the final PR head.

## Capacity wakeup boundary

An additional repetition on integrated head `6cdcb8e2` passed 359/360 top-level
runs: `TestM2DispatchedWorkArea` passed 29/30, while all other tests passed 30/30.
Its host-capacity case failed 10.003 seconds after releasing load, even though
the subsequent diagnostic found the same dispatch finished with one attempt,
one worker launch and `ready_for_review`. The wakeup deadline incorrectly
covered Git execution, manager checks and final result delivery as well.

The worker fixture now holds completion behind a release marker. With only that
barrier added, the old harness failed deterministically after its ten-second
result wait while the worker was already running (17.220 seconds of Go package
execution including setup). The harness now observes the same record's durable
running claim and area lease within the existing operation deadline. It cancels
that context before releasing the fixture, then checks completion and restart
under the existing scenario context. The focused case passed in 13.284 seconds.
Cancelling the wakeup context before work may complete makes the distinction
explicit without a sleep or a larger duration constant.

```sh
dv heavy -- env GOMAXPROCS=2 go test ./e2e/m1 -run '^TestM2DispatchedWorkArea$/^host-capacity$' -count=1 -parallel=4 -timeout=6m -v
```

The final PR records the repeated verification of the combined CPU, socket-path
and capacity-wakeup corrections, including every earlier failed measurement.

## Packet frame regression and pass rate

`TestM2WorkerChoice` and `TestM2WorkerQuotaWindows` were the two most frequent
failures in the filing report, and they failed for one cause. `fbabe7cc`
(feat(worker): frame messages and verify approval records, 2026-10-23 17:01 UTC)
wrapped every worker input in a `<lina-message ...>` frame and updated the
Antigravity, Claude and dependency fixtures to unwrap it, but not
`e2e/m2/worker-choice-codex.mjs`. That fixture stored `p.input[0].text`, so its
`second-packet` file held the frame while `first-packet` and `packet` held the
body the other fixtures received. Both assertions compare those bytes:

```text
antigravity_test.go:443: fallback changed the packet            (quota fallback and double stop)
antigravity_test.go:443: quota handoff changed the task packet  (quota windows)
```

Only a head that contained `fbabe7cc` could fail, and those were the heads that
had merged current dev; the same branches passed before that merge. That is why
the failures looked unrelated to the branches. `a372220a` (PR #1295) unwraps the
frame in the Codex fixture and is in dev.

### Pass rate before and after

Both runs used the first shard of the repetition protocol above:
`-count=30 -parallel=4 -timeout=60m` with `GOMAXPROCS=2`. The before run used
current dev with one file reverted to its `a372220a^` content and nothing else;
the after run used current dev unchanged. `TestM2WorkerQuotaPartialRecovery` is
the control: it runs the same fixture in another mode and never compares packets.

| Test | Before (no unwrap) | After (dev `5f8f2561`) |
| --- | --- | --- |
| `TestM2WorkerChoice` | 0/30 (0%) | 30/30 (100%) |
| `TestM2WorkerQuotaWindows` | 0/30 (0%) | 30/30 (100%) |
| `TestM2WorkerQuotaPartialRecovery` | 30/30 (100%) | 30/30 (100%) |

The before run recorded 90 failed repetitions in 1051.5 seconds: 30
`quota handoff changed the task packet` and 60 `fallback changed the packet`
across the `quota-fallback` and `double-stop` cases. The after run passed all 90
repetitions with no failure or skip in 786.1 seconds. Repetition cannot mask this
defect: a head without the unwrap fails every attempt.

### Verification on the merged head

The pull-request head adds this document to `origin/dev` at `1fbda9fb`, so its
code is that commit. The same first-shard command run on the merged head
(`be257e76`) passed all 90 repetitions in 511.39 seconds of package execution,
with no failure, skip or missing observation: `TestM2WorkerChoice` 30/30,
`TestM2WorkerQuotaWindows` 30/30 and `TestM2WorkerQuotaPartialRecovery` 30/30.
The two audited `internal/core/boot` tests also passed 5/5 on that head.

### Audit of the most recent 100 pull-request CI runs

A snapshot taken on 2026-10-23 covers the 100 most recent pull-request runs of
the CI workflow, created between 16:45:57Z and 20:36:31Z: 82 success, 11 failure,
five cancelled and two unfinished. Every failed run's log was inspected, and
neither named test appears in any of them.

Three tests failed on two or more distinct branches, and all three are unit
tests, outside this issue's e2e scope:

| Test | Branches | Current dev |
| --- | --- | --- |
| `TestCatalogCoversClosedWireCodes` | feat/765, feat/952, feat/1011 | 5/5 |
| `TestCoreProcess` | feat/950, feat/952 | 5/5 |
| `TestDelegatedGoalWorkerRetainsRequesterBinding` | feat/979, feat/980 | 5/5 |

Each failing head changes the protocol or worker surface that the test
enumerates: the host protocol in #1372, which adds the launcher codes the catalog
test reports missing, and in #1317; generated card surfaces and conversation
results in #1272 and #1270; worker steering and message guards in #1238 and
#1239. These are deterministic branch-local mismatches, not a shared timing
fault. Every other failing test ran on one branch only, so no other e2e test met
the two-branch threshold.
