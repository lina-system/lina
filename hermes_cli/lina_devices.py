"""``lina devices``: the work devices ``lina worker`` bids over (``devices.workers`` in the LINA root's config), with
their readiness (lina#37).

Readiness of one device: ssh reachable (a local device always is), the pinned omo present (``lina omo status`` on the
device: ``current`` is the pin) when its runner is omo, free disk, the cap and what runs now, from the same ``probe``
the bidding uses. ``check <name> --run-smoke`` hands one tiny real task to that device through ``lina worker`` and
reports ``done`` or ``failed`` with the run ID; it runs only when asked.

Each device also shows its runners (lina#222): pinned and installed version, sign-in and one reason when not ready,
from ``lina runners status --offered`` on the device. ``sign-in`` gathers the runners a person has to sign in to into
one question per runner, and ``service`` reads or restarts OCX and the worker of a device through the service layer.
"""

from __future__ import annotations

import json
import re
import shlex
import subprocess
from pathlib import Path

from hermes_cli.worker import bidding, settings
from hermes_cli.worker.transport import for_device

_NAME = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_-]{0,40}$")
SSH_UNREACHABLE = 255
SMOKE_BRIEF = ("Device smoke test from `lina devices check --run-smoke`. Write out/smoke.txt containing the single "
               "line `ok`, then report.md with a `## 만든 것` section naming out/smoke.txt and a "
               "`## 다음에 알아야 할 것` section with one line about this device. Change nothing else.\n")


class DevicesError(RuntimeError):
    pass


def _base(base: Path | None) -> Path:
    """The worker's LINA root (lina#227): the device list read, bid over and written here is the one the worker reads."""
    from hermes_cli.lina_work_settings import base as settings_base
    return base or settings_base()


def probe(dev: settings.Device, worker_root: Path, runner: str) -> dict:
    """The bidding probe of *dev*; ``ssh: False`` when ssh itself could not reach it."""
    reply = for_device(dev, worker_root).call(["probe", "", runner], timeout=60)
    if reply.code == 0:
        return {"ssh": True, **(reply.json() or {"error": "no answer"})}
    if reply.json().get("protocol_error"):
        return {"ssh": dev.local or reply.code != SSH_UNREACHABLE, **reply.json()}
    return {"ssh": dev.local or reply.code != SSH_UNREACHABLE, "error": (reply.err.strip() or f"exit {reply.code}")[-160:]}


def omo_status(dev: settings.Device) -> dict:
    """``lina omo status --json`` on *dev* (in this process for a local device)."""
    if dev.local:
        from hermes_cli.lina_omo import status
        return status()
    return _remote_json(dev, ["omo", "status", "--json"])


def runner_status(dev: settings.Device, worker_root: Path) -> list | dict:
    """The runner rows *dev*'s ``hello`` carries (lina#222), through the negotiated transport (in-process for a local
    device). A device without hello, or whose hello predates the runner rows, gives ``{"legacy": True}``."""
    contact = for_device(dev, worker_root)
    reply = contact.refresh(timeout=60)
    descriptor = contact.descriptor or {}
    if reply.code != 0:
        return {"error": descriptor.get("reason") or reply.err.strip()[-160:] or f"exit {reply.code}",
                "legacy": descriptor.get("reason") == "unknown verb hello"}
    rows = (descriptor.get("capabilities") or {}).get("runners") or []
    return rows if all("pinned" in item for item in rows) else {"error": "hello has no runner rows", "legacy": True}


def _remote_json(dev: settings.Device, args: list[str]) -> list | dict:
    remote = shlex.join([*shlex.split(dev.lina), *args])
    try:
        done = subprocess.run(["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", dev.host, remote],
                              capture_output=True, text=True, encoding="utf-8", errors="replace", timeout=120,
                              check=False)
    except (OSError, subprocess.TimeoutExpired) as exc:
        return {"error": str(exc)}
    try:
        return json.loads(done.stdout) if done.returncode == 0 else {"error": done.stderr.strip()[-160:]}
    except ValueError:
        return {"error": f"lina {' '.join(args[:2])} gave no JSON"}


def runner_rows(dev: settings.Device, worker_root: Path, found: dict) -> list[dict]:
    """The device's runner rows; when it cannot be asked, the runners the hub offers, each ``unreachable`` (or
    ``update`` when it answers but its hello has no runner rows yet)."""
    from hermes_cli.lina_runners import offered
    got = runner_status(dev, worker_root) if found["ssh"] and not found.get("protocol_error") else None
    if isinstance(got, list):
        return got
    why = "update" if got and got.get("legacy") else "unreachable"
    return [{"name": name, "type": kind, "pin": pin.name, "pinned": pin.version, "installed": None, "sign_in": None,
             "ready": False, "why": why}
            for name, kind, pin in offered(settings.section(worker_root.parent)) if pin is not None]


def readiness(dev: settings.Device, worker_root: Path) -> dict:
    from hermes_cli.worker import runners
    worker = settings.section(worker_root.parent)
    runner = runners.default_name(worker)
    found = probe(dev, worker_root, runner)
    row = {"name": dev.name, "host": dev.host, "cap": dev.cap, "runner": runner, "ssh": found["ssh"],
           "free_gb": found.get("free_gb"), "running": found.get("running"), "serve": found.get("serve")}
    omo = omo_status(dev) if found["ssh"] and not found.get("protocol_error") and runner == "omo" else {}
    row["omo"] = None if runner != "omo" else bool(omo.get("pinned") and omo.get("current") == omo.get("pinned"))
    why = bidding.readiness(dev, found, repo=None, min_free_gb=int(settings.setting("min_free_gb", worker=worker)))
    if why == "only when named with --host":
        why = None
    if why is None and row["omo"] is False:
        why = f"pinned omo {omo.get('pinned') or '?'} not current ({omo.get('error') or omo.get('current') or 'none'})"
    row["ready"], row["why"] = why is None, why
    row["runners"] = runner_rows(dev, worker_root, found)
    return row


def render(row: dict) -> str:
    omo = {None: "-", True: "ok", False: "missing"}[row["omo"]]
    return (f"{row['name']:<14} {row['host']:<14} cap {row['cap']} · running {row.get('running', '-')} · ssh "
            f"{'ok' if row['ssh'] else 'unreachable'} · omo {omo} · free disk {row.get('free_gb', '-')}GB · "
            + ("ready" if row["ready"] else f"not ready: {row['why']}"))


def _devices(base: Path) -> list[settings.Device]:
    return settings.devices(base)


def device(name: str, base: Path) -> settings.Device:
    for dev in _devices(base):
        if dev.name == name:
            return dev
    raise DevicesError(f"no device {name!r} in devices.workers (`lina devices add {name} --host <ssh alias>`)")


def list_devices(base: Path | None = None, *, as_json: bool = False, out=print) -> int:
    base = _base(base)
    rows = [readiness(dev, settings.root(base)) for dev in _devices(base)]
    if as_json:
        out(json.dumps(rows, ensure_ascii=False))
    elif not rows:
        out("no devices: `lina devices add <name> --host <ssh alias|local>`")
    for row in [] if as_json else rows:
        _render_with_runners(row, out)
    return 0


def _render_with_runners(row: dict, out) -> None:
    from hermes_cli.lina_runners import render as render_runners
    out(render(row))
    for line in render_runners([item for item in row.get("runners") or [] if item.get("pinned") or item.get("why")]):
        out(f"  {line}")


def sign_in_questions(rows: list[dict]) -> list[dict]:
    """One inbox question per runner a person has to sign in to, listing those nodes and how to sign in on each
    (lina#222). Writing it into the inbox and the sign-in button are #230 / #234."""
    from hermes_cli.lina_runners import how_to_sign_in
    asks: dict[str, dict] = {}
    for row in rows:
        for item in row.get("runners") or []:
            if item.get("sign_in") != "signed_out":
                continue
            ask = asks.setdefault(item["name"], {"runner": item["name"], "nodes": []})
            ask["nodes"].append({"node": row["name"],
                                 "how": how_to_sign_in(item.get("sign_in_by") or "", item["name"], row["name"])})
    for ask in asks.values():
        names = ", ".join(node["node"] for node in ask["nodes"])
        ask["question"] = f"Sign in to {ask['runner']} on {len(ask['nodes'])} node(s): {names}"
    return list(asks.values())


def sign_in(base: Path | None = None, *, as_json: bool = False, out=print) -> int:
    """``lina devices sign-in``: the sign-in questions over every device (read-only)."""
    base = _base(base)
    asks = sign_in_questions([readiness(dev, settings.root(base)) for dev in _devices(base)])
    if as_json:
        out(json.dumps(asks, ensure_ascii=False))
        return 0
    if not asks:
        out("no runner waits for a sign-in")
    for ask in asks:
        out(ask["question"])
        for node in ask["nodes"]:
            out(f"  {node['node']}: {node['how']}")
    return 0


def service_call(dev: settings.Device, worker_root: Path, which: str, action: str) -> dict:
    """OCX or worker status/restart on *dev* (lina#222), always through the node's ``service`` verb: the worker
    restart keeps #218's compare-and-act with the instance its ``hello`` reports now; OCX goes to the node's service
    layer (``lina_runners.node_service``)."""
    transport = for_device(dev, worker_root)
    if which == "ocx":
        reply = transport.call(["service", "ocx", action])
        return reply.json() or {"service": "ocx", "action": "error",
                                "reason": reply.err.strip()[-160:] or f"exit {reply.code}"}
    reply = transport.refresh(timeout=60)
    descriptor = transport.descriptor or {}
    if reply.code != 0:
        return {"service": "worker", "action": "error",
                "reason": descriptor.get("reason") or reply.err.strip()[-160:] or f"exit {reply.code}"}
    if action == "status":
        return {"service": "worker", "action": "status", "serve": descriptor.get("serve"),
                "instance_id": descriptor.get("instance_id")}
    return {"service": "worker", **transport.service(action, descriptor.get("instance_id") or "").json()}


def service(name: str, which: str, action: str, *, base: Path | None = None, as_json: bool = False,
            out=print) -> int:
    base = _base(base)
    result = service_call(device(name, base), settings.root(base), which, action)
    if as_json:
        out(json.dumps(result, ensure_ascii=False))
    else:
        detail = result.get("reason") or result.get("state") or ("serving" if result.get("serve") else "not serving")
        out(f"{name} {which} {result.get('action')}: {detail}")
    return 1 if result.get("action") == "error" else 0


def add(name: str, host: str, *, base: Path | None = None, cap: int = 1, fast: bool = False, no_repo: bool = False,
        last: bool = False, manual: bool = False, lina: str | None = None, out=print) -> int:
    if not _NAME.match(name):
        raise DevicesError(f"device name {name!r}: letters, digits, - and _ only")
    entry = {"host": host, "cap": max(1, cap)}
    entry.update({key: True for key, on in (("fast", fast), ("no_repo", no_repo), ("last", last)) if on})
    if manual:
        entry["auto"] = False
    if lina:
        entry["lina"] = lina
    _write(_base(base), name, entry)
    out(f"{name}: {host}, cap {entry['cap']} in devices.workers (`lina devices check {name}`)")
    return 0


def _write(base: Path, name: str, entry: dict | None) -> None:
    """One device entry through the work settings layer (lina#256), so the change is a revision in its history."""
    from hermes_cli.lina_work_settings import SettingsError, set_path
    from hermes_cli.lina_work_settings_store import Store, cli_by
    try:
        Store(base).apply(lambda doc: set_path(doc, f"devices.workers.{name}", entry), cli_by())
    except SettingsError as exc:
        raise DevicesError(f"refused by the work settings: {exc}") from exc


def remove(name: str, *, base: Path | None = None, out=print) -> int:
    base = _base(base)
    device(name, base)
    _write(base, name, None)
    out(f"{name}: removed from devices.workers")
    return 0


def check(name: str, *, base: Path | None = None, run_smoke: bool = False, timeout: float = 1800,
          as_json: bool = False, out=print) -> int:
    base = _base(base)
    dev = device(name, base)
    row = readiness(dev, settings.root(base))
    if run_smoke:
        row["smoke"] = smoke(dev, base, timeout=timeout)
    if as_json:
        out(json.dumps(row, ensure_ascii=False))
    else:
        _render_with_runners(row, out)
        if run_smoke:
            out(f"smoke: {row['smoke']['state']} · run {row['smoke']['id'] or '-'}"
                + (f" · {row['smoke']['why']}" if row["smoke"].get("why") else ""))
    if run_smoke:
        return 0 if row["smoke"]["state"] == "done" else 1
    return 0 if row["ready"] else 1


def smoke(dev: settings.Device, base: Path, *, timeout: float) -> dict:
    """One tiny real task on *dev* through ``lina worker submit --host <dev> --wait``: its end state and run ID."""
    from hermes_cli.worker.hub import ENDED, Hub, HubError
    from hermes_cli.worker.transport import TransportError
    worker_root = settings.root(base)
    brief = settings.hub_dir(worker_root) / "smoke" / f"{dev.name}.md"
    brief.parent.mkdir(parents=True, exist_ok=True)
    brief.write_text(SMOKE_BRIEF, encoding="utf-8")
    lines: list[str] = []
    hub = Hub(worker_root, out=lines.append)
    try:
        hub.submit({"briefs": [str(brief)], "mode": "ulw", "host": dev.name, "topic": f"device smoke {dev.name}",
                    "inputs": [], "wait": timeout, "dry_notify": True})  # a check messages nobody
    except (HubError, TransportError, OSError) as exc:
        return {"state": "failed", "id": None, "why": str(exc)[:300]}
    task_id = next((line.split()[0] for line in lines if " handed out " in line), None)
    job = hub.store.load(task_id) if task_id else None
    if not job or not job.get("host"):
        return {"state": "failed", "id": task_id, "why": "did not start on the device (queued or refused)"}
    state = hub.meta(job).get("state") or "unknown"
    if state not in ENDED:
        return {"state": "failed", "id": task_id, "why": f"not ended after {timeout:.0f}s (state {state})"}
    return {"state": "done" if state == "done" else "failed", "id": task_id,
            **({} if state == "done" else {"why": f"ended {state}"})}
