# Clients

## `lina-helper` (agent-hub)

```sh
lina-helper [--host HOST] ACTION [--arg k=v]... [--json OBJECT] [--out PATH]
```

Python 3.9+, standard library only. `HOST` comes from `--host` or `LINA_HELPER_SSH_HOST` and must match `[A-Za-z0-9][A-Za-z0-9_.-]{0,127}`. `ACTION` is one of `status`, `permissions`, `screenshot`, `ax.tree`, `cua.status`, `cua.restart`, `cua.lease`, `cua.release`. Screen input goes through cua-driver (Hermes `computer_use`), not this client.

It runs `ssh -T -o BatchMode=yes -o ConnectTimeout=8 -o ServerAliveInterval=5 -o ServerAliveCountMax=2 HOST '<quoted command>'` where the remote command (built with `shlex.join`) is `'/Applications/LINA Helper.app/Contents/Helpers/lina-helperctl' ACTION [--arg k=v]... [--json OBJECT] [--out -]`. `--arg` values are strings; use `--json '{"depth":2}'` for numbers and booleans. It prints the helper's JSON response and exits 0 only when `ok` is true. No retries. The fixed bundle path works with both root and user installs, including when `/usr/local/bin` is absent.

Local failures (bad host/action/arg, `--out` on a non-screenshot, existing `--out` file) print `{"v":1,"ok":false,"error":{"code":"bad_request","detail":...}}`. Timeouts (30 s, screenshot 45 s) give `transport_timeout`; non-JSON, wrong-shape or >128 MiB responses (the CLI's own response ceiling), or `ok:true` with a non-zero exit, give `invalid_remote_response`. A screenshot destination that cannot be created or written gives `output_unwritable` and leaves no partial file.

### CLI contract for `lina-helperctl` (issues #1, #3)

- `lina-helperctl ACTION [--arg k=v]...` prints exactly one JSON response line on stdout and exits 0 iff `ok:true`.
- `--out -` (screenshot only): the CLI writes the file bytes to **stdout** and the JSON response as the **last line of stderr**. Other stderr lines are free-form diagnostics. On failure stdout is empty and the JSON error is still the last stderr line.
- `lina-helper --out PATH` writes stdout to `PATH` only after an `ok:true` response, creating it exclusively (never overwrites).

`lina-helperctl policy` is a local CLI shortcut: it sends `status` and prints
only `{"policy":{"profile":"observe","source":"builtin"}}` (with the active
values). The SSH wrapper's `status` action includes this object under
`result.policy` along with the normal status fields.

### Tests

`make test` runs `python3 -m unittest discover -s clients/tests` (no network, no Mac needed).
