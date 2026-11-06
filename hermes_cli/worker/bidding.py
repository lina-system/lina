"""Which device takes a task: every candidate in ``devices.workers`` is asked at once and the least full ready one wins.

Ready means: the runner is installed and its daemon up, ``lina worker serve`` runs, there is disk, the repo path
exists, and a slot is free under the device's cap. The score is (running / cap, 0 for a ``fast`` device, load per
core); lowest wins within the first available integer ``last`` tier (false/absent = 0, true = 1).
``no_repo`` never takes a task with a repo,
``auto: false`` takes work only when named with ``--host``. When nobody can, the task waits in the hub's queue.
"""

from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

from hermes_cli.worker.settings import Device


def readiness(dev: Device, probe: dict, *, repo: str | None, min_free_gb: int,
              manual: bool = False) -> str | None:
    """Why *dev* cannot take the task now, or None when it can."""
    checks = (
        (not manual and not dev.auto, "only when named with --host"),
        (bool(repo) and dev.no_repo, "takes no repo work"),
        ("error" in probe, f"unreachable: {probe.get('error')}"),
        (not probe.get("ready"), probe.get("why") or "runner not ready"),
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


def probe_all(candidates: list[Device], worker_root: Path, *, repo: str | None, runner: str) -> list[dict]:
    from hermes_cli.worker.transport import for_device

    def ask(dev: Device) -> dict:
        reply = for_device(dev, worker_root).call(["probe", repo or "", runner], timeout=60)
        if reply.code != 0:
            return {"error": (reply.err.strip() or f"exit {reply.code}")[-160:]}
        return reply.json() or {"error": "no answer"}

    if not candidates:
        return []
    with ThreadPoolExecutor(len(candidates)) as pool:
        return list(pool.map(ask, candidates))


def bid(candidates: list[Device], worker_root: Path, *, repo: str | None, runner: str, min_free_gb: int = 5,
        manual: bool = False) -> tuple[Device | None, list[dict]]:
    pool = [dev for dev in candidates if manual or dev.auto]
    return choose(pool, probe_all(pool, worker_root, repo=repo, runner=runner), repo=repo, min_free_gb=min_free_gb,
                  manual=manual)
