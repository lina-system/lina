"""The node side of the daily runner sync (lina#238): the ``runner-sync`` verb and what ``hello`` adds for it.

The hub sends ``{"runners": {<name>: {"version", "integrity" | "artifacts"}}}`` on stdin. For each runner the node
checks again, right before it installs, that no run holds a slot (the hub's look can be minutes old); a busy node
refuses and follows the next day. Otherwise it writes the moved pin into its own ``runner-pins.json`` and upgrades
through ``lina_runners.upgrade`` (install beside the current one with the installer that owns the binary, hash
checked, ``--version`` trial, switch, shim trial, back to ``previous`` when the shim does not answer). A runner the
node has not installed is only pinned and reported ``prepare``; ``lina runners install`` installs it. A failed
upgrade puts the node's pin back to what it runs.
"""

from __future__ import annotations

import re
import subprocess
from pathlib import Path
from typing import Callable

from hermes_cli import lina_deps, lina_runners
from hermes_cli import lina_runner_sync_state as state

VERB = "runner-sync"
_VERSION = re.compile(r"^[0-9][0-9A-Za-z.\-]{0,63}$")


def _entry(name: str, value, table: dict[str, lina_runners.RunnerPin]) -> dict:
    runner = table.get(name)
    if runner is None or not runner.follow:
        raise ValueError(f"{name} is not a runner the sync moves")
    if not isinstance(value, dict) or not _VERSION.match(str(value.get("version") or "")):
        raise ValueError(f"{name}: no valid version")
    if runner.installer == "npm" and not str(value.get("integrity") or "").startswith("sha512-"):
        raise ValueError(f"{name}: an npm runner moves only with its sha512 integrity")
    if runner.installer != "npm" and not (isinstance(value.get("artifacts"), dict) and all(
            isinstance(a, dict) and a.get("url") and (a.get("sha256") or a.get("sha512"))
            for a in value["artifacts"].values())):
        raise ValueError(f"{name}: a downloaded runner moves only with a hash for each artifact")
    return {key: value[key] for key in state.MOVED_KEYS if key in value}


def busy(worker_root: Path) -> int:
    from hermes_cli.worker import slots
    return slots.held(worker_root)


def apply(worker_root: Path, payload: dict, *, run: Callable[..., subprocess.CompletedProcess] = subprocess.run,
          fetch: Callable[[str, Path], None] = lina_runners.download) -> dict:
    root = worker_root.parent
    table = lina_runners.pins(root=root)
    wanted = payload.get("runners") if isinstance(payload, dict) else None
    if not isinstance(wanted, dict) or not wanted:
        return {"action": "error", "reason": "runner-sync expects {\"runners\": {<name>: {version, ...}}}"}
    try:
        entries = {name: _entry(name, value, table) for name, value in wanted.items()}
    except ValueError as exc:
        return {"action": "error", "reason": str(exc)}
    results = {name: _one(worker_root, root, name, entry, run, fetch) for name, entry in entries.items()}
    lina_runners.clear_report_cache(worker_root)
    return {"action": VERB, "results": results}


def _one(worker_root: Path, root: Path, name: str, entry: dict, run, fetch) -> dict:
    held = busy(worker_root)
    if held:
        return {"state": "busy", "version": entry["version"], "reason": f"{held} run(s) hold a slot; next day"}
    runner = lina_runners.pins(root=root)[name]
    current = lina_runners.current_version(root, runner)
    if current == entry["version"] and runner.version == entry["version"]:
        return {"state": "same", "version": current}
    before = state.overlay(root).get(name)
    state.write_overlay(root, name, entry, why=VERB)
    if current is None:
        return {"state": "prepare", "version": entry["version"], "reason": "not installed here (`lina runners install`)"}
    said: list[str] = []
    code = lina_runners.upgrade(name, root, entry["version"], entry.get("integrity") or None, fetch=fetch, run=run,
                                out=said.append)
    if code:
        state.write_overlay(root, name, before, why=f"{VERB} failed")
        return {"state": "failed", "version": entry["version"], "current": lina_runners.current_version(root, runner),
                "reason": (" ".join(said) or f"upgrade exited {code}")[-300:]}
    return {"state": "switched", "version": entry["version"], "previous": current}


def platform() -> str | None:
    try:
        return lina_deps.platform_key()
    except lina_deps.DepsError:
        return None
