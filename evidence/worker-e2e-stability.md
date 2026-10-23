# Worker e2e stability investigation (#812)

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

| Test | Starting dev | Final branch |
| --- | --- | --- |
| `TestM1BackupInstalledConversation` | 30/30 (100%) | Pending |
| `TestM1ReplayExportInstalledCore` | 30/30 (100%) | Pending |
| `TestM1TUIToolRows` | 30/30 (100%) | Pending |
| `TestM2ClaudeWorkerThroughInstalledCore` | 30/30 (100%) | Pending |
| `TestM2DispatchedWorkArea` | 30/30 (100%) | Pending |
| `TestM2WorkCompletions` | 30/30 (100%) | Pending |
| `TestM2WorkDefinition` | 30/30 (100%) | Pending |
| `TestM2WorkerChoice` | 30/30 (100%) | Pending |
| `TestM2WorkerQuotaPartialRecovery` | 30/30 (100%) | Pending |
| `TestM2WorkerQuotaWindows` | 30/30 (100%) | Pending |
| `TestM3AntigravityThroughInstalledCore` | 30/30 (100%) | Pending |
| `TestPersonaReplayExactSources` | 30/30 (100%) | Pending |

The final-branch run and full foundation gate are pending.

## Causal control

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

The measured baseline supports retaining the existing source fixes and documenting
their provenance. The historical failures include real timing races already fixed
in dev and distinct branch regressions; a finite repetition run does not establish
that every possible schedule is free of failures.
