"""Route sources of runner blocks (lina#531): ``route: {source: <name>}`` names the program that maps OCX's models
onto the runner's own model choices. A source answers three calls, the same for every runner that names it:

* ``prepare(root, run)``: what it needs on this device (built once, after the runner is installed); a short line;
* ``check(root, run)``: ``(0, output)`` in step, ``(10, output)`` changes to review, anything else an error;
* ``apply(root, run)``: write the reviewed route; its exit code.

``ocx-omo`` (``lina_omo_routing``) is one such source; a runner with no ``route`` has none, and every call is a
no-op for it.
"""

from __future__ import annotations

import importlib
import subprocess
from pathlib import Path
from typing import Callable

from hermes_cli.lina_runners import RunnerPin, RunnersError

SOURCES = {"ocx-omo": "hermes_cli.lina_omo_routing"}
IN_STEP, CHANGES = 0, 10
Run = Callable[..., subprocess.CompletedProcess]


def source(runner: RunnerPin):
    """The module of *runner*'s route source; None when the block has no route."""
    name = runner.route.get("source") if isinstance(runner.route, dict) else None
    if not name:
        return None
    if name not in SOURCES:
        raise RunnersError(f"{runner.name}: unknown route source {name!r} (known: {', '.join(SOURCES)})")
    return importlib.import_module(SOURCES[name])


def prepare(root: Path, runner: RunnerPin, *, run: Run = subprocess.run) -> str | None:
    found = source(runner)
    return found.prepare(root, run=run) if found else None


def check(root: Path, runner: RunnerPin, *, run: Run = subprocess.run) -> tuple[int, str] | None:
    found = source(runner)
    return found.check(root, run=run) if found else None


def apply(root: Path, runner: RunnerPin, *, run: Run = subprocess.run) -> int | None:
    found = source(runner)
    return found.apply(root, run=run) if found else None
