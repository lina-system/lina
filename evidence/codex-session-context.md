# Codex session completion coverage

The classifier fix for issue [#1001](https://github.com/lina-system/lina/issues/1001)
landed through [PR #1187](https://github.com/lina-system/lina/pull/1187),
commit `02e39a49`, for issue #1152. This PR preserves that implementation
unchanged and adds two complementary regression cases.

## Existing fix

The [request layout](../docs/design/conversation-and-memory.md#request-layout)
places the current input before the ContextPacket. Retrieved packet items can
also have the user role. The classifier now identifies the last user input
before the first developer ContextPacket item, rather than treating retrieved
context as the current input.

Historically, `TestM2InstalledCodexSession` failed on dev commit
`f40665198f70d95b38ecc269f46d7e6f5bdfef7b` with two empty worker tool results
and `provider steps=9`, instead of seven control requests and two separately
handled outcome turns. That is pre-fix evidence, not a failure of current dev.

## Additional coverage

`TestCodexWorkCompletionFixtureContext`, added by #1187, already covers normal
completion, tool discovery, older worker context and newer person inputs.
`TestWorkerCompletionFixtureContext` retains only the additional cases:

- An interrupted worker is recognized without reporting the result marker from
  an earlier completed worker or recalled memory.
- A current unrelated external input is not answered as an older worker outcome
  and receives no scripted worker response.

Both cases use Core's real request assembler and evidence renderer, with older
results in history and trailing persona and memory evidence. Duplicate normal
completion, discovery and person-input cases are removed.

The installed-Core scenario retains its version reporting, steering, confirmed
interruption, continuation, worker-created file, single applied start effect,
and durable notices after restart. Its model and worker are synthetic.
Production behavior and design contracts are unchanged.

## Focused verification

The requested validation uses one heavy-work slot:

```sh
go test -count=1 -v ./e2e/m1 -run '^(TestCodexWorkCompletionFixtureContext|TestWorkerCompletionFixtureContext)$'
go test -count=2 -v ./e2e/m1 -run '^TestM2InstalledCodexSession$'
```

After merging dev `aadd9917`, all six existing cases and both additional cases
passed. The installed-Core session passed both repetitions (12.45 s and 7.09 s).
Current-head CI and review results are recorded on the pull request. Earlier
red/green and broader diagnostic runs remain in the issue report as historical
evidence.
