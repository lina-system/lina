"""The work record ``lina work`` reads its running jobs from.

``WorkRecord`` is the one interface: ``jobs()`` returns every job with ``id``, ``state`` (``running`` while it runs,
``ended`` once it ended, ``free`` when it failed, was cancelled or rejected), ``repo``, ``runner`` and ``text`` (its
brief and topic, where the ``owner/repo#n`` it works on is named). ``WorkerHubRecord`` reads ``lina worker``'s hub
(lina#35) through its store. When ``lina work`` (#36) moves the ledger into the core, a ledger-backed record
replaces it here and nothing else changes.
"""
from __future__ import annotations

from pathlib import Path
from typing import Protocol

RUNNING = ("new", "queued", "running")
FREE = ("failed", "cancelled")


class WorkRecord(Protocol):
    def jobs(self) -> list[dict]: ...


class WorkerHubRecord:
    def __init__(self, base: Path | None = None):
        from hermes_cli.worker import settings
        self.root = settings.root(base)

    def jobs(self) -> list[dict]:
        from hermes_cli.worker.hub_store import Store
        found = []
        for job in Store(self.root).all():
            state = job.get("state")
            if state in RUNNING:
                kind = "running"
            elif state in FREE or job.get("verdict") == "rejected":
                kind = "free"
            else:
                kind = "ended"
            brief = Path(str(job.get("brief") or ""))
            text = brief.read_text(encoding="utf-8-sig", errors="replace") if brief.is_file() else ""
            found.append({"id": job["id"], "state": kind, "repo": job.get("repo"), "runner": job.get("runner"),
                          "text": f"{job.get('topic') or ''}\n{text}"})
        return found
