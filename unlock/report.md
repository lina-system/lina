# Verification report — 0.2.0

Date: 2026-10-24, Asia/Seoul.

## Delivery state

The source/handoff package now contains the native authorization plugin, helper, GUI agent, SSH bridge, Hermes plugin, signing helper, staged installer, transactional activation, timed rollback, confirmation and removal workflows.

The package has **not** been installed into the Mac's security-agent directory or authorization database. Actual loginwindow unlock, SecurityAgentHelper loading, headless CUA and FaceTime acceptance are not verified. Current development keychain preflight fails and correctly prevents activation.

## Red-first evidence

- The initial deny-all grant implementation failed the valid-grant requirement before implementation: [initial red](evidence/red.txt).
- The initial receipt API returned false, and the new requirement that only the exact consumed grant prove participation failed before adding the durable receipt: [receipt red](evidence/receipt-red.txt).

These are new-feature requirement tests, not reproductions of an existing deployed product bug.

## Passing checks

- Portable native tests: grant TTL/rollback/expiry, user/session/boot binding, revocation, filesystem protections, and exactly one winner among 12 independent competing consumers.
- Portable request-flow tests: synthetic full trigger/receipt/postcheck flow; no receipt, failed keychain, changed session, relock, timeout, missing permission or failed trigger never returns ready. Health checks never trigger an unlock. All armed paths revoke.
- Python tests (21 cases): complete original-rule preservation including nontrivial thresholds, partial-write rollback, crash recovery, timeout/reboot rollback, external drift protection, reserved-name/cycle rejection, confirmation gates, bounded SSH handling, host injection rejection, and offline Hermes registration/operation routing.
- Native macOS 27.0.1 / Apple M4: plugin, helper and GUI agent compile under `-Wall -Wextra -Werror`; ad-hoc signatures verify; ordinary-host plugin callbacks and separate synthetic positive callbacks pass; GUI protocol validation, socket peer UID/audit-token code-hash validation and wire framing pass.
- Read-only native status confirms the console is readable and this package is not installed/active.

[Native package build](evidence/native-package-build.txt), [Linux package checks](evidence/linux-package-tests.txt). CI receipts for the delivered revision are linked in the handoff release record.

## Actual keychain observation

The noninteractive probe used only newly generated synthetic generic-password items and did not enumerate existing credentials. In the current unlocked development execution:

```json
{"legacy_keychain_status":-25293,"data_protection_status":-34018}
```

[Probe receipt](evidence/keychain-probe.txt). The development app has an ad-hoc signature and no valid production identity was found on the build host. DP returned a missing-entitlement error; legacy returned an authentication failure. The precise legacy cause is not established. No keychain reset, password recovery, credential dumping or security-setting workaround was attempted.

The installed agent's actual TCC identity and production-signed keychain probe remain acceptance requirements. The installer checks them before activating screen authentication. A terminal-launched process reporting Accessibility=true is not substituted for that check.

## Remaining live acceptance

See [acceptance matrix](docs/ACCEPTANCE.md). The Return-key trigger and the authentication engine's username context are conservative implementation choices that need real loginwindow testing. OS support may block the feature even with correctly signed code. A working ordinary bundle loader or synthetic Allow callback does not prove operating-system authorization-host compatibility.

A successful actual screen-unlock test must also preserve normal password/Touch ID fallback and pass the keychain checks. Display persistence, real CUA capture/input, FaceTime and reboot/initial-login recovery require their own observations.

## CI portability repair

The first macOS CI run exposed a staging-test dependency on the runner-owned Python installation. The production watchdog now selects a root-owned system Python and validates its ancestor directories. The isolated staging test supplies its own interpreter fixture, with a separate test proving writable interpreter ancestry is rejected. Production trust checks were preserved.
