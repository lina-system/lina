"""LINA's pinned omo (lina#34): install, upgrade, rollback and status of the omo work devices run.

``pm/lina-deps.json`` pins omo (``omo-ai``, version + npm integrity) next to the Node and OCX of lina#32, and omo is
installed the same way, with the same helpers (``lina_ocx.install_node`` / ``npm_install`` / ``set_current``):

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

import json
import os
import shlex
import subprocess
from dataclasses import dataclass
from pathlib import Path
from typing import Callable

from hermes_cli import lina_ocx

OcxError = lina_ocx.OcxError


@dataclass(frozen=True)
class OmoPin:
    package: str
    version: str
    integrity: str


def omo_pin(path: Path = lina_ocx.PIN_FILE) -> OmoPin:
    omo = json.loads(path.read_text(encoding="utf-8-sig"))["omo"]
    return OmoPin(omo["package"], omo["version"], omo["integrity"])


def vendor_dir(root: Path) -> Path:
    return root / "vendor" / "omo"


def current_version(root: Path) -> str | None:
    return lina_ocx.current_version(root, vendor_dir(root))


def entry_path(root: Path, version: str = "current") -> Path:
    """omo's CLI entry (``bin/omo.js``) of an installed version, or of ``current``."""
    return vendor_dir(root) / version / "node_modules" / omo_pin().package / "bin" / "omo.js"


def omo_task_path(root: Path) -> Path:
    """The file ocx-omo reads omo's built-in category and agent model order from."""
    return vendor_dir(root) / "current" / "node_modules" / omo_pin().package / "plugin" / "extensions" / "omo-task.js"


def agent_dir() -> Path:
    """omo's agent dir as omo resolves it: ``OMO_CODING_AGENT_DIR``, else the account's ``~/.omo/agent``."""
    configured = os.environ.get("OMO_CODING_AGENT_DIR", "").strip()
    return Path(configured).expanduser() if configured else Path.home() / ".omo" / "agent"


def shim_path(root: Path) -> Path:
    return root / "bin" / "omo"


def node_bin(root: Path) -> Path:
    return lina_ocx.node_dir(root, lina_ocx.pins().node_version) / "bin" / "node"


def omo_env() -> dict[str, str]:
    """The person's environment with omo held on Node. omo is the person's own tool and its home is the account's
    ``~/.omo``, so HOME is not moved to a profile home and its own credentials are not scrubbed."""
    from tools.environments.local import build_subprocess_env
    return build_subprocess_env(scrub_secrets=False, inherit_profile_home=False, extra={"OMO_RUNTIME": "node"})


def write_shim(root: Path, node: Path) -> Path:
    shim = shim_path(root)
    shim.parent.mkdir(parents=True, exist_ok=True)
    tmp = shim.with_name(".omo.tmp")
    tmp.write_text("#!/bin/sh\n"
                   "# LINA's pinned omo (lina#34), written by `lina omo install`: vendor/omo/current on the pinned Node.\n"
                   "# OMO_RUNTIME=node keeps omo on this Node even when a bun is on PATH.\n"
                   "export OMO_RUNTIME=node\n"
                   f"exec {shlex.quote(str(node / 'bin' / 'node'))} {shlex.quote(str(entry_path(root)))} \"$@\"\n",
                   encoding="utf-8")
    tmp.chmod(0o755)
    os.replace(tmp, shim)
    return shim


def reported_version(root: Path, node: Path, version: str,
                     run: Callable[..., subprocess.CompletedProcess] = subprocess.run) -> str | None:
    """The version ``omo --version`` of an installed *version* prints (``omo 5.1.69 (engine: …)``), else None."""
    try:
        result = run([str(node / "bin" / "node"), str(entry_path(root, version)), "--version"], env=omo_env(),
                     capture_output=True, text=True, encoding="utf-8", errors="replace", timeout=120, check=False)
    except (OSError, subprocess.TimeoutExpired):
        return None
    words = (result.stdout or "").split()
    return words[1] if result.returncode == 0 and len(words) > 1 and words[0] == "omo" else None


def _install_version(root: Path, version: str, integrity: str, fetch, run) -> Path:
    """Node, then omo *version* beside the others; it must answer ``--version`` with *version*."""
    node = lina_ocx.install_node(root, lina_ocx.pins(), fetch)
    lina_ocx.npm_install(root, node, vendor_dir(root), omo_pin().package, version, integrity, run)
    got = reported_version(root, node, version, run)
    if got != version:
        raise OcxError(f"omo {version} installed but `omo --version` reports {got or 'nothing'}")
    return node


def install(root: Path | None = None, *, fetch: Callable[[str, Path], None] = lina_ocx.download,
            run: Callable[..., subprocess.CompletedProcess] = subprocess.run, out=print) -> int:
    """``lina omo install`` without the routing step: the pinned Node and omo, ``current`` and the shim."""
    from hermes_cli import lina_omo_routing
    root = root or lina_ocx.lina_root()
    pin = omo_pin()
    node = _install_version(root, pin.version, pin.integrity, fetch, run)
    out(f"✓ omo {pin.version}: {vendor_dir(root) / pin.version} ({pin.integrity[:20]}…)")
    active = current_version(root)
    if active is None:
        lina_ocx.set_current(root, pin.version, vendor_dir(root))
        active = pin.version
    if active != pin.version:
        out(f"  current stays {active}; `lina omo upgrade {pin.version}` switches to the pin")
    out(f"✓ shim: {write_shim(root, node)} (omo {active} on Node {lina_ocx.pins().node_version}; "
        "any other omo on this device is left alone)")
    out(f"✓ ocx-omo: {lina_omo_routing.build(root, node, run=run)}")
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


def _previous_file(root: Path) -> Path:
    return vendor_dir(root) / "previous"


def upgrade(root: Path | None = None, version: str | None = None, integrity: str | None = None, *,
            fetch: Callable[[str, Path], None] = lina_ocx.download,
            run: Callable[..., subprocess.CompletedProcess] = subprocess.run, out=print) -> int:
    """Install *version* beside the current one, check ``omo --version``, then switch ``current``."""
    root = root or lina_ocx.lina_root()
    pin = omo_pin()
    version = version or pin.version
    integrity = integrity or (pin.integrity if version == pin.version else None)
    if integrity is None:
        out(f"omo {version} is not the pinned version ({pin.version}): pass its npm integrity with --integrity")
        return 1
    previous = current_version(root)
    if previous is None:
        out("omo is not installed. Run `lina omo install`.")
        return 1
    if previous == version:
        out(f"✓ omo {version} is already current")
        return 0
    try:
        node = _install_version(root, version, integrity, fetch, run)
    except OcxError as exc:
        out(f"omo: {exc}; current stays {previous}")
        return 1
    _previous_file(root).write_text(previous + "\n", encoding="utf-8")
    lina_ocx.set_current(root, version, vendor_dir(root))
    write_shim(root, node)
    out(f"✓ current: {previous} -> {version} (`lina omo rollback` switches back)")
    _handoff_hint(root, out, run)
    return 0


def rollback(root: Path | None = None, *, run: Callable[..., subprocess.CompletedProcess] = subprocess.run,
             out=print) -> int:
    root = root or lina_ocx.lina_root()
    record = _previous_file(root)
    previous = record.read_text(encoding="utf-8-sig").strip() if record.exists() else ""
    if not previous or not entry_path(root, previous).exists():
        out("Nothing to roll back to: no upgrade recorded a previous omo version")
        return 1
    current = current_version(root)
    lina_ocx.set_current(root, previous, vendor_dir(root))
    record.unlink()
    out(f"✓ current: {current} -> {previous}")
    _handoff_hint(root, out, run)
    return 0


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
