"""The node's own model proxy service, by its kind (lina#589): what the device screen shows of it and the one restart
it offers, through the node's ``service proxy status|restart`` verb.

* ``ocx``: LINA's OCX service, read and restarted as ``service ocx`` does (``lina_services``), refusals included.
* ``magpie``: the node's local magpie service, the systemd user unit / launchd label ``worker.proxy.service`` names
  (default ``magpie``). Only that service is restarted; LINA never installs or configures magpie and never touches
  its accounts or its link to the hub.

A restart is refused while a run holds a slot on the node: its model calls go through the proxy.
"""

from __future__ import annotations

import os
import subprocess
from pathlib import Path

from hermes_cli.worker import proxy

TIMEOUT_S = 30


def _run(argv: list[str]) -> subprocess.CompletedProcess | None:
    try:
        return subprocess.run(argv, capture_output=True, text=True, encoding="utf-8", errors="replace",
                              timeout=TIMEOUT_S, check=False)
    except (OSError, subprocess.TimeoutExpired):
        return None


def _backend() -> str | None:
    from hermes_cli import lina_ocx_service
    return lina_ocx_service.backend()


def _launchd(label: str) -> str:
    return f"gui/{os.getuid()}/{label}"  # windows-footgun: ok — launchd only


def magpie_state(unit: str) -> str:
    """``running``, ``stopped``, ``not installed`` or ``unknown`` (no launchd or systemd, or it did not answer)."""
    kind = _backend()
    if kind == "launchd":
        done = _run(["launchctl", "print", _launchd(unit)])
        if done is None:
            return "unknown"
        if done.returncode != 0:
            return "not installed"
        return "running" if "state = running" in done.stdout else "stopped"
    if kind == "systemd":
        done = _run(["systemctl", "--user", "show", "--property=LoadState,ActiveState", unit])
        if done is None or done.returncode != 0:
            return "unknown"
        found = dict(line.split("=", 1) for line in done.stdout.splitlines() if "=" in line)
        if found.get("LoadState") == "not-found":
            return "not installed"
        return "running" if found.get("ActiveState") == "active" else "stopped"
    return "unknown"


def _magpie_restart(unit: str) -> tuple[bool, str]:
    kind = _backend()
    if kind == "launchd":
        argv = ["launchctl", "kickstart", "-k", _launchd(unit)]
    elif kind == "systemd":
        argv = ["systemctl", "--user", "restart", unit]
    else:
        return False, "no launchd or systemd on this node"
    done = _run(argv)
    if done is None:
        return False, f"{argv[0]} did not answer"
    return done.returncode == 0, " ".join((done.stderr or done.stdout).split())


def state(worker_root: Path) -> dict:
    """``{name, state}`` of the node's proxy service, by kind."""
    from hermes_cli import lina_services
    from hermes_cli.worker import device
    kind = device.node_proxy(worker_root)
    if kind.kind == proxy.OCX:
        return {"name": "ocx", "state": lina_services.state(lina_services.BY_NAME["ocx"])}
    return {"name": kind.service, "state": magpie_state(kind.service)}


def act(worker_root: Path, action: str, ocx_act) -> dict:
    """``status`` or ``restart`` of the node's proxy service. *ocx_act* is ``service ocx``'s own act, unchanged."""
    from hermes_cli.worker import device, slots
    kind = device.node_proxy(worker_root).kind
    head = {"service": "proxy", "kind": kind}
    if action == "status":
        return {**head, "action": "status", **state(worker_root)}
    with slots._locked(worker_root):  # admit and reserve take this lock: no slot is granted between the check and the restart
        held = slots.held(worker_root)
        if held:
            return {**head, "action": "error", "busy": held,
                    "reason": f"{held} run(s) active on this node: the {kind} restart waits until they end"}
        if kind == proxy.OCX:
            return {**ocx_act("restart"), **head}
        unit = device.node_proxy(worker_root).service
        done, said = _magpie_restart(unit)
        if not done:
            return {**head, "action": "error", "reason": said or f"magpie service {unit} did not restart"}
        return {**head, "action": "restart", "name": unit, "state": magpie_state(unit)}
