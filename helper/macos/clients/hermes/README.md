# LINA Helper Hermes plugin

Standalone Hermes plugin `lina-helper`. Screen reading and input are Hermes `computer_use` (backend `lina-helper-cua`, `clients/hermes-computer-use/`). This plugin chooses which Mac that is and offers quick checks, **by Mac name**; the model never sees or supplies a host.

| Tool | What it does | Arguments |
|---|---|---|
| `mac_use` | Point this session's `computer_use` at a Mac (default `agent-hub`); restarts the session's cua connection | `mac` (required) |
| `mac_status` | Helper `status` + `cua.status`: grants, profile, cua daemon, input lease holder, this session's target | `mac` (default: current target) |
| `mac_screenshot` | Helper `screenshot` without a computer_use session → PNG saved locally; returns `path`, `width`, `height`, `bytes` | `mac`, `display` (`"main"` or id) |

Results are JSON strings. Success: `{"ok":true,"mac":NAME,...}`. Failure, never an exception: `{"ok":false,"error":{"code":...,"detail":...},"mac":NAME}`. Codes come from the helper unchanged (`not_allowed`, `permission_missing`, `busy`, …) plus plugin-side `unknown_mac`, `ssh_failed`, `exec_failed`, `invalid_remote_response`, `output_unwritable`, `client_missing`, `internal`. The plugin does not decide what is allowed; the helper's policy on each Mac does.

The target per session is kept in process memory (`core.targets`), shared with the computer-use backend in the same Hermes process. `core.helper_action(mac, action, args)` is the backend's way to call `cua.lease`/`cua.release`.

## Config: `macs.yaml`

Next to the plugin (`clients/hermes/macs.yaml`, gitignored), or any path in `LINA_HELPER_MACS_FILE`. Copy `macs.example.yaml`:

```yaml
macs:
  agent-hub:
    host: local        # the Mac Hermes runs on: run lina-helperctl directly, no ssh
  agent-mac:
    host: agent-mac     # ssh alias
```

- A name not in the file is refused before any subprocess runs. Names match `[A-Za-z0-9][A-Za-z0-9_.-]{0,63}`; hosts that are not `local` must be a plain SSH alias/hostname.
- Remote calls: `ssh -T -o BatchMode=yes -o ConnectTimeout=10 -o ServerAliveInterval=5 -o ServerAliveCountMax=2 HOST '<shlex.join argv>'`, built by `clients/lina-helper`'s `command()`; `lina-helperctl` is called at its bundle path, `/Applications/LINA Helper.app/Contents/Helpers/lina-helperctl`, there and on `local`.
- Format: YAML if `ruamel.yaml` (Hermes ships it, see `hermes_yaml.py`) or PyYAML is importable, otherwise the same file is read as JSON. Plain system `python3` has neither, so tests use JSON.
- The file is read at registration (to list the names as the `mac` enum) and again on every call. If it is missing the plugin still registers and only `agent-hub` (local) is known.
- Screenshots go to `LINA_HELPER_CACHE_DIR`, else `<Hermes home>/cache/lina-helper/` (profile-relative via `hermes_constants.get_hermes_home()`), mode `0700`, one new file per call. They are not pruned by the plugin.

## Install into a profile (the operator)

The plugin imports `clients/lina-helper` (no copy), so install it as a **symlink** to this directory, not a copy:

```sh
ln -s "$PWD/clients/hermes" ~/.hermes/profiles/<profile>/plugins/lina-helper
cp clients/hermes/macs.example.yaml clients/hermes/macs.yaml   # then edit
hermes -p <profile> plugins enable lina-helper
```

Then restart that profile's gateway/session. Hermes scans `<HERMES_HOME>/plugins/<name>/` (`hermes_cli/plugins_discovery.py:209`), a user plugin is loaded only when listed in `plugins.enabled` (`plugins_discovery.py:299-302`, message "run `hermes plugins enable <name>`"), and `-p/--profile` selects the profile (`hermes_cli/main.py:500`). To use a copied directory instead, also set `LINA_HELPER_CLIENT` to the path of `clients/lina-helper`.

Registration contract used (`hermes_cli/plugins_loader.py:454-515`, `hermes_cli/plugins.py:456`): `__init__.py` exposes `register(ctx)`, which calls `ctx.register_tool(name, toolset, schema, handler)`; `schema` is `{name, description, parameters}` (JSON Schema); the handler is `handler(args, **ctx_kwargs)` and returns a string (`tools/registry.py:893-909`).

## Tests

```sh
python3 -m unittest discover -s clients/hermes/tests -t clients/hermes/tests -v
```

A fake runner replaces `subprocess.run`; nothing touches ssh, a Mac or `~/.hermes`. `make test` runs them.
