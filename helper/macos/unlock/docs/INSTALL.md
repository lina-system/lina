# Install, test, and remove

Run on the intended Mac. Use the source and build from the same immutable revision. Keep an independent SSH recovery connection available during the live trial.

## 1. Build and sign

```sh
make native-test
python3 tools/sign.py --identity 'Developer ID Application: YOUR NAME (TEAMID)' --team YOURTEAMID \
  --provisioning-profile PATH/TO/HermesUnlockAgent.provisionprofile
python3 tools/installer.py plan
```

The GUI agent claims `com.apple.application-identifier` and `keychain-access-groups`. Both are restricted entitlements on macOS: without an embedded profile that authorizes them, AMFI kills the agent at launch ("Code has restricted entitlements, but the validation of its code signature failed"). The Data Protection keychain probe needs those entitlements (`-34018` without them). Create the profile once on developer.apple.com:

1. Certificates, Identifiers & Profiles → Identifiers → + → App IDs → App → Platform macOS, Explicit Bundle ID `local.hermes.unlock.agent`. No extra capability is required. Register.
2. Profiles → + → Distribution → Developer ID → App ID `local.hermes.unlock.agent` → select the Developer ID Application certificate used for signing → name it → Generate → Download.

`sign.py` refuses a profile that is not Developer ID, is expired, does not cover the App ID or keychain group, or does not list the signing certificate, and it fails if the signed agent does not launch.

The final signing command needs an existing Developer ID identity. The current development machine has no valid signing identities; the delivered development binary is not a production-signed release. Do not change security settings or reset a keychain to make the development preflight pass.

## 2. Stage the package

Replace `macuser` with the currently logged-in target GUI user's short name:

```sh
sudo python3 tools/installer.py install --user macuser --enable-ssh-command
```

This stages the plugin without changing `system.login.screensaver`. It installs:

- `/Library/Application Support/HermesUnlock`: root helper, user agent, fixed allowed UID and recovery tools.
- `/Library/Security/SecurityAgentPlugins/HermesUnlockAuthorizationPlugin.bundle`.
- `local.hermes.unlock.agent` LaunchAgent, plus runtime preparation and rollback LaunchDaemons.
- `/var/db/hermes-unlock`: root-only installation/activation state.
- Optional `/etc/sudoers.d/hermes-unlock`: digest-pinned, exact `status`, `unlock`, `revoke` commands for the selected user; no wildcard or general sudo permission.

An ad-hoc development install is possible only when the operator explicitly adds `--allow-ad-hoc-for-lab`. It must still pass the same activation/keychain gates and is not recommended as an unattended deployment.

## 3. Grant the GUI agent Accessibility permission

In System Settings → Privacy & Security → Accessibility, add/enable:

`/Library/Application Support/HermesUnlock/Hermes Unlock Agent.app`

The actual LaunchAgent's identity must be checked. A successful Accessibility query from a terminal-launched development process does not establish the installed agent's permission.

```sh
sudo '/Library/Application Support/HermesUnlock/bin/hermes-unlockctl' status
sudo '/Library/Application Support/HermesUnlock/bin/hermes-unlockctl' health
```

`health` never starts an unlock. It requires the configured user already unlocked and checks both keychains using unique temporary synthetic items, with authentication UI disabled. It does not enumerate existing items. A probe interruption can leave only its own uniquely named `local.hermes.unlock.probe.*` synthetic item; ordinary successful probes remove their item.

Do not proceed unless `health` returns `desktop_ready: true`.

## 4. Begin a rollback-protected activation trial

```sh
sudo python3 tools/installer.py activate --rollback-after 120
```

The original screen rule is copied intact into a reserved fallback right, preserving complex `k-of-n` rules and existing mechanisms. The screen rule becomes an OR between the Hermes right and that complete original right. No global authentication rule is edited.

The rollback watchdog runs every 15 seconds. The trial expires after the configured continuous-time deadline; sleep time counts. A reboot before confirmation causes rollback. Keep the recovery connection open. The default can be extended up to 600 seconds for a deliberate test, never indefinitely.

## 5. Verify a real locked-to-unlocked transition

Lock the target user's screen through the normal macOS UI. From the independent SSH connection, promptly run:

```sh
sudo python3 tools/installer.py verify-unlock
```

This must begin while the screen is locked. It invokes the installed helper and records success only if the screen opens, the exact grant was consumed, the console session did not change, both keychains pass, and an active display exists.

If verified:

```sh
sudo python3 tools/installer.py confirm
```

`confirm` refuses without successful live verification from the current unexpired trial. It also repeats the non-unlocking health preflight. Once confirmed, timed rollback is disabled. This is still only screen/keychain acceptance; finish the headless/CUA/FaceTime tests separately.

If a test fails, roll back immediately or allow the deadline to expire:

```sh
sudo python3 tools/installer.py rollback
```

The installed recovery copy is usable if the source checkout is unavailable:

```sh
sudo /usr/bin/python3 '/Library/Application Support/HermesUnlock/tools/installer.py' rollback
```

Use the system's existing Python 3 if `/usr/bin/python3` is unavailable. The LaunchDaemon itself records the root-owned interpreter used at install time.

## 6. Remove

```sh
sudo python3 tools/installer.py uninstall
```

This restores the original rule before removing dependencies, unloads only Hermes services, removes the installed package and optional sudoers file, and retains root-only state/history for diagnosis. An external edit to the screen rule causes a conflict; the tool does not silently erase that edit or remove a still-needed plugin. Resolve the difference from the stored `original` and live rule before retrying. Reinstallation requires a clean uninstall first.
