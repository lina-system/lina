# Installing LINA Helper

Build and sign on the hub Mac, install on the agent's Mac. Design: [design.md](design.md) sections 2, 4 and 11.

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
