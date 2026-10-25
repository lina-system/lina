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
make app           # macOS: build and ad-hoc sign build/LINA Helper.app
make clean         # remove app and unlock build products
make -C unlock native-check   # unlock package native checks (macOS)
```

The app uses SwiftPM under `app/` and only Apple frameworks. `make test` runs Swift
tests on macOS and the existing unlock tests everywhere. `make swift-test` runs
only the Swift tests. In an enclosing sandbox that prevents SwiftPM from creating
its own sandbox, pass `SWIFT_FLAGS=--disable-sandbox` to make. Tests that require a
named Unix listener explicitly skip if the environment forbids `bind`; socketpair
transport and connection-handler tests still run.

`make app` signs only ad-hoc, with no signing identity or keychain access. It does
not install or launch anything. Developer ID signing and installation are separate
future work.

## Local development

Ad-hoc helpers reject all clients unless **the helper process** has
`LINA_HELPER_DEV=1`. With a team signature, valid peer code must have the same team,
even in development mode. All modes also require the peer UID to be the helper's
UID or root. For an isolated manual run on macOS:

```sh
export LINA_HELPER_HOME="$(mktemp -d /tmp/lina-helper.XXXXXX)"
LINA_HELPER_DEV=1 "build/LINA Helper.app/Contents/MacOS/lina-helper" &
helper_pid=$!
"build/LINA Helper.app/Contents/Helpers/lina-helperctl" status --caller local-test
"build/LINA Helper.app/Contents/Helpers/lina-helperctl" permissions
kill "$helper_pid"
```

The override is the helper's state directory: the socket is `run/helper.sock` and
test audit logs are in `logs/` below it. Without an override, state and logs use
the locations in the design. Logs redact string arguments to UTF-8 byte length
and SHA-256; content arrays/objects use their canonical JSON byte length and hash.
The screen-recording `next_alert` field is an estimate from the first observed
grant, or `null` when unavailable; no privacy databases are read or modified.

The CLI accepts `ACTION [--arg k=v]... [--json ARGS] [--out PATH|-] [--caller NAME]`.
`--arg` values are strings; use `--json` for typed values. Later options replace
earlier keys. Requests include caller, parent PID and `SSH_CLIENT` attribution.
Responses are JSON and the exit status is zero only on success. For an action
returning a file, `--out PATH` copies it without overwriting an existing file;
`--out -` streams bytes to stdout and prints the JSON response to stderr.

The policy schema is `{"actions":{"action.name":{"limit_name":123}}}`. The
default file is `/Library/Application Support/LINA Helper/policy.json`. Only a
regular, root-owned file without group/world write bits is accepted; symlinks,
invalid JSON and unreadable files fail closed to `status` and `permissions`.
`LINA_HELPER_POLICY` overrides that path only when `LINA_HELPER_DEV=1`; ownership
checks still apply. Action implementations receive their policy limits and a
deadline through `ActionContext`. Only `status` and `permissions` are implemented
in this issue; future action files register handlers in `ActionDispatcher`.

## Status

v0 in progress. Milestone "LINA Helper v0" ends when the helper is installed on the agent's Mac, its grants were given once and survive a reboot, and one Hermes request (screenshot or accessibility click) succeeds.
