# kit/sandbox implementation notes

## Implementation notes

Design authority: [Go Core](../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-14 sandbox acceptance

The #50 acceptance canary checks canon, secrets, material revisions, symlink aliases, the client socket and its hard-link aliases with network both blocked and authorized; `e2e/m1/sandbox.json` covers a missing or failing launcher with production Core composition over installed state and the installed TUI.
Each case submits a TUI message through the configured conversation route, receives a scripted provider reply, waits for turn completion and recovers the reply after reopening the TUI without another model call.
It also checks the stable sandbox-failure cause and localized setup notices.
The configured conversation route and its installed-Core reply proof are specified in [host-protocol.md](../../docs/design/host-protocol.md#conversation-payloads).
