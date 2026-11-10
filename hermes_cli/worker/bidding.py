"""Which device takes a task: every candidate in ``devices.workers`` is asked at once and the least full ready one wins.

Ready means: the runner is installed and its daemon up, ``lina worker serve`` runs, the node's model path answers
(``probe`` asks it on the node, and its hello reports the same), there is disk, the repo path exists, the node's OCX
answers when it reports one (``capabilities.ocx`` of its hello), it has the memory floor
(``worker.min_free_mem_gb``), it is not cooling off after quick failures (``worker.cooloff_*``; a named run still
starts), and a slot is free under the device's cap. The score is (running / cap, 0 for a ``fast`` device, load per
core); lowest wins within the first available integer ``last`` tier (false/absent = 0, true = 1).
``no_repo`` never takes a task with a repo,
``auto: false`` takes work only when named with ``--host``. When nobody can, the task waits in the hub's queue.
Roles: only ``work`` devices are bid on; a ``files`` device takes work only by ``--host`` and never with a repo;
a ``person`` device never takes work.
"""

from __future__ import annotations

import json
import time
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

from hermes_cli.worker import settings
from hermes_cli.worker.settings import FILE_KIND, ROLES, Device

ENDS_KEPT = 10  # the run ends a node record keeps for the cool-off


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
        (probe.get("model_path") == "down", probe.get("model_path_why") or "model path does not answer"),
        (int(probe.get("free_gb") or 0) < min_free_gb, f"disk {probe.get('free_gb')}GB < {min_free_gb}GB"),
        (low_memory(probe), f"memory {probe.get('mem_available_gb')}GB < {mem_floor(probe)}GB"),
        (ocx_down(probe.get("ocx")), "ocx not answering"),
        (not manual and bool(probe.get("cooling")), f"cooling off after {probe.get('cooling')} quick failures"),
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


def ocx_down(ocx) -> bool:
    """Whether a node that reports an OCX (``external`` or a ``version``) has one that does not answer (``health``
    not 200). A node with no OCX at all (a file node, one that reaches models another way) is never down for this."""
    if not isinstance(ocx, dict) or not (ocx.get("external") is True or ocx.get("version") is not None):
        return False
    return ocx.get("health") != 200


def mem_floor(probe: dict) -> int:
    return int(probe.get("mem_floor_gb", settings.DEFAULTS["min_free_mem_gb"]))


def low_memory(probe: dict) -> bool:
    """Available memory under the floor; an older node whose probe has no ``mem_available_gb`` is never low."""
    return probe.get("mem_available_gb") is not None and int(probe["mem_available_gb"]) < mem_floor(probe)


def record_end(store, node: str, state: str, took: float | None, at: float | None = None) -> None:
    """One run end on *node*'s record (``ends``, the newest last), for the cool-off. *took* None: its length is not
    known, so it never counts as a quick failure. Enough ends are kept for ``cooloff_failures``."""
    kept = max(ENDS_KEPT, int(settings.setting("cooloff_failures", worker=settings.section(store.worker_root.parent))))
    with store.edit_node(node) as record:
        ends = [*(record.get("ends") or []), {"at": time.time() if at is None else at, "state": state, "took": took}]
        record["ends"] = ends[-kept:]


def cooling(record: dict | None, worker: dict, now: float) -> int | None:
    """The quick failures *record* cools off after, or None: its newest ``cooloff_failures`` ends all ``failed``, each
    no longer than ``cooloff_quick_s``, within ``cooloff_window_s`` of each other, and the newest less than
    ``cooloff_s`` ago. A newer end of any other state (``done``) clears it."""
    count = int(settings.setting("cooloff_failures", worker=worker))
    quick = float(settings.setting("cooloff_quick_s", worker=worker))
    ends = ((record or {}).get("ends") or [])[-count:]
    if count < 1 or len(ends) < count or any(
            end.get("state") != "failed" or end.get("took") is None or end["took"] > quick for end in ends):
        return None
    if ends[-1]["at"] - ends[0]["at"] > float(settings.setting("cooloff_window_s", worker=worker)):
        return None
    return count if now - ends[-1]["at"] < float(settings.setting("cooloff_s", worker=worker)) else None


def with_node_facts(found: dict, descriptor: dict | None, node: str, worker_root: Path,
                    now: float | None = None) -> dict:
    """*found* (a node's probe) with what the hub knows of the node apart from it: ``ocx`` from the hello the same
    contact asked first, ``cooling`` from the run ends on its node record and the hub's memory floor
    (``mem_floor_gb``). ``lina devices`` merges the same way, so both say the same reasons."""
    from hermes_cli.worker.hub_store import Store
    worker = settings.section(worker_root.parent)
    facts = {"mem_floor_gb": int(settings.setting("min_free_mem_gb", worker=worker))}
    capabilities = (descriptor or {}).get("capabilities") or {}
    ocx = capabilities.get("ocx")
    if isinstance(ocx, dict):
        facts["ocx"] = ocx
    # the hello asks the model path too: it stands in when the probe reply does not carry a fresher answer
    for key in ("model_path", "model_path_why"):
        if key not in found and key in capabilities:
            facts[key] = capabilities[key]
    if count := cooling(Store(worker_root).load_node(node), worker, time.time() if now is None else now):
        facts["cooling"] = count
    return {**found, **facts}


def model_path_refusal(dev: Device, worker_root: Path) -> str | None:
    """Why a run named onto *dev* is refused at submit: the model path its node asks does not answer now, so the run
    would end ``provider_error`` straight away. A file node answers in ``file_node.submit_refusal``."""
    if dev.kind == FILE_KIND:
        return None
    from hermes_cli.worker.transport import for_device
    contact = for_device(dev, worker_root)
    reply = contact.call(["probe", "", ""], timeout=60)
    if reply.code != 0:
        return None  # unreachable or a protocol error: the bid and the placement say why
    found = with_node_facts(reply.json() or {}, contact.descriptor, dev.name, worker_root)
    return found.get("model_path_why") if found.get("model_path") == "down" else None


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
        found = with_node_facts(reply.json() or {"error": "no answer"}, contact.descriptor, dev.name, worker_root)
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
