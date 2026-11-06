"""The ``worker`` service (lina#35): launchd ``ai.lina.worker`` / systemd user unit ``lina-worker`` running
``lina worker serve`` in the foreground, written through ``lina_services.service_definition``.

Runs live in their own transient units or sessions, so stopping or restarting the service never ends a run; the
next ``serve`` picks their ends up from the task folders.
"""

from __future__ import annotations

import os
import subprocess
import sys
import time
from pathlib import Path

from hermes_cli.worker import device, serve, settings

LAUNCHD_LABEL = "ai.lina.worker"
SYSTEMD_UNIT = "lina-worker"
START_WAIT_S = 30.0


def backend() -> str | None:
    return {"darwin": "launchd"}.get(sys.platform, "systemd" if sys.platform.startswith("linux") else None)


def definition_path(kind: str) -> Path:
    if kind == "launchd":
        from hermes_constants import get_real_home
        return Path(get_real_home()) / "Library" / "LaunchAgents" / f"{LAUNCHD_LABEL}.plist"
    from hermes_cli.gateway import user_systemd_unit_dir
    return user_systemd_unit_dir() / f"{SYSTEMD_UNIT}.service"


def definition(kind: str, base: Path) -> str:
    from hermes_cli.lina_services import service_definition, service_logs
    label = LAUNCHD_LABEL if kind == "launchd" else SYSTEMD_UNIT
    env = {"HERMES_HOME": str(base), "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
           "PYTHONPATH": settings.child_pythonpath()}
    return service_definition(kind, label, [sys.executable, "-m", "hermes_cli.main", "worker", "serve"], env,
                              logs=service_logs(base, "worker"))


def _run(argv: list[str]) -> subprocess.CompletedProcess:
    return subprocess.run(argv, capture_output=True, text=True, encoding="utf-8", errors="replace", timeout=30,
                          check=False)


def _launchd_target() -> str:
    return f"gui/{os.getuid()}"  # windows-footgun: ok — launchd only


def running(base: Path | None = None) -> bool:
    """The service loop wrote its heartbeat within the last minute."""
    age = serve.heartbeat_age(settings.root(base))
    return age is not None and age < 60


def state(base: Path | None = None) -> str:
    kind = backend()
    if kind is None or not definition_path(kind).exists():
        return "not installed"
    return "running" if running(base) else "stopped"


def start(base: Path | None = None, out=print) -> int:
    from hermes_cli.lina_ocx import lina_root
    base = base or lina_root()
    kind = backend()
    if kind is None:
        out(f"worker: no launchd or systemd on {sys.platform}")
        return 1
    if running(base):
        out("✓ worker already running")
        return 0
    path = definition_path(kind)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(definition(kind, base), encoding="utf-8")
    if kind == "launchd":
        _run(["launchctl", "bootout", f"{_launchd_target()}/{LAUNCHD_LABEL}"])
        loaded = _run(["launchctl", "bootstrap", _launchd_target(), str(path)])
    else:
        _run(["systemctl", "--user", "daemon-reload"])
        _run(["systemctl", "--user", "enable", SYSTEMD_UNIT])
        loaded = _run(["systemctl", "--user", "restart", SYSTEMD_UNIT])
    if loaded.returncode != 0:
        out(f"worker: {kind} refused {path.name}: {(loaded.stderr or loaded.stdout).strip()}")
        return 1
    deadline = time.monotonic() + START_WAIT_S
    while time.monotonic() < deadline:
        if running(base):
            out(f"✓ worker running ({path})")
            return 0
        time.sleep(0.5)
    out(f"worker: started {path.name}, but `lina worker serve` wrote no heartbeat under {settings.root(base)}")
    return 1


def stop(base: Path | None = None, out=print) -> int:
    kind = backend()
    if kind is None or not definition_path(kind).exists():
        out("worker: no service installed")
        return 0
    if kind == "launchd":
        _run(["launchctl", "bootout", f"{_launchd_target()}/{LAUNCHD_LABEL}"])
    else:
        _run(["systemctl", "--user", "stop", SYSTEMD_UNIT])
    # The stopped loop's last heartbeat stays fresh for a minute: without this, `restart` reads it as "already
    # running" and never starts the service again.
    (settings.root(base) / device.HEARTBEAT).unlink(missing_ok=True)
    out("✓ worker stopped (running tasks keep running)")
    return 0


def restart(base: Path | None = None, out=print) -> int:
    stop(base, out=lambda line: None)
    return start(base, out=out)


VERBS = {"start": start, "stop": stop, "restart": restart}
