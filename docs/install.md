# Installing LINA Helper

Build and sign on the hub Mac. Install with root on the agent's Mac, or as the current admin user on another Mac. Design: [design.md](design.md) sections 2, 4 and 11.

`tools/sign.py` and `tools/install.py` use only the Python standard library (`/usr/bin/python3` is enough). The team ID `TEAMID` is not a secret.

## 1. Build and sign (the hub Mac)

```sh
make app                       # builds build/LINA Helper.app (ad-hoc, for local checks)
python3 tools/sign.py --identity 'Developer ID Application: <owner> (TEAMID)' \
    --team TEAMID --app 'build/LINA Helper.app'
```

`sign.py` signs `Contents/Helpers/lina-helperctl` (identifier `app.lina-os.helper.ctl`), then the app (identifier `app.lina-os.helper`), both with `--options runtime --timestamp`, then verifies each with `codesign --verify --strict -R '=<requirement>'` and the app with `--deep`. It refuses an identity that is not `Developer ID Application: … (TEAMID)`, a team that is not 10 characters `[A-Z0-9]`, an identity whose team differs from `--team`, and an app whose `Info.plist` bundle ID is not `app.lina-os.helper`. No provisioning profile in v0.

## 2. Copy to the agent's Mac

```sh
ditto -c -k --keepParent 'build/LINA Helper.app' build/LINA-Helper.zip
scp build/LINA-Helper.zip <user>@<host>:/tmp/lina-helper/
scp -r tools <user>@<host>:/tmp/lina-helper/
ssh <user>@<host> mkdir -p /tmp/lina-helper/app/Resources
scp app/Resources/PolicyProfiles.json <user>@<host>:/tmp/lina-helper/app/Resources/
```

Create `/tmp/lina-helper` on the agent's Mac first if it does not exist (`ssh <user>@<host> mkdir -p /tmp/lina-helper`). Use `ditto`/`scp` of the zip, not `scp -r` of the bundle, so symlinks, resource forks and the signature stay intact.

## 3. Install (the agent's Mac)

```sh
cd /tmp/lina-helper
ditto -x -k LINA-Helper.zip .
sudo /usr/bin/python3 tools/install.py install --user <user> --team TEAMID --app 'LINA Helper.app'
```

`install` (run as root):

1. copies the bundle to a staging directory on the same volume and verifies it: `codesign --verify --strict --deep`, identifier `app.lina-os.helper`, `lina-helperctl` identifier, and the Developer ID designated requirement for `--team`. Ad-hoc bundles are refused unless `--allow-ad-hoc-for-lab` (grants would not survive);
2. renames it to `/Applications/LINA Helper.app`;
3. writes `/Library/Application Support/LINA Helper/policy.json` (root:wheel `0644`, only if absent), the LaunchAgent `/Library/LaunchAgents/app.lina-os.helper.plist`, and the symlink `/usr/local/bin/lina-helperctl`;
4. bootstraps the agent into `gui/<uid of the user>`;
5. records everything it created in `/var/db/lina-helper/state.json` (root `0600`).

Check:

```sh
sudo /usr/bin/python3 tools/install.py status --user <user>
```

`status` prints JSON: installed version, designated requirement, whether the agent is loaded, and the output of `lina-helperctl permissions` run as `<user>`.

## User mode over SSH (no sudo)

Use an SSH alias such as `target-mac` that connects as the admin account which
will run the helper. That same account must already be logged into an Aqua GUI
session. An SSH login alone does not create the `gui/<uid>` launchd domain. The
account needs write access to `/Applications` (normally `root:admin 0775`).

After building and signing on the hub Mac as above:

```sh
ditto -c -k --keepParent 'build/LINA Helper.app' build/LINA-Helper.zip
ssh target-mac mkdir -p /tmp/lina-helper/app/Resources
scp build/LINA-Helper.zip target-mac:/tmp/lina-helper/
scp -r tools target-mac:/tmp/lina-helper/
scp app/Resources/PolicyProfiles.json target-mac:/tmp/lina-helper/app/Resources/
ssh target-mac
cd /tmp/lina-helper
ditto -x -k LINA-Helper.zip .
python3 tools/install.py install --mode user --team TEAMID --app 'LINA Helper.app'
python3 tools/install.py status --mode user
'/Applications/LINA Helper.app/Contents/Helpers/lina-helperctl' policy
```

`--mode` defaults to `root`; use `--mode user` for **every** user-mode command.
User mode refuses effective UID 0. It derives the account and home directory
from the current effective UID; `--user` is optional and must name that same
account if supplied. It uses the same signature, identifier and designated
requirement checks as root mode.

The installed bundle remains `/Applications/LINA Helper.app`, owned by the
invoking user. Staging and rollback bundles live in
`/Applications/.lina-helper-<uid>-staging/` and
`/Applications/.lina-helper-<uid>-previous/`. Keeping both next to the app makes
all bundle swaps same-volume renames even when the home directory is on a
separate volume. Staging is removed after use; the previous bundle stays for
rollback. The receipt is
`~/Library/Application Support/LINA Helper/install/state.json` (`0600`).

The LaunchAgent is `~/Library/LaunchAgents/app.lina-os.helper.plist`, user-owned
`0644`, with the same contents as the root-mode plist. The installer uses
`launchctl bootout/bootstrap gui/<current uid>`, probes permissions by running
the bundled CLI directly as the current user, and creates neither a
`/usr/local/bin` link nor a root policy file. It never changes ownership to root.

For later versions, copy and unzip the signed bundle again, then run on the target:

```sh
python3 tools/install.py upgrade --mode user --app 'LINA Helper.app'
python3 tools/install.py rollback --mode user
python3 tools/install.py status --mode user
# When removing the installation:
python3 tools/install.py remove --mode user
```

Upgrade and rollback compare grants before and after exactly as root mode does;
`--skip-grant-check` is the explicit opt-out. Remove deletes only recorded
installer paths, leaving root policies and helper state, outbox and audit logs
alone. Install mode does not migrate an existing root installation to user mode.

## Policy profiles

When `/Library/Application Support/LINA Helper/policy.json` does not exist, the
helper uses built-in **observe**: `status`, `permissions`, `screenshot`
(`timeout_s: 15`) and `ax.tree` (`max_depth: 8`, `max_nodes: 2000`). Input stays
disabled. A safe root-owned policy can select `{"profile":"observe"}` or
`{"profile":"operate"}`. Operate adds `ax.press`, `input.click`, `input.keys`
and `app.open`, each with an empty limits object. `status` and `permissions` are
always served. The defaults live in `app/Resources/PolicyProfiles.json`, used by
both the installer and the app and copied into the signed bundle. Include this
resource at the repo-relative path when copying `tools/` to a deployment folder.

The v0 `{"actions":{...}}` policy still replaces the defaults and reports the
profile as `custom`. Root mode continues writing the exact v0 operate-equivalent
policy only if none exists. A file with both `profile` and `actions`, an unknown
profile, invalid JSON, non-root ownership, group/other write permission, a
symlink, a non-regular file, or size over 1 MiB is **restricted** to `status` and
`permissions`. Other read failures also restrict access.

`status` includes `"policy":{"profile":"observe","source":"builtin"}`.
Safe root policies report source `root`; unsafe or invalid policies report
`{"profile":"restricted","source":"restricted"}`. The CLI shortcut `policy`
sends a regular `status` request and prints only `{"policy":...}`. Policy files
are reread on every request; no restart is needed. Team-signed helpers ignore
`LINA_HELPER_DEV` and `LINA_HELPER_POLICY` entirely. Only ad-hoc builds honour
these development overrides, and file trust checks still apply.

Enabling operate in user mode requires a root policy written separately by a
person on that Mac, as described in [fleet.md](fleet.md), section 4. Never run
executables from a user-owned helper bundle as root.

## 4. Person step: switch on the permissions (once)

Over Screen Sharing on the agent's Mac, as `<user>`:

System Settings → Privacy & Security → **Accessibility** → `+` → `/Applications/LINA Helper.app` → switch on.
System Settings → Privacy & Security → **Screen & System Audio Recording** → `+` → `/Applications/LINA Helper.app` → switch on.

About 2 minutes. The grants stay through upgrades and reboots as long as the bundle ID and team stay the same. Then `status` should report both grants as true.

## 5. Upgrade

Build, sign, copy and unzip as above, then:

```sh
sudo /usr/bin/python3 tools/install.py upgrade --user <user> --app 'LINA Helper.app'
```

`upgrade` takes the team from the installed bundle. It verifies the candidate, requires its designated requirement (`codesign -d -r-`) to equal the installed bundle's, boots the agent out, moves the installed bundle to `/Library/Application Support/LINA Helper/previous/`, renames the staged candidate into place, bootstraps, and compares `lina-helperctl permissions` before and after. If the grants differ, or either the installed or the new helper does not answer `permissions`, it exits with an error (before any change when the installed one is silent; otherwise keeping the previous bundle for `rollback`). `--skip-grant-check` skips the comparison explicitly. It never overwrites an existing `policy.json`.

## 6. Roll back

```sh
sudo /usr/bin/python3 tools/install.py rollback --user <user>
```

Restores `previous/` the same way (bootout, swap by rename, bootstrap); the bundle that was installed becomes the new `previous/`, so a second `rollback` goes forward again.

## 7. Remove

```sh
sudo /usr/bin/python3 tools/install.py remove --user <user>
```

Boots the agent out and deletes only the paths recorded in `state.json` (and only if they are among the installer's own paths). It does not touch TCC; the Settings entries stay and can be removed by a person. Files it did not create, such as a `policy.json` that existed before, stay.

## Tests

```sh
python3 -m unittest discover -s tools/tests -v
```

They use a fake backend on a temp root and run on Linux too.
