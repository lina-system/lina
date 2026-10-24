# Messenger add-on

LINA's own small TypeScript program for the person's messenger accounts. It
offers read operations only, as JSON lines on standard input and output, and
runs the `agent-messenger` SDK pinned in `package-lock.json` on the person's
Node.js. Core starts one process per account inside the OS sandbox, keeps the
session in `plugins/messenger-addon/<account>/`, and renders whatever the add-on
answers ([the messenger add-on](../../docs/design/integrations.md#the-messenger-add-on)).

It runs on the person's Node.js 22.18.0 or newer. The source imports `.ts`
modules directly, which needs the type stripping those releases enable by
default; no build step. Optional native packages are omitted; the locked LevelDB
package uses its included prebuild without an install script.

Platform drivers and their account connection screens are implemented in #1071,
#1085, #1086 and #1087. This package supplies their read protocol and supervisor;
an unregistered platform fails closed with `unsupported_platform`. LINE is
registered: `line-fixture-main.ts` runs the same driver against a recorded
synthetic LINE platform for the tests and Core's end-to-end run.

`main.ts` opens the platform driver named by `LINA_MESSENGER_ADDON_PLATFORM`
(`kakaotalk`, `discord`, `whatsapp` or `line`) for the account in
`LINA_MESSENGER_ADDON_ACCOUNT`, keeping its session under
`AGENT_MESSENGER_CONFIG_DIR`. `fixture-main.ts` answers from a recorded
synthetic platform for the tests and Core's end-to-end run; it contacts nothing.

## Verification

```sh
npm ci --ignore-scripts --omit=dev --omit=optional
npm audit signatures
npm test
```

`foundation` runs the same three commands on the committed lockfile in an
isolated copy with no credentials. `npm test` needs no installed package: the
contract tests read the sources and the manifest directly. The installed-SDK
inventory test runs in the dependency check and enumerates write and extraction
entry points without importing or executing upstream code.

## Upstream sync

The SDK's release is pinned, not followed. Adopting a new one is one reviewed
change, titled for the release:

1. Read the release's changes to the platform directories the drivers use
   (KakaoTalk, Discord, WhatsApp and LINE), to `src/shared`, and to the LINE
   library upstream vendors. List every method that a driver calls and every
   method that was added, removed or renamed.
2. Compare that list with `src/allowlist.ts`. A write, a read-state change, a
   credential extraction or a forced device takeover stays out, whatever the
   release offers. A read the add-on needs is added with its platform.
3. Update the driver and `src/platforms.ts` for changed call shapes, then pin
   the exact release with no optional packages:
   `npm install agent-messenger@<version> --save-exact --ignore-scripts`.
   `package.json` and `package-lock.json` change together, and every lock entry
   keeps its public-registry source and SHA-512 integrity value.
4. Run the contract tests. Record the reviewed range, the methods that were
   added, removed or renamed, and which of them stay out in the change's
   message and pull request.

## Core relay

A managed session exposes `BeginSignIn`, `Answer` and `WhoAmI` to the platform
connection surface. Prompts and responses are transient: the relay sends user
input straight to that account's existing child. Only the add-on stores its
session. A missing runtime fails that source while Core continues serving.

## LINE driver

`src/line.ts` reads chats, the newest 200 messages of a chat and live messages,
and signs in with the SDK's QR and PIN flow as a secondary Android device. The
SDK keeps the session and the Letter Sealing keys under
`AGENT_MESSENGER_CONFIG_DIR`, which Core sets to the account's 0700 data
directory outside backups; nothing is written anywhere else. It declares
`newest-N` history and no surviving cursor, so Core reads the newest messages
again after a restart and drops what it already stored by message id. A message
LINE reports as undecryptable is passed on with `decryptionFailed` and empty
text, never as an empty message. A token LINE revoked reports `needs_sign_in`.
A live stream that throws or closes without being stopped is not retried in
the add-on: `serve.ts` writes one `{"event":"ended","error":<code>}` frame
(`needs_sign_in` for a revoked session, which also drops the client) and Core's
`Live` returns `ErrLiveEnded` with that cause.

- Seen: agent-messenger 2.39.1 `src/platforms/line` — `getChats` merges joined
  group chats with message boxes and maps `user`/`group`/`room`/`square`;
  `getMessages` pages backwards from the latest message with no position beyond
  the call; `decryptMessageText` returns `text: null` plus `decryption_error`
  when Letter Sealing fails; `loginWithQR` emits `onQRUrl` then `onPincode`;
  `login` with a stored token throws an error whose code is
  `AUTHENTICATION_FAILED` when LINE revoked it; `LineCredentialManager` writes
  `line-credentials.json` 0600 under the configured directory, and the vendored
  LINE library stores its key file in `line-storage/` there.
- Inferred: message order for a page is the response's own order reversed, so
  the driver sorts by send time and then by the 64-bit id; LINE's read methods
  expose no group member list, so `members` returns none rather than guessing
  from authors.
- Unknown here: whether LINE keeps serving the newest N for a long-idle
  account, and how often a QR sign-in needs a PIN. No personal account,
  credential or live response is used by the tests.

## Reviewed reference

- Seen: agent-messenger 2.39.1, upstream commit
  `9d2de96a43fe3e0e172e8e399454c475146a2697`; its published platform source includes
  writes, read-state changes and token extraction alongside reads. The fixed
  dispatcher refuses those entry points. The dependency is MIT licensed.
- Inferred: one persistent process and one data directory per account avoid
  sharing sessions across sources. Core passes and checks SourceID on each read.
- Unknown here: live platform behavior and sign-in eligibility. No personal
  account or credential is used by this package's synthetic acceptance tests.
