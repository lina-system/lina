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
    health: http://127.0.0.1:<port>/healthz   # optional, read by the health report (lina#586)
```

LINA does not install, configure or restart magpie: it is installed on the node separately and only read here. No
step below touches the node's OCX settings or the OCX service; OCX keeps running, so going back is a config change.

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
