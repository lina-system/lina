"""The runner contract: how ``lina worker`` drives one coding agent through one task.

A runner works in a task folder (``Task``) and keeps what it needs between calls in a ``Handle`` that is stored in
the folder, so another process (``send``, ``cancel``, a restarted service) can pick the run up by task ID alone.

* ``start(task, prompt, model)`` begins run 1; ``follow_up(handle, text)`` delivers more work to the same session:
  into the live run when the agent can take it (``steered``), else as the next run (``started``).
* ``answer(handle, text)`` answers the question the agent is waiting on (``waiting_human``).
* ``state(handle)`` is ``running``, ``waiting_human`` or an end: ``done``, ``stalled``, ``failed``, ``cancelled``.
  ``done`` is refined by the worker from what the run delivered (``done_empty``, ``review``; ``task.end_state``).
* ``result(handle)`` names the files the run produced; ``stop(handle)`` ends it.
"""

from __future__ import annotations

import json
import os
import threading
from dataclasses import dataclass, field
from pathlib import Path
from typing import Protocol

LIVE = ("running", "waiting_human")
RUNNER_ENDS = ("done", "stalled", "failed", "cancelled")
ENDING_STATES = ("done", "review", "done_empty", "stalled", "failed", "cancelled", "unknown")


class RunnerError(RuntimeError):
    pass


@dataclass(frozen=True)
class ModelProfile:
    """The main model of a run, recorded with it: where it came from (``mode:<mode>``, ``--model``, ``runner``)."""
    provider: str | None = None
    id: str | None = None
    thinking: str | None = None
    alias: str | None = None
    source: str = "runner"

    def as_dict(self) -> dict:
        return {"provider": self.provider, "id": self.id, "thinking": self.thinking, "alias": self.alias,
                "source": self.source}

    @classmethod
    def from_dict(cls, data: dict | None) -> ModelProfile | None:
        if not data:
            return None
        return cls(data.get("provider"), data.get("id"), data.get("thinking"), data.get("alias"),
                   data.get("source") or "runner")


@dataclass
class Task:
    id: str
    dir: Path
    limits: dict = field(default_factory=dict)

    @property
    def work(self) -> Path:
        return self.dir / "work"

    def path(self, name: str) -> Path:
        return self.dir / name

    def event(self, run: int, kind: str, **data) -> None:
        import time
        with self.path(f"events.{run}.jsonl").open("a", encoding="utf-8") as fh:
            fh.write(json.dumps({"at": time.time(), "kind": kind, **data}, ensure_ascii=False) + "\n")


@dataclass
class Handle:
    task: Task
    run: int
    data: dict = field(default_factory=dict)

    def save(self) -> None:
        path = self.task.path("handle.json")
        tmp = path.with_name(f".handle.{os.getpid()}.{threading.get_ident()}.tmp")
        tmp.write_text(json.dumps({"run": self.run, **self.data}, ensure_ascii=False), encoding="utf-8")
        os.replace(tmp, path)

    @classmethod
    def load(cls, task: Task, run: int | None = None) -> Handle:
        path = task.path("handle.json")
        data = json.loads(path.read_text(encoding="utf-8-sig")) if path.exists() else {}
        saved = int(data.pop("run", 1))
        return cls(task, run or saved, data)


class Runner(Protocol):
    name: str

    def available(self) -> tuple[bool, str]: ...

    def start(self, task: Task, prompt: str, model: ModelProfile | None) -> Handle: ...

    def follow_up(self, handle: Handle, text: str, *, run: int | None = None) -> tuple[str, Handle]: ...

    def answer(self, handle: Handle, text: str) -> dict: ...

    def state(self, handle: Handle) -> str: ...

    def result(self, handle: Handle) -> dict[str, Path]: ...

    def stop(self, handle: Handle) -> None: ...


def standard_result(task: Task) -> dict[str, Path]:
    """``report.md``, ``final.md`` and every file under ``out/``, by the name the manifest records them under."""
    files: dict[str, Path] = {}
    if (task.work / "report.md").is_file():
        files["report.md"] = task.work / "report.md"
    if task.path("final.md").is_file():
        files["final.md"] = task.path("final.md")
    out = task.work / "out"
    if out.is_dir():
        for path in sorted(p for p in out.rglob("*") if p.is_file()):
            files[path.relative_to(task.work).as_posix()] = path
    return files
