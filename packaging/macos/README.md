# macOS package

The macOS parts of a desktop install: the unlock plug-in, the lock guard, the
installer and the rollback command. The behaviour is specified in
[Work on a locked Mac](../../docs/design/non-competition.md#work-on-a-locked-mac);
this file lists what a release carries and how a person or an administrator
puts it back.

## Contents

| File | Installed as | Made by |
| --- | --- | --- |
| `LinaUnlock.bundle` | `/Library/Security/SecurityAgentPlugins/LinaUnlock.bundle` | [`native/macos/unlock-plugin/build.sh`](../../native/macos/unlock-plugin/build.sh) |
| `com.lina.locked-screen.guard` | `/Library/PrivilegedHelperTools/com.lina.locked-screen.guard` | [`native/macos/lock-guard/build.sh`](../../native/macos/lock-guard/build.sh) |
| `com.lina.locked-screen.guard.plist` | `/Library/LaunchDaemons/com.lina.locked-screen.guard.plist` | the same build |
| `lina-locked-screen` | the Node's artifact, run from its own path | the same build |
| `measured-builds.txt` | `/var/db/lina/locked-screen/measured-builds` | the eval, one build per line |

All three executables are arm64, signed with LINA's Developer ID team and the
hardened runtime, and notarized with a stapled ticket. A release that cannot
show that evidence does not ship them: locked-Mac work stays off.

## Install and remove

The Node runs the installer with the plug-in, the guard, its job and the
measured list from its own artifact:

```sh
sudo lina-locked-screen install --bundle <bundle> --guard <guard> --plist <plist> \
  --measured <measured list> --socket <broker socket> --uid <person's uid> --deadline <ms>
sudo lina-locked-screen uninstall
```

The administrator's password is entered in macOS's own dialog; LINA never sees
it. Install and uninstall order, the backup, the self-test and the manual lock
and unlock are described in
[the lock guard's documentation](../../native/macos/lock-guard/README.md#install-and-uninstall).

## Rollback

```sh
sudo lina-locked-screen restore
```

As the person's own account it asks the guard and needs no administrator
password; as root it restores in place. It needs no Node, no Core and no
network, so it works over SSH and from another administrator account while the
Mac is locked.

## Manual recovery

When the guard itself cannot run, restore the rule by hand. This writes the
rule macOS ships and needs no LINA part at all:

```sh
sudo security authorizationdb write system.login.screensaver use-login-window-ui
```

The next unlock is then the normal password or Touch ID unlock. If the plug-in
is still installed, the guard sees that the rule differs from what LINA wrote
and turns the feature off. Optionally check the rule afterwards:

```sh
security authorizationdb read system.login.screensaver
```

Removing the rest by hand, when the installer cannot run:

```sh
sudo launchctl bootout system/com.lina.locked-screen.guard
sudo rm -f /Library/LaunchDaemons/com.lina.locked-screen.guard.plist
sudo rm -f /Library/PrivilegedHelperTools/com.lina.locked-screen.guard
sudo rm -rf /Library/Security/SecurityAgentPlugins/LinaUnlock.bundle
sudo security authorizationdb remove com.lina.locked-screen.unlock
sudo security authorizationdb remove com.lina.locked-screen.selftest
sudo rm -rf /var/db/lina/locked-screen
```

Remove the rule before the bundle, never the other way round: while the rule
names the plug-in, the unlock path needs it.
