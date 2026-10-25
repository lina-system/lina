# LINA Helper

One signed macOS app that holds the privacy permissions (Accessibility, Screen Recording, later Full Disk Access and Automation) on an agent Mac. Hermes tools, scripts and later LINA ask it to do a bounded action (take a screenshot, read the accessibility tree, press an element, type keys) over a local socket instead of each tool getting its own grant.

- Bundle ID `app.lina-os.helper`, installed at `/Applications/LINA Helper.app`, signed by the team Developer ID. Same bundle ID and same team signature → grants survive updates and reboots.
- Requests: newline-delimited JSON over a Unix socket in the user's Application Support folder; peer UID and code signature checked; actions limited by a root-owned policy file; every request in an audit log.
- From the hub Mac, Hermes calls `clients/lina-helper` which runs `lina-helperctl` over SSH.
- No MDM, no generic command execution, no edits to the TCC database.

Full design: [docs/design.md](docs/design.md).

## Layout

| Path | What |
|---|---|
| `docs/design.md` | design: identity, permissions, API, audit, updates, unlock absorption, client |
| `app/` | LINA Helper app and `lina-helperctl` (Swift) — v0 |
| `clients/` | Hermes-side client script, later the Hermes plugin |
| `unlock/` | imported Hermes Unlock package (`thisisjun786/hermes-macos-unlock@e5a3204`): AuthorizationPlugin, root helper, GUI agent, installer. Absorbed into the app in v1 |

## Build and test

```sh
make test          # everything that runs without a Mac GUI session (also on Linux for unlock/)
make -C unlock native-check   # unlock package native checks (macOS)
```

App build, signing and install targets arrive with the v0 issues. Signing runs only on the hub Mac with the team identity in its login keychain; CI never signs.

## Status

v0 in progress. Milestone "LINA Helper v0" ends when the helper is installed on the agent's Mac, its grants were given once and survive a reboot, and one Hermes request (screenshot or accessibility click) succeeds.
