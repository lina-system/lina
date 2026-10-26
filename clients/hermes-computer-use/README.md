# Hermes computer use through LINA Helper

Two pieces route Hermes's built-in `computer_use` tool through LINA Helper, so it runs with the helper's grants, under the helper's per-Mac policy, and every call leaves one line in that Mac's audit log (`~/Library/Logs/LINA Helper/`).

- `clients/hermes-cua-driver`: the driver command Hermes runs. It answers `manifest` from the bundled cua-driver and runs `mcp` as the bundled driver's stdio proxy against the helper's audit gate (`run/cua.sock`). `LINA_HELPER_CUA_HOST=<ssh alias>` runs the proxy on another Mac over ssh (testing; the gate then sees it as a child of sshd there).
- `clients/hermes-computer-use/` (this directory): computer-use provider `lina-helper-cua`. It is the built-in cua backend with the driver command set to the wrapper and the cua permission mode fixed to `standard`.

Why the provider is needed: every bot profile runs with `approvals.mode: off`. Hermes maps any approval bypass to cua's `unrestricted` mode, and for that mode the built-in backend starts its own private daemon through `CuaDriver.app` (`tools/computer_use/cua_backend_daemon.py`), outside the helper. With the provider, the bypass no longer decides where calls go; the helper's policy profile on the Mac is the boundary.

## Install into a profile (the operator)

```sh
ln -s ~/code/lina-helper/clients/hermes-computer-use ~/.hermes/profiles/<profile>/plugins/lina-helper-cua
```

and in that profile's `config.yaml`:

```yaml
computer_use:
  backend: lina-helper-cua
```

A symlink is required: the provider finds `clients/hermes-cua-driver` next to its real path. No environment variable is needed on the hub Mac. `HERMES_CUA_DRIVER_CMD`, if set, wins over the wrapper. Hermes reads `computer_use.backend` per call, so a new session picks it up; the gateway does not need a restart for a config value read per call, but follow your usual rule.

Undo: remove the `computer_use.backend` line (falls back to the built-in `cua` and the standalone CuaDriver.app).

## Check

In a new session of that profile: run computer_use once (capture an app, one harmless click). Then on that Mac:

```sh
'/Applications/LINA Helper.app/Contents/Helpers/lina-helperctl' cua.status   # check_permissions.source.attribution == host
tail ~/Library/Logs/LINA\ Helper/audit-$(date +%Y%m%d).jsonl                  # cua.call:get_window_state, cua.call:click, …
```

and no new `com.trycua.driver` row in the TCC database.

## Known gaps (0.21-era Hermes on cua-driver 0.38)

- The gate adds `tree_format: both` to `get_window_state` and drops `element_index` when `element_token` is present; without these, element clicks fail on 0.38.
- 0.38 reports capture coordinates for the full desktop while `click` wants window-local points; the model may need a second try (seen once in testing). Fixing this belongs in Hermes's cua backend.
