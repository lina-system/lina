# internal/core/messenger/kakaotalk implementation notes

Design authority: [integrations.md](../../../../docs/design/integrations.md#platforms),
[The messenger add-on](../../../../docs/design/integrations.md#the-messenger-add-on),
[Sync and coverage](../../../../docs/design/integrations.md#sync-and-coverage)
and [Messenger adapter boundary](../../../../docs/design/integrations.md#messenger-adapter-boundary).
Research: [messengers.md](../../../../docs/research/messengers.md) (Platforms: KakaoTalk).

## Seen, inferred, unknown

Read on 2026-10-24 from agent-messenger 2.39.1; nothing was run against a real
account.

**Seen.**

- KakaoTalk has no public protocol for a personal account. The pinned SDK
  speaks LOCO, the reverse-engineered protocol of the official app, and offers
  `getChats` (with `all` and `search` options), `getMessagePage(chatId,
  {count, from})`, `getMembers`, `getProfile` and `onPush` `MSG` packets. Sign-in
  is `KakaoCredentialManager` plus `attemptLogin(email, password, deviceUuid,
  deviceType, force)`, `requestPasscode` and `registerDevice`.
- `getMessagePage` answers `{messages, next_cursor, complete}`. A log record
  carries `log_id`, `type`, `author_id`, `author_name`, `message`, `sent_at` and
  an `attachment` object; a reply names its source in
  `attachment.src_logId`.
- `log_id` is a 64-bit integer that already exceeds `Number.MAX_SAFE_INTEGER`.
  The driver keeps it as the string the SDK carries, and Core validates it as an
  opaque identifier.
- The chat list item carries `chat_id`, `type`, `active_members`,
  `display_name`/`title` and `last_message`. `classifyKakaoChat` maps the type
  onto direct or group/open; an unknown class is refused rather than guessed.
- Kakao lists the behaviour the connect card quotes: calling KakaoTalk's
  protocol or API through unapproved programs, and the possible permanent
  restriction of all KakaoTalk services.

**Inferred.**

- The platform offers no edit or deletion signal to this SDK, so both are
  `false` in the driver's descriptor and every message is reported as first
  read. A message missing from a later page is never a deletion.
- A read does not enter the room, so it does not advance anyone's read
  watermark; the driver names no method that could.
- The measured schedule is 15 minutes, a page is 100 messages, and the platform
  window is two days (172800 s): the window murmur works against in the
  research, with its 40-hour safety threshold inside it. These are the add-on's
  general bounds, which the KakaoTalk driver confirms in its descriptor.
- The chat list is read in one `getChats` call; the SDK walks its own paging
  internally, so Core receives the whole list.

**Unknown.**

- Whether an account, region or client version changes the log record shape or
  the passing rules for the tablet slot. A shape Core does not know refuses the
  batch with `upstream_changed` and keeps the cursor.
- Whether a stored session can be rejected with a code other than
  `invalid_access_token` or `login_rejected`. Any other login code is reported
  as the source failing with its cause, not as needing sign-in.
- The exact behaviour of the passcode slot when the phone does not confirm
  within the add-on's own timeout. The relay stops after ten minutes and the
  person starts sign-in again; the session never keeps the password.
- Whether `MSG` pushes arrive for every chat while a tablet session is open.
  The live subscription is best effort; the periodic read covers what a missed
  push left behind.

## Read-only boundary

The driver names only reads and the sign-in methods in
`adapters/messenger/src/allowlist.ts`; the add-on's dispatch table has no write
or read-state route. The fixture test drives every operation through the line
protocol and asserts that every SDK method reached is in that table and none is
forbidden: no `--force`, no PC device type, no cache extraction, no mark-read.

## One account and the connect card

KakaoTalk is one account per main. `Connect` refuses a second account
(`ErrConnected`) and records the acceptance with the person and the time only
when the platform names the account, so an accepted card that is abandoned
leaves no source row. A source whose stored session the platform rejected signs
in again through `SignIn`, which keeps the recorded acceptance and its date.

The add-on runs one session for this platform
(`addon.Config.SingleAccount`), so the sign-in's pending session and the
source's account id share one process and one data directory; the saved session
therefore survives the change from pending to the platform's user id.

## Sign-in

The add-on's own flow is relayed unchanged: email, then password (read without
echo), then the passcode Kakao shows, which the person enters on the phone while
the tablet registration finishes in the background. A repeated step shows the
same passcode instead of starting a second login. Core keeps no prompt, answer,
passcode or password; the session lives in the add-on's data directory, outside
backups. Each relay is bound to the client connection that accepted the terms
and expires after ten minutes.

## Sync

- The chat list's newest message id is stored per chat. A listing that names the
  message the chat already read through is recorded as coverage without a page
  read, so a quiet chat is fresh rather than stale.
- Pages read forward from the committed `log_id`, 100 messages at a time; the
  records, the cursor and the coverage commit in one transaction.
- Live `MSG` pushes are admitted into the chat they name and never move the
  cursor, which belongs to page reads.
- A chat whose time since the last complete read exceeds the platform window is
  in a gap: the gap is recorded once, told to the person once through a
  background notice naming the chat, and ended by the next complete read. A
  later gap is told again.
- A source that needs sign-in keeps that state while every other source,
  including another account of the same platform, keeps reading.

## Running Core and setup

The `kakaotalk` capability is installed in Core's normal boot. `lina kakaotalk
--socket <socket>` reads one JSON request from stdin and prints the result;
`--request <file>` reads it from a private file instead.

1. `{"operation":"list"}` reports state.
2. `{"operation":"card"}` prints the connect card's terms and the cited links.
3. `{"operation":"connect","accept":true}` starts sign-in; the client asks for
   the email and password and prints the passcode to confirm on the phone.
4. `{"operation":"sign_in","account":"<platform account ID>"}` starts sign-in
   again for a source that needs it.
5. `{"operation":"disconnect","account":"<platform account ID>"}` stops sync
   and deletes the add-on's data directory. Records already read stay.

The client prints the add-on's prompts and shows a code with `show.kind=code`.
A password typed at a terminal is read without echo. No actual account was
used for verification: the driver is exercised against LOCO-shaped fixtures
through the add-on's line protocol, and the service against a scripted add-on.
