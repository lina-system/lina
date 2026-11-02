# Hermes macOS Unlock

An independently implemented, password-free screen-unlock integration for Hermes: a native macOS authorization plugin, a root helper, a per-user GUI agent, and an SSH-backed Hermes plugin.

**Delivery state:** complete source/handoff package with native builds and isolated integration tests. **Not deployed or verified against a real locked loginwindow.** The current development build fails the target's noninteractive keychain preflight; activation is intentionally blocked until that is resolved. See [verification report](report.md) and [Hermes handoff](HERMES_HANDOFF.md).

## Components

| Component | Responsibility |
|---|---|
| Hermes plugin | `mac_unlock_status`, `mac_unlock`, `mac_unlock_revoke`; target comes from operator configuration, not model arguments |
| SSH bridge | Existing SSH identity; noninteractive sudo for exact commands; 25-second transport timeout; no automatic retry |
| Root helper | Serialize requests, identify the console session, mint a five-second one-use grant, verify the consumed receipt and observed result |
| GUI agent | Root-authenticated local IPC, one wake/Return attempt, active display checks, noninteractive temporary-item keychain probes |
| AuthorizationPlugin | Validate the engine's username against the locked console session and consume a matching root-owned grant |
| Installer/watchdog | Staged deployment, separate activation, complete original-rule preservation, timed rollback, drift detection, verified confirmation |

The root helper verifies the GUI socket peer's UID and audit-token code hash against the installed agent. The agent accepts privileged requests only from root. No network listener, Mac password, or generic command-execution endpoint is added. The grant is bound to user UID, GUI-session UUID, boot UUID and a continuous clock; it expires during sleep as well.

`desktop_ready: true` requires an unlocked, unchanged session, an active display and successful legacy plus Data Protection Keychain probes. A newly unlocked session additionally requires the exact grant's consumed receipt. This does **not** certify FaceTime media, CUA capture, initial login or FileVault recovery.

## Build and checks

Requirements: Python 3.9+, C compiler; macOS Command Line Tools for native artifacts.

```sh
make test          # portable grant/request-flow tests and Python integration tests
make native-check # macOS build, bundle callbacks, IPC peer/signature tests
make native-test  # also reads current console and process Accessibility state
python3 tools/installer.py plan
```

These commands do not install the package or change system authentication. Native positive authorization tests use synthetic OS facts in a separate executable; the real bundle is loaded only into an ordinary unprivileged test host.

`make macos` creates development ad-hoc signatures. Running it again replaces any production signatures, so sign **after the final build**:

```sh
python3 tools/sign.py --identity 'Developer ID Application: YOUR NAME (TEAMID)' --team YOURTEAMID \
  --provisioning-profile PATH/TO/HermesUnlockAgent.provisionprofile
```

Use a real ten-character team identifier. `--provisioning-profile` is required: the agent claims restricted entitlements (application identifier and keychain access group), and macOS kills it at launch unless a Developer ID profile for `TEAMID.local.hermes.unlock.agent` authorizes them. See [INSTALL.md](docs/INSTALL.md). This repository contains no signing identity or credentials. Developer ID signatures do not by themselves prove compatibility with SecurityAgentHelper; the live acceptance procedure still applies.

## Installation and acceptance

See [INSTALL.md](docs/INSTALL.md) for the full sequence. Installation and activation are separate. Normal installation requires Developer ID-signed artifacts; `--allow-ad-hoc-for-lab` is an explicit experimental exception, not a way around the keychain preflight.

The default activation trial lasts 120 continuous seconds, with the rollback watchdog running every 15 seconds. An unconfirmed trial is also rolled back after reboot. Confirmation requires a recorded locked-to-unlocked transition with the grant receipt and both keychain probes. If another tool changes the screen authorization rule, rollback stops with a conflict rather than overwriting that change; installed files remain available for recovery.

An installer removes only paths it exclusively created. It never resets a keychain, disables SIP, turns off FileVault, replaces global `authenticate` policy, changes sleep settings, or changes `screenUnlockMode`.

## Hermes integration

LINA's core tools `mac_unlock_status`, `mac_unlock` and `mac_unlock_revoke` (`tools/lina_helper/unlock.py` in the repository root) call this package over SSH on a Mac whose entry in `devices.macs` (config.yaml) has `unlock: true`. The model names the Mac; the SSH alias comes only from the device list. Availability checks are offline; an offline Mac does not block LINA startup.

The core client never installs software, changes security policy, configures SSH keys, or answers calls. A failed/ambiguous result remains a failure; no retry loop is hidden behind `unlock`.

## Limits that matter

- This operates on an already logged-in user's screen lock. FileVault preboot, initial GUI login and logout recovery need separate handling.
- The GUI trigger posts one Return key while the exact requested session remains locked. Its behavior in the real loginwindow is an **unverified OS integration**, not established by the synthetic tests.
- Apple's authorization-plugin and Data Protection Keychain limitations may prevent this architecture from meeting the intended runtime requirement on a particular macOS version. [Research and primary sources](research.md)
- No protected-display overlay or local-input guardian is implemented. A successful screen unlock exposes the normal desktop to any attached displays and local input.
- Persistent virtual display setup and FaceTime auto-answer remain Hermes workflow integration, not functionality claimed by this package. [Acceptance matrix](docs/ACCEPTANCE.md)
