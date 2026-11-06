"""The devices view of the client API (lina#234): every entry of ``devices.workers`` by its role, and the owner's
two device actions.

Nodes (``role: work`` and ``role: files``) are read through one negotiated contact each (``transport.for_device``):
its first call is ``hello``, then ``probe``, so a node costs the same two ssh calls as before. The probe gives the
machine row (``worker.board.machine``); the same contact's ``hello`` descriptor gives one runner cell per runner the
node reports (lina#222, #218): the hub's pinned (fleet) version, the installed one, the sign-in state and one reason
when the runner is not ready. A node that cannot be reached shows the runners the hub offers, each ``unreachable``;
one whose ``hello`` predates the runner rows shows them ``update``. The omo cell comes from ``lina omo status`` on
the node, which is slow: each node's last answer is served and a background read refreshes it at most every 10
minutes, started by a read (a failed read keeps the last answer).

Person devices (``role: person``, the entry's name is its ssh alias) are never nodes: no probe, no hello, no
service. One ssh call in the person's login shell prints the first line of ``--version`` of each runner the hub
pins and of omo, at most every 15 minutes in the background, started by a read; a failed read marks the device
unreachable and keeps the last versions. The read changes nothing on the device.

``restart`` and ``sign_in`` are the owner's actions (the route checks the owner-device gate first). Both write one
line to ``<LINA root>/board/device-actions.jsonl``; a sign-in, which can take long, writes a second line when it
ends. No sign-in address, code or token is ever put in a reply, the log file or the process log.
"""

from __future__ import annotations

import importlib
import json
import logging
import re
import shlex
import subprocess
import threading
import time
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from typing import Callable

from hermes_cli.worker import settings
from hermes_cli.worker.transport import for_device

logger = logging.getLogger(__name__)

OMO_EVERY_S = 600
PERSON_EVERY_S = 900
PERSON_TIMEOUT_S = 30
SSH_UNREACHABLE = 255
NODE_ROLES = ("work", "files")
STATES = ("down", "warn", "ok")
SIGN_IN = ("signed_in", "signed_out", "api_key", "ocx", "unknown")
WHY = {"update": "update", "prepare": "prepare", "sign in": "sign_in", "sign_in": "sign_in", "unknown": "unknown"}
SERVICES = ("worker", "ocx")
SIGN_IN_MODULE = "hermes_cli.lina_sign_in"
ACTIONS_LOG = Path("board") / "device-actions.jsonl"
_VERSION = re.compile(r"\d+\.\d+\.\d+(?:-[0-9A-Za-z.\-]+)?")
# What a sign-in reason may carry that must never be shown or kept: an address, a device code, a long token.
_SECRETISH = re.compile(r"\S*://\S*|\b[A-Z0-9]{4,}-[A-Z0-9]{4,}\b|\b[A-Za-z0-9_\-]{24,}\b")
_STATE_WORD = re.compile(r"^[a-z_]{1,32}$")


def _spawn(work: Callable[[], None]) -> None:
    from agent.memory_provider import spawn_context_thread
    spawn_context_thread(work, name="lina-board-devices").start()


class _Lazy:
    """Each key's last answer; a read starts a background refresh when the last one began *every* seconds ago or
    more, and the refresh gets the previous answer to decide what to keep."""

    def __init__(self, every: float):
        self.every = every
        self.lock = threading.Lock()
        self.values: dict = {}
        self.started: dict = {}
        self.busy: set = set()

    def get(self, key, refresh: Callable[[object], object]):
        now = time.time()
        with self.lock:
            value = self.values.get(key)
            due = key not in self.busy and now - self.started.get(key, float("-inf")) >= self.every
            if due:
                self.busy.add(key)
                self.started[key] = now
        if not due:
            return value
        _spawn(lambda: self._run(key, refresh, value))
        with self.lock:
            return self.values.get(key)

    def _run(self, key, refresh, previous) -> None:
        try:
            value = refresh(previous)
        except Exception:  # a device read in the background must never die silently or take the view down
            logger.warning("device background read %s failed", key, exc_info=True)
            value = previous
        with self.lock:
            self.values[key] = value
            self.busy.discard(key)

    def reset(self) -> None:
        with self.lock:
            self.values.clear()
            self.started.clear()
            self.busy.clear()


OMO = _Lazy(OMO_EVERY_S)
PERSON = _Lazy(PERSON_EVERY_S)


def _workers(base: Path) -> dict:
    devices = settings._config(base).get("devices")
    workers = devices.get("workers") if isinstance(devices, dict) else None
    return workers if isinstance(workers, dict) else {}


def role(base: Path, name: str) -> str:
    """The entry's role (``work`` when unset). Read from the config entry until ``Device`` carries it (lina#229)."""
    entry = _workers(base).get(name)
    value = entry.get("role", "work") if isinstance(entry, dict) else "work"
    return value if value in (*NODE_ROLES, "person") else "work"


def person_names(base: Path) -> list[str]:
    return [str(name) for name, entry in _workers(base).items()
            if isinstance(entry, dict) and entry.get("role") == "person" and entry.get("off") is not True]


def _contact(dev: settings.Device, worker_root: Path, runner: str) -> tuple[dict, dict | None, bool]:
    """``(probe, descriptor, reachable)`` from one negotiated contact: ``hello`` first, then ``probe``."""
    contact = for_device(dev, worker_root)
    reply = contact.call(["probe", "", runner], timeout=60)
    reachable = dev.local or reply.code != SSH_UNREACHABLE
    if reply.code == 0:
        probe = reply.json() or {"error": "no answer"}
    elif reply.json().get("protocol_error"):
        probe = reply.json()
    else:
        probe = {"error": (reply.err.strip() or f"exit {reply.code}")[-160:]}
    return probe, contact.descriptor, reachable


def _fleet() -> dict:
    from hermes_cli.lina_runners import pins
    return {name: pin.version for name, pin in pins().items()}


def _omo_fleet() -> str | None:
    from hermes_cli.lina_omo import omo_pin
    return omo_pin().version


def _cell(name: str, kind, fleet, installed, sign_in, ready, why) -> dict:
    return {"name": name, "type": kind, "fleet": fleet, "installed": installed,
            "signIn": sign_in if sign_in in SIGN_IN else None, "ready": ready, "why": why}


def runner_cell(row: dict, fleet: dict) -> dict:
    """One runner the node reports. A runner LINA does not pin has no fleet version and ``ready`` null; a pinned
    one whose installed version is not the hub's fleet version needs an update, whatever the node says."""
    name = str(row.get("name") or row.get("pin") or "?")
    if row.get("pinned") is None:
        return _cell(name, row.get("type"), None, row.get("installed"), row.get("sign_in"), None, None)
    target = fleet.get(row.get("pin") or name)
    installed = row.get("installed")
    ready = bool(row.get("ready"))
    why = None if ready and row.get("why") is None else WHY.get(str(row.get("why")), "unknown")
    if installed is not None and target is not None and installed != target:
        ready, why = False, "update"
    return _cell(name, row.get("type"), target, installed, row.get("sign_in"), ready, why)


def omo_cell(name: str, kind, status: dict | None, probe_ready: bool | None, fleet: str | None) -> dict:
    """omo on a node: its own pin and ``lina omo status`` (served from the last background read)."""
    if status is None:
        return _cell(name, kind, fleet, None, "ocx", False, "unknown")
    installed = status.get("current")
    daemon = status.get("daemon") or {}
    ready = probe_ready if probe_ready is not None else bool(daemon.get("running"))
    why = ("prepare" if installed is None else "update" if fleet is not None and installed != fleet
           else None if ready else "prepare")
    return _cell(name, kind, fleet, installed, "ocx", why is None, why)


def _omo_status(key, dev: settings.Device):
    from hermes_cli.lina_devices import omo_status

    def refresh(previous):
        got = omo_status(dev)
        return previous if not isinstance(got, dict) or "error" in got else got
    return OMO.get(key, refresh)


def _cells(dev: settings.Device, base: Path, probe: dict, descriptor: dict | None, reachable: bool,
           default_runner: str) -> list[dict]:
    from hermes_cli.lina_runners import offered
    fleet, omo_fleet = _fleet(), _omo_fleet()
    offers = offered(settings.section(base))
    omo_names = {name for name, kind, _ in offers if kind == "omo"}
    descriptor = descriptor or {}
    rows = (descriptor.get("capabilities") or {}).get("runners") if isinstance(descriptor, dict) else None
    hello_ok = reachable and "error" not in probe and not probe.get("protocol_error")
    if hello_ok and isinstance(rows, list) and all(isinstance(r, dict) and "pinned" in r for r in rows):
        status = _omo_status((str(base), dev.name), dev) if omo_names else None
        probe_ready = bool(probe.get("ready")) if default_runner in omo_names else None
        return [omo_cell(str(r.get("name")), r.get("type"), status, probe_ready, omo_fleet)
                if r.get("type") == "omo" else runner_cell(r, fleet) for r in rows]
    why = "update" if hello_ok or (reachable and descriptor.get("reason") == "unknown verb hello") else "unreachable"
    cells = []
    for name, kind, pin in offers:
        if kind == "omo":
            cells.append(_cell(name, kind, omo_fleet, None, "ocx", False, why))
        elif pin is not None:
            cells.append(_cell(name, kind, pin.version, None, None, False, why))
    return cells


def nodes(base: Path, metrics: dict | None, now: float) -> list[dict]:
    """The work and files devices: work first, then files; in each, down, then warnings, then the rest."""
    from hermes_cli.worker import runners
    from hermes_cli.worker.board import machine
    worker_root = settings.root(base)
    worker = settings.section(base)
    runner = runners.default_name(worker)
    min_free = int(settings.setting("min_free_gb", worker=worker))
    devs = [dev for dev in settings.devices(base) if role(base, dev.name) in NODE_ROLES]
    if not devs:
        return []
    with ThreadPoolExecutor(len(devs)) as pool:
        contacts = list(pool.map(lambda dev: _contact(dev, worker_root, runner), devs))
    rows = []
    for dev, (probe, descriptor, reachable) in zip(devs, contacts):
        row = machine(dev, probe, (metrics or {}).get(dev.name) or (metrics or {}).get(dev.host), now, min_free)
        row.update(role=role(base, dev.name), reachable=reachable,
                   runners=_cells(dev, base, probe, descriptor, reachable, runner))
        rows.append(row)
    return sorted(rows, key=lambda row: (NODE_ROLES.index(row["role"]), STATES.index(row["state"])))


def _person_tools() -> list[tuple[str, str, str | None]]:
    """``(name, bin, fleet version)`` of each runner the hub pins (an adapter checked by its package, which has no
    ``--version`` worth starting, is left out, as ``lina_runners.person_installed`` does) and omo."""
    from hermes_cli.lina_runners import pins
    tools = [(pin.name, pin.bin, pin.version) for pin in pins().values() if pin.check != "package"]
    return tools + [("omo", "omo", _omo_fleet())]


def _versions_script(bins: list[str]) -> str:
    lines = []
    for name in bins:
        quoted = shlex.quote(name)
        lines.append(f"printf '@@%s\\n' {quoted}; command -v {quoted} >/dev/null 2>&1 && "
                     f"{quoted} --version </dev/null 2>/dev/null | head -n 1")
    return "; ".join(lines) + "; true"


def parse_versions(text: str) -> dict[str, str | None]:
    found: dict[str, str | None] = {}
    current = None
    for line in text.splitlines():
        if line.startswith("@@"):
            current = line[2:].strip()
            found[current] = None
        elif current is not None and found[current] is None and line.strip():
            hit = _VERSION.search(line)
            found[current] = hit.group(0) if hit else line.strip()[:40]
    return found


def person_read(alias: str, bins: list[str], previous: dict | None) -> dict:
    """One read-only ssh call to the person device: each bin's ``--version`` first line in the login shell. macOS
    has no ``timeout`` command, so the one bound is this process's ``subprocess.run(timeout=…)``."""
    remote = 'exec "${SHELL:-/bin/sh}" -lc ' + shlex.quote(_versions_script(bins))
    argv = ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "--", alias, remote]
    try:
        done = subprocess.run(argv, capture_output=True, text=True, encoding="utf-8", errors="replace",
                              stdin=subprocess.DEVNULL, timeout=PERSON_TIMEOUT_S, check=False)
    except (OSError, subprocess.TimeoutExpired):
        done = None
    if done is None or done.returncode != 0:
        return {"reachable": False, "checkedAt": time.time(), "versions": (previous or {}).get("versions") or {}}
    return {"reachable": True, "checkedAt": time.time(), "versions": parse_versions(done.stdout)}


def people(base: Path) -> list[dict]:
    tools = _person_tools()
    bins = [tool_bin for _, tool_bin, _ in tools]
    out = []
    for name in person_names(base):
        seen = PERSON.get((str(base), name), lambda previous, alias=name: person_read(alias, bins, previous))
        versions = (seen or {}).get("versions") or {}
        out.append({"name": name, "role": "person", "reachable": (seen or {}).get("reachable"),
                    "checkedAt": (seen or {}).get("checkedAt"),
                    "tools": [{"name": tool, "fleet": fleet, "installed": versions.get(tool_bin)}
                              for tool, tool_bin, fleet in tools]})
    return out


def view(base: Path, metrics: dict | None, now: float) -> dict:
    return {"machines": nodes(base, metrics, now), "person": people(base)}


class Refused(Exception):
    def __init__(self, status: int, detail: str):
        super().__init__(detail)
        self.status, self.detail = status, detail


_SIGNING: set = set()
_SIGNING_LOCK = threading.Lock()
_LOG_LOCK = threading.Lock()


def clean(text) -> str | None:
    """A reason fit to show and keep: one line, addresses, codes and tokens hidden."""
    if text is None:
        return None
    return _SECRETISH.sub("[hidden]", " ".join(str(text).split()))[:200] or None


def log_action(base: Path, by: dict | None, device: str, action: str, target: dict, state: str,
               reason: str | None) -> None:
    line = {"at": time.time(), "by": {k: (by or {}).get(k) for k in ("principal", "device")}, "device": device,
            "action": action, **target, "state": state, "reason": clean(reason)}
    path = base / ACTIONS_LOG
    with _LOG_LOCK:
        path.parent.mkdir(parents=True, exist_ok=True)
        with path.open("a", encoding="utf-8") as fh:
            fh.write(json.dumps(line, ensure_ascii=False) + "\n")


def _node(base: Path, name: str) -> settings.Device:
    if name in person_names(base):
        raise Refused(409, "a person device is never a node")
    for dev in settings.devices(base):
        if dev.name == name:
            return dev
    raise Refused(404, f"no device {name} in devices.workers")


def _field(body, key: str) -> str | None:
    value = body.get(key) if isinstance(body, dict) else None
    return value if isinstance(value, str) else None


def _reply(name: str, action: str, state: str, reason: str | None = None) -> dict:
    return {"ok": True, "device": name, "action": action, "state": state, "reason": clean(reason)}


def _logged(base: Path, by: dict, name: str, action: str, target: dict, work: Callable[[], dict]) -> dict:
    try:
        result = work()
    except Refused as exc:
        log_action(base, by, name, action, target, "refused", exc.detail)
        raise
    log_action(base, by, name, action, target, result["state"], result["reason"])
    return result


def restart(name: str, body, by: dict, base: Path) -> dict:
    """``POST /devices/{name}/restart`` ``{"service": "worker"|"ocx"}``: through the node's service layer."""
    service = _field(body, "service")

    def work() -> dict:
        from hermes_cli.lina_devices import service_call
        dev = _node(base, name)
        if service not in SERVICES:
            raise Refused(400, 'the body is {"service": "worker"|"ocx"}')
        result = service_call(dev, settings.root(base), service, "restart")
        if result.get("action") == "error":
            raise Refused(502, clean(result.get("reason")) or f"{name} refused the restart")
        return _reply(name, "restart", "restarted")
    return _logged(base, by, name, "restart", {"service": service}, work)


def _start_sign_in(name: str, runner: str, base: Path) -> dict:
    """The one place that calls lina#230's start function; its name and reply shape are that PR's."""
    return importlib.import_module(SIGN_IN_MODULE).start(name, runner, base=base)


def _sign_in_module_present() -> bool:
    try:
        importlib.import_module(SIGN_IN_MODULE)
    except ModuleNotFoundError as exc:
        if exc.name == SIGN_IN_MODULE:
            return False
        raise
    return True


def sign_in(name: str, body, by: dict, base: Path) -> dict:
    """``POST /devices/{name}/sign-in`` ``{"runner": "<name>"}``: starts the sign-in in the background and answers
    ``started`` at once; its end is a second log line, and the app sees it in the node's ``signIn``."""
    from hermes_cli.lina_runners import offered
    runner = _field(body, "runner")
    key = (str(base), name, runner)

    def work() -> dict:
        _node(base, name)
        if runner not in {offer for offer, _, _ in offered(settings.section(base))}:
            raise Refused(400, 'the body is {"runner": "<a runner of worker.runners>"}')
        if not _sign_in_module_present():
            raise Refused(501, "sign-in is not on this core yet (lina#230)")
        with _SIGNING_LOCK:
            if key in _SIGNING:
                raise Refused(409, f"a sign-in to {runner} on {name} is already running")
            _SIGNING.add(key)
        return _reply(name, "sign-in", "started")
    started = _logged(base, by, name, "sign-in", {"runner": runner}, work)
    _spawn(lambda: _finish_sign_in(name, runner, by, base, key))
    return started


def _finish_sign_in(name: str, runner: str, by: dict, base: Path, key) -> None:
    try:
        result = _start_sign_in(name, runner, base)
        result = result if isinstance(result, dict) else {}
        state = str(result.get("state") or "unknown")
        state = state if _STATE_WORD.match(state) else "unknown"
        reason = result.get("reason")
    except Exception as exc:  # health: allow BLE001 -- the end must reach the log; no traceback: it may hold the sign-in address or code
        logger.warning("sign-in to %s on %s failed: %s", runner, name, type(exc).__name__)
        state, reason = "error", type(exc).__name__
    finally:
        with _SIGNING_LOCK:
            _SIGNING.discard(key)
    log_action(base, by, name, "sign-in", {"runner": runner}, state, reason)
