# Lock guard, installer and rollback

The parts that install and remove the lock screen's authorization rule for
[work on a locked Mac](../../../docs/design/non-competition.md#work-on-a-locked-mac),
and the root daemon that can only put that rule back. The plug-in itself is
[`../unlock-plugin`](../unlock-plugin/README.md).

The person's own unlock never depends on a LINA part: a failed install restores
what was there, the guard restores a rule whose plug-in vanished or changed, and
the rollback command is one local request that needs no Node, no Core and no
network.

## Layout

| Path | Built on | Purpose |
| --- | --- | --- |
| `src/rule.c` | Linux and macOS | The rule model: the stock and LINA definitions, equality, a sealed text form. |
| `src/sha256.c` | Linux and macOS | SHA-256 for the backup's seal. |
| `src/state.c` | Linux and macOS | The backup, the pending install check, the enabled marker, the paths. |
| `src/install.c` | Linux and macOS | Install and uninstall, in the order the design fixes. |
| `src/guard.c` | Linux and macOS | One guard pass and the restore, the only rule write outside the install. |
| `src/restore.c` | Linux and macOS | The restore request frame and the rollback command. |
| `src/darwin_host.c` | macOS | Authorization Services, `SecStaticCode`, launchd, the session state, root-only files. |
| `src/guard_main.c` | macOS | The daemon, its socket and `restore`. |
| `src/installer_main.c` | macOS | `lina-locked-screen install\|uninstall\|restore` and the administrator dialog. |
| `src/guard_entry.c` | macOS | The guard binary's entry point. |
| `com.lina.locked-screen.guard.plist` | macOS | The launchd job: at load, on a plug-in folder change, kept alive. |
| `measured-builds.txt` | macOS | The macOS builds the eval passed on, one per line. Empty until the eval runs. |
| `tests/` | Linux and macOS | The fake authorization database and the tests against it. |
| `build.sh` | macOS | Builds, signs and verifies the guard and the installer. |

## Rights and the rule

| Name | Definition |
| --- | --- |
| `com.lina.locked-screen.unlock` | class `evaluate-mechanisms`, mechanism `LinaUnlock:unlock,privileged`, `tries` 1, `shared` false |
| `com.lina.locked-screen.selftest` | the same definition; the install evaluates it once and expects a deny |
| `system.login.screensaver` | class `rule`, `k-of-n` 1, the list `com.lina.locked-screen.unlock`, then `use-login-window-ui` |

The install accepts only the shipped rule (`class` `rule`, the single rule
`use-login-window-ui`) or the rule an earlier LINA install wrote. Any other
definition stops the install with the shape it saw (`platform-sso`,
`smart-card`, `plugin`, `other`) and is not touched.

The self-test right stays installed: the eval and the guard use it to prove that
macOS still loads the plug-in. Uninstall removes it with the unlock right.

## State

Every file is root-owned; the backup and the enabled marker are readable by root
only.

| Path | Content |
| --- | --- |
| `/var/db/lina/locked-screen/backup` | The stock rule it found, the macOS build, and their SHA-256. Written only from a stock rule, kept across a re-install, and never restored when its seal does not match. |
| `/var/db/lina/locked-screen/pending` | The install's manual-unlock check: when it was written, and when the guard saw the lock. |
| `/var/db/lina/locked-screen/enabled` | `socket=`, `uid=`, `deadline_ms=` for the plug-in. Absent means the feature is off. |
| `/var/db/lina/locked-screen/measured-builds` | A copy of `measured-builds.txt` from the release. |
| `/var/db/lina/locked-screen/eval-test-mac` | Present only on the eval's test Mac; it waives the build check and nothing else does. |
| `/var/db/lina/locked-screen/evaluation` | The plug-in's crash marker. The guard removes one older than 5 seconds after the install passes its checks. |
| `/var/db/lina/locked-screen/restore-retry` | The reason of a restore whose rule write failed. The guard retries it on every pass until it succeeds. |
| `/Library/Security/SecurityAgentPlugins/LinaUnlock.bundle` | The plug-in, copied from the release and checked before the rule names it. |
| `/Library/PrivilegedHelperTools/com.lina.locked-screen.guard` | The guard. |
| `/Library/LaunchDaemons/com.lina.locked-screen.guard.plist` | The job that starts it. |
| `/var/run/lina/locked-screen-guard.sock` | The restore request. Root owns it; the `admin` group reads and writes it. |

The backup holds every key of the rule except the database's own `created` and
`modified`; those are written fresh by the database on every write, so the
restore leaves them new while every other key matches the backup.

## Install and uninstall

```sh
# Install: macOS asks for the administrator's password in its own dialog.
sudo ../unlock-plugin/build.sh                     # builds, signs and staples the plug-in
LINA_TEAM_ID=XXXXXXXXXX LINA_SIGN_IDENTITY='Developer ID Application: ...' ./build.sh
sudo build/lina-locked-screen install \
  --bundle ../unlock-plugin/build/LinaUnlock.bundle \
  --guard build/com.lina.locked-screen.guard \
  --plist build/com.lina.locked-screen.guard.plist \
  --measured build/measured-builds.txt \
  --socket "$HOME/Library/Application Support/LINA/broker.sock" --uid "$(id -u)" --deadline 500

# Uninstall. It turns the feature off first, restores the rule while the bundle
# still exists, then removes the right, the bundle and the state.
sudo build/lina-locked-screen uninstall
```

The install runs in this order, and every failure removes what that attempt
wrote and leaves the rule as it found it: Apple silicon; a measured macOS build
(the release's own list decides, so a first install does not need an installed
one); no restore waiting for a retry; the rule shape; the backup; the measured
list; the bundle copy and its signature; the guard job; the two rights; the
self-test deny; the rule write and its read-back; the enabled marker; the
pending record. The verified bundle is copied beside the installed one and
swapped with it in one step (`renamex_np` with `RENAME_SWAP`), so an earlier
install's rule never names a missing bundle.

A failure keeps LINA's right and the bundle when the rule cannot be read or
still names the right, because removing either would leave the lock screen
naming something that is gone. An install over a rule an earlier LINA install
wrote backs up nothing, so a restore falls back to the stock rule; it never
restores a rule that names a right the uninstall is about to remove.

The last step asks the person to lock the Mac and unlock it by hand once. The
guard records the lock, and a manual unlock inside two minutes of it completes
the install. No unlock inside that window restores the backup, so the next
attempt starts from the stock rule. An unlock the guard first sees after the
window, because it was stopped or could not read the session, restores too.

Uninstall leaves a rule someone else wrote alone and reports it. When that rule
still names LINA's right, the right and the bundle stay as well, so the rule
never names a missing plug-in; the feature is off either way.

## Guard

The guard passes over its state at load, on every change of the plug-in folder
and once a second. Each pass first retries a restore that failed, then checks
the pending manual unlock, then restores the backup — or the stock rule when the
backup is unusable or absent — when it finds any of these:

- the bundle is missing, or its signature is invalid or unreadable, while the
  rule names LINA's right; or that right is missing;
- the rule differs from what LINA wrote, while the feature is on;
- the macOS build is not in the measured list, and this is not the eval's test
  Mac.

A restore turns the feature off and removes the pending record. When its rule
write fails it leaves `restore-retry`, because turning the feature off removed
what triggered it; an install waits until that retry succeeds. A pass that finds
nothing to restore while the feature is on removes a crash marker left by an
evaluation that crashed. The guard writes no other rule: every path that writes
a rule writes either the backup's rule or `use-login-window-ui`.

## Rollback and manual recovery

```sh
# The rollback command. Run as root it restores in place; as the person it asks
# the guard over its socket, so it needs no administrator password.
sudo /usr/local/bin/lina-locked-screen restore     # or: com.lina.locked-screen.guard restore

# Manual recovery when the guard itself cannot run. This is the whole restore:
# it needs no LINA part at all.
sudo security authorizationdb write system.login.screensaver use-login-window-ui
```

The manual command is what `use-login-window-ui` means to `loginwindow`: the
next unlock is the normal password or Touch ID unlock. If LINA's bundle is still
present, the guard sees the rule differ from what LINA wrote and turns the
feature off; if the guard is gone too, nothing else of LINA's is in the unlock
path.

`restore` and the guard need no Node, no Core and no network. Validate an
unchanged rule with:

```sh
security authorizationdb read system.login.screensaver
```

## Build and test

```sh
# Linux foundation check: compiles both portable cores and runs their tests,
# including the fake authorization database.
node scripts/verify/check-native.mjs

# macOS: builds, signs and verifies the guard and the installer for arm64.
LINA_TEAM_ID=XXXXXXXXXX LINA_SIGN_IDENTITY='Developer ID Application: ...' \
  native/macos/lock-guard/build.sh
```

`build.sh` uses an ad-hoc signature when `LINA_SIGN_IDENTITY` is unset, which is
enough to compile and verify locally but is not shippable.

The fake authorization database keeps the database's timestamps on every write,
so the tests read back what macOS would store. `verify_code` proves the
signature, LINA's team and the plug-in's identifier with the hardened runtime;
notarization is proven by `build.sh` (`notarytool` and `stapler`) and recorded as
evidence for the release that ships the bundle.

Running the install, uninstall, guard and rollback on a real Mac, and the
`manual recovery` path above, are evidence work under the eval harness issue.
The macOS cells of the [non-competition matrix](../../../docs/design/non-competition.md#matrix)
stay `unverified until evidence`.
