# LINA Helper Hermes plugin

Standalone Hermes plugin `lina-helper`. It lets an agent act on a Mac running LINA Helper **by Mac name**. The model never sees or supplies a host.

| Tool | Helper action | Arguments (besides required `mac`) |
|---|---|---|
| `mac_status` | `status` | – |
| `mac_screenshot` | `screenshot` | `display` (`"main"` or id) → PNG saved locally; returns `path`, `width`, `height`, `bytes` |
| `mac_ax_tree` | `ax.tree` | `app` (default `frontmost`), `depth` ≤ 8, `max_nodes` ≤ 2000 |
| `mac_ax_press` | `ax.press` | `app`, `path`, `expect_role` (optional) |
| `mac_click` | `input.click` | `x`, `y`, `button` |
| `mac_keys` | `input.keys` | `text`, or `key` + `modifiers` |
| `mac_cua` | cua-driver | `tool` (`^[a-z_]{1,64}$`), `args` (object) |

`mac_cua` runs `/Applications/LINA Helper.app/Contents/Helpers/cua-driver call --socket "$HOME/Library/Application Support/LINA Helper/run/cua.sock" <tool> <json>` on the target (the socket exists once the helper embeds cua-driver, issue #20).

Results are JSON strings. Success: `{"ok":true,"mac":NAME,"result":{...}}` (screenshot: `path`, `bytes`, `width`, `height`). Failure, never an exception: `{"ok":false,"error":{"code":...,"detail":...},"mac":NAME}`. Codes come from the helper unchanged (`not_allowed`, `permission_missing`, `bad_request`, `busy`, `target_not_found`, `timeout`, …), plus plugin-side `unknown_mac`, `not_configured`, `ssh_failed`, `exec_failed`, `invalid_remote_response`, `output_unwritable`, `cua_failed`, `client_missing`, `internal`. The plugin does not decide what is allowed; the helper's policy on each Mac does.

## Config: `macs.yaml`

Next to the plugin (`clients/hermes/macs.yaml`, gitignored), or any path in `LINA_HELPER_MACS_FILE`. Copy `macs.example.yaml`:

```yaml
macs:
  the hub Mac:
    host: local        # the Mac Hermes runs on: run lina-helperctl directly, no ssh
  the agent's Mac:
    host: <host>     # ssh alias
```

- A name not in the file is refused before any subprocess runs. Names match `[A-Za-z0-9][A-Za-z0-9_.-]{0,63}`; hosts that are not `local` must be a plain SSH alias/hostname.
- Remote calls: `ssh -T -o BatchMode=yes -o ConnectTimeout=10 -o ServerAliveInterval=5 -o ServerAliveCountMax=2 HOST '<shlex.join argv>'`, built by `clients/lina-helper`'s `command()`; `lina-helperctl` is called at its bundle path, `/Applications/LINA Helper.app/Contents/Helpers/lina-helperctl`, there and on `local`.
- Format: YAML if `ruamel.yaml` (Hermes ships it, see `hermes_yaml.py`) or PyYAML is importable, otherwise the same file is read as JSON. Plain system `python3` has neither, so tests use JSON.
- The file is read at registration (to list the names as the `mac` enum) and again on every call. If it is missing the plugin still registers; calls return `not_configured`.
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
