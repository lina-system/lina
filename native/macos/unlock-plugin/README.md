# Locked-Mac unlock plug-in

An authorization plug-in for `/Library/Security/SecurityAgentPlugins/` with one
privileged mechanism and no interface. It answers allow only for an armed ticket
from LINA's signed lock broker, and deny in every other case, inside a deadline
the plug-in enforces itself. It stores no password and reads none.

The behaviour is specified in
[Work on a locked Mac](../../../docs/design/non-competition.md#work-on-a-locked-mac);
the platform facts behind it are in
[the locked-Mac research note](../../../docs/research/locked-mac-computer-use.md#proving-the-peer).

## Layout

| Path | Built on | Purpose |
| --- | --- | --- |
| `src/lina_unlock.c`, `src/lina_unlock.h` | Linux and macOS | The decision core: the order, the deadline and the socket exchange. |
| `src/protocol.h` | Linux and macOS | The fixed request and reply frames. |
| `src/mechanism.c` | Linux and macOS | One decision, one log line, one result. |
| `src/engine_from_callbacks.c` | Linux and macOS | The plug-in's engine: `SetResult` and `WriteLog` only. |
| `src/darwin_host.c` | macOS | Files, socket, `LOCAL_PEERTOKEN`, `SecCode` checks, clock, randomness. |
| `src/plugin_darwin.c` | macOS | `AuthorizationPluginCreate` and the mechanism callbacks. |
| `tests/` | Linux and macOS | The core's tests and a scripted host with an injected clock. |
| `Info.plist` | macOS | Bundle metadata. |
| `build.sh` | macOS | Builds the arm64 bundle, signs it and verifies the signature. |

## Rights and mechanism

The installer writes these; the plug-in only answers when one of them evaluates
its mechanism. The names are fixed here so the installer, the guard and the
self-test agree.

| Name | Definition |
| --- | --- |
| `com.lina.locked-screen.unlock` | class `evaluate-mechanisms`, mechanism `LinaUnlock:unlock,privileged`, `tries` 1, `shared` false |
| `com.lina.locked-screen.selftest` | the same mechanism, evaluated outside the lock screen; the install expects a deny |
| `system.login.screensaver` | class `rule`, `k-of-n` 1, the rule list `com.lina.locked-screen.unlock`, then `use-login-window-ui` |

`tries` 1 is part of the contract: the engine's default would repeat a denied
mechanism up to 10,000 times. The mechanism never returns undefined or user
cancelled, because either one ends the rule list before
`use-login-window-ui` is tried.

## Install state

The plug-in reads two root-owned files. Nothing else persists.

| Path | Written by | Content |
| --- | --- | --- |
| `/var/db/lina/locked-screen/enabled` | installer, removed when the feature is off | `socket=<broker socket path>`, `uid=<the person's uid>`, `deadline_ms=<1..500>` |
| `/var/db/lina/locked-screen/evaluation` | the plug-in | crash marker; present while an evaluation runs |

The directory is root-only. The plug-in writes the marker before it connects and
clears it before it answers. A marker left behind by a crashed evaluation makes
every later evaluation deny without connecting until the guard has checked the
install. The deadline defaults to 500 ms and is clamped to 1–500 ms; the eval
sets the value in `deadline_ms`.

## Decision

In order; the first failing step denies.

1. The enabled marker is present and readable.
2. The crash marker is absent.
3. The crash marker is written.
4. A non-blocking connect to the broker socket of the person who turned the feature on.
5. `LOCAL_PEERTOKEN` returns the peer's audit token.
6. `SecCodeCopyGuestWithAttributes` with `kSecGuestAttributeAudit` finds the peer's code.
7. `SecCodeCheckValidity` passes against the built-in requirement: Apple-issued, LINA's team and the broker's identifier.
8. The code has the hardened runtime flag and no `com.apple.security.get-task-allow`.
9. The audit token's audit uid is the person who turned the feature on.
10. A fresh nonce is written.
11. The reply carries that nonce and an armed ticket.
12. The audit token is read again and is unchanged.

Every other case denies with its own reason, and the crash marker is cleared on
the way out. The answer is one system-log line with the ticket id and the
reason; no credential is logged, and no context or hint value is read or
written, because the engine exposes only `SetResult` and `WriteLog`.

## Wire format

One fixed request and one fixed reply, both documented in `src/protocol.h`.

| Frame | Bytes | Fields |
| --- | --- | --- |
| request | 40 | `LNU1`, version, 3 reserved, 32-byte nonce |
| reply | 56 | `LNR1`, version, flags (`0x01` armed), 2 reserved, 32-byte nonce, 16-byte ticket id |

## Build and test

```sh
# Linux foundation check: compiles the core and runs its tests.
node scripts/verify/check-native.mjs

# macOS: builds build/LinaUnlock.bundle for arm64, signs and verifies it.
LINA_TEAM_ID=XXXXXXXXXX LINA_SIGN_IDENTITY='Developer ID Application: ...' \
  native/macos/unlock-plugin/build.sh
```

`build.sh` uses an ad-hoc signature when `LINA_SIGN_IDENTITY` is unset, which is
enough to compile and verify locally but is not shippable. With
`LINA_NOTARY_PROFILE` set it also submits the bundle to notarytool and staples
the ticket.

The core's tests run in the Linux foundation check. The bundle build, the
signature check and notarization need macOS; their result is recorded as
evidence for the release that ships the bundle. The macOS cells of the
[non-competition matrix](../../../docs/design/non-competition.md#matrix) stay
`unverified until evidence` until the locked-Mac eval produces its record.
