"""Runner registry: ``worker.runners`` in config names each runner and its type.

```yaml
worker:
  default_runner: codex
  runners:
    codex:  {type: command, argv: [codex, exec, --full-auto, "-"]}
    claude: {type: command, argv: [claude, -p], follow_up_argv: [claude, -p, --continue]}
```

A new coding agent needs a config entry only: ``command`` runs any CLI that reads its prompt on stdin.
"""

from __future__ import annotations

from pathlib import Path

from hermes_cli.worker.runners.base import RunnerError
from hermes_cli.worker.runners.command import CommandRunner

TYPES = {"command": CommandRunner}
DEFAULT = "omo"


def specs(worker: dict) -> dict[str, dict]:
    configured = worker.get("runners") if isinstance(worker.get("runners"), dict) else {}
    return {str(name): spec for name, spec in configured.items() if isinstance(spec, dict)}


def default_name(worker: dict) -> str:
    return str(worker.get("default_runner") or DEFAULT)


def build(name: str, spec: dict, limits: dict | None = None, root: Path | None = None):
    kind = spec.get("type")
    if kind not in TYPES:
        raise RunnerError(f"runner {name}: unknown type {kind!r} (known: {', '.join(TYPES)})")
    return TYPES[kind](name, spec, limits)


def get(name: str, worker: dict, limits: dict | None = None, root: Path | None = None):
    table = specs(worker)
    if name not in table:
        raise RunnerError(f"no runner {name!r} (configured: {', '.join(table)})")
    return build(name, table[name], limits, root)
