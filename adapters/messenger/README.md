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
unregistered platforms fail closed with `unsupported_platform`.
`src/whatsapp.ts` is the WhatsApp driver: a linked device, pairing-code sign-in,
the history the phone pushed at link time, and a poll of the SDK's per-chat
buffer for live messages. Its coverage and cursor rules are in
[the WhatsApp source](../../docs/design/integrations.md#whatsapp-source) and
[the Go notes](../../internal/core/messenger/whatsapp/REFERENCE.md).

`main.ts` opens the platform driver named by `LINA_MESSENGER_ADDON_PLATFORM`
(`kakaotalk`, `discord`, `whatsapp` or `line`) for the account in
`LINA_MESSENGER_ADDON_ACCOUNT`, keeping its session under
`AGENT_MESSENGER_CONFIG_DIR`. `fixture-main.ts` answers from a recorded
synthetic platform for the tests and Core's end-to-end run; it contacts nothing.
`fixture-whatsapp-main.ts` does the same for the real WhatsApp driver, with a
fake SDK that replays `whatsapp.json`: what the phone pushed, then live messages
at recorded offsets.

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

## Reviewed reference

- Seen: agent-messenger 2.39.1, upstream commit
  `9d2de96a43fe3e0e172e8e399454c475146a2697`; its published platform source includes
  writes, read-state changes and token extraction alongside reads. The fixed
  dispatcher refuses those entry points. The dependency is MIT licensed.
- Inferred: one persistent process and one data directory per account avoid
  sharing sessions across sources. Core passes and checks SourceID on each read.
- Unknown here: live platform behavior and sign-in eligibility. No personal
  account or credential is used by this package's synthetic acceptance tests.
