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
- `lina runners status [--json] [--offered]`: per runner `pinned`, `current`
  (the `current` link), `installed` (what the active install is: an npm
  runner's installed `package.json` with its entry present, another runner's
  `--version`; `installed_why` when it cannot say), `versions`,
  `person_installed`, `sign_in` (`signed_in`, `signed_out`, `api_key`, `ocx`,
  or `unknown` with `sign_in_why`), `ready` and one `why` (`prepare`, `update`,
  `sign in`, `unknown`). A runner that updated itself in place shows `update`;
  a missing entry shows `prepare`. `--offered` reports the runners this node
  offers in `worker.runners`, matched by name or by the command in `argv`; this
  is what `hello` carries.

Each `--version` the status asks is bounded by 10 s, the rows run in parallel,
and the report `hello` carries is bounded by 20 s as a whole. Credential files
are only checked for existence, never opened.

## Automatic updates

Off where the runner documents a switch: the Claude Code shim sets
`DISABLE_AUTOUPDATER=1` (code.claude.com/docs/en/env-vars). Codex, Grok, dsh,
cursor-agent and agy have no switch LINA could confirm from their help or docs;
`status` shows `update` when a runner moved itself off its pin.

## Devices

`lina devices` shows each device's runners from that device's `hello`, through
the negotiated transport (in-process for a local one). A device ssh cannot reach
lists the runners the hub offers, each `unreachable`; a device without hello, or
whose hello has no runner rows yet, each `update`.

`lina devices sign-in [--json]` gathers the runners that are `signed_out` on some
nodes into one question per runner, naming the nodes and how to sign in on each.
Writing it into the inbox and the hub's sign-in button are #230 and #234.

`lina devices service <name> <ocx|worker> <status|restart>`: on any device
through its `service` verb. The worker goes through #218's
`NegotiatedTransport.service` with the instance its `hello` reports; OCX is
`service ocx <action>`, which the node hands to `lina_runners.node_service` and
the service layer (`lina_services.act`, its refusals included: OCX is not
restarted while the worker or gateway runs on it).

## Daily sync (lina#238)

Each runner pin with `follow` moves once a day to its source's newest version,
after a trial on the hub, and the nodes follow the same day. omo, OCX and the
ACP adapters keep LINA's pins; `gh`, `opencode` and `pi` are not runners.

| Runner | Source (`follow`) | Hash |
|---|---|---|
| `codex` | npm `@openai/codex` dist-tag `latest` | npm `dist.integrity` |
| `claude` | npm `@anthropic-ai/claude-code` dist-tag `stable` | npm `dist.integrity` |
| `dsh` | npm `@deepseek-ai/dsh` dist-tag `latest` | npm `dist.integrity` |
| `grok` | `https://x.ai/cli/stable` (the body is the version) | sha256 the hub measures |
| `cursor-agent` | the `downloads.cursor.com/lab/<version>/` in `https://cursor.com/install` | sha256 the hub measures |
| `agy` | the per-platform release manifest | the manifest's sha512 |

For grok and cursor-agent the hub downloads the new version for each platform
its nodes report (`hello`'s `capabilities.platform`, plus its own), records
the sha256, and compares the bytes with the hash or length the download's
answer carries (GCS `x-goog-hash` md5, a plain md5 `etag`); a mismatch moves
nothing that day.

`lina runners sync [--plan] [--only grok,codex] [--json]`, per runner:

1. the source names its newest version;
2. a version above the hub's pin is installed on the hub beside the current one
   and asked one real request (the pin's `trial`: "Reply with exactly OK and
   nothing else." must come back as a line `OK`; dsh is asked once over ACP).
   codex, claude and dsh also need a new `200` in the hub's OCX log
   (`ocx logs --json`, read-only with `ocx.external`): a `requestId` that was
   not in the log read just before the trial, of the trial's kind (the pin's
   `trial.ocx_match`: `model_contains`, `model_endswith`, `protocol`). A pin with
   `ocx: true` and no `ocx_match`, or a log that cannot be read before the trial,
   fails without a trial. OCX records no client, so another request of the same
   model and protocol at the same moment cannot be told apart: the `OK` answer
   is the main evidence, the `200` shows the runner went through OCX. While a
   run holds a hub slot (checked before the install and before the switch), the
   hub neither installs nor switches that day and the nodes do not follow;
   nothing is held;
3. when it passes, the hub writes the move into `$LINA_HOME/runner-pins.json`,
   switches, and sends each node its `runner-sync` verb with the new pin. The
   node checks again that no run holds a slot, writes the same file in its own
   root, upgrades (`lina runners upgrade`: hash, `--version`, switch, shim
   trial, back to `previous` on failure) and answers.

`runner-pins.json` is laid over `pm/lina-deps.json` by `lina_runners.pins()`;
the repo pin is the floor. Every change appends the previous entry to
`runner-pins.history.jsonl`. Nothing moves when:

- the runner is held: a failed hub trial holds the runner, a failed node install
  holds it on that node only (`runner-sync/holds.json`). `lina runners sync clear
  <runner> [--node <node>]` releases it;
- the jump is major (the first number changes; a year such as cursor-agent's
  `2026.11.05-...` is not one): it is held without a trial until `sync clear
  <runner> --accept <version>`;
- the source names a version below the pin (reported, not moved);
- the hub is not signed in to the runner: a sign-in question, not a failure.
  The question names the hub by its `devices.workers` name (the first
  `host: local` device, else `hub`), so `lina devices sign-in <name> <runner>`
  finds it;
- the hub has no LINA copy of the runner (`hub prepare needed`): a line in the
  notice and the issue line
  ``- prepare: <runner> is not installed on the hub; run `lina runners install <runner>` ``
  in `sync status --issues`, not a hold. Once it is installed, the next day's
  run moves it;
- a run holds a hub slot (`hub busy`): see the retry below.

A node with a run holding a slot, an unreachable node, a node without the
`runner-sync` verb (`update`) and a node that never installed the runner
(`prepare`, see `lina runners install`) are skipped and follow on a later day.
`hello` carries `pin_from` (`repo` or `sync`) per runner row, `platform`, and
`ocx` (`external`, `version`, `health`: the `/healthz` status code);
`lina devices --json` adds `hub_pinned` and `same_as_hub` to each row.

Each run writes `runner-sync/YYYYMMDD.json` (sources, the hub's trial, each
node's result, holds) and `runner-sync/last.json` (`started`, `finished`,
`rc`), and sends one short English notice through `worker.notice.command` to
`worker.runner_sync.notify` (default `default`) when something moved, failed
or waits; a quiet day sends none. `--plan` installs, writes and sends nothing.

`lina runners sync status` shows the hub's pins, holds, the last run, issues
and notes; `--issues` prints only the issue lines (`- none` when there are
none): holds, a node behind the hub's pin for over 24 hours
(`runner-sync/lag.json`), an OCX whose health is not 200, and sign-ins.
Unreachable nodes and lags under 24 hours are notes.

`worker.runner_sync: {at: "04:00"}` (off by default) lets `lina worker serve` on
the hub start the sync as its own owned process once the local time is past
`at`; the start written into `last.json` and the sync's lock keep a restarted
service from running it twice on one day. A start that fails is written into
`last.json` (`rc: 1`, `error`) and `runner-sync/sync.log`, is not retried that
day, and shows as `- daily: could not start: ...` in `sync status --issues`.

When the day's run found the hub busy for some runners, it writes `retry: true`
and those runners (`busy`) into `last.json`. 30 minutes after that run finished,
on the same day, the tick starts one more run with `--only` those runners
(`retried: true` in `last.json`). If the hub is busy again, that day is over: a
day has at most two runs, and a new day starts without the retry. The second
run's results are laid over the day's record, so the first run's other runners
stay in `runner-sync/YYYYMMDD.json` and in the issues.

## Which copy a run uses

The runner `argv` in `docs/lina-worker.example.yaml` still names bare commands
(`grok`, `codex-acp`), and so may the hub's and the nodes' configs. An `acp` or
`command` runner whose `argv[0]` is such a bare name, pinned in
`pm/lina-deps.json` `runners`, starts `$LINA_HOME/bin/runners/<name>` when that
shim is an executable file (after `lina runners install <name>`), and the name
on `PATH` otherwise. The bid probe (`lina worker hosts`), the spawned command
and the `launched` event use the same choice. An `argv[0]` that is a path, or a
name LINA does not pin (such as `lina-dsh-acp`), is run as written.
