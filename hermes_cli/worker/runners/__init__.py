"""Runner registry: ``worker.runners`` in config names each runner and its type; omo is there when nothing is.

```yaml
worker:
  default_runner: omo
  runners:
    codex:  {type: command, argv: [codex, exec, --full-auto, "-"]}
    claude: {type: command, argv: [claude, -p], follow_up_argv: [claude, -p, --continue]}
```

A new coding agent needs a config entry only: ``command`` runs any CLI that reads its prompt on stdin.
"""

from __future__ import annotations

from pathlib import Path

from hermes_cli.worker.runners.base import RunnerError
from hermes_cli.worker.runners.acp import AcpRunner
from hermes_cli.worker.runners.command import CommandRunner
from hermes_cli.worker.runners.omo import OmoRunner

TYPES = {"omo": OmoRunner, "command": CommandRunner, "acp": AcpRunner}
DEFAULT = "omo"


def specs(worker: dict) -> dict[str, dict]:
    configured = worker.get("runners") if isinstance(worker.get("runners"), dict) else {}
    table = {"omo": {"type": "omo"}}
    table.update({str(name): spec for name, spec in configured.items() if isinstance(spec, dict)})
    return table


def spec_for(worker: dict, name: str, hub_spec: dict | None = None) -> dict | None:
    """The spec a node builds runner *name* from, for a run and for its probe alike: the node's own entry of that name
    (its local paths), else the hub's *hub_spec*, else the built-in omo; None when neither side defines it. The model
    is always the hub's (lina#257): a node entry without ``model_args`` takes the hub's, so the job's model reaches
    the CLI."""
    configured = worker.get("runners") if isinstance(worker.get("runners"), dict) else {}
    hub_spec = hub_spec or {}
    spec = configured.get(name) or hub_spec or specs(worker).get(name)
    if spec and spec is not hub_spec and not spec.get("model_args") and hub_spec.get("model_args"):
        spec = {**spec, "model_args": hub_spec["model_args"]}
    return spec or None


def default_name(worker: dict) -> str:
    return str(worker.get("default_runner") or DEFAULT)


def build(name: str, spec: dict, limits: dict | None = None, root: Path | None = None):
    kind = spec.get("type")
    if kind not in TYPES:
        raise RunnerError(f"runner {name}: unknown type {kind!r} (known: {', '.join(TYPES)})")
    if kind == "omo":
        return OmoRunner(name, spec, limits, root)
    return TYPES[kind](name, spec, limits)


def get(name: str, worker: dict, limits: dict | None = None, root: Path | None = None):
    table = specs(worker)
    if name not in table:
        raise RunnerError(f"no runner {name!r} (configured: {', '.join(table)})")
    return build(name, table[name], limits, root)
