"""A device's slots: how many tasks hold one, and the reservation the hub takes before it places a task.

A task holds a slot from the hub's reservation until its newest requested run ended (``rc.<n>``), whether that run
is waiting for ``serve``, preparing or running: counting only launched runs let a batch land on one ``cap: 1``
device and ``serve`` launch them all. The reservation is checked and written under one lock, so two hubs placing
at once cannot both take the last slot; ``serve`` holds launches to the cap the hub reserved with.

A requested run marked ``slot_wait.<n>`` (a file node's follow-up while the device is full) holds no slot: it waits,
and ``admit`` lets the one that waited longest take a free slot under the same lock. Other devices never write it.
"""

from __future__ import annotations

import contextlib
import fcntl  # windows-footgun: ok — work devices are POSIX (launchd/systemd) only
import time
from pathlib import Path

from hermes_cli.worker import settings

RESERVED = "reserved"
RESERVE_TTL_S = 600.0  # a reservation the hub never followed with a request (it crashed) frees its slot
CAP_FILE = "cap"
POOL_FILE = "slot_pool"
WAIT = "slot_wait"


def task_pool(task_dir: Path) -> str:
    """Unmarked tasks predate named reservations and belong to the bid pool."""
    try:
        return (task_dir / POOL_FILE).read_text(encoding="utf-8-sig").strip()
    except FileNotFoundError:
        return "bid"


def requested(task_dir: Path) -> int:
    runs = [int(p.name.split(".")[1]) for p in task_dir.glob("request.*") if p.name.split(".")[1].isdigit()]
    return max(runs, default=0)


def waiting_since(task_dir: Path) -> float | None:
    """When the newest requested run began to wait for a slot; None when it does not wait."""
    run = requested(task_dir)
    mark = task_dir / f"{WAIT}.{run}"
    if not run or not mark.exists() or (task_dir / f"rc.{run}").exists():
        return None
    try:
        return float(mark.read_text(encoding="utf-8-sig").strip())
    except (OSError, ValueError):
        return 0.0


def holds_slot(task_dir: Path) -> bool:
    run = requested(task_dir)
    if run:
        return not (task_dir / f"rc.{run}").exists() and not (task_dir / f"{WAIT}.{run}").exists()
    reserved = task_dir / RESERVED
    return reserved.exists() and time.time() - reserved.stat().st_mtime < RESERVE_TTL_S


def launched(task_dir: Path) -> bool:
    """A run of this task was handed to its own process and has not ended."""
    run = requested(task_dir)
    return bool(run) and (task_dir / f"taken.{run}").exists() and not (task_dir / f"rc.{run}").exists()


def _task_dirs(worker_root: Path) -> list[Path]:
    return sorted(settings.tasks_dir(worker_root).glob("O*-*"))


def held(worker_root: Path, exclude: str | None = None, *, pool: str | None = None) -> int:
    return sum(holds_slot(d) for d in _task_dirs(worker_root)
               if d.name != exclude and (pool is None or task_pool(d) == pool))


def running_others(worker_root: Path, task_id: str, *, pool: str | None = None) -> int:
    return sum(launched(d) for d in _task_dirs(worker_root)
               if d.name != task_id and (pool is None or task_pool(d) == pool))


def cap(worker_root: Path, *, pool: str = "bid") -> int | None:
    """The cap the hub last reserved with; None before any reservation (no hub limit known yet)."""
    try:
        return int((worker_root / (CAP_FILE if pool == "bid" else "cap.named")).read_text(encoding="utf-8-sig").strip())
    except (OSError, ValueError):
        return None


@contextlib.contextmanager
def _locked(worker_root: Path):
    worker_root.mkdir(parents=True, exist_ok=True)
    with (worker_root / "slots.lock").open("w") as handle:
        fcntl.flock(handle, fcntl.LOCK_EX)  # windows-footgun: ok — POSIX work devices only
        try:
            yield
        finally:
            fcntl.flock(handle, fcntl.LOCK_UN)  # windows-footgun: ok — POSIX work devices only


def admit(worker_root: Path, task_id: str, run: int, device_cap: int, *, pool: str = "bid") -> bool:
    """Let *task_id*'s waiting run *run* take a slot: under the slot lock, only while fewer than *device_cap* runs
    hold one in *pool* and it waited longest there. Its wait mark goes then, so it holds the slot from here on; a run
    that does not wait is admitted already."""
    with _locked(worker_root):
        mark = settings.tasks_dir(worker_root) / task_id / f"{WAIT}.{run}"
        if not mark.exists():
            return True
        if held(worker_root, exclude=task_id, pool=pool) >= device_cap:
            return False
        queue = sorted((since, d.name) for d in _task_dirs(worker_root)
                       if task_pool(d) == pool and (since := waiting_since(d)) is not None)
        if queue and queue[0][1] != task_id:
            return False
        mark.unlink()
        return True


def reserve(worker_root: Path, task_id: str, device_cap: int, *, pool: str = "bid") -> dict:
    """Take a slot for *task_id* when fewer than *device_cap* tasks hold one; ``full`` otherwise."""
    with _locked(worker_root):
        (worker_root / (CAP_FILE if pool == "bid" else "cap.named")).write_text(f"{device_cap}\n", encoding="utf-8")
        taken = held(worker_root, exclude=task_id, pool=pool)
        if taken >= device_cap:
            return {"action": "full", "held": taken, "cap": device_cap}
        task_dir = settings.tasks_dir(worker_root) / task_id
        task_dir.mkdir(parents=True, exist_ok=True)
        (task_dir / POOL_FILE).write_text(pool + "\n", encoding="utf-8")
        (task_dir / RESERVED).write_text(f"{time.time():.3f}\n", encoding="utf-8")
        return {"action": "reserved", "held": taken + 1, "cap": device_cap}
