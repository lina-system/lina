# Work settings

Which devices bid and how many runs each takes, which runners there are and the model each starts with, the model
aliases, defaults per mode and fallback, and the routing rules (lina#256). They stay where `lina worker` reads them on
every command, in the LINA root's `config.yaml`; this layer adds validation, revisions, rollback, a CLI, a client API
and the OCX model list. A change applies from the next submit.

## Keys

```yaml
devices:
  workers:
    build-a: {host: build-a, role: work, cap: 2, named_cap: 2, last: 0, fast: true, no_repo: false}
    nas:     {host: nas, kind: file, role: files, omo: omo, deny: ["#recycle"], volumes: [/volume1],
              root: lina-file-node, model_check: "http://127.0.0.1:10100/healthz", scan_s: 300}
    laptop:  {host: laptop-ssh, role: person}  # status only: never a node
worker:
  default_runner: omo
  access: {files-agent: full, reader: read-only, builder: workspace-write}
  runners:
    codex: {type: command, argv: [codex, exec, "-"], on: true, model: "opencodex/impl-model:high",
            fallback_model: opencodex/small-model, access: workspace-write}
  models:
    aliases: {planner: {provider: opencodex, id: plan-model, thinking: medium}}
    defaults: {research: planner}      # by mode: ulw, research, mass, mass-research
    fallback: opencodex/small-model
  routing:                             # first match wins, else default_runner
    - {name: docs, when: {labels_any: [docs]}, runner: agy}
    - {name: small, when: {runner_label: runner/dsh, size: [S]}, runner: dsh}
```

`on`, `model`, `fallback_model`, `models.fallback` and `routing` are read at submit (see below).
`worker.access` maps agent names to their access ceiling (`read-only`, `workspace-write` or `full`), read at submit;
it is distinct from a runner's `access`. It participates in revisions, rollback and outside-edit tracking.
A model is an alias or `<provider>/<id>[:<effort>]`.

Device `kind` is `lina` (default) or `file`. Only `kind: file` accepts `omo`, `deny`, `volumes`, `root`,
`model_check` and `scan_s`; its role defaults to `files`, and cannot be `work` or `person`.
Omitted file fields use the worker reader's defaults: `omo: omo`, empty `deny`/`volumes`, `root: lina-file-node`,
`model_check: http://127.0.0.1:10100/healthz`, `scan_s: 300`. See [file-node configuration](file-node.md#configuration).

## At submit

`lina worker submit` picks through this layer (lina#257):

- the runner: `--runner`, else the first `routing` rule that holds for `--labels a,b` and `--size S|M|L` (a card
  session passes the issue's runner label and size), else `default_runner`. A runner with `on: false` is refused.
- the model: `--model`, else the runner's `model`, else the mode default (`models.defaults`), else the runner's own.
  When OCX has it `off`, `out_of_quota` or `no_account` and the fallback (the runner's `fallback_model`, else
  `models.fallback`) is usable, the run starts on the fallback and gets one `model_fallback` receipt
  `{from, to, why}`; when the fallback is not usable either, the chosen model stays. `--model` is checked, never
  replaced. A submit never calls OCX: it decides from the models list this process last read (`…/models` or
  `lina worker settings models`, kept 10 minutes), else from `work-settings/models-last-good.json`, the snapshot
  every good read writes (whole: a temp file, then a rename) for the submits that run in a process of their own
  (`lina worker submit`, `lina work submit`). It keeps only what the check uses: per model its
  provider, id, efforts and off flags; per account its provider, paused and sign-in flags and quota windows (no
  account name, email, id or label); and `at`, the read time. It stays fresh while someone has the models view open
  (the hub page reads it once a minute); older than 10 minutes, missing or unreadable, the check is `unchecked` and
  the chosen model stays.
- from `lina work next --submit` (lina#283): each pick goes with its issue's labels and size (its `size/S|M|L` label,
  else its body's `Size:` line). The runner is the first `routing` rule that holds (`routing:<rule>`), else the
  runner its runner label names (`--runner`, as before), else `default_runner`; with no rules every issue goes where
  its label says. A rule whose runner is off is passed over, and a runner that is off is refused with one line.
- the job and its `submitted` receipt carry `settings: {rev, runner_source: --runner|routing:<rule>|default,
  model_source, ocx_check: ok|off|out_of_quota|no_account|unchecked}`; the run record and the run card serve
  `model` and `settingsRevision`. A follow-up keeps the run's model, and on the device the job's model wins over the
  device's own runner entry (which still gives the local `argv`).

Validation (nothing is written on a refusal, the reply names each path): unknown keys; unknown runners in
`default_runner` and `routing`; an off default runner; node fields on a `person` device (only `host`, `role` and
`kind: lina` are accepted); any device without a non-empty `host`; a kind other than `lina` or `file`;
file-only fields on another kind; a file device with `role: work|person`; empty/non-text `omo` or `root`;
`deny`/`volumes` other than lists of non-blank texts (empty lists are allowed); `model_check` other than an
http/https URL with a host; `scan_s` other than a positive integer (booleans are refused);
`worker.access` other than a mapping of agent names (letters, digits, `-`, `_`, up to 41 characters) to
`read-only|workspace-write|full`; a `command` runner without `argv`; credential-like keys
(`key`, `token`, `secret`, `password`) and a runner's `env`, which stay in `config.yaml` untouched and never reach a
revision; at most 128 devices, 64 runners and 64 rules.

## Revisions and rollback

Beside `config.yaml`: `work-settings/revisions/rNNNNNN.yaml` (only these keys), `work-settings/audit.jsonl` (one
line per revision: `rev`, `at`, `by {kind: cli|app|outside|rollback, principal, device}`, `base`, `changes [{path,
from, to}]`) and `work-settings/lock` (one writer at a time). Each change writes the changed keys in one grouped
config write (comments stay), then the next revision. An edit of these keys made any other way (by hand, say)
becomes its own revision with `by.kind: outside` on the next read. Rollback writes the previous revision's content as
a new revision, so a second rollback undoes the first. `lina devices add|remove` write through the layer.

## Who writes

The CLI on the hub always writes (principal: the OS user, device `local`). Through the API a write needs the
dashboard credential, and the peer's `tailscale whois` must give the owner login on one of the owner's devices
(`owner.devices`), or a device in `owner.agents` with any login, at most 30 writes a minute per device. Any whois
error refuses the write; reads never depend on it. With neither an owner login with devices nor an agent node
configured nobody writes through the API (`writable: false`).

`owner.agents` is for the nodes of the owner's own fleet that run agents, such as the hub, so those agents change
the settings through the same page path. Such a node is often tagged and then has no user login (whois gives the
node's own name), so a listed agent device passes whatever its login, and the revision's `by` records that login as
the principal and the node as the device. Trusting a listed node adds nothing over the hub's CLI, which writes with
no gate: list only nodes that already run the owner's agents. A device in `owner.devices` still needs the owner
login, and a device in neither list is refused. With `agents` empty or absent the gate is as before.

Behind the hub's own `tailscale serve` (to a loopback port) the direct peer is `127.0.0.1` or `::1`, which whois
does not know; then the gate (the `writable` flag and the write, and its rate limit) judges the last
`X-Forwarded-For` entry, which `tailscale serve` sets to the tailnet source address, replacing what the client sent.
Any other direct peer's `X-Forwarded-For` is ignored, and a loopback request without the header (or with a
malformed one) is judged as `127.0.0.1`, as before. Trusting a loopback peer's header adds nothing over the CLI: a
process that can reach the loopback port already runs on the hub, where `lina worker settings set` writes with no
gate. uvicorn's `proxy_headers` stays as the dashboard sets it. These keys are not work settings, so the API cannot
change them:

```yaml
worker:
  settings:
    owner:
      login: <tailnet login>
      devices: [<device name>]         # the owner's own devices: need the owner login
      agents: [<device name>]          # the owner's agent nodes (the hub): any login
    tailscale: tailscale               # command or argv list
```

## CLI

`lina worker settings show [--json] [--rev rN] | set <dotted.path> <yaml value> | edit | validate [file] |
history [--limit N] | diff [rA] [rB] | rollback | route [--labels a,b] [--size S|M|L] | models [--json]`, e.g.
`lina worker settings set worker.runners.codex.model opencodex/impl-model:high`, `set devices.workers.hub.cap 3`,
`set worker.routing null`. A refusal prints `lina worker settings: <reason>` and exits 2.

## API

`GET/PATCH /api/lina/work/settings` (JSON Merge Patch, ETag, If-Match required: 428 missing, 412 stale, 422 with
`{detail, errors: [{path, message}]}`), `GET …/history`, `GET …/history/{rev}`, `POST …/rollback`, `GET …/models`,
`POST …/route`; schema [docs/api/settings-client.yaml](api/settings-client.yaml). `/api/lina/events` carries
`{source: settings, kind: revised|rolled_back|outside, id: rN}`.

`…/models` lists OCX's enabled models (read-only, through `lina_models`) with efforts, state (`ok`, `off`,
`out_of_quota`, `no_account`, `unknown`), quota left (100 - the used percent of the best usable account) and reset,
and for each runner, mode default and the fallback whether its model is usable now, with one warning line when not.
When OCX cannot be read it serves the last good list (up to 10 minutes) with an error.
