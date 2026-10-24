# internal/core/messenger/telegram implementation notes

Design authority: [integrations.md](../../../../docs/design/integrations.md#platforms),
[Credentials and sign-in](../../../../docs/design/integrations.md#credentials-and-sign-in),
[Messenger adapter boundary](../../../../docs/design/integrations.md#messenger-adapter-boundary)
and [Sync and coverage](../../../../docs/design/integrations.md#sync-and-coverage).
Research: [messengers.md](../../../../docs/research/messengers.md). These notes
record how the port meets the contract; they do not restate it.

## Reference

Upstream, read read-only for the research note: agent-messenger 2.39.1 at
commit `9d2de96a43fe3e0e172e8e399454c475146a2697` (2026-10-20),
`src/platforms/telegram`. Its Telegram platform is a TDLib client that signs in
with the person's own `api_id` and reads chats, the newest N messages and
members. LINA ports the same shape onto gotd/td `v0.163.0`, the maintained Go
MTProto client, as [the research note](../../../../docs/research/messengers.md)
decides.

The port uses the research note's recorded agent-messenger reading and the
pinned gotd API types. No Hermes, Honcho, LCM-X or CRW behavior is ported here.

### Seen

- Sign-in is a phone number, then a code delivered in the app (5 digits), then
  an optional 2FA password. The code prompt names the length and the next
  delivery method the platform offers (`auth.sentCode`).
- A session is an auth key plus DC and salt; it can be resumed without a new
  code. Treating it as ordinary text would leak the account.
- Dialogs (`messages.getDialogs`) page with an offset peer and a top-message id.
- History (`messages.getHistory`) pages by message id, newest first, at most
  100 per request.
- Edits arrive as `updateEditMessage` / `updateEditChannelMessage` carrying the
  message again with `edit_date`. Deletions arrive as `updateDeleteMessages`
  (ids only, account-wide) or `updateDeleteChannelMessages` (channel id and ids).
- Reads that change state exist as separate methods (`messages.readHistory`,
  `messages.readMentions`, `messages.readReactions`,
  `messages.readMessageContents`, `channels.readHistory`) and presence as
  `account.updateStatus` and `messages.setTyping`. A client chooses whether to
  send them.
- `FLOOD_WAIT_n` is returned as an RPC error with code 420.

### Inferred

- The person's own messages are those whose `from_id` is the connected account;
  `out` repeats that for the account's own copies. Channel posts have no
  `from_id` and speak for the channel, not for a person.
- A message a bot or a bot-forwarding business account wrote is automation, not
  a person's words.
- A channel's message ids are per channel; private chats and basic groups share
  one account-wide id sequence, so `updateDeleteMessages` ids can match more than
  one chat. The deletion is restricted to private chats and basic groups; channel and
  supergroup IDs are separate namespaces.
- "First fill reads back as far as the platform allows": history paging walks
  backwards until a short page, then the steady read follows new ids above the
  newest id seen.
- Attachment bytes are fetched with `upload.getFile` only when the person asks;
  the page carries metadata and the file location stays in the adapter.

### Unknown

- The sync interval and read window this platform should use. `Capabilities`
  currently states one minute and no read window, to be measured during
  acceptance of this adapter.
- Whether the platform's history paging ends with an empty page or a short page
  in every chat kind; the adapter treats a page shorter than the request as the
  end.
- How a `FLOOD_WAIT` longer than the adapter's retry budget should surface; the
  adapter retries three times and then reports the source as failing with the
  platform's cause.

## Sign-in and the session

`Authenticate` drives the platform's code and 2FA flow through a `Prompt`
supplied by the app or the TUI. The adapter keeps neither the code nor the
password; the session it keeps is the MTProto auth key, stored as an opaque blob
in the secret store under `<person, telegram, session:<account>>`. No standalone session file is written; the secret store uses the existing
0600 file fallback where an OS keychain is unavailable. `DeleteSession` removes it when the source disconnects.

The person's `api_id` and `api_hash` live under `<person, telegram, api_id>` and
`api_hash`. LINA never creates them, never scripts my.telegram.org, and a source
without them stays `needs_sign_in` with `ErrNoCredentials`.

## Reads

`SignIn` reports state without prompting. `ListChats` pages dialogs and records
each peer's input form. Completion uses the raw dialog count and response type,
before folders or unsupported peers are filtered. A page that cannot advance
reports a failure rather than committing a partial list. `Page` reads history
from the cursor:

- fill: page backwards from the oldest id read so far, oldest first in the page;
- steady: read the newest page and keep only ids above the newest id the fill
  recorded, continuing below the page's oldest id when it was full.

The cursor is `tgc1:<newest>:<oldest>:<phase>:<newestUnix>:<before>:<pending>`.
The pending high watermark is promoted only when catch-up finishes. It carries
only platform message ids and a time, no names or text. An unrecognised cursor
starts a first fill, so a cursor from another adapter is never misread.

`Members` lists a direct chat's peer or a group's participants. `Live` delivers
edits and deletions; new messages arrive through sync, so a live event never
moves a cursor. Attachment metadata is read with the page and its bytes only on
`FetchAttachment`.

No request that marks a message read or shows presence exists in this package.
`TestReadsNeverTouchReadStateOrPresence` asserts it against an in-process
MTProto server, and the installed-Core scenario asserts it again.

## Rate limits

`FLOOD_WAIT_n` is honoured before the same request is repeated, up to three
attempts, through an injectable wait so tests do not sleep on the platform's
schedule. A request that is still limited reports the platform's error and the
source keeps its committed cursor.

## Core wiring

Core opens a Telegram source through `boot`'s messenger factory with its own
secret store; `Config.Options` exists for tests to point the client at an
in-process MTProto server. One adapter serves both the sync loop and the live
subscription of a source, and `Service.Close` closes it.

Writes, grants and approvals belong to #1084; this adapter declares no write
verb.

## Installed sign-in

`boot.telegramControl` serves the owner-only `telegram_source` protocol route.
The `/telegram` TUI form masks every input and clears secrets on submission,
cancellation and reconnect. An attempt has a random temporary session key and
a five-minute, connection-bound lifetime. Core copies the session under the
platform-returned account ID only after authentication, removes the temporary
key, connects the source, and wakes sync and live-event workers. Core starts
the messenger loop even when no test configuration is supplied.

Acceptance uses a child Core process and an in-process fake MTProto server.
The owner client performs the full code/2FA exchange through the socket; an
explicit subscription barrier precedes one edit and one deletion push.

## Missed updates

`Recover` establishes the account state and channel dialog pts before first fill.
Each subsequent sync requests account differences (including slices) and channel
differences before history paging. Core admits each event through the same
per-source admission lock as live events. Completed response checkpoints persist
as versioned secret-store blobs under `updates:<account>`; a failed admission or
save replays from the previous checkpoint. Too-long or non-progressing differences
report `telegram_update_gap` instead of treating the source as current.

Seen: pinned gotd types expose account/channel differences and too-long responses.
Inferred: canon's idempotent admission permits replay after an interrupted save.
Unknown: Telegram's retention window; no missing message is inferred deleted.
