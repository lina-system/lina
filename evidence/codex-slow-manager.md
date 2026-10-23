# Slow manager worker admission verification

Issue [#983](https://github.com/thisisjun786/lina/issues/983) reported a
post-merge failure of `TestM2InstalledCodexSlowManager`. On 2026-10-23,
the finding was already closed by a later passing run. This record checks
the existing [manager admission contract](../docs/design/work-and-delegation.md#development-method)
on current `dev`; it changes no runtime behavior or test deadline.

## Observations

| Source | Commit | Result |
| --- | --- | --- |
| [Failure before recovery](https://github.com/thisisjun786/lina/actions/runs/39929265547/job/120333423734) | `12f57f6dbfebda53daab971ac5359e89e95c0a27` | The package reached its six-minute timeout while this scenario had run for four seconds. Its stack was waiting for the initial Core connection. |
| [Later CI run](https://github.com/thisisjun786/lina/actions/runs/39930348507) | `e0af3373b4e0be0eb468d9ff57c11e8138aa530e` | The scenario passed in 37.04 s. Other scenarios failed, so the workflow as a whole was not green. |
| Local focused run, three repetitions | `f38a584f94379edfeb074ea233a18fa8524a93e6` | All passed: 37.59 s, 37.42 s and 38.32 s; package time 113.344 s, exit 0. |

The failing job recorded:

```text
panic: test timed out after 6m0s
    running tests:
        TestM2DispatchedWorkArea/success (1s)
        TestM2InstalledCodexSession (5s)
        TestM2InstalledCodexSlowManager (4s)
        TestM2WorkerQuotaWindows/success (3s)
```

The historical failure is CI evidence, not a local reproduction. The focused
run happened before this documentation change, on the then-current `origin/dev`,
using Go 1.27.3 on Linux amd64:

```sh
dv heavy -- go test -count=3 -v ./e2e/m1 -run '^TestM2InstalledCodexSlowManager$' -timeout 6m
```

## Running Core coverage

The existing [headless scenario](../e2e/m1/codex_session_test.go) installs and
starts Core, drives the conversation tool registry through a scripted HTTP
provider, and launches a separate fixture worker over the Codex stdio boundary.

- It holds the manager's final response for 31 seconds, beyond the worker's
  30-second start deadline, and requires successful worker admission afterward.
- It steers an active worker, confirms interruption, resumes it, and reads the
  worker-created `result.txt` containing `LINA_WORKER_RESULT`.
- It checks version and terminal-result notices in the originating conversation,
  exactly one applied `worker_start` effect, and durable notices after Core restart.

The observations support the existing admission behavior. They do not identify
the cause of the overall package timeout, prove that intermittent failures are
absent, or establish live-provider compatibility. The full suite was not rerun
locally; the model and worker are fixtures. No product fix or new regression
test was warranted by these results.
