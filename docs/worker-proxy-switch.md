# Moving one worker node between OCX and magpie

A worker node reaches its models through one proxy, chosen in the node's own LINA config (`worker.proxy`, lina#585).
Without the section the node uses its own OCX, as before. A node is moved to its local magpie, and back, alone: the
hub's model aliases (`worker.models.aliases`) stay in OCX's names, and every model keeps one identity in magpie's
catalog form (`claude/claude-opus-5-5`) on both kinds, through the name table `hermes_cli/worker/proxy_names.json`.

```yaml
worker:
  proxy:
    kind: magpie        # ocx (default) or magpie
    remote_id: hub      # the id the node's magpie gives its remote link to the hub (default hub)
    health: http://127.0.0.1:3425/v1/models   # optional: the address of the proxy check (lina#586)
    service: magpie     # the node's local magpie service: systemd user unit / launchd label (default magpie)
```

## The check the bid reads

Before a node takes work for a runner that reaches models through its proxy (omo, and claude, codex and their ACP
adapters), the node asks its proxy, by kind (lina#586). `ocx` asks `/healthz` for 200, as before. `magpie` has no
`/healthz` (it answers 404). It asks `http://127.0.0.1:3425/v1/models` (or `worker.proxy.health`) and needs a 200
whose list has at least one model. A node that fails is passed with the kind and the address in its reason
(`model path does not answer (magpie http://127.0.0.1:3425/v1/models: HTTP none)`, or `model path lists no model
(...)`). The node's hello reports `capabilities.proxy {kind, health}` (and `models` for magpie), and
`lina worker hosts` and `lina devices` show it as `proxy magpie 200, 23 models`.

**Limit:** a local `/v1/models` 200 shows only that the node's magpie runs and knows the hub's models; it does not
prove the node's link to the hub. The real-call probe (`hermes_cli/lina_runner_probe.py`, run by the daily runner sync, lina#531) and step 5 below stay the
proof that a call goes through. The check reads no key and never calls the hub.

LINA does not install or configure magpie and never touches its accounts or its link to the hub: it is installed on
the node separately and only read here. No step below touches the node's OCX settings or the OCX service; OCX keeps
running, so going back is a config change.

## The device screen (lina#589)

`GET /api/lina/devices` gives each node a `proxy`: `ocx` as before (health and version); `magpie` its link to the hub
(`link`, the `remote_id`), the model count of its local `/v1/models` and the state of its local magpie service
(`worker.proxy.service`); null for a node that reports no proxy. On a magpie node the runners that reach models through
the proxy show `signIn: magpie` and their sign-in is refused (409): their accounts are signed in on the hub's magpie
(`proxy.signIn: hub`), not on the node. The restart action `{"service": "proxy"}` (and `lina devices service <node>
proxy restart`) restarts only the node's own proxy service by its kind, the OCX or the local magpie service, and the
node refuses it while a run is active on it.

## Switch a node to magpie

Run on the node, as the user the worker runs as.

1. **Back up the agent configs and hash them.** Copy the configs the next steps change (omo's `~/.omo/models.json`,
   `~/.omo/settings.json`, `~/.omo/omo.jsonc`, and the claude and codex configs the connection commands write) to one
   dated folder, and write their hashes next to them:

   ```bash
   backup=~/proxy-switch-backup/$(date +%Y%m%d-%H%M%S)
   mkdir -p "$backup" && cp -p ~/.omo/models.json ~/.omo/settings.json ~/.omo/omo.jsonc "$backup"/
   # plus the claude and codex config files the connection commands change
   (cd "$backup" && sha256sum * > SHA256SUMS)
   ```

2. **Connect the agents to the node's magpie** with magpie's own (official) connection commands for claude, codex and
   omo. They add magpie's provider and leave the node's other proxy settings alone.
3. **Set the kind** in the node's LINA `config.yaml`: `worker.proxy.kind: magpie` (and `remote_id` when the node's
   magpie names its link to the hub something other than `hub`). The next run on the node opens its session as
   `magpie` with `<remote_id>/<provider>/<id>`; runs already going keep their session.
4. **Move omo's routes:** `lina omo routing plan`, review it, then `lina omo routing apply` (see below). The
   connection command for omo changes only omo's default model; its role and category routes stay on OCX until this
   step.
5. **Check one real call:** submit one small run to the node (`--host <node>`) and see it end; then find the hub's
   usage line for the node's key on the hub's magpie (the call must show there, not on OCX).

A model with no equivalent through magpie (the `--fast` priority tiers) is refused on that node with the model named;
it is not replaced by another model. Pick the model without `-fast`, or run it on an `ocx` node.

## Roll back

1. Set `worker.proxy.kind: ocx` in the node's LINA `config.yaml` (or remove the `proxy` section).
2. Restore the backed-up configs and check them against the hashes:

   ```bash
   cp -p "$backup"/models.json "$backup"/settings.json "$backup"/omo.jsonc ~/.omo/
   # plus the claude and codex config files
   (cd ~/.omo && sha256sum -c --ignore-missing "$backup"/SHA256SUMS)   # the restored files match the backup
   ```

3. `lina omo routing check` reports the OCX routes again (or, without the backup, `lina omo routing apply` writes them
   back); one real call through the node shows on OCX.

## omo's role and category routes

`lina omo routing plan|apply|check` follows `worker.proxy.kind` (lina#587). On a `magpie` node ocx-omo reads
`providers.magpie.models` of omo's `models.json` (written by magpie's connection command; LINA does not try OCX's
integration there) and writes the same 14 routes with names `magpie/<remote_id>/<provider>/<id>`, through the same
name table. omo's recommended order and reasoning strength and LINA's policy extras stay as they are; a model the
proxy lacks (the `-fast` tiers) and the second of two OCX namespaces with one magpie provider (`google`,
`google-antigravity`) are left out and listed under `not routed`. `check` keeps its exit codes (0 in step, 10 changes
to review, 2 error); right after the kind changes every route shows as changed until `apply`. Applying on a `magpie`
node and then again on an `ocx` node gives back the OCX routes exactly, each apply keeping its `omo.jsonc` backup.

The run records, the settings check and the run page show the same model names before and after: the identity does
not depend on the proxy.

## Usage, quota and model screens

`lina models`, the client API (`/api/lina/models*`, `/api/lina/usage`, `/api/lina/models/state`) and the board's
quota view read the proxies `worker.proxy` names in the LINA root's config (lina#588). Absent, that is OCX alone, read
exactly as before. On a `magpie` node or hub they read its gateway, read-only (LINA never installs, configures or
restarts magpie), with a 5 s timeout:

```yaml
worker:
  proxy:
    kind: ocx                 # the runners' proxy; the screens read it unless `read` says otherwise
    read: [ocx, magpie]       # during the move: both, side by side, labelled by kind
    url: http://127.0.0.1:<port>   # magpie's gateway (default: the origin of `health`, else 127.0.0.1:3425)
    key_env: MAGPIE_KEY       # optional: the `.env` variable holding a gateway key (magpie answers its own machine without one)
```

- Models: `GET /v1/models`, each id shown by its LINA name through the name table (`hub/claude/claude-opus-5-5`
  reads `claude/claude-opus-5-5`), raw when its provider is not in the table.
- Usage: `GET /v1/magpie/usage?period=today|7d|30d`, in OCX's row shape; `byMachine` has one row per caller key
  name, so a node's use shows under the key it calls with, as an OCX `link:<machine>` key does.
- Quota: `GET /v1/magpie/quotas?kept=1` (what magpie last read; no vendor is asked); `5 hours`, `7 days` and
  `30 days` become the five-hour, weekly and monthly windows, any other window a custom one.

A count or window magpie does not give is `null` (unknown), never 0. A magpie that does not answer makes the screen
answer as an unread OCX does (503, or a quota error); with both kinds read, `lina models`, the models and usage
routes and `GET /api/lina/quota` still show the other kind and mark magpie's part `unknown` (the quota as one error),
while the board snapshot's quota part is one error, as when OCX is unread.
