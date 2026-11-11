"""The real-call probe of a runner block on one device (lina#531): its ``probe`` (``trial``) run against the version the
device runs now, recorded as ``done`` or ``failed`` with a one-line cause in ``<LINA root>/runner-probes.json``.

The recorded result is what readiness shows per device and runner: ``lina runners status`` / ``hello`` rows carry
it, ``lina devices`` prints it, and the bidding probe (``worker hosts``, a bid) refuses a runner whose last probe of
the version it runs failed. The static checks (installed, pinned, signed in) stay the first filter; a probe of
another version than the one installed is not shown.

The hub asks a node through the ``runner-probe`` verb (``{"runners": {<name>: {...}}}``), the daily runner sync's
step after the nodes followed; a node with a run holding a slot answers ``busy`` and is probed another day.
"""

from __future__ import annotations

import json
import os
import subprocess
import time
from pathlib import Path
from typing import Callable

from hermes_cli import lina_runners

VERB = "runner-probe"
RESULTS = "runner-probes.json"


def results_path(root: Path) -> Path:
    return root / RESULTS


def results(root: Path) -> dict[str, dict]:
    try:
        data = json.loads(results_path(root).read_text(encoding="utf-8-sig"))
    except (OSError, ValueError):
        return {}
    return {name: value for name, value in data.items() if isinstance(value, dict)} if isinstance(data, dict) else {}


def record(root: Path, name: str, result: dict) -> None:
    data = {**results(root), name: result}
    root.mkdir(parents=True, exist_ok=True)
    tmp = results_path(root).with_name(f".{RESULTS}.{os.getpid()}.tmp")
    tmp.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    os.replace(tmp, results_path(root))


def verdict(root: Path, runner: lina_runners.RunnerPin, installed: str | None) -> dict | None:
    """The recorded probe of the version *installed*; None when there is none for it."""
    found = results(root).get(runner.name)
    return found if found and installed and found.get("version") == installed else None


def probe(root: Path, runner: lina_runners.RunnerPin, *,
          run: Callable[..., subprocess.CompletedProcess] = subprocess.run, popen=subprocess.Popen,
          ocx_logs=None, clock: Callable[[], float] = time.time) -> dict:
    """One real call to the current version of *runner*, recorded; returns the record."""
    from hermes_cli import lina_runner_sync_trial as trials
    version = lina_runners.current_version(root, runner)
    if version is None:
        result = {"state": "failed", "why": "not installed", "version": None, "at": clock()}
    else:
        ok, why = trials.trial(root, runner, version, run=run, popen=popen,
                               ocx_logs=ocx_logs or trials.read_ocx_logs, clock=clock)
        result = {"state": "done" if ok else "failed", "why": why, "version": version, "at": clock()}
    record(root, runner.name, result)
    return result


def tend(root: Path, runner: lina_runners.RunnerPin, asked: dict, *,
         run: Callable[..., subprocess.CompletedProcess] = subprocess.run, **kw) -> dict:
    """One device's daily care of one block: refresh its model list and apply its route when asked, then probe."""
    from hermes_cli import lina_runner_models, lina_runner_routes
    done: dict = {}
    if asked.get("refresh"):
        problem = lina_runner_models.refresh(root, runner, run)
        done["models"] = {"state": "failed", "why": problem} if problem else {"state": "refreshed"}
    if asked.get("route"):
        code = lina_runner_routes.apply(root, runner, run=run)
        done["route"] = {"state": "applied"} if code in (0, None) else {"state": "failed", "why": f"apply exit {code}"}
    return {**probe(root, runner, run=run, **kw), **done}


def apply(worker_root: Path, payload: dict, **kw) -> dict:
    """The node side of ``runner-probe``: for each named runner, the hub's asks first (``refresh``: its model list,
    ``route``: apply its route source, both after the hub's own probe passed), then its probe."""
    from hermes_cli.worker import slots
    root = worker_root.parent
    wanted = payload.get("runners") if isinstance(payload, dict) else None
    if not isinstance(wanted, dict) or not wanted:
        return {"action": "error", "reason": "runner-probe expects {\"runners\": {<name>: {...}}}"}
    table = lina_runners.pins(root=root)
    unknown = [name for name in wanted if name not in table]
    if unknown:
        return {"action": "error", "reason": f"no runner block {', '.join(unknown)}"}
    held = slots.held(worker_root)
    out = {}
    for name, asked in wanted.items():
        if held:
            out[name] = {"state": "busy", "why": f"{held} run(s) hold a slot; next day"}
            continue
        out[name] = tend(root, table[name], asked if isinstance(asked, dict) else {}, **kw)
    lina_runners.clear_report_cache(worker_root)
    return {"action": VERB, "results": out}
