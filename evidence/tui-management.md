# TUI management acceptance

Issue [#110](https://github.com/thisisjun786/lina/issues/110) groups the memory,
agent, account, channel and agent talk-log commands in
[G1-30](../docs/design/go-roadmap.md#1e-client-protocol-and-tui).
The acceptance run on 2026-10-22 used `dev` commit
`689ff60e` and found all of its done conditions implemented. This record adds
no product behavior. The contracts remain
[T-04](../docs/design/tui.md#approvals-questions-and-sign-in-t-04),
[T-14](../docs/design/tui.md#agent-surfaces-and-settings-as-text-t-14),
[C-25 and C-27](../docs/design/go-core.md#plugins-channels-and-exchanges), and
[C-47](../docs/design/go-core.md#model-path-and-accounts).

| Done condition | Evidence inspected and executed | Result |
| --- | --- | --- |
| Forget from the TUI gives zero hits in the next request. | `TestM1TUIMemoryAgentsInstalledCore/round_trip` in [tui_memory_agents_test.go](../e2e/tui_memory_agents_test.go) sends remember, correct and forget through the installed TUI. It checks zero list hits, absence of forgotten content in the next captured model request, and absence after Core restart. | Passed |
| Agent create, choose, rename and delete round-trip by id. | The same round trip checks returned identities, selection of the new conversation, private-memory isolation, the renamed display, removal of the selected identity, and refusal of its history subscription after deletion. | Passed |
| ChatGPT sign-in completes through the TUI relay, including a pasted URL. | `TestM1TUIAccounts` in [tui_accounts_test.go](../e2e/tui_accounts_test.go) exercises both loopback and SSH paste with the real account and relay services. `TestM1TUIAccountsInstalledCore` in [tui_accounts_installed_test.go](../e2e/tui_accounts_installed_test.go) verifies the boot composition, durable owner binding, listing and sign-out. | Passed |
| A channel is paired and bound from the TUI; the talk log has one line per exchange and expands it. | `TestM1TUIChannelPairing` in [tui_channels_test.go](../e2e/tui_channels_test.go) connects, pairs through a platform update, binds the paired chat and disconnects. `TestM1TUITalkLogInstalledCore` lists and expands a persisted exchange. `TestTalkListReplaysOnceAndExpandsSelectedIdentity` in [talk_test.go](../internal/tui/ui/talk_test.go) checks duplicate projection and expansion by identity; inspection of [talk.go](../internal/tui/ui/talk.go) confirms that each summary's fields are flattened to one physical line. | Passed |
| All sub-issues are closed. | GitHub reported [#111](https://github.com/thisisjun786/lina/issues/111), [#112](https://github.com/thisisjun786/lina/issues/112) and [#113](https://github.com/thisisjun786/lina/issues/113) closed on the acceptance date. | Confirmed |

Reproduce the focused run from the repository root:

```sh
go test -count=1 ./internal/tui/ui/... ./internal/tui/state/... ./internal/clientapi/...
go test -count=1 -timeout=6m ./e2e -run '^(TestM1TUIMemoryAgentsInstalledCore|TestM1TUIAccounts|TestM1TUIAccountsInstalledCore|TestM1TUIChannelsInstalledCore|TestM1TUIChannelPairing|TestM1TUITalkLogInstalledCore)$' -v
```

Both commands passed. The six end-to-end test groups completed in 8.248 seconds
using real PTYs, generated client messages and isolated synthetic state. The
account tests inject a signed issuer transport, channel pairing injects Telegram
HTTP responses, and talk-log acceptance seeds the real exchange store. These
checks establish the client flows; they do not establish live provider or live
Telegram acceptance. The remaining work in the broader memory, account and
channel parents [#63](https://github.com/thisisjun786/lina/issues/63),
[#79](https://github.com/thisisjun786/lina/issues/79) and
[#96](https://github.com/thisisjun786/lina/issues/96) retains its own gates.
