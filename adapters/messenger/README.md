# Messenger add-on

LINA's own small TypeScript program for the person's messenger accounts. It
offers read operations only, as JSON lines on standard input and output, and
runs the `agent-messenger` SDK pinned in `package-lock.json` on the person's
Node.js. Core starts one process per account inside the OS sandbox, keeps the
session in `plugins/messenger-addon/<account>/`, and renders whatever the add-on
answers ([the messenger add-on](../../docs/design/integrations.md#the-messenger-add-on)).

It runs on the person's Node.js 22.18.0 or newer. The source imports `.ts`
modules directly, which needs the type stripping those releases enable by
default; no build step and no native package is involved.

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
contract tests read the sources and the manifest directly.

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
