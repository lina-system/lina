# Codex session completion fixture

Issue [#1001](https://github.com/lina-system/lina/issues/1001) reported two empty
worker tool results and nine provider steps in `TestM2InstalledCodexSession`.
The expected count is seven control requests, with two worker outcome turns
handled separately.

## Cause

On `dev` commit `f40665198f70d95b38ecc269f46d7e6f5bdfef7b`, the focused scenario
failed before the fix:

```text
codex_session_test.go:284: worker tool failed:
codex_session_test.go:284: worker tool failed:
codex_session_test.go:369: provider steps=9
--- FAIL: TestM2InstalledCodexSession (7.21s)
```

The [request layout](../docs/design/conversation-and-memory.md#request-layout)
places the current input before the ContextPacket. Retrieved packet items also
have the user role. The fixture selected the last user-role item, which was
persona settings, and missed the actual worker outcome earlier in the request.
It then treated the final developer context item as a tool result.

Two diagnostic runs reproduced the same failure. The requests contained both
valid terminal worker events: interruption, then completion with the expected
output. All seven control steps had succeeded. The failure was in the fixture's
input classification, not worker launch or the Codex control protocol.

## Correction and coverage

The fixture skips reference-bearing ContextPacket evidence when finding the
latest input. It still stops at a newer person or unrelated external input, so
an old completion cannot consume a subsequent request. Tool-discovery receipts
remain separate from the current input.

`TestWorkerCompletionFixtureContext` uses the real request assembler and
evidence renderer. It covers completed and interrupted workers, trailing
persona and memory evidence, discovery receipts, and newer person/external
inputs. A result marker in history or recalled context cannot turn an
interruption into a successful result.

The installed-Core scenario retains its original assertions: worker version
reporting, steering, confirmed interruption, continuation, the worker-created
file, exactly one applied start effect, and durable notices after Core restart.
The model and installed worker are synthetic; this does not claim verification
against a live provider account. Production behavior and design contracts are
unchanged.

## Focused verification

All commands ran through `dv heavy`. Before the helper correction, the new
regression failed its completed, interrupted and discovery cases with
`handled=false, want true`; its newer-input cases passed. After the correction,
all five cases passed.

```sh
go test -count=1 -v ./e2e/m1 -run '^TestWorkerCompletionFixtureContext$'
go test -count=3 -v ./e2e/m1 -run '^TestM2InstalledCodexSession$'
go test -count=1 -v ./e2e/m1 -run '^TestM2InstalledCodexSlowManager$'
```

The original scenario passed all three repetitions (2.81 s, 1.90 s and 2.02 s).
The slow-manager scenario passed in 33.86 s with its existing 31-second manager
delay. The same normal scenario had failed in all three baseline/diagnostic
runs. No timeouts, sleeps, control counts or product assertions were relaxed.
Final changed-range foundation and current-head CI/review results are recorded
on the pull request.
