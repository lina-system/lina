"""LINA's pinned omo (lina#34): install, upgrade, rollback and status of the omo work devices run.

``pm/lina-deps.json`` describes omo as one runner block (``runners.omo``, lina#531) like every other worker tool, and
``lina omo install|upgrade|rollback`` are ``lina_runners``' install, upgrade and rollback of that block; what stays
here is omo's own daemon (status and the handoff hint) and the omo command line:

* ``vendor/omo/<ver>/``: omo installed by the pinned Node's npm with ``--prefix``; the lock must carry the pinned
  integrity. ``vendor/omo/current`` points at the active version, ``vendor/omo/previous`` records the one before
  the last upgrade.
* ``bin/omo``: a shim that runs ``vendor/omo/current`` on the pinned Node. ``OMO_RUNTIME=node`` keeps omo there:
  without it omo re-executes itself under any ``bun`` on PATH (and then writes ``~/.bun``).

Nothing is installed globally, and an omo the person installed on their own (``bun add -g``, the desktop app) is
never touched. omo's home stays ``~/.omo``; LINA only writes the routing keys of ``~/.omo/omo.jsonc``, through
``lina omo routing apply`` (``lina_omo_routing.py``). omo is not open source (Sustainable Use License 1.0): it is
fetched from npm onto the person's machine at install time, never vendored into this repo or a release artifact.

An upgrade never restarts a running omo daemon: it prints the one command that hands the daemon over to the new
version, to be run when the device is idle (draining a device is ``lina worker``'s job, lina#35).
"""

from __future__ import annotations

import dataclasses
import json
import os
import subprocess
from dataclasses import dataclass
from pathlib import Path
from typing import Callable

from hermes_cli import lina_ocx, lina_runners

OcxError = lina_ocx.OcxError
NAME = "omo"  # omo's runner block (lina#531)


@dataclass(frozen=True)
class OmoPin:
    package: str
    version: str
    integrity: str


def omo_pin(path: Path = lina_ocx.PIN_FILE) -> OmoPin:
    """omo's block in ``pm/lina-deps.json`` ``runners`` (lina#531), with the config block and the daily sync's move."""
    block = lina_runners.pins(path)[NAME]
    return OmoPin(block.package, block.version, block.integrity)


def runner() -> lina_runners.RunnerPin:
    """omo's runner block with the pinned version: every install step below is ``lina_runners``' code."""
    pin = omo_pin()
    return dataclasses.replace(lina_runners.pins()[NAME], package=pin.package, version=pin.version,
                               integrity=pin.integrity)


def vendor_dir(root: Path) -> Path:
    return lina_runners.vendor_dir(root, runner())


def current_version(root: Path) -> str | None:
    return lina_ocx.current_version(root, vendor_dir(root))


def entry_path(root: Path, version: str = "current") -> Path:
    """omo's CLI entry (``bin/omo.js``) of an installed version, or of ``current``."""
    return lina_runners.entry_path(root, runner(), version)


def omo_task_path(root: Path) -> Path:
    """The file ocx-omo reads omo's built-in category and agent model order from."""
    return vendor_dir(root) / "current" / "node_modules" / omo_pin().package / "plugin" / "extensions" / "omo-task.js"


def agent_dir() -> Path:
    """omo's agent dir as omo resolves it: ``OMO_CODING_AGENT_DIR``, else the account's ``~/.omo/agent``."""
    configured = os.environ.get("OMO_CODING_AGENT_DIR", "").strip()
    return Path(configured).expanduser() if configured else Path.home() / ".omo" / "agent"


def shim_path(root: Path) -> Path:
    return lina_runners.shim_path(root, runner())


def node_bin(root: Path) -> Path:
    return lina_ocx.node_dir(root, lina_ocx.pins().node_version) / "bin" / "node"


def omo_env() -> dict[str, str]:
    """The person's environment with omo held on Node. omo is the person's own tool and its home is the account's
    ``~/.omo``, so HOME is not moved to a profile home and its own credentials are not scrubbed."""
    from tools.environments.local import build_subprocess_env
    return build_subprocess_env(scrub_secrets=False, inherit_profile_home=False, extra={"OMO_RUNTIME": "node"})


def install(root: Path | None = None, *, fetch: Callable[[str, Path], None] = lina_ocx.download,
            run: Callable[..., subprocess.CompletedProcess] = subprocess.run, out=print) -> int:
    """``lina omo install`` without the routing step: ``lina_runners.install_pin`` of omo's block, then its route
    source prepared (``lina_runner_routes``)."""
    from hermes_cli import lina_runner_routes
    root = root or lina_ocx.lina_root()
    block = runner()
    lina_runners.install_pin(root, block, fetch=fetch, run=run, out=out)
    prepared = lina_runner_routes.prepare(root, block, run=run)
    if prepared:
        out(f"✓ {block.route.get('source')}: {prepared}")
    return 0


def daemon(root: Path, run: Callable[..., subprocess.CompletedProcess] = subprocess.run) -> dict | None:
    """``omo host status`` through the shim (read-only), parsed; None when it cannot be asked."""
    if not shim_path(root).exists():
        return None
    try:
        result = run([str(shim_path(root)), "host", "status", "--json"], env=omo_env(), capture_output=True,
                     text=True, encoding="utf-8", errors="replace", timeout=30, check=False)
        status = json.loads((result.stdout or "").strip().splitlines()[-1])
    except (OSError, subprocess.TimeoutExpired, ValueError, IndexError):
        return None
    return status if isinstance(status, dict) else None


def _handoff_hint(root: Path, out, run) -> None:
    status = daemon(root, run)
    if status and status.get("reachable"):
        out(f"  omo's daemon (pid {status.get('pid')}) keeps running the old version; when the device is idle, "
            f"move it with: {shim_path(root)} host handoff")


def upgrade(root: Path | None = None, version: str | None = None, integrity: str | None = None, *,
            fetch: Callable[[str, Path], None] = lina_ocx.download,
            run: Callable[..., subprocess.CompletedProcess] = subprocess.run, out=print) -> int:
    """``lina_runners.upgrade_pin`` of omo's block; a running daemon is left on the old version (one hint)."""
    root = root or lina_ocx.lina_root()
    code = lina_runners.upgrade_pin(root, runner(), version, integrity, fetch=fetch, run=run, out=out)
    if code == 0:
        _handoff_hint(root, out, run)
    return code


def rollback(root: Path | None = None, *, run: Callable[..., subprocess.CompletedProcess] = subprocess.run,
             out=print) -> int:
    root = root or lina_ocx.lina_root()
    code = lina_runners.rollback_pin(root, runner(), out=out)
    if code == 0:
        _handoff_hint(root, out, run)
    return code


def installed_versions(root: Path) -> list[str]:
    base = vendor_dir(root)
    return sorted(path.name for path in base.iterdir() if path.name != "current" and not path.name.startswith(".")
                  and path.is_dir() and entry_path(root, path.name).exists()) if base.is_dir() else []


def status(root: Path | None = None, *, run: Callable[..., subprocess.CompletedProcess] = subprocess.run) -> dict:
    """``lina omo status``: pin, installed versions, ``current``, the daemon and the routing check (all read-only)."""
    from hermes_cli import lina_omo_routing
    root = root or lina_ocx.lina_root()
    host = daemon(root, run)
    code, output = lina_omo_routing.check(root, run=run)
    return {"pinned": omo_pin().version, "installed": installed_versions(root), "current": current_version(root),
            "shim": str(shim_path(root)) if shim_path(root).exists() else None,
            "daemon": {"running": bool(host and host.get("reachable")), "pid": (host or {}).get("pid"),
                       "engine": (host or {}).get("engineVersion")} if host is not None else None,
            "routing": {"exit": code, "state": lina_omo_routing.CHECK_STATES.get(code, "error"), "output": output}}


def render_status(data: dict) -> list[str]:
    host = data["daemon"]
    daemon_line = ("unknown (omo not installed or `omo host status` did not answer)" if host is None else
                   f"running (pid {host['pid']}, engine {host['engine']})" if host["running"] else "not running")
    routing = data["routing"]
    lines = [f"pinned:    omo {data['pinned']}",
             f"installed: {', '.join(data['installed']) or '(none)'}",
             f"current:   {data['current'] or '(none: run `lina omo install`)'}",
             f"daemon:    {daemon_line}",
             f"routing:   {routing['state']} (ocx-omo check exit {routing['exit']})"]
    return lines + [f"  {line}" for line in routing["output"].splitlines() if line.strip()]
