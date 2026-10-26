# Hermes computer use through LINA Helper

Computer-use provider `lina-helper-cua`: Hermes' upstream cua backend, launched through LINA Helper on the chosen Mac. Design: `docs/fleet.md` §1, §4, §7.

- Driver command: `clients/hermes-cua-driver`. `mcp` execs the bundled cua-driver's MCP proxy against the helper's audit gate on the hub Mac, or over ssh on another Mac. `call`/`status` go to the same gate (Hermes' CLI fallback). `manifest` returns the bundled driver's manifest with the MCP command pointing back at the wrapper.
- Target Mac: per Hermes session, set with the `lina-helper` plugin tool `mac_use` (default `hub`; names from `clients/hermes/macs.yaml`).
- cua mode is always `standard` (all bots run `approvals.mode: off`, which upstream maps to `unrestricted` and a private CuaDriver.app daemon outside the helper).
- Input lease: before input the backend calls `cua.lease` with its session label (renewed every 2 min, released on stop); another bot holding the Mac gets `mac_busy`.

## Install into a profile (the operator)

```sh
ln -s ~/code/lina-helper/clients/hermes            ~/.hermes/profiles/<p>/plugins/lina-helper
ln -s ~/code/lina-helper/clients/hermes-computer-use ~/.hermes/profiles/<p>/plugins/lina-helper-cua
```

`config.yaml`: `plugins.enabled` includes `lina-helper`, and

```yaml
computer_use:
  backend: lina-helper-cua
```

Symlinks are required (each finds its siblings through its real path). No environment variables. Undo: remove the `backend` line.

## Check

New session: `mac_status` (grants, profile operate, cua running, lease), `computer_use` capture + one harmless click, then `mac_use agent-mac` and the same. On each Mac `~/Library/Logs/LINA Helper/audit-YYYYMMDD.jsonl` has `cua.call:get_window_state`, `cua.call:click`; `cua.status` shows `attribution: host`.
