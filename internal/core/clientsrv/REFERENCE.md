# internal/core/clientsrv implementation notes

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-42 local listener implementation

The local listener uses the generated `kit/protocol` types.
It creates missing socket directories at 0700, requires an existing parent to be private and owned by Core's OS user, and binds the socket at 0600 before accepting clients.
The listener refuses an occupied socket path.
Boot may first reclaim its own recorded stale socket under the C-03 rules.
Kernel peer credentials must match Core's user before any frame is processed.
Linux uses `SO_PEERCRED`; the Darwin credential adapter uses `LOCAL_PEERCRED` and `LOCAL_PEERPID`, while private socket binding remains Linux-only in this increment.

### C-42 tailnet acceptance

`e2e/m3/tailnet-listener.json` runs installed Core through `boot.Run`, with real TCP and Unix sockets, a synthetic Unix LocalAPI and a loopback network seam that observes the requested tailnet bind/source addresses.
It compares complete welcomes, performs real tool rounds and a background model request, and proves typed, approval-free, repeated stop cancels that request from either transport.
The built CLI separately proves public-bind startup refusal.
This does not claim a two-device Tailscale deployment test; WebSocket framing remains #203.
