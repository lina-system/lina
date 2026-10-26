# LINA Helper on every Mac

Status: design, 2026-10-26. Follows the v0 acceptance on the agent's Mac (`docs/acceptance.md`).

Decisions (the owner, relayed by the operator, 2026-10-26):

- LINA Helper goes on every Mac, and the operator and the bots use it.
- Every Mac uses the same setting: input allowed (`operate`) and a per-Mac audit log.
- Screen control is **one path: cua-driver**. Hermes `computer_use` is a cua-driver MCP client. LINA Helper is the macOS-permission shell that cua runs inside, plus helper-only actions. There is no second click path.

## 1. Layers

1. **Permissions (TCC).** On each Mac only `/Applications/LINA Helper.app` holds Accessibility and Screen & System Audio Recording (later Full Disk Access and Automation). Fixed bundle ID `app.lina-os.helper`, Developer ID team `TEAMID`: rebuilds and upgrades keep the grants.
2. **cua daemon.** The helper runs the bundled cua-driver 0.38.0 as an embedded child (one per Mac user) while the profile is `operate`. The child inherits the helper's grants (TCC responsibility chain); it holds none of its own. Every screen read, click and keystroke from an agent is done by this daemon.
3. **Audit gate.** Agents never reach the daemon directly. The public socket `~/Library/Application Support/LINA Helper/run/cua.sock` is the helper's gate: one audit line per request, then the request is forwarded to the private `run/cua-daemon.sock`. The gate also enforces the input lease (§4).
4. **Hermes.** `computer_use` with backend `lina-helper-cua` (upstream `CuaDriverBackend`, only the launch changed) plus the plugin `lina-helper` (`mac_use`, `mac_status`, `mac_screenshot`). Hermes core is not modified.

## 2. Machines

| Mac | Account | Role | Login after boot | sudo over SSH | Profile | Screen for capture |
|---|---|---|---|---|---|---|
| the agent's Mac | `<user>` | LINA's own Mac | (not recorded) | (not recorded) | `operate` | virtual display (no monitor; stayed up across a reboot) |
| the hub Mac | `<user>` | agent hub: Hermes, signing | (not recorded) | (not recorded) | `operate` | external monitor, main |
| a laptop | `<user>` | the owner's laptop | (not recorded) | (not recorded) | `operate` | built-in; not checked yet (offline) |
| a laptop | `<user>` | the owner's laptop | (not recorded) | (not recorded) | `operate` | built-in |
| a desktop Mac (not here yet) | `<user>` | the owner's own work | (not recorded) | (not recorded) | `operate` | decide on arrival |

computer_use needs a shareable display. The hub Mac and the laptops have one while they are awake; the agent's Mac's virtual display has stayed up across a reboot. When a laptop's lid is closed or it sleeps, capture fails and computer_use says so. No HDMI dummy is needed for the current Macs. If capture ever reports "no shareable display" on the hub Mac (its display sleeps after 10 minutes), re-check before buying one.

Secrets live outside this repository. The per-Mac audit log (§6) records it; irreversible actions are governed by each bot's SOUL rules.

## 3. Install layout

(not recorded) The installer has a user mode with no root at all. `/Applications` is `root:admin 0775`, so the bundle path is the same everywhere.

| Item | Root mode (the agent's Mac) | User mode (every other Mac) |
|---|---|---|
| App | `/Applications/LINA Helper.app`, `root:wheel` | `/Applications/LINA Helper.app`, owned by the user |
| LaunchAgent | `/Library/LaunchAgents/app.lina-os.helper.plist`, `gui/<uid>` of the configured user | `~/Library/LaunchAgents/app.lina-os.helper.plist`, `gui/<uid>` of the SSH user |
| CLI | `Contents/Helpers/lina-helperctl` (+ `/usr/local/bin` link) | `Contents/Helpers/lina-helperctl` |
| Policy | `/Library/Application Support/LINA Helper/policy.json`, root `0644` | same file, written once by the owner (§5) |
| State, sockets, outbox | `~/Library/Application Support/LINA Helper/` (`0700`) | same |
| Audit log | `~/Library/Logs/LINA Helper/audit-YYYYMMDD.jsonl` (`0600`, 30 days) | same |

Deployment from the hub Mac: build → `tools/sign.py` → `ditto -c -k --keepParent` → `scp` with the whole `tools/` → `python3 tools/install.py install|upgrade [--mode user]` over SSH. Signing stays on the hub Mac.

A user-owned bundle can be replaced by any process running as that user. That does not widen anything: TCC checks the designated requirement (bundle ID + team), so a modified bundle loses the grants; the bundled cua-driver is checked by code identity before it runs.

## 4. cua daemon, gate and lease

- **Bundle**: upstream cua-driver 0.38.0 `darwin-arm64`; tarball SHA-256 and arm64 cdhash pinned in `app/cua-driver.pin`; upstream signature kept (team `YCK386LBJ7`).
- **Spawn**: `cua-driver serve --embedded --socket <state>/run/cua-daemon.sock --no-permissions-gate --permission-mode standard --host-bundle-id app.lina-os.helper` via `posix_spawn` with `POSIX_SPAWN_START_SUSPENDED`; the suspended child's code is checked against the pinned requirement and cdhash before it is resumed. `CUA_DRIVER_EMBEDDED=1`, telemetry off.
- **Tool list**: under `operate` every cua tool is allowed (the owner: free use). The boundary is the profile (`observe` runs no daemon at all) plus the audit log.
- **Supervision**: runs only while the profile is `operate`; restarted on exit (backoff 1 s → 60 s) and when a grant changes; stopped when the profile changes or the helper exits.
- **Gate** (`run/cua.sock`): peers must be the same user and be the bundled cua-driver (the MCP proxy) or team-signed code. One audit line per request (`cua.call:<tool>`; arguments redacted, strings as length + SHA-256 so typed text never appears; peer pid, executable, parent process), written before forwarding; an audit failure blocks the request. Compatibility for the 0.21-era Hermes backend: `get_window_state` without `tree_format` gets `"both"`, and `element_index` is dropped when `element_token` is present.
- **Input lease**, so two bots never drive one Mac at once:
  - `cua.lease {holder, ttl_s ≤ 300}` grants or extends; another unexpired holder → `busy` "<holder> is using this Mac (Ns left)". `cua.release {holder}`. Kept in helper memory; a helper restart clears it.
  - Input tools (click, type, keys, drag, scroll, set_value, menus, launch/bring to front, scripts, browser actions, …) are refused while someone else holds the lease; reads (window state, screenshots, lists) never need it.
  - The gate enforces it: an input request whose `args.session` (Hermes' per-run label) is not the holder is refused without forwarding and audited. A call that carries no `session` (Hermes sends none for `bring_to_front`) inherits the label last seen from the same MCP proxy process; Hermes' CLI fallback (a new process per call) gets the label added by the wrapper from `LINA_HELPER_CUA_HOLDER`, which the backend sets.
  - The Hermes backend takes the lease with its session label before its first input and renews it every 2 minutes while acting; `stop` releases it.

## 5. Policy and person steps

`policy.json` (root-owned, written only through sudo): `{"profile":"operate"}` or `{"profile":"observe"}`; the v0 explicit `{"actions":{...}}` form still loads. No file → built-in `observe` (status, permissions, screenshot, ax.tree; no daemon). Unsafe or invalid file → status + permissions only. A team-signed helper ignores `LINA_HELPER_DEV`/`LINA_HELPER_POLICY`.

On each Mac, once (the owner): `sudo sh ~/lina-helper-operate.sh` (writes `{"profile": "operate"}` with system binaries only, opens the Accessibility pane), then switch on LINA Helper under Accessibility and Screen & System Audio Recording. The operator places the script; the operator collects the request.

Helper actions under `operate`: `status`, `permissions`, `screenshot`, `ax.tree`, `cua.status`, `cua.restart`, `cua.lease`, `cua.release`. The helper's own click/keys/press/app-open actions are removed: input is cua only. Helper-only actions planned: `file.read` and `events.send` (#7), `unlock.*` (#6).

## 6. Audit

Each Mac keeps its own log (§3). Helper actions carry the caller and the SSH origin; cua calls carry `cua.call:<tool>` and the calling process; refusals (policy, peer, lease) are logged too; daemon restarts are `cua.lifecycle`. Every input action leaves exactly one line. Collecting the logs in one place is a later step.

## 7. Hermes side

- **computer_use backend** `lina-helper-cua` (`clients/hermes-computer-use/`): subclass of upstream `CuaDriverBackend`. It replaces only how the cua MCP session is launched (`clients/hermes-cua-driver mcp`, which execs `<Helper>/cua-driver mcp --embedded --socket <gate>` on the hub Mac, or `ssh <alias> '<Helper>/cua-driver mcp --embedded --socket "$HOME/…/cua.sock"'` for another Mac), fixes the cua mode to `standard`, and takes the lease before input. Verification ladder, capture and input logic stay upstream.
  - Why `standard` is forced: every bot runs `approvals.mode: off`, which Hermes maps to cua `unrestricted`, and for that the upstream backend starts its own daemon through CuaDriver.app outside the helper.
- **Target Mac**: plugin tool `mac_use(mac)` sets the computer_use target for the current Hermes session (default `hub`) and restarts that session's cua connection. Unknown names are refused (`clients/hermes/macs.yaml`, operator config).
- **Plugin `lina-helper`** (`clients/hermes/`): `mac_use`, `mac_status` (grants, profile, cua state, lease holder, current target), `mac_screenshot` (quick check without a computer_use session). `mac_click`, `mac_keys`, `mac_ax_press`, `mac_ax_tree` and `mac_cua` are removed; cua covers them. Helper-only actions get tools when they exist.
- **Profile settings** (the operator): symlink `clients/hermes` → `<profile>/plugins/lina-helper` and `clients/hermes-computer-use` → `<profile>/plugins/lina-helper-cua`; `plugins.enabled` includes `lina-helper`; `computer_use.backend: lina-helper-cua`. Same values in all eight profiles. (The directory name is the backend name; `lina-helper` is taken by the tools plugin.)

Tested 2026-10-26 on the agent's Mac from the hub Mac: Hermes' own backend through the wrapper — list_apps, Finder capture (76 elements with element tokens and an image), background click, TextEdit New Document pressed by element, 15 characters typed and read back; a model session with `approvals.mode: off` captured and clicked; the gate logged one line per call; cua-driver reported `attribution: host`; no new `com.trycua.driver` TCC row.

## 8. Retiring the standalone cua-driver 0.21

After the new backend works in all eight profiles (one computer_use run on the hub Mac and one on the agent's Mac per profile): the operator removes the `mac_desktop` MCP entry and the 0.21.0 app; the owner deletes the two `com.trycua.driver` rows (Accessibility, Screen Recording) on the hub Mac and the agent's Mac.

## 9. Known gaps

- Hermes' cua backend is written for cua-driver 0.21. Besides the two differences the gate bridges (§4), capture describes full-desktop coordinates while `click` takes window-local points; a model may need a second try. Fixing it belongs in Hermes.
- The lease lives in helper memory; a helper restart drops it.
