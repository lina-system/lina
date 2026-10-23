# internal/core/gateway/telegram implementation notes

## Implementation notes

Design authority: [Go Core](../../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-27 Telegram acceptance

Package scenarios use real canon and SIGKILL barriers before admission commit, after commit and during a send.
The #96 completion scenario, `e2e/m1/telegram.json`, reopens a saved connection and its secret through production boot with only the platform HTTP transport replaced.
It holds the bot identity lookup until shutdown, verifies READY and the unavailable connection through the channel protocol, and requires Core to cancel and join the request before stopping.
It does not install a separate canon service or start its own adapter.
Live channel turn composition and app delivery views attach in their owning flows.
