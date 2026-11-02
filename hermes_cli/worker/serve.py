"""``lina worker serve``: the ``ai.lina.worker`` / ``lina-worker`` service loop.

On a work device it takes the runs the hub asked for (``request.<n>`` in a task folder), prepares a new task, starts
each run as its own process, reports an end the hub did not take yet again, and fails a run whose process died
without an end. On a hub (a machine with ``devices.workers``) it also runs the hub tick every 30 s: scheduled
submissions, queued starts, ends a device could not report, unreachable devices and runs waiting on a person.
"""

from __future__ import annotations

import os
import time
from pathlib import Path

from hermes_cli.worker import device, settings, slots
from hermes_cli.worker.runners.base import Task
from hermes_cli.worker.task import read_json

DEAD_AFTER_S = 30.0
HUB_TICK_S = 30.0


def _take(task: Task, run: int) -> bool:
    try:
        fd = os.open(task.path(f"taken.{run}"), os.O_CREAT | os.O_EXCL | os.O_WRONLY)
    except FileExistsError:
        return False
    os.write(fd, f"{time.time():.3f}\n".encode())
    os.close(fd)
    return True


def _start(worker_root: Path, task: Task, run: int) -> None:
    if task.path("cancelled").exists():  # cancelled before this pass: nothing is prepared and no session opened
        task.path(f"rc.{run}").write_text("0\n", encoding="utf-8")
        device.finish_run(worker_root, task, run, "cancelled")
        return
    if run == 1 and not task.path("prepared").exists():
        try:
            device.prepare(worker_root, task)
        except (device.RunnerError, OSError) as exc:
            with task.path("stderr.1.txt").open("a", encoding="utf-8") as fh:
                fh.write(f"lina worker: prepare failed: {exc}\n")
            task.path("rc.1").write_text("1\n", encoding="utf-8")
            device.finish_run(worker_root, task, 1, "failed")
            return
    device.launch(worker_root, task, run)


def _dead(task: Task, run: int) -> bool:
    taken = task.path(f"taken.{run}")
    return (taken.exists() and not task.path(f"rc.{run}").exists() and time.time() - taken.stat().st_mtime > DEAD_AFTER_S
            and not device.alive(task, run))


def serve_once(worker_root: Path) -> None:
    tasks = settings.tasks_dir(worker_root)
    tasks.mkdir(parents=True, exist_ok=True)
    (worker_root / device.HEARTBEAT).write_text(f"{time.time():.3f}\n", encoding="utf-8")
    device.reap()
    cap = slots.cap(worker_root)
    for task_dir in sorted(tasks.glob("O*-*")):
        task = device.task_of(worker_root, task_dir.name)
        for request in sorted(task_dir.glob("request.*")):
            run = int(request.name.split(".")[1])
            if task.path(f"taken.{run}").exists():
                continue
            if (cap is not None and not task.path("cancelled").exists()
                    and slots.running_others(worker_root, task.id) >= cap):
                continue  # the device is full: the request waits for a later pass
            if _take(task, run):
                _start(worker_root, task, run)
        for pending in task_dir.glob("notify_pending.*"):
            device.report_end(worker_root, task, pending.read_text(encoding="utf-8-sig").strip(), int(pending.name.split(".")[1]))
        run = device.last_run(task)
        if run and _dead(task, run):
            task.path(f"rc.{run}").write_text("1\n", encoding="utf-8")
            with task.path(f"stderr.{run}.txt").open("a", encoding="utf-8") as fh:
                fh.write("lina worker: the run's process ended without an end report\n")
            device.finish_run(worker_root, task, run, "failed")


def serve(worker_root: Path | None = None, *, once: bool = False) -> int:
    worker_root = worker_root or settings.root()
    last_tick = 0.0
    while True:
        serve_once(worker_root)
        if settings.devices(device.base_of(worker_root)) and time.monotonic() - last_tick >= HUB_TICK_S:
            from hermes_cli.worker import hub
            hub.tick(worker_root)
            last_tick = time.monotonic()
        if once:
            return 0
        time.sleep(2.0)


def heartbeat_age(worker_root: Path) -> float | None:
    path = worker_root / device.HEARTBEAT
    stamp = read_json(path) if path.exists() else None
    return time.time() - float(stamp) if isinstance(stamp, (int, float)) else None
