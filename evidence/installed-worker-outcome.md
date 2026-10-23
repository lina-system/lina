# Installed worker outcome verification

Issue [#892](https://github.com/thisisjun786/lina/issues/892) reported a
post-merge failure of `TestM2InstalledWorkerOutcome`. On 2026-10-23, the issue
was already closed by a later passing run. This record preserves the failure
and the observed recovery against the existing
[session contract](../docs/design/work-and-delegation.md#session-implementation-132).

## Observations

| Source | Commit | Result |
| --- | --- | --- |
| [First failing CI run](https://github.com/thisisjun786/lina/actions/runs/39909795809) | `c0e37f9b3c15c6e4594fef9e7011cd5d64ae58b0` | `after-delivery` passed; `pending-at-restart` reached its context deadline while waiting for the initial model continuation (30.03 s). |
| [Later CI run](https://github.com/thisisjun786/lina/actions/runs/39910291829) | `1ad0d1a8260ac14957f004f7a95022a856382b85` | Both subtests passed (9.93 s); the full workflow succeeded. |
| Local focused run | `1ad0d1a8260ac14957f004f7a95022a856382b85` | Both subtests passed; package time 9.335 s, exit 0. |
| Local focused run after updating from `dev` | `97a6dd69ec089ae45db064f60c45c2e169d3feb3` | Both subtests passed; package time 15.826 s, exit 0. |

The existing test ran before this documentation change. The historical failure
is CI evidence, not a new local reproduction. These observations do not identify
the original cause or establish which intervening change caused recovery.

## Executable coverage

The [headless test](../e2e/m1/worker_outcome_test.go) installs and starts Core,
drives its conversation tools through a scripted HTTP provider, and launches a
separate fixture worker over stdio.

- The worker creates `result.txt`. Core reads its actual bytes through
  `worker_read_file` before the persona reports one checked result.
- `after-delivery` holds worker completion until the initial turn finishes,
  then verifies the result in the originating conversation.
- `pending-at-restart` waits for the committed completion inbox entry before
  stopping Core. Restart recovers one queued follow-up and reads the artifact.
- Both paths restart Core again, recover the recorded terminal snapshot and
  conversation outcome, and verify that the worker launched only once.

The focused command is:

```sh
go test ./e2e/m1 -run '^TestM2InstalledWorkerOutcome$' -count=1 -v -timeout=6m
```

The local execution used the development heavy-job scheduler. Synthetic model
and worker fixtures make this a test of Core execution, persistence and delivery;
live account compatibility and intermittent-failure frequency remain unmeasured.
