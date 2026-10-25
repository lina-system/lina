# LINA Helper on every Mac

Status: design, 2026-10-26. Follows the v0 acceptance on the agent's Mac (`docs/acceptance.md`). Decision (the owner, relayed by the operator): once the agent's Mac keeps its grants across a reboot and an upgrade, install LINA Helper on every Mac so the operator and the other bots use it, together with cua-driver.

## 1. Machines

| Mac | Account | Role | Login after boot | sudo over SSH | Policy profile |
|---|---|---|---|---|---|
| the agent's Mac | `<user>` | LINA's own Mac, agent-operated | (not recorded) | (not recorded) | `operate` (root policy, done in v0) |
| the hub Mac | `<user>` | agent hub: Hermes, signing | (not recorded) | (not recorded) | `observe` until #20 is live, then `operate` only if a task needs it |
| a laptop | `<user>` | the owner's personal laptop | (not recorded) | (not recorded) | `observe` |
| a laptop | `<user>` | the owner's personal laptop | (not recorded) | (not recorded) | `observe` |
| a desktop Mac (not here yet) | `<user>` | the owner's own work | (not recorded) | (not recorded) | `observe` |
| More Macs (later) | per machine | per role | (not recorded) | (not recorded) | decided when they arrive |

Secrets live outside this repository. It stays on `observe` until the embedded cua-driver (#20) replaces the standalone cua-driver 0.21.0 there (otherwise two grant-bearing input paths exist), and then moves to `operate` only when a concrete task needs it (the operator, 2026-10-26).

## 2. Install layout

(not recorded) The installer gets a user mode that needs no root at all. `/Applications` is `root:admin 0775`, so the bundle path stays the same everywhere (the screen-capture approval record is keyed by that path).

| Item | Root mode (the agent's Mac, unchanged) | User mode (every other Mac) |
|---|---|---|
| App | `/Applications/LINA Helper.app`, `root:wheel` | `/Applications/LINA Helper.app`, owned by the user |
| LaunchAgent | `/Library/LaunchAgents/app.lina-os.helper.plist`, bootstrapped into `gui/<uid>` of the configured user | `~/Library/LaunchAgents/app.lina-os.helper.plist`, `gui/<uid>` of the SSH user |
| CLI | `Contents/Helpers/lina-helperctl` (+ `/usr/local/bin` link) | `Contents/Helpers/lina-helperctl` (no link; `/usr/local/bin` is root-owned or missing) |
| Policy | `/Library/Application Support/LINA Helper/policy.json`, root `0644` | none until the owner writes it (§4); built-in `observe` applies |
| State, outbox, socket | `~/Library/Application Support/LINA Helper/` (`0700`) | same |
| Audit log | `~/Library/Logs/LINA Helper/audit-YYYYMMDD.jsonl` (`0600`, 30 days) | same |

The client always calls `/Applications/LINA Helper.app/Contents/Helpers/lina-helperctl` by absolute path, so it does not depend on `/usr/local/bin`.

A user-owned bundle can be replaced by any process running as that user. That does not widen anything: TCC checks the designated requirement (bundle ID + team), so a modified bundle fails its signature and loses the grants, and only the hub Mac can sign for the team.

Deployment from the hub Mac, per Mac: build → `tools/sign.py` → `ditto -c -k --keepParent` → `scp` with `tools/` → `python3 tools/install.py install --mode user` (or `upgrade`) over SSH. Signing stays on the hub Mac. One deploy script loops over the Mac list and stops on the first failure.

## 3. Policy profiles

| Profile | Helper actions | cua-driver tools (§5) | Where |
|---|---|---|---|
| `observe` | `status`, `permissions`, `screenshot`, `ax.tree` | not running (§4) | built in; applies when no root policy exists |
| `operate` | all v0 actions (`ax.press`, `input.click`, `input.keys`, `app.open` added) | running, read and input tools | root policy file only |

- Without a root policy the helper serves `observe` instead of the v0 "status and permissions only". A root policy, when present and safe, replaces the built-in profile completely, so the agent's Mac keeps exactly what it has.
- Input is never on by default on a personal Mac. It needs the root policy, which needs the owner's password on that Mac; no agent can write it.
- An unsafe root policy (wrong owner or mode, bad JSON) falls back to `status` + `permissions` only, not to `observe`, so a broken file never silently changes what is allowed.
- `status` reports the active profile and whether it came from the root policy or the built-in default.

## 4. Turning input on (the owner, per Mac)

On the Mac, in Terminal, one line that uses only system binaries:

```
sudo /bin/mkdir -p "/Library/Application Support/LINA Helper" && echo '{"profile":"operate"}' | sudo /usr/bin/tee "/Library/Application Support/LINA Helper/policy.json"
```

Nothing from the helper bundle runs as root. In user mode the bundle is owned by the user, so any agent running as that user could replace a binary inside it; running such a binary under sudo would hand that agent root. The file comes out root-owned `0644` (group inherited from the directory), which is what the policy loader requires.

Threat model on a personal Mac: agents reach it as `<user>` over SSH. Without the helper they have no Accessibility or Screen Recording grant (TCC charges sshd's chain), so they cannot read the screen or post input. The helper is the only grant holder, so what it serves is the real limit. The helper process itself is hardened-runtime and team-signed, so a same-UID process can neither attach to it nor inject a library. What a same-UID process can do is rewrite any file the user owns, including the bundle, the LaunchAgent plist and anything in the helper's state directory, and connect to any socket the user owns. Consequences, each part of an issue:

- A team-signed helper ignores `LINA_HELPER_DEV` and `LINA_HELPER_POLICY`; only ad-hoc dev builds honour them. In user mode the LaunchAgent plist is user-writable, so an environment switch must not be able to change the policy path. (#19)
- A rewritten plist can make launchd start a different program, but that program does not satisfy the designated requirement and has no grants. A modified bundle fails its signature for the same reason.
- The embedded cua-driver (§5) is a second grant-bearing process. Its tool allowlist is a YAML file it reads at startup, and its socket is reachable by every same-UID process. A same-UID process can race the file between the helper writing it and the child reading it, and can connect before the helper has checked what was loaded. That file handoff cannot be made tamper-proof without root. So the child runs only under `operate`, where input is already allowed and the allowlist is hygiene, not a boundary (an agent with input can reach the same data through the UI). Under `observe` there is no cua-driver child, and reads go through the helper's own `screenshot` and `ax.tree`, which take no file input. (#20)
- The bundled `cua-driver` executable is user-writable. The helper spawns it suspended (`POSIX_SPAWN_START_SUSPENDED`), checks the suspended child's code (`SecCodeCopyGuestWithAttributes` on the child pid, `SecCodeCheckValidity` against `identifier "cua-driver" and anchor apple generic and certificate leaf[subject.OU] = "YCK386LBJ7"` plus the pinned cdhash), and only then resumes it; a mismatch kills it before its first instruction and is logged. The running code is what is checked, so replacing the file between check and exec does not help. Measured on the hub Mac with 0.38.0: an ad-hoc re-signed copy fails the check while suspended (`-67050`) and is killed before it runs; a copy with one byte changed inside the arm64 slice still passes, because the signature identity is unchanged and macOS validates pages only when they are loaded. Page integrity of hardened-runtime code is the kernel's job at page-in; #20 acceptance checks that a modified page in executed code kills the child. (#20)
- cua-driver reads its policy once at startup (`CUA_DRIVER_POLICY_FILE`, YAML; `/dev/fd/N` is refused because the loader wants a `.yaml` extension). The helper still deletes the file once the socket is up and compares the `user policy sha256` from `cua-driver status` with what it wrote, killing the child on a mismatch, to catch accidents. (Checked with 0.38.0 on the hub Mac: a three-tool policy served `get_screen_size` and refused `list_apps` with "not allowed by the YAML policy".)

The policy schema gains a `profile` key: `{"profile":"operate"}` or `{"profile":"observe"}` selects a profile compiled into the helper, with its default limits. The explicit `{"actions":{…}}` form from v0 keeps working (the agent's Mac). Writing `{"profile":"observe"}` turns input off again; removing the file has the same effect. `lina-helperctl policy` (no root) prints the active profile and its source. The helper rereads the policy on every request, so no restart is needed; the cua-driver child is restarted when the profile changes.

## 5. cua-driver inside the helper

Today cua-driver 0.21.0 runs as its own app (`com.trycua.driver`) and holds its own Accessibility and Screen Recording grants on the agent's Mac and the hub Mac. The goal is one grant per Mac.

cua-driver supports this directly: `serve --embedded` keeps the daemon in its spawner's TCC responsibility chain, so a daemon spawned by LINA Helper as a direct child (not through `open`/LaunchServices) checks TCC as LINA Helper and never prompts. Upstream reference: `libs/cua-driver/rust/Skills/cua-driver/EMBEDDING.md`.

- Bundle: the upstream cua-driver 0.38.0 `darwin-arm64` binary (SHA-256 checked against the release `SHA256SUMS`) ships as `Contents/Helpers/cua-driver`, keeping its upstream signature (team `YCK386LBJ7`). License MIT; `LICENSE` and `THIRD_PARTY_NOTICES.md` go to `Contents/Resources/cua-driver/`. The pinned version and hash live in one file in the repo; updating is a PR that changes both.
- Spawn: at start the helper spawns `cua-driver serve --embedded --socket <state>/run/cua.sock --no-permissions-gate --permission-mode standard --host-bundle-id app.lina-os.helper` directly (`posix_spawn`, not `open`) with `CUA_DRIVER_EMBEDDED=1`, `CUA_DRIVER_HOST_BUNDLE_ID=app.lina-os.helper`, `CUA_DRIVER_RS_TELEMETRY_ENABLED=0`, `CUA_DRIVER_POLICY_FILE=<state>/run/cua-policy.yaml` (§4). The socket directory is the helper's `0700` run directory.
- Supervision: the child runs only while the active profile is `operate` (§4). The helper starts it when the profile becomes `operate`, stops it when it stops being `operate`, restarts it if it exits (backoff 1 s → 60 s) and when a grant changes (TCC answers are cached per process), and stops it on exit.
- Tool allowlist (cua-driver YAML policy, deny by default; hygiene, not a boundary, see §4): read tools `get_window_state`, `get_desktop_state`, `get_accessibility_tree`, `list_apps`, `list_windows`, `get_screen_size`, `get_cursor_position`, `zoom`, `check_permissions`, `health_report`, `get_guide`, `start_session`, `end_session`, `get_session`, `list_sessions`, and input tools `click`, `double_click`, `right_click`, `drag`, `scroll`, `move_cursor`, `type_text`, `press_key`, `hotkey`, `set_value`, `invoke_menu`, `launch_app`, `bring_to_front`, `run_steps`, `verify_state`. Browser, clipboard, recording, script, config, update and kill tools stay off until a use needs them.
- Helper actions: `cua.status` (child pid, version, socket path, `check_permissions` result with `source.attribution`), `cua.restart`.
- Callers: `ssh <mac> '/Applications/LINA Helper.app/Contents/Helpers/cua-driver' call --socket <sock> <tool> <json>` for one call, or `... mcp --embedded --socket <sock>` as a stdio MCP proxy. The proxy only forwards; the embedded daemon does every TCC check and enforces the tool allowlist.
- Acceptance: on the agent's Mac (`operate`), `check_permissions` through the helper's socket reports `attribution: host` and both grants true, and `get_window_state` returns a tree and an image without any new TCC row for `com.trycua.driver`. A cua-driver binary with one byte changed is refused before it runs. With the profile switched to `observe`, no child is running and the socket is gone.

The standalone cua-driver 0.21.0 is not updated in place: it is Hermes's pinned computer-use driver, and changing that pin is Hermes administration (the operator). Once Hermes's computer use runs through the helper's socket, the standalone driver's grants can be switched off and the app removed (a separate step, with the operator).

## 6. Hermes side

Issue #8 (Hermes plugin) comes first in v1. The plugin takes a Mac name, never a raw host:

- Tools: `mac_status`, `mac_screenshot`, `mac_ax_tree`, `mac_ax_press`, `mac_click`, `mac_keys`, `mac_cua` (one cua-driver tool call by name with JSON arguments). Every tool has a required `mac` argument.
- The Mac list is operator configuration (a YAML file next to the plugin, edited by the operator): name → SSH host (or `local` for the Mac Hermes runs on) and account. Unknown names are refused before any SSH call.
- The plugin does not decide what is allowed. The helper's profile on each Mac does, and a refusal comes back as `not_allowed` with the profile name.
- Installing the plugin into each bot profile is a Hermes configuration change handed to the operator.

## 7. Audit

Each Mac keeps its own audit log (§2), and the helper's audit lines already carry the caller and the SSH origin. cua-driver calls through the helper's socket are logged by cua-driver itself; the helper logs `cua.*` actions and child restarts. Pulling the logs into one place is a later step.

## 8. Person tasks (collected, sent once through the operator)

After the helper is installed on all reachable Macs:

1. On the hub Mac and the laptops: System Settings → Privacy & Security → Accessibility and Screen & System Audio Recording → `+` → `/Applications/LINA Helper.app` → on. Two switches per Mac. the laptops need the owner at the Mac or Screen Sharing.
2. Later, only if a task needs input on a Mac: the one-line `sudo ... tee` command in §4 on that Mac (one password). Every Mac except the agent's Mac starts on `observe`; the hub Mac waits for #20 (§1).
3. a desktop Mac: when it arrives, same as 1.

## 9. Order

1. Installer user mode, built-in `observe`, profile policy, client bundle path (#19).
2. Embedded cua-driver (#20), accepted on the agent's Mac first.
3. Hermes plugin (#8).
4. Deploy to the hub Mac, a laptop, a laptop; send the person tasks in §8.
