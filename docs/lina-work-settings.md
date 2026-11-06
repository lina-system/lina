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
    nas:     {host: nas, role: files, cap: 1, auto: false, no_repo: true}
    laptop:  {role: person}            # status only: never a cap, never a host
worker:
  default_runner: omo
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

`on`, `model`, `fallback_model`, `models.fallback` and `routing` are read at submit (see below); `role` and `access`
are recorded only. A model is an alias or `<provider>/<id>[:<effort>]`.

## At submit

`lina worker submit` picks through this layer (lina#257):

- the runner: `--runner`, else the first `routing` rule that holds for `--labels a,b` and `--size S|M|L` (a card
  session passes the issue's runner label and size), else `default_runner`. A runner with `on: false` is refused.
- the model: `--model`, else the runner's `model`, else the mode default (`models.defaults`), else the runner's own.
  When OCX has it `off`, `out_of_quota` or `no_account` and the fallback (the runner's `fallback_model`, else
  `models.fallback`) is usable, the run starts on the fallback and gets one `model_fallback` receipt
  `{from, to, why}`; when the fallback is not usable either, the chosen model stays. `--model` is checked, never
  replaced. When OCX cannot be read (and the last good list is gone) the check is `unchecked`; a run never waits.
- the job and its `submitted` receipt carry `settings: {rev, runner_source: --runner|routing:<rule>|default,
  model_source, ocx_check: ok|off|out_of_quota|no_account|unchecked}`; the run record and the run card serve
  `model` and `settingsRevision`. A follow-up keeps the run's model, and on the device the job's model wins over the
  device's own runner entry (which still gives the local `argv`).

Validation (nothing is written on a refusal, the reply names each path): unknown keys; unknown runners in
`default_runner` and `routing`; an off default runner; `cap`, `named_cap`, `last`, `auto` or `host` on a `person`
device; a work or files device without `host`; a `command` runner without `argv`; credential-like keys
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
dashboard credential, and the peer's `tailscale whois` must give the owner login on one of the owner's devices, at
most 30 writes a minute per device. Any whois error refuses the write; reads never depend on it. With no owner
configured nobody writes through the API (`writable: false`). These keys are not work settings, so the API cannot
change them:

```yaml
worker:
  settings:
    owner: {login: <tailnet login>, devices: [<device name>]}
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
