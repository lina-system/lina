# internal/core/messenger/whatsapp implementation notes

Design authority:
[integrations.md#whatsapp-source](../../../../docs/design/integrations.md#whatsapp-source),
[integrations.md#sync-and-coverage](../../../../docs/design/integrations.md#sync-and-coverage)
and [the messenger add-on](../../../../docs/design/integrations.md#the-messenger-add-on).
These notes record how this package meets them; they do not restate the contract.

## What this package is

The reads run in `adapters/messenger/src/whatsapp.ts`. This package is the Go
side of one WhatsApp source: the platform name, the capability descriptor that
the add-on's platform-wide set cannot state (link-time history, position cursor,
no edit or deletion signal, live events, no writes), and the refusal.

`boot.resolveMessenger` wraps the add-on adapter of every `whatsapp` source with
`Wrap`, so the descriptor the sync loop reads and the cause the source records
come from here.

## Refusal

The add-on reports a platform cause code such as `source_forbidden` as
`addon.ErrRefused`. `Wrap` turns exactly `source_forbidden` into `ErrForbidden`
with a plain sentence; every other cause passes through unchanged, so a killed
process or a malformed frame keeps its own message instead of being called a
restriction.

## Coverage numbers

`Interval` is 15 minutes and `ReadWindow` 30 minutes. The window is not the
platform's re-read window but the point past which a read gap certainly lost
messages, because the add-on keeps only what arrived while it ran. A gap shown on
a chat therefore means the range may be missing, not that it can be refetched.

## As-built: the live subscription

- Upstream exposes messages through a per-chat buffer, not an event sink. The
  driver polls that buffer at `liveMs` (one second; the fixture sets 20
  milliseconds) and delivers each message id once through the `live` frame,
  which now names its chat.
- The subscription seeds itself from the buffer at start, so a restart does not
  replay up to 500 messages per chat as live events. A message that arrives
  between a committed page and the seed stays in the buffer and the next page
  read carries it; the scheduled read advances the cursor and coverage.
- Core admits a live event with `Store.AdmitLive`: one transaction, no cursor
  and no coverage, idempotent by message id, and refused for a chat that is not
  listed or is out of sync. The service lists chats first when a live event
  names one it has not seen.
- One subscription per source account runs beside the scheduled reads. A
  subscription that ends on error marks that source failing with its cause and
  the next interval retries; the other sources and the conversation keep
  running.

## As-built: the cursor

The driver's cursor is `{"at": <timestamp>, "ids": <ids at that timestamp>}`.
Messages that share a timestamp are all read before the position moves past it,
and the page sorts by timestamp then id, so a repeated read adds nothing.

## Unknown here

- A message the platform delivers with a timestamp older than the committed
  position sorts behind it and stays unread until the buffer rolls. Upstream has
  no sequence number to order by; a real link is what would show whether this
  happens.
- QR sign-in needs a display kind the add-on's protocol does not have, so the
  add-on exposes the pairing-code path (`connectForPairing`) only. The research
  records QR as available upstream.
- Real account behavior, the platform's refusal wording and the 500-message
  bound under load are upstream facts read from the pinned source, not measured
  here. Every accepted case is synthetic.
