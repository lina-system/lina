# Installed Core conformance QA

Issue [#992](https://github.com/thisisjun786/lina/issues/992) reports an unfinished
`TestM2ConformanceInstalledCore` when its acceptance package reached the
six-minute deadline. The relevant contract is
[Judgment and delivery](../docs/design/work-and-delegation.md#judgment-and-delivery).

## Failure evidence

In [run 39930348507](https://github.com/thisisjun786/lina/actions/runs/39930348507),
`e2e-m2-4` ran on `e0af3373b4e0be0eb468d9ff57c11e8138aa530e`.
`TestM2FileLanding` failed its two serial cases after 120.22 and 120.21 seconds,
reporting `manual work turn did not settle context deadline exceeded`.

Conformance registered its six parallel cases at 09:20:37 UTC. All six were
still waiting in `testing.(*testState).waitParallel` when the package timed out
at 09:21:45 UTC. The run contains no conformance assertion failure or completed
conformance case. Its QA failure is an unfinished-test observation, not a
recorded conformance verdict mismatch.

[PR #1002](https://github.com/thisisjun786/lina/pull/1002), for
[#984](https://github.com/thisisjun786/lina/issues/984), addresses the preceding
file-landing fixture failure: it answers Core's auxiliary compaction requests
before handling ordinary scripted tool calls and reports failed turns promptly.

## Existing behavior

The [installed scenario](../e2e/m1/conformance_test.go) and its
[shared harness](../e2e/m1/antigravity_test.go) drive the running Core through
conversation tools. Core starts a supervised worker in a private Git worktree,
observes its delivery, records the review and returns the result to the
conversation. The success case checks the produced file and commit. Every
case restarts Core and checks the saved result without another model call.

| Case | Checked outcome |
| --- | --- |
| `success` | Structured `verified` verdict binds the receipt and delivered head; the work is applied. |
| `scope` | An out-of-area path records a scope miss without a manager call or findings. |
| `scope-oversized` | Unavailable change evidence blocks before scope judgment, without a verdict, findings or manager call. |
| `failed-check` | Merge prerequisites block without a verdict, findings or manager call. |
| `prose` | Prose alone records neither a verdict nor findings. |
| `findings` | A structured P1 survives the one correction and leaves the task needing input. |

On unchanged `dev` commit `f38a584f94379edfeb074ea233a18fa8524a93e6`, this command
passed all six cases in 16.735 seconds:

```sh
dv heavy -- go test -count=1 -parallel=4 -v -timeout=6m ./e2e/m1 \
  -run '^TestM2ConformanceInstalledCore$'
```

The provider, worker executable and GitHub responses are synthetic; Core,
process supervision, Git commits, files, database records and restart are real.
This run establishes the scenario's behavior on that commit. It does not prove
recovery of the shared CI package, live-provider quality or real GitHub writes.

## Reproduction with the preceding scenario

The latest completed CI run on the same `dev` commit,
[39931332597](https://github.com/thisisjun786/lina/actions/runs/39931332597),
repeated the file-landing failures (240.43 seconds total) and package timeout
(360.037 seconds) before conformance completed.

A local run on that unchanged commit isolated the preceding failure:

```sh
dv heavy -- go test -count=1 -parallel=4 -v -timeout=6m ./e2e/m1 \
  -run '^(TestM2FileLanding|TestM2ConformanceInstalledCore)$'
```

The command failed in 251.924 seconds: both file-landing cases reached their
turn deadline, consuming 241.54 seconds, while all six conformance cases then
passed. This separates the preceding fixture failure from conformance behavior.
The existing failing acceptance test supplies the red evidence; no artificial
failure, relaxed assertion or longer deadline was introduced.

## Recovery evidence

The verification branch incorporated current `dev` and PR #1002 at commit
`ee9cc46bda45f26d1331c901a17fa21babc2e6c9`. The same reproduction command then
passed in 30.741 seconds: file landing took 20.38 seconds and all six
conformance cases passed. Both file-landing cases observed a published
compaction summary and retained source coverage through restart.

The next run used the exact 43-test `e2e-m2-4` selection from run 39930348507,
with `-count=1 -timeout=6m -mod=readonly -json -parallel=4`. It finished in
120.019 seconds. Every selected top-level test had a terminal result, and
`TestM2ConformanceInstalledCore` and all six of its cases passed. No package
timeout occurred.

The shared run still failed two separate scenarios:
`TestM2ConversationWorkerProgressInstalledCore`
([#991](https://github.com/thisisjun786/lina/issues/991)) and
`TestM2TUIHomeWaitingInstalledCore`
([#990](https://github.com/thisisjun786/lina/issues/990)). Therefore this is
conformance recovery evidence, not a green result for the whole shard or suite.
The branch subsequently incorporated `dev` at
`c33564cd78503203eb9cb6636da06292927a4c9a`, including #990's fix; the shared-run
numbers above describe the earlier tested commit only.

PR #1002 has merged into `dev`. This change records the investigation and
verification without further runtime or test changes. No external behavior is
ported and no product contract changes are needed.
