"""The ``web`` service (lina#288): launchd ``ai.lina.web`` / systemd user unit ``lina-web`` running the dashboard
server of the LINA root in the foreground (the app at ``/app/``, lina#168), written through
``lina_services.service_definition``.

It is LINA's own server, beside a Hermes Agent dashboard the same account may run:

* The one-backend-per-host record (``gateway/host_rendezvous.py``) lives in a per-account folder Hermes Agent's
  backend uses too. The job gets its own folder under the LINA root (``HERMES_GATEWAY_LOCK_DIR``) and
  ``--isolated``, so it never attaches to, or refuses behind, Hermes Agent's backend, and never publishes itself
  as the backend Hermes Agent's ``dashboard`` attaches to.
* Stop and restart unload this label only; ``dashboard --stop``'s process scan is never used.
* The job never builds the UI: ``--skip-build`` with ``HERMES_WEB_DIST`` pinned to the install's build (no recovery
  build), and start refuses without that build.
"""

from __future__ import annotations

import os
import socket
import subprocess
import sys
import time
from pathlib import Path

LAUNCHD_LABEL = "ai.lina.web"
SYSTEMD_UNIT = "lina-web"
START_WAIT_S = 60.0
_WILDCARD = frozenset({"0.0.0.0", "::", "*", ""})


def backend() -> str | None:
    return {"darwin": "launchd"}.get(sys.platform, "systemd" if sys.platform.startswith("linux") else None)


def definition_path(kind: str) -> Path:
    if kind == "launchd":
        from hermes_constants import get_real_home
        return Path(get_real_home()) / "Library" / "LaunchAgents" / f"{LAUNCHD_LABEL}.plist"
    from hermes_cli.gateway import user_systemd_unit_dir
    return user_systemd_unit_dir() / f"{SYSTEMD_UNIT}.service"


def web_dist() -> Path:
    """The install's built dashboard UI, never a ``HERMES_WEB_DIST`` the starting shell has."""
    return Path(__file__).resolve().parent / "web_dist"


def lock_dir(root: Path) -> Path:
    return root / "web" / "host-locks"


class EndpointError(ValueError):
    """``dashboard.port`` of the LINA root is not a port number."""


def endpoint(root: Path) -> tuple[str, int]:
    """``dashboard.host`` and ``dashboard.port`` of the LINA root through the config loader (defaults, managed
    overlay, ``${VAR}`` expansion), bound to the root whatever profile this process runs as."""
    from hermes_cli.config import cfg_get, load_config_readonly
    from hermes_constants import reset_hermes_home_override, set_hermes_home_override
    token = set_hermes_home_override(root)
    try:
        config = load_config_readonly()
    finally:
        reset_hermes_home_override(token)
    host, port = cfg_get(config, "dashboard", "host"), cfg_get(config, "dashboard", "port")
    text = str(port).strip()
    if isinstance(port, bool) or not text.isdigit() or not 0 < int(text) < 65536:
        raise EndpointError(f"dashboard.port in {root / 'config.yaml'} is not a port number (1-65535)")
    return str(host), int(text)


def definition(kind: str, root: Path) -> str:
    from hermes_cli.lina_services import service_definition, service_logs
    from hermes_cli.worker import settings
    label = LAUNCHD_LABEL if kind == "launchd" else SYSTEMD_UNIT
    host, port = endpoint(root)
    # `-p default`: the root itself, never the profile a person last picked with `lina profile use` (active_profile).
    argv = [sys.executable, "-m", "hermes_cli.main", "-p", "default", "dashboard", "--host", host, "--port", str(port),
            "--no-open", "--skip-build", "--isolated"]
    env = {"HERMES_HOME": str(root), "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
           "PYTHONPATH": settings.child_pythonpath(), "HERMES_WEB_DIST": str(web_dist()),
           "HERMES_GATEWAY_LOCK_DIR": str(lock_dir(root))}
    return service_definition(kind, label, argv, env, logs=service_logs(root, "web"))


def _run(argv: list[str]) -> subprocess.CompletedProcess:
    return subprocess.run(argv, capture_output=True, text=True, encoding="utf-8", errors="replace", timeout=30,
                          check=False)


def _launchd_target() -> str:
    return f"gui/{os.getuid()}"  # windows-footgun: ok — launchd only


def _answers(host: str, port: int) -> bool:
    try:
        with socket.create_connection(("127.0.0.1" if host in _WILDCARD else host, port), timeout=1):
            return True
    except OSError:
        return False


def own_pids(root: Path) -> list[int]:
    """Dashboard processes whose live ``HERMES_HOME`` is this LINA root (read-only; another home's never count)."""
    from hermes_cli.main_dashboard import _find_stale_dashboard_pids
    return _find_stale_dashboard_pids(scope_home=str(root))


def running(root: Path) -> bool:
    try:
        host, port = endpoint(root)
    except EndpointError:
        return False
    return bool(own_pids(root)) and _answers(host, port)


def state(root: Path | None = None) -> str:
    from hermes_cli.lina_ocx import lina_root
    root = root or lina_root()
    kind = backend()
    if kind is None or not definition_path(kind).exists():
        return "not installed"
    try:
        endpoint(root)
    except EndpointError as exc:
        return f"stopped ({exc})"
    return "running" if running(root) else "stopped"


def start(root: Path | None = None, out=print) -> int:
    from hermes_cli.lina_ocx import lina_root
    root = root or lina_root()
    kind = backend()
    if kind is None:
        out(f"web: no launchd or systemd on {sys.platform}")
        return 1
    dist = web_dist()
    if not (dist / "index.html").is_file():
        out(f"web: no built web UI at {dist}; build it first (npm run build -w web), the service never builds it")
        return 1
    try:
        host, port = endpoint(root)
    except EndpointError as exc:
        out(f"web: {exc}")
        return 1
    if running(root):
        out("✓ web already running")
        return 0
    if _answers(host, port):
        out("web: the configured dashboard.port is already in use by another process; set dashboard.port in "
            "config.yaml to a free port")
        return 1
    path = definition_path(kind)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(definition(kind, root), encoding="utf-8")
    if kind == "launchd":
        _run(["launchctl", "bootout", f"{_launchd_target()}/{LAUNCHD_LABEL}"])
        loaded = _run(["launchctl", "bootstrap", _launchd_target(), str(path)])
    else:
        _run(["systemctl", "--user", "daemon-reload"])
        _run(["systemctl", "--user", "enable", SYSTEMD_UNIT])
        loaded = _run(["systemctl", "--user", "restart", SYSTEMD_UNIT])
    if loaded.returncode != 0:
        out(f"web: {kind} refused {path.name} (exit {loaded.returncode}); see the logs in {root / 'logs'}")
        return 1
    deadline = time.monotonic() + START_WAIT_S
    while time.monotonic() < deadline:
        if running(root):
            out(f"✓ web running ({path}); the app is at /app/ on the configured dashboard.host/dashboard.port")
            return 0
        time.sleep(0.5)
    out(f"web: started {path.name}, but no dashboard of {root} answered on the configured "
        f"dashboard.host/dashboard.port (see {root / 'logs'})")
    return 1


def stop(root: Path | None = None, out=print) -> int:
    """Unload this service's own label; another dashboard on the account (Hermes Agent's) is never touched."""
    from hermes_cli.lina_ocx import lina_root
    root = root or lina_root()
    kind = backend()
    if kind is None or not definition_path(kind).exists():
        out("web: no service installed")
        return 0
    if kind == "launchd":
        _run(["launchctl", "bootout", f"{_launchd_target()}/{LAUNCHD_LABEL}"])
    else:
        _run(["systemctl", "--user", "stop", SYSTEMD_UNIT])
    deadline = time.monotonic() + START_WAIT_S
    while running(root) and time.monotonic() < deadline:  # restart must not find the old server still answering
        time.sleep(0.2)
    out("✓ web stopped")
    return 0


def restart(root: Path | None = None, out=print) -> int:
    stop(root, out=lambda line: None)
    return start(root, out=out)


VERBS = {"start": start, "stop": stop, "restart": restart}
