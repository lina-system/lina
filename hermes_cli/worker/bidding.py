"""Which device takes a task: every candidate in ``devices.workers`` is asked at once and the least full ready one wins.

Ready means: the runner is installed and its daemon up, ``lina worker serve`` runs, there is disk, the repo path
exists, and a slot is free under the device's cap. The score is (running / cap, 0 for a ``fast`` device, load per
core); lowest wins within the first available integer ``last`` tier (false/absent = 0, true = 1).
``no_repo`` never takes a task with a repo,
``auto: false`` takes work only when named with ``--host``. When nobody can, the task waits in the hub's queue.
Roles: only ``work`` devices are bid on; a ``files`` device takes work only by ``--host`` and never with a repo;
a ``person`` device never takes work.
"""

from __future__ import annotations

import json
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

from hermes_cli.worker.settings import FILE_KIND, ROLES, Device


def role_refusal(dev: Device, repo: str | None) -> str | None:
    """Why *dev* takes no task (with *repo*) whatever its probe says, even when named with ``--host``."""
    if dev.role not in ROLES:
        return f"role {dev.role!r} is not one of {'|'.join(ROLES)}: takes no work"
    if dev.role == "person":
        return "role person: takes no work"
    if repo and (dev.role == "files" or dev.kind == FILE_KIND):
        return "a file node takes no repository work"
    return None


def readiness(dev: Device, probe: dict, *, repo: str | None, min_free_gb: int,
              manual: bool = False) -> str | None:
    """Why *dev* cannot take the task now, or None when it can."""
    checks = (
        (bool(probe.get("protocol_error")), probe.get("reason")),
        (bool(refused := role_refusal(dev, repo)), refused),
        (not manual and not dev.auto, "only when named with --host"),
        (bool(repo) and dev.no_repo, "takes no repo work"),
        ("error" in probe, f"unreachable: {probe.get('error')}"),
        (not probe.get("ready"), probe.get("why") or "runner not ready"),
        # after readiness, so `lina devices` (which drops this reason) still shows why a file node cannot run
        (not manual and dev.role != "work", "only when named with --host"),
        (not probe.get("serve"), "lina worker serve is not running"),
        (int(probe.get("free_gb") or 0) < min_free_gb, f"disk {probe.get('free_gb')}GB < {min_free_gb}GB"),
        (not probe.get("repo", True), "repo path missing"),
        (int(probe.get("named_running" if manual else "bid_running", probe.get("running")) or 0)
         >= (dev.manual_cap if manual else dev.cap), "full"),
    )
    return next((why for failed, why in checks if failed), None)


def choose(candidates: list[Device], probes: list[dict], *, repo: str | None, min_free_gb: int = 5,
           manual: bool = False) -> tuple[Device | None, list[dict]]:
    """The winner (or None: queue) and one row per candidate with its probe, score or the reason it was passed."""
    rows, ready = [], []
    for dev, probe in zip(candidates, probes):
        capacity = dev.manual_cap if manual else dev.cap
        running = int(probe.get("named_running" if manual else "bid_running", probe.get("running")) or 0)
        row = {**probe, "host": dev.name, "cap": capacity, "running": running}
        why = readiness(dev, probe, repo=repo, min_free_gb=min_free_gb, manual=manual)
        if why:
            row["why"] = why
        else:
            row["score"] = (running / capacity, 0 if dev.fast else 1,
                            float(probe.get("load") or 0) / max(1, int(probe.get("cores") or 1)))
            ready.append((dev, row))
        rows.append(row)
    if ready and not manual:
        tier = min(dev.last for dev, _ in ready)
        for dev, row in ready:
            if dev.last > tier:
                row.pop("score")
                row["why"] = "only when the others are full"
        ready = [(dev, row) for dev, row in ready if dev.last == tier]
    best = min(ready, key=lambda pair: pair[1]["score"], default=None)
    return (best[0] if best else None), rows


def missing_access(descriptor: dict | None, access: str | None) -> str | None:
    """Why a device whose hello lacks *access* must not take the run (an older node would ignore the mode)."""
    if not access:
        return None
    modes = ((descriptor or {}).get("capabilities") or {}).get("access_modes") or []
    return None if access in modes else f"does not enforce access {access} (update the node)"


def takes_runner_spec(descriptor: dict | None) -> bool:
    """Whether the node's hello says its ``probe`` builds a runner from the hub's spec (lina#337)."""
    from hermes_cli.worker.protocol import PROBE_SPEC
    return bool(((descriptor or {}).get("capabilities") or {}).get(PROBE_SPEC))


def probe_all(candidates: list[Device], worker_root: Path, *, repo: str | None, runner: str,
              access: str | None = None, runner_spec: dict | None = None) -> list[dict]:
    """Each candidate's probe of *runner*. A node that takes the hub's *runner_spec* is sent it on stdin, so it builds
    the runner as the run will (``device.runner_for``); an older node is asked as before, and when it has no runner of
    that name its reason says it could not take the hub's spec."""
    from hermes_cli.worker.transport import for_device

    def ask(dev: Device) -> dict:
        contact = for_device(dev, worker_root)
        if runner_spec and contact.descriptor is None:
            contact.refresh(timeout=60)
        sends = bool(runner_spec) and takes_runner_spec(contact.descriptor)
        reply = contact.call(["probe", repo or "", runner], json.dumps(runner_spec).encode() if sends else b"",
                             timeout=60)
        if reply.code != 0:
            if reply.json().get("protocol_error"):
                return reply.json()
            return {"error": (reply.err.strip() or f"exit {reply.code}")[-160:]}
        found = reply.json() or {"error": "no answer"}
        if runner_spec and not sends and str(found.get("why") or "").startswith("no runner "):
            found = {**found, "why": f"{found['why']}; {dev.name} does not take the hub's runner spec (update the node)"}
        why = missing_access(contact.descriptor, access)
        return {**found, "protocol_error": True, "reason": f"{dev.name} {why}"} if why else found

    if not candidates:
        return []
    with ThreadPoolExecutor(len(candidates)) as pool:
        return list(pool.map(ask, candidates))


def bid(candidates: list[Device], worker_root: Path, *, repo: str | None, runner: str, min_free_gb: int = 5,
        manual: bool = False, access: str | None = None,
        runner_spec: dict | None = None) -> tuple[Device | None, list[dict]]:
    pool = [dev for dev in candidates if manual or (dev.auto and dev.role == "work")]
    return choose(pool, probe_all(pool, worker_root, repo=repo, runner=runner, access=access, runner_spec=runner_spec),
                  repo=repo, min_free_gb=min_free_gb, manual=manual)
