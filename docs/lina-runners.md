# LINA runners

`lina runners` installs, pins and reports every runner a node offers besides omo
(lina#222). The pins live in `pm/lina-deps.json` under `runners`, next to the
Node, OCX and omo pins.

| Runner | Installer | Integrity | Sign-in (owner, 11-06) |
|---|---|---|---|
| `codex` (`@openai/codex`) | npm on the pinned Node | npm `dist.integrity` | through OCX |
| `claude` (`@anthropic-ai/claude-code`) | npm on the pinned Node | npm `dist.integrity` | through OCX |
| `claude-agent-acp`, `codex-acp` (`@zed-industries/*`, for #221) | npm on the pinned Node | npm `dist.integrity` | through OCX |
| `dsh` (`@deepseek-ai/dsh`) | npm on the pinned Node | npm `dist.integrity` | no read-only check known (`unknown`) |
| `grok` | the binary `https://x.ai/cli/install.sh` downloads | sha256 recorded by LINA (the source publishes none) | `grok login` (`~/.grok/auth.json` exists) |
| `cursor-agent` | the tarball `https://cursor.com/install` downloads | sha256 recorded by LINA (the source publishes none) | `CURSOR_API_KEY` |
| `agy` | the tarball `https://antigravity.google/cli/install.sh` reads from its release manifest | the manifest's sha512 | `agy` (`~/.gemini/antigravity-cli/antigravity-oauth-token` exists) |

## Layout

- `vendor/runners/<name>/<version>/`: one installed version. npm runners are
  installed with `lina_ocx.npm_install` (the lock must carry the pinned
  integrity before any package code runs); the others are downloaded, checked
  against the pinned hash for this platform and only then kept.
- `vendor/runners/<name>/current`: the active version; `previous` records the one
  before the last upgrade.
- `bin/runners/<name>`: the shim LINA runs it through. It puts the pinned Node
  first on `PATH` for npm runners and sets the pin's `env`.

Nothing is installed globally. A runner the person installed (`~/.local/bin`, a
global npm) is only read: `status` shows its path and `--version` as
`person_installed`.

## Commands

- `lina runners install [name]`: the pinned version of one runner, or of all.
- `lina runners upgrade <name> [version] [--integrity sha512-...]`: installs
  beside the current one, trials it (its `--version` must report the version;
  the ACP adapters, which start a server instead, are checked by their installed
  `package.json`), switches `current`, and switches back when the switched shim
  does not answer with the new version. Only npm runners accept a version other
  than the pin. Moving the pin across nodes is #238.
- `lina runners rollback <name>`: back to the version before the last upgrade.
- `lina runners status [--json] [--offered]`: per runner `pinned`, `installed`,
  `versions`, `person_installed`, `sign_in` (`signed_in`, `signed_out`,
  `api_key`, `ocx`, or `unknown` with `sign_in_why`), `ready` and one `why`
  (`prepare`, `update`, `sign in`). `--offered` reports only the runners this
  node offers in `worker.runners`, matched by name or by the command in `argv`;
  this is what `hello` will carry.

Each `--version` the status asks is bounded by 10 s, and the rows run in
parallel. Credential files are only checked for existence, never opened.

## Automatic updates

Off where the runner documents a switch: the Claude Code shim sets
`DISABLE_AUTOUPDATER=1` (code.claude.com/docs/en/env-vars). Codex, Grok, dsh,
cursor-agent and agy have no switch LINA could confirm from their help or docs;
`status` shows `update` when a runner moved itself off its pin.

## Devices

`lina devices` shows each device's runners from `lina runners status --offered`
on that device (in-process for a local one). A device ssh cannot reach lists the
runners the hub offers, each `unreachable`; a device without `lina runners` yet,
each `update`.

`lina devices sign-in [--json]` gathers the runners that are `signed_out` on some
nodes into one question per runner, naming the nodes and how to sign in on each.
Writing it into the inbox and the hub's sign-in button are #230 and #234.

`lina devices service <name> <ocx|worker> <status|restart>`: the worker goes
through #218's `NegotiatedTransport.service` with the instance its `hello`
reports; OCX goes through `lina_runners.node_service`, which calls the service
layer (`lina_services.act`, its refusals included: OCX is not restarted while
the worker or gateway runs on it).

## Not wired yet

`hermes_cli/worker/protocol.py` is unchanged in this round (#220 edits it). The
next round replaces the runner list in `hello()`'s `capabilities.runners` with
`lina_runners.node_report(worker_root)` and lets the `service` verb carry
`ocx|worker` to `lina_runners.node_service`; until then OCX of a remote device
is refused, and the runner `argv` in `docs/lina-worker.example.yaml` still names
the bare commands instead of `$LINA_HOME/bin/runners/<name>`.
