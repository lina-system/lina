# Clients

## `lina-helper` (the hub Mac)

```sh
lina-helper [--host HOST] ACTION [--arg k=v]... [--out PATH]
```

Python 3.9+, standard library only. `HOST` comes from `--host` or `LINA_HELPER_SSH_HOST` and must match `[A-Za-z0-9][A-Za-z0-9_.-]{0,127}`. `ACTION` is one of `status`, `permissions`, `screenshot`, `ax.tree`, `ax.press`, `input.click`, `input.keys`, `app.open`.

It runs `ssh -T -o BatchMode=yes -o ConnectTimeout=8 -o ServerAliveInterval=5 -o ServerAliveCountMax=2 HOST '<quoted command>'` where the remote command (built with `shlex.join`) is `/usr/local/bin/lina-helperctl ACTION [--arg k=v]... [--out -]`. It prints the helper's JSON response and exits 0 only when `ok` is true. No retries.

Local failures (bad host/action/arg, `--out` on a non-screenshot, existing `--out` file) print `{"v":1,"ok":false,"error":{"code":"bad_request","detail":...}}`. Timeouts (30 s, screenshot 45 s) give `transport_timeout`; non-JSON, wrong-shape or >1 MiB responses, or `ok:true` with a non-zero exit, give `invalid_remote_response`. A screenshot destination that cannot be created or written gives `output_unwritable` and leaves no partial file.

### CLI contract for `lina-helperctl` (issues #1, #3)

- `lina-helperctl ACTION [--arg k=v]...` prints exactly one JSON response line on stdout and exits 0 iff `ok:true`.
- `--out -` (screenshot only): the CLI writes the file bytes to **stdout** and the JSON response as the **last line of stderr**. Other stderr lines are free-form diagnostics. On failure stdout is empty and the JSON error is still the last stderr line.
- `lina-helper --out PATH` writes stdout to `PATH` only after an `ok:true` response, creating it exclusively (never overwrites).

### Tests

`make test` runs `python3 -m unittest discover -s clients/tests` (no network, no Mac needed).
