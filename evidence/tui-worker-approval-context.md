# Long worker approval outcome

Issue #1157 reports `TestM2TUILongWorkerApprovalInstalledCore` reaching resolved
approval and question cards without displaying the worker's final result.

## Contract and cause

The [request layout](../docs/design/conversation-and-memory.md#request-layout)
places ContextPacket after the current input. Retrieved evidence in that packet
also has the user role. The old `respondWorkerCompletion` fixture stopped at the
last user-role item, so it missed the earlier worker completion and returned its
fallback startup reply.

The shared fixture correction on dev (`063ec5a9`, merged through #1214) selects
the latest user input before the ContextPacket's first developer item. This
keeps retrieved evidence and appended discovery results out of that selection.
Core's request layout and the [approval contract](../docs/design/tui.md#approvals-questions-and-sign-in-t-04)
remain unchanged.

The TUI journey checks the worker's completion handshake and file contents before
waiting for the reply. This distinguishes a worker execution failure from a
missing model response, while still requiring the visible final result.

## Reproduction

On dev commit `d1e2a00b1403472664fdf4b4825ec8e94ef12b9f`, run through the
host's heavy-work gate:

```sh
go test ./e2e/m1 -run '^TestM2TUILongWorkerApprovalInstalledCore$' -count=1 -v
```

The unmodified scenario failed in 32.02 seconds:

```text
screen missing "QUESTION_NAME_RESULT": context deadline exceeded
--- FAIL: TestM2TUILongWorkerApprovalInstalledCore (32.02s)
```

Both decision cards were resolved. The fallback startup reply appeared again
instead of the outcome. A second run with the diagnostic ordering confirmed
`worker completed and artifact matched after approval and question answer`,
then failed at the same missing-result assertion in 30.86 seconds (2/2 failures).

## Verification scope

The scenario runs installed Core and the real TUI in a PTY with isolated state.
The model and Codex stdio peer are synthetic. It pages through the long approval,
requires separate selection and confirmation, answers the worker's question,
checks the written artifact and requires the conversation result. The related
owner-name scenario also covers an agent renamed after Core startup.

This is acceptance-fixture verification, not an authenticated provider run or a
change to product behavior.
