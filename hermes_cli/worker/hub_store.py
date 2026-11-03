"""The hub's records under ``$LINA_HOME/worker/hub/``: one job file per task (the current view), ``receipts.jsonl``
(append-only history, never rewritten), fetched results in ``out/<agent>/<ID>/``, schedules, notices and locks.
"""

from __future__ import annotations

import contextlib
import fcntl  # windows-footgun: ok — the hub runs on POSIX (launchd/systemd) only
import json
import os
import tempfile
import threading
import time
from pathlib import Path

from hermes_cli.worker import settings


_HELD = threading.local()  # lock files this thread holds


class Store:
    def __init__(self, worker_root: Path):
        self.root = settings.hub_dir(worker_root)
        self.worker_root = worker_root
        self.jobs, self.outs = self.root / "jobs", self.root / "out"
        self.schedules, self.notices, self.locks = self.root / "schedules", self.root / "notices", self.root / "locks"
        self.receipts, self.log_file = self.root / "receipts.jsonl", self.root / "worker.log"

    def log(self, message: str) -> None:
        self.root.mkdir(parents=True, exist_ok=True)
        with self.log_file.open("a", encoding="utf-8") as fh:
            fh.write(f"{time.strftime('%F %T')} {message}\n")

    def receipt(self, task_id: str, kind: str, **data) -> None:
        self.root.mkdir(parents=True, exist_ok=True)
        record = {"at": time.strftime("%FT%T%z"), "ts": round(time.time(), 3), "task": task_id, "kind": kind, **data}
        with self.receipts.open("a", encoding="utf-8") as fh:
            fh.write(json.dumps(record, ensure_ascii=False, default=str) + "\n")

    def receipts_of(self, task_id: str) -> list[dict]:
        if not self.receipts.is_file():
            return []
        found = []
        for line in self.receipts.read_text(encoding="utf-8-sig").splitlines():
            record = json.loads(line)
            if record["task"] == task_id or record.get("group") == task_id:
                found.append(record)
        return found

    def path(self, task_id: str) -> Path:
        return self.jobs / f"{task_id}.json"

    def load(self, task_id: str) -> dict | None:
        path = self.path(task_id)
        return json.loads(path.read_text(encoding="utf-8-sig")) if path.is_file() else None

    def save(self, job: dict) -> None:
        """Replace the job file atomically; each write has its own temp file, so overlapping saves never collide."""
        self.jobs.mkdir(parents=True, exist_ok=True)
        fd, tmp = tempfile.mkstemp(prefix=f".{job['id']}.", suffix=".tmp", dir=self.jobs)
        try:
            with os.fdopen(fd, "w", encoding="utf-8") as fh:
                fh.write(json.dumps(job, ensure_ascii=False, indent=1))
            os.replace(tmp, self.path(job["id"]))
        except BaseException:
            Path(tmp).unlink(missing_ok=True)
            raise

    @contextlib.contextmanager
    def edit(self, task_id: str):
        """Read-modify-write of one job under its lock (the one ``notify`` holds): the job as stored now, saved on
        exit. None when there is no such job."""
        with self.lock(task_id):
            job = self.load(task_id)
            yield job
            if job is not None:
                self.save(job)

    def update(self, task_id: str, **changes) -> dict | None:
        with self.edit(task_id) as job:
            if job is not None:
                job.update(changes)
        return job

    def all(self) -> list[dict]:
        if not self.jobs.is_dir():
            return []
        return sorted((json.loads(p.read_text(encoding="utf-8-sig")) for p in self.jobs.glob("*.json")),
                      key=lambda job: (job["created"], job["id"]))  # glob order differs by OS

    def new_id(self, agent: str, prefix: str = "OT") -> str:
        """``OT-<AGENT3><MMDDhhmmss>`` (``OG-`` a group, ``OS-`` a schedule); ``-2``, ``-3`` … on a clash."""
        base = f"{prefix}-{''.join(ch for ch in agent.upper() if ch.isalnum())[:3] or 'X'}{time.strftime('%m%d%H%M%S')}"
        taken = self.path if prefix == "OT" else (lambda tid: self.schedules / f"{tid}.json")
        candidate, n = base, 1
        while (taken(candidate).exists() or (prefix == "OG" and any(j.get("group") == candidate for j in self.all()))
               or not self._reserve_id(candidate)):
            n += 1
            candidate = f"{base}-{n}"
        return candidate

    def _reserve_id(self, candidate: str) -> bool:
        """Claim *candidate* once across processes: two hubs in the same second never get the same ID."""
        ids = self.root / "ids"
        ids.mkdir(parents=True, exist_ok=True)
        try:
            os.close(os.open(ids / candidate, os.O_CREAT | os.O_EXCL | os.O_WRONLY))
        except FileExistsError:
            return False
        return True

    def out_dir(self, job: dict) -> Path:
        return self.outs / job["bot"] / job["id"]

    @contextlib.contextmanager
    def lock(self, name: str, *, wait: bool = True):
        """An exclusive lock across processes; reentrant in one thread (``notify`` saves through ``edit``)."""
        self.locks.mkdir(parents=True, exist_ok=True)
        path = str(self.locks / f"{name}.lock")
        held = _HELD.__dict__.setdefault("paths", set())
        if path in held:
            yield True
            return
        with open(path, "w") as handle:
            try:
                fcntl.flock(handle, fcntl.LOCK_EX | (0 if wait else fcntl.LOCK_NB))
            except BlockingIOError:
                yield False
                return
            held.add(path)
            try:
                yield True
            finally:
                held.discard(path)
                fcntl.flock(handle, fcntl.LOCK_UN)
