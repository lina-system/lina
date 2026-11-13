# A worker node's model proxy: magpie

Every node reaches its models through its own local magpie. On every node except the hub, that magpie is linked to
the hub's magpie, which holds the accounts. The node's LINA config describes that magpie in `worker.proxy`
(lina#585). Every key is optional:

```yaml
worker:
  proxy:
    remote_id: hub      # the id the node's magpie gives its remote link to the hub (default hub); "": its own magpie holds the models
    health: http://127.0.0.1:3425/v1/models   # the address of the proxy check (lina#586; this is the default)
    url: http://127.0.0.1:3425                # magpie's gateway for the usage, quota and model screens (default: the origin of `health`)
    key_env: MAGPIE_KEY # the `.env` variable holding a gateway key, when the gateway answers only with one (lina#625)
    service: magpie     # the node's local magpie service: systemd user unit / launchd label (default magpie)
```

A model has one name: magpie's catalog form `<provider>/<id>` (`claude/claude-opus-5-5`). The omo runner opens its
session as `magpie` with `<remote_id>/<provider>/<id>` (`hub/claude/claude-opus-5-5`), and `lina omo routing` writes
`magpie/<remote_id>/<provider>/<id>`.

**The hub** (the machine whose own magpie holds the models, lina#623) has no remote link: its magpie lists the models
plainly. Set `remote_id: ""` (explicitly empty) there. Runs then open as `magpie` with `<provider>/<id>`, `lina omo
routing` writes `magpie/<provider>/<id>`, and the device screen shows the link as `local`. A value with `/` is refused.

`remote_id` is the one setting that tells the hub from a node: magpie's model list does not mark which entries come
through a link, and a model id may itself hold `/`, so the link cannot be read off the list.

## The check the bid reads

Before a node takes work for a runner that reaches models through magpie (omo, and claude, codex and their ACP
adapters, whose pin sign-in is `magpie`), the node asks its magpie (lina#586). magpie has no `/healthz` (it answers
404). The node asks `http://127.0.0.1:3425/v1/models` (or `worker.proxy.health`) and needs a 200 whose list has at
least one model. A node that fails is passed with the address in its reason (`model path does not answer (magpie
http://127.0.0.1:3425/v1/models: HTTP none)`, or `model path lists no model (...)`). The node's hello reports
`capabilities.proxy` `{url, health, models, remote_id, service}`, and `lina worker hosts` and `lina devices` show it as
`magpie 200, 23 models`. A node whose own magpie answers only with a gateway key sends `Authorization: Bearer <key>`
from the variable `worker.proxy.key_env` names, the same key the screens below read with; without it, no key is sent.

**Limit:** a local `/v1/models` 200 shows only that the node's magpie runs and knows the hub's models; it does not
prove the node's link to the hub. The real-call probe (`hermes_cli/lina_runner_probe.py`, run by the daily runner
sync, lina#531) stays the proof that a call goes through. The check never calls the hub.

LINA does not install or configure magpie and never touches its accounts or its link to the hub: it is installed on
the node separately and only read here.

## The device screen (lina#589)

`GET /api/lina/devices` gives each node a `proxy`: its link to the hub (`link`, the `remote_id`, `local` on the hub),
the model count of its local `/v1/models` and the state of its local magpie service (`worker.proxy.service`); null for
a node that reports no proxy (a file node). The runners that reach models through magpie show `signIn: magpie` and
their sign-in is refused (409): their accounts are signed in on the hub's magpie (`proxy.signIn: hub`), not on the
node. The restart action `{"service": "proxy"}` (and `lina devices service <node> proxy restart`) restarts only the
node's local magpie service, and the node refuses it while a run is active on it.

## Set up a node

Run on the node, as the user the worker runs as.

1. **Install magpie** on the node and link it to the hub's magpie (magpie's own install and link commands).
2. **Connect the agents to the node's magpie** with magpie's own connection commands for claude, codex and omo.
3. **Set `worker.proxy`** in the node's LINA `config.yaml` when it differs from the defaults above (`remote_id` when
   the node's magpie names its link to the hub something other than `hub`, `""` on the hub itself, `health` on a node
   whose magpie listens on another port).
4. **Write omo's routes:** `lina omo routing plan`, review it, then `lina omo routing apply` (see below). The
   connection command for omo changes only omo's default model; its role and category routes come from this step.
5. **Check one real call:** submit one small run to the node (`--host <node>`) and see it end; then find the hub's
   usage line for the node's key on the hub's magpie.

## omo's role and category routes

`lina omo routing plan|apply|check` (lina#587) runs ocx-omo with `--remote-id <remote_id>`. ocx-omo reads
`providers.magpie.models` of omo's `models.json` (written by magpie's connection command for omo) and writes the 14
routes as `magpie/<remote_id>/<provider>/<id>`, searching magpie's providers in the policy's order (`codex`,
`claude`, `antigravity`, `grok`). omo's recommended order and reasoning strength and LINA's policy extras stay as
they are; a model magpie does not list is left out and listed under `not routed`. `check` exits 0 in step, 10 with
changes to review, 2 on an error.

## Usage, quota and model screens

`lina models`, the client API (`/api/lina/models*`, `/api/lina/usage`, `/api/lina/models/state`), the work settings'
model list and the board's quota view read the magpie gateway `worker.proxy` names in the LINA root's config
(lina#588), read-only (LINA never installs, configures or restarts magpie), with a 5 s timeout:

- Models: `GET /v1/models`, each id shown as `<provider>/<id>` (a node's `hub/claude/claude-opus-5-5` reads
  `claude/claude-opus-5-5`).
- Usage: `GET /v1/magpie/usage?period=today|7d|30d`; `byMachine` has one row per caller key name, so a node's use
  shows under the key it calls with.
- Quota: `GET /v1/magpie/quotas?kept=1` (what magpie last read; no vendor is asked); `7 days` and `30 days` become
  the weekly and monthly windows, any other window a custom one. The quota view has no five-hour window: `5 hours`
  and a family's `... · 5 hours` are not logged, served or warned on (lina#654). A service's total is its weekly
  window, else its monthly one; Antigravity's is its `Gemini · 7 days` window and Cursor's its `Total` (lina#662).

A count or window magpie does not give is `null` (unknown), never 0. A magpie that does not answer makes the routes
answer 503, `lina models` print `unknown: ...`, and the board's quota part one error.

## Notes from the first nodes (2026-11-12)

- **omo's shared RPC daemon keeps the old catalog.** If it still holds an old model catalog after the routes changed,
  stop it when the node has no open session (`omo host stop`); the next run starts it with the new `models.json`.
- **8 GB nodes sit close to the memory floor.** A bid also needs `worker.min_free_mem_gb` (default 4 GB) of available
  memory. On such a node that floor is near what is free with the OS and magpie running, so a pass with
  `memory N GB < 4GB` in `lina worker hosts` is the memory floor, not the proxy.
- **A node where another user's magpie already holds port 3425** gets a different port for its own magpie, for
  example `MAGPIE_ADDR=127.0.0.1:3428` in the magpie unit's drop-in, and the same `MAGPIE_ADDR` in the environment of
  the connection commands, so magpie's official commands use that address. Set `worker.proxy.health` to
  `http://127.0.0.1:3428/v1/models` on that node.
