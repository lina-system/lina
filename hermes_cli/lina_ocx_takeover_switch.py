"""``lina models takeover switch`` (lina#33): the step the operator runs in one quiet window.

1. Stop and unload the old OCX job; wait until its listeners' ports are free.
2. Copy the now-quiet data dir again (``takeover copy --replace``: the rehearsed copy is moved aside, the source is
   only read). The copy carries ``service-api-token``, which OCX reads on a non-loopback bind, so the agents keep
   their token.
3. Start ``ai.lina.ocx`` / ``lina-ocx`` with ``--port`` of the old main listener; the copied ``config.json`` keeps
   the old ``hostname`` and loopback listener, so OCX binds the same addresses and ports. ``ocx.port`` in the LINA
   root's ``config.yaml`` follows.
4. On every listener: ``/healthz`` names the pid in LINA's data dir and ``GET /v1/models`` answers 200 (with the
   copied token where one is required).

Any failed step stops LINA's OCX, puts ``ocx.port`` back and reloads the old job; the old data dir was never
written, so that is the whole way back. After a good switch the old job is disabled (so a login does not load it
again), the credit refresh cron entry is enabled, and the follow-ups this tool does not do are printed.
"""

from __future__ import annotations

import json
import os
import socket
import subprocess
import time
import urllib.error
import urllib.request
from pathlib import Path

from hermes_cli import lina_ocx, lina_ocx_service, lina_ocx_takeover as takeover, lina_ocx_takeover_refresh

PORTS_FREE_WAIT_S = 30.0
HEALTH_WAIT_S = 60.0
FOLLOW_UPS = (
    "Retire the old credit refresh job (it would refresh the same OAuth accounts as LINA's cron entry).",
    "Point whatever renders secrets into the old data dir at LINA's copy ({data}), so the old folder is not "
    "brought back.",
    "Remove the old job's definition ({job}) once LINA has run through a full day; it is disabled, not deleted.",
)


def _run(argv: list[str]) -> subprocess.CompletedProcess:
    return subprocess.run(argv, capture_output=True, text=True, encoding="utf-8", errors="replace", timeout=30,
                          check=False)


def _domain() -> str:
    return f"gui/{os.getuid()}"  # windows-footgun: ok — launchd only


def stop_old(job: dict) -> subprocess.CompletedProcess:
    if job["kind"] == "launchd":
        return _run(["launchctl", "bootout", f"{_domain()}/{job['name']}"])
    return _run(["systemctl", "--user", "stop", job["name"]])


def start_old(job: dict) -> subprocess.CompletedProcess:
    if job["kind"] == "launchd":
        _run(["launchctl", "enable", f"{_domain()}/{job['name']}"])
        return _run(["launchctl", "bootstrap", _domain(), job["path"]])
    return _run(["systemctl", "--user", "start", job["name"]])


def disable_old(job: dict) -> None:
    if job["kind"] == "launchd":
        _run(["launchctl", "disable", f"{_domain()}/{job['name']}"])
    else:
        _run(["systemctl", "--user", "disable", job["name"]])


def _url_host(host: str) -> str:
    return f"[{host}]" if ":" in host and not host.startswith("[") else host


def _get(listener: takeover.Listener, path: str, token: str | None) -> tuple[int, bytes]:
    request = urllib.request.Request(f"http://{_url_host(listener.host)}:{listener.port}{path}")
    if token and listener.token_required:
        request.add_header("Authorization", f"Bearer {token}")
        request.add_header("x-opencodex-api-key", token)
    try:
        with urllib.request.urlopen(request, timeout=3) as response:
            return response.status, response.read()
    except urllib.error.HTTPError as exc:
        return exc.code, b""
    except (urllib.error.URLError, OSError):
        return 0, b""


def _answers(listener: takeover.Listener) -> bool:
    with socket.socket(socket.AF_INET6 if ":" in listener.host else socket.AF_INET, socket.SOCK_STREAM) as probe:
        probe.settimeout(1)
        return probe.connect_ex((listener.host.strip("[]"), listener.port)) == 0


def _wait(condition, timeout: float) -> bool:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if condition():
            return True
        time.sleep(0.5)
    return condition()


def _own(listener: takeover.Listener, data: Path, token: str | None) -> bool:
    status, body = _get(listener, "/healthz", token)
    pid_file = data / "ocx.pid"
    if status != 200 or not pid_file.exists():
        return False
    try:
        return int(json.loads(body).get("pid")) == int(pid_file.read_text(encoding="utf-8-sig").strip())
    except (ValueError, TypeError, AttributeError):
        return False


def healthy(listeners: list[takeover.Listener], data: Path, token: str | None, out) -> bool:
    for item in listeners:
        if not _wait(lambda: _own(item, data, token) and _get(item, "/v1/models", token)[0] == 200, HEALTH_WAIT_S):
            out(f"switch: {item.role} {item.host}:{item.port} did not answer GET /v1/models 200 from LINA's OCX")
            return False
        out(f"✓ {item.role} {item.host}:{item.port}: GET /v1/models 200"
            + (" with the copied API token" if item.token_required else ""))
    return True


def start_lina(root: Path, version: str, port: int) -> subprocess.CompletedProcess:
    """``ai.lina.ocx`` / ``lina-ocx`` on *port*, written and loaded like ``lina services start ocx`` does."""
    kind = lina_ocx_service.backend()
    lina_ocx.write_start_port(lina_ocx.data_dir(root), port)
    path = lina_ocx_service.definition_path(kind)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(lina_ocx_service.definition(kind, root, version, port), encoding="utf-8")
    if kind == "systemd":
        lina_ocx_service._run(["systemctl", "--user", "enable", lina_ocx_service.SYSTEMD_UNIT])
    return lina_ocx_service._load(kind, path)


def _set_port(root: Path, port: int) -> None:
    from hermes_cli.config_backend import write_config_key
    write_config_key(root / "config.yaml", "ocx.port", port)


def _token(source: Path) -> str | None:
    path = source / "service-api-token"
    return (path.read_text(encoding="utf-8-sig").strip() or None) if path.is_file() else None


def _back(root: Path, job: dict, previous_port: int, out, *, lina_started: bool) -> int:
    if lina_started:
        lina_ocx_service.stop(root, out=lambda line: None)
        out("  stopped LINA's OCX")
    _set_port(root, previous_port)
    reloaded = start_old(job)
    out(f"  reloaded the old job {job['name']}" + ("" if reloaded.returncode == 0 else
                                                     f" (it said: {(reloaded.stderr or reloaded.stdout).strip()})"))
    out("switch failed; the old proxy and its data dir are as they were")
    return 1


def _preflight(root: Path, plan: dict, out) -> str | None:
    version = lina_ocx.current_version(root)
    if version is None:
        out("switch: OCX is not installed. Run `lina models install`.")
    elif lina_ocx_service.backend() is None:
        out("switch: no launchd or systemd here")
    elif not plan["old_job"]["found"]:
        out(f"switch: the old job's definition {plan['old_job']['path']} is missing, so it could not be reloaded "
            "on failure; pass --job")
    else:
        return version
    return None


def switch(source: Path, job: str | None = None, argv: str | None = None, root: Path | None = None,
           out=print) -> int:
    root = root or lina_ocx.lina_root()
    source = source.expanduser().resolve()
    plan = takeover.build_plan(source, argv, job)
    version = _preflight(root, plan, out)
    if version is None:
        return 1
    old, previous_port = plan["old_job"], lina_ocx.configured_port(root)
    listeners = [takeover.Listener(**item) for item in plan["listeners"]]
    main = listeners[0]
    stop_old(old)
    out(f"✓ stopped the old job {old['name']}")
    if not _wait(lambda: not any(_answers(item) for item in listeners), PORTS_FREE_WAIT_S):
        out("switch: the old listeners still answer after stopping the job")
        return _back(root, old, previous_port, out, lina_started=False)
    if takeover.copy(source, root, replace=True, out=out) != 0:
        return _back(root, old, previous_port, out, lina_started=False)
    _set_port(root, main.port)
    loaded = start_lina(root, version, main.port)
    if loaded.returncode != 0:
        out(f"switch: LINA's OCX service did not load: {(loaded.stderr or loaded.stdout).strip()}")
        return _back(root, old, previous_port, out, lina_started=True)
    if not healthy(listeners, lina_ocx.data_dir(root), _token(source), out):
        return _back(root, old, previous_port, out, lina_started=True)
    disable_old(old)
    lina_ocx_takeover_refresh.enable_job(root, out=out)
    out(f"✓ LINA's OCX {version} serves the old listeners; the old job {old['name']} is stopped and disabled")
    out("Follow-ups this tool does not do:")
    for line in FOLLOW_UPS:
        out("  - " + line.format(data=lina_ocx.data_dir(root), job=old["path"]))
    return 0
