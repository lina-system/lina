# Discord bot source behavioral reference

Read before implementation: [integrations.md](../../../../docs/design/integrations.md#platforms),
[Messengers research](../../../../docs/research/messengers.md) (Discord, bot) and
the Discord adapter's own [reference](../../gateway/discord/REFERENCE.md). The
reference installations named by the porting rule (`~/.hermes/hermes-agent`,
`~/.hermes/plugins`, `~/code/codex-relay-workflow`) were not present on this
host, so the public Hermes Agent commit cited by the gateway reference was not
read again for this increment.

- **Seen:** the research note records agent-messenger's Discord bot mode at tag
  `2.39.1`: bot token, servers, channels, history with `before`/`after`,
  threads, and text that needs the privileged message-content intent. It also
  records that Discord's terms forbid self-bots, which is why the bot mode is
  the official route and the personal account stays with the add-on.
- **Seen:** the existing channel adapter in this repository already owns the
  Gateway transport: identify/resume, heartbeat, close 4007/4009 recovery, a
  durable session cursor, and a content intent requested only for an explicit
  `all_messages` binding.
- **Inferred for LINA:** the source reuses that transport instead of growing a
  second Discord client. One Gateway session per bot token serves a channel
  connection and a bot-mode source together, and each receives every event.
  The shared Gateway checkpoint is durable in `discord_sessions`, independent
  of channel connections, and advances after every subscriber accepts a dispatch.
  The source's history cursor is its chat cursor in `messenger_chat`; live
  events are newer than the last page, and a later page read admits them again
  without adding anything.
  Core's `boot` composition supplies the REST client and Gateway listener through
  source-owned ports, keeping the messenger and gateway flow packages independent.
- **Inferred for LINA:** a bot-mode source always needs message text, so its
  session requests the message-content intent. When Discord refuses it, the
  source reports that text is unavailable and admits nothing, rather than
  storing records with empty bodies.
- **Inferred for LINA:** the person's own numeric user id is the only proof of
  `person`, exactly as channel pairing records it. Any bot author is
  `automation`, and everyone else is an external participant.
- **Inferred for LINA:** reference-only messages are not evidence of a missing
  content intent. A content-free type 21 thread starter points at the message
  that began its thread, and a forward carries the copied words and media in
  `message_snapshots`; history reads both and advances past them.
- **Inferred for LINA:** `MESSAGE_UPDATE` carries only the fields Discord
  changed. An update without an author is completed from the message REST
  keeps; an update that changes neither text nor edit time, such as an embed
  refresh, is skipped; and an update whose text is unavailable is skipped
  rather than failing the shared session.
- **Inferred for LINA:** a live event the store cannot admit yet, such as an
  edit held behind an unsettled write, comes back as a retryable dispatch. The
  shared session replays it on the same socket after a bounded backoff, so the
  event is neither acknowledged nor lost, and other subscribers keep their
  session.
- **Unknown:** live Discord behaviour with the intent disabled in the
  developer portal, limits for very large guilds, and archived-thread
  retention. Synthetic Gateway and REST fixtures establish LINA's protocol and
  admission only.
