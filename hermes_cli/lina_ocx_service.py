"""The ``ocx`` service (lina#32): launchd ``ai.lina.ocx`` / systemd user unit ``lina-ocx`` running the pinned bun
and OCX ``start --port <port>`` in the foreground, written through ``lina_services.service_definition``.

It never writes or loads ``com.opencodex.proxy`` and never calls ``ocx service install``: an OCX the person already
runs on this account is left alone, and a start refuses while anything else holds the port.
"""

from __future__ import annotations

import os
import subprocess
import sys
import time
from pathlib import Path

from hermes_cli import lina_ocx

LAUNCHD_LABEL = "ai.lina.ocx"
SYSTEMD_UNIT = "lina-ocx"
START_WAIT_S = 60.0


def backend() -> str | None:
    return {"darwin": "launchd"}.get(sys.platform, "systemd" if sys.platform.startswith("linux") else None)


def definition_path(kind: str) -> Path:
    if kind == "launchd":
        from hermes_constants import get_real_home
        return Path(get_real_home()) / "Library" / "LaunchAgents" / f"{LAUNCHD_LABEL}.plist"
    from hermes_cli.gateway import user_systemd_unit_dir
    return user_systemd_unit_dir() / f"{SYSTEMD_UNIT}.service"


def definition(kind: str, root: Path, version: str, port: int) -> str:
    from hermes_cli.lina_services import service_definition
    label = LAUNCHD_LABEL if kind == "launchd" else SYSTEMD_UNIT
    return service_definition(kind, label, lina_ocx.ocx_argv(root, version, "start", "--port", str(port)),
                              lina_ocx.ocx_env(root))


def _run(argv: list[str]) -> subprocess.CompletedProcess:
    return subprocess.run(argv, capture_output=True, text=True, encoding="utf-8", errors="replace", timeout=30, check=False)


def _launchd_target() -> str:
    return f"gui/{os.getuid()}"  # windows-footgun: ok — launchd only


def _load(kind: str, path: Path) -> subprocess.CompletedProcess:
    if kind == "launchd":
        _run(["launchctl", "bootout", f"{_launchd_target()}/{LAUNCHD_LABEL}"])
        return _run(["launchctl", "bootstrap", _launchd_target(), str(path)])
    _run(["systemctl", "--user", "daemon-reload"])
    return _run(["systemctl", "--user", "restart", SYSTEMD_UNIT])


def _unload(kind: str) -> None:
    if kind == "launchd":
        _run(["launchctl", "bootout", f"{_launchd_target()}/{LAUNCHD_LABEL}"])
    else:
        _run(["systemctl", "--user", "stop", SYSTEMD_UNIT])


def state(root: Path | None = None) -> str:
    root = root or lina_ocx.lina_root()
    version = lina_ocx.current_version(root)
    if version is None:
        return "not installed"
    port = lina_ocx.configured_port(root)
    return f"running ({version}, port {port})" if lina_ocx.own_proxy_running(root, port) else f"stopped ({version})"


def start(root: Path | None = None, out=print) -> int:
    root = root or lina_ocx.lina_root()
    kind, version = backend(), lina_ocx.current_version(root)
    if kind is None:
        out(f"ocx: no launchd or systemd on {sys.platform}")
        return 1
    if version is None:
        out("ocx: not installed. Run `lina models install`.")
        return 1
    port = lina_ocx.configured_port(root)
    if lina_ocx.own_proxy_running(root, port):
        out(f"✓ ocx already running on port {port}")
        return 0
    if lina_ocx.port_taken(port):
        out(f"ocx: port {port} is already in use by another process; set ocx.port in config.yaml to a free port")
        return 1
    try:
        lina_ocx.write_start_port(lina_ocx.data_dir(root), port)
    except lina_ocx.OcxError as exc:
        out(f"ocx: {exc}")
        return 1
    path = definition_path(kind)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(definition(kind, root, version, port), encoding="utf-8")
    if kind == "systemd":
        _run(["systemctl", "--user", "enable", SYSTEMD_UNIT])
    loaded = _load(kind, path)
    if loaded.returncode != 0:
        out(f"ocx: {kind} refused {path.name}: {(loaded.stderr or loaded.stdout).strip()}")
        return 1
    deadline = time.monotonic() + START_WAIT_S
    while time.monotonic() < deadline:
        if lina_ocx.own_proxy_running(root, port):
            out(f"✓ ocx {version} running on port {port} ({path})")
            return 0
        time.sleep(0.5)
    out(f"ocx: started {path.name}, but nothing from {lina_ocx.data_dir(root)} answered on port {port}")
    return 1


def stop(root: Path | None = None, out=print) -> int:
    kind = backend()
    if kind is None or not definition_path(kind).exists():
        out("ocx: no service installed")
        return 0
    _unload(kind)
    out("✓ ocx stopped")
    return 0


def restart(root: Path | None = None, out=print) -> int:
    stop(root, out=lambda line: None)
    return start(root, out=out)


VERBS = {"start": start, "stop": stop, "restart": restart}
