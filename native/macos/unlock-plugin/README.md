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
| `/var/db/lina/locked-screen/evaluation` | the plug-in; the guard removes a stale one | crash marker; present while an evaluation runs |

The directory is root-only. The plug-in writes the marker before it connects and
clears it before it answers. A marker left behind by a crashed evaluation makes
every later evaluation deny without connecting until the guard has checked the
install. The deadline defaults to 500 ms and is clamped to 1–500 ms; the eval sets the
value in `deadline_ms`. It covers the whole evaluation: the clock starts when
the mechanism is invoked, so a stalled install-state read, marker write or peer
inspection denies as well.

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
written. The decision path uses only `SetResult` and `WriteLog`.

## Deactivation

`MechanismInvoke` returns only after it has answered, so a later
`MechanismDeactivate` has no evaluation left to stop. It calls `DidDeactivate`
with the engine reference stored at creation and returns that status. The
engine waits for the callback; returning success without it leaves the
evaluation waiting. A missing callback returns an internal error instead of
reporting a handshake that did not happen. `MechanismDestroy` frees the
mechanism and does not call `DidDeactivate` again.

## Wire format

One fixed request and one fixed reply, both documented in `src/protocol.h`.

| Frame | Bytes | Fields |
| --- | --- | --- |
| request | 40 | `LNU1`, version, 3 reserved, 32-byte nonce |
| reply | 56 | `LNR1`, version, flags (`0x01` armed), 2 reserved, 32-byte nonce, 16-byte ticket id |

## Build and test

```sh
# Linux foundation check: compiles the core and the plug-in entry points, and runs their tests.
node scripts/verify/check-native.mjs

# macOS: builds build/LinaUnlock.bundle for arm64, signs and verifies it.
LINA_TEAM_ID=XXXXXXXXXX LINA_SIGN_IDENTITY='Developer ID Application: ...' \
  native/macos/unlock-plugin/build.sh
```

`build.sh` uses an ad-hoc signature when `LINA_SIGN_IDENTITY` is unset, which is
enough to compile and verify locally but is not shippable. With
`LINA_NOTARY_PROFILE` set it also submits the bundle to notarytool and staples
the ticket.

The core's tests run in the Linux foundation check, which also parses
`build.sh` with `bash -n`: the script only ever runs on macOS, so nothing else
would catch a shell error in it.

The bundle build and the signature check run on an Apple silicon hosted macOS
runner in
[`macos-unlock-plugin.yml`](../../../.github/workflows/macos-unlock-plugin.yml)
as two jobs. `bundle (arm64)` runs on every push and pull request that touches
`native/macos/**`: it builds `build.sh` ad-hoc, runs `codesign --verify --strict`,
`codesign -dvvv` and `lipo -archs`, fails when the hardened runtime flag is
absent, and keeps the bundle as a build artifact.

`signed bundle (arm64, Developer ID)` produces the shippable signature. It runs
on `dev`, `main` and `v*` tags, and on any other ref only when someone starts it
by hand:

```sh
gh workflow run macos-unlock-plugin.yml --ref <branch> -f sign=true
```

The job declares the `macos-signing` environment, which requires the owner's
approval, so the key reaches a runner only for one approved run. This is the
rule, not a convenience: the job runs the checked-out `build.sh` next to the
imported key, so a ref that may replace that script must never reach it
unattended. The identity comes from four environment secrets: `MACOS_SIGNING_P12`
(base64 of the exported certificate and key), `MACOS_SIGNING_P12_PASSWORD`,
`MACOS_SIGN_IDENTITY` (the identity's common name) and `MACOS_TEAM_ID`. The job
fails and names the missing ones when they are absent, and fails when the
signature reports another team. Its step summary holds the `codesign` output for
the head it ran on. Notarization stays optional and needs `LINA_NOTARY_PROFILE`
on a machine where the bundle is released.

The arm64 build and signature run recorded for this bundle is in
[`evidence/macos-unlock-bundle.json`](../../../evidence/macos-unlock-bundle.json).
It covers the ad-hoc path; it is extended once the `macos-signing` environment
holds the certificate.

The macOS cells of the
[non-competition matrix](../../../docs/design/non-competition.md#matrix) stay
`unverified until evidence` until the locked-Mac eval produces its record. A
build and a signature check are not that record.
