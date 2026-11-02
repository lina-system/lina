"""One task folder on a device: the manifest, the read-only inputs and the end state of a run.

``$LINA_HOME/worker/tasks/<ID>/`` keeps the layout the dashboard and receipt readers know: ``prompt.<n>``,
``grant.json``, ``input.manifest.json``, ``manifest.json``, ``final.md``, ``events.<n>.jsonl``, ``wait.<n>.json``,
``progress.<n>.jsonl``, ``stderr.<n>.txt`` beside ``work/`` (``report.md``, ``out/``, ``input/``) and ``git.txt``.
"""

from __future__ import annotations

import hashlib
import json
import os
import re
import stat
import subprocess
import time
from pathlib import Path

from hermes_cli.worker.report import next_notes
from hermes_cli.worker.runners.base import Task, standard_result


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as fh:
        for block in iter(lambda: fh.read(1 << 20), b""):
            digest.update(block)
    return digest.hexdigest()


def read_json(path: Path, default=None):
    """A JSON file, or the last line of a file a tool printed one JSON line into."""
    try:
        text = path.read_text(encoding="utf-8-sig", errors="replace").strip()
    except OSError:
        return default
    for candidate in (text, text.splitlines()[-1] if text else ""):
        try:
            return json.loads(candidate)
        except ValueError:
            continue
    return default


def write_json(path: Path, data) -> None:
    tmp = path.with_name(f".{path.name}.tmp")
    tmp.write_text(json.dumps(data, ensure_ascii=False, indent=1), encoding="utf-8")
    os.replace(tmp, path)


def stamp(task: Task, name: str) -> None:
    task.path(name).write_text(f"{time.time():.3f}\n", encoding="utf-8")


def number(task: Task, name: str) -> float | None:
    try:
        return float(task.path(name).read_text(encoding="utf-8-sig").strip())
    except (OSError, ValueError):
        return None


def input_files(task: Task) -> dict[str, str]:
    base = task.work / "input"
    if not base.is_dir():
        return {}
    return {path.relative_to(task.work).as_posix(): sha256(path)
            for path in sorted(base.rglob("*")) if path.is_file()}


def lock_inputs(task: Task) -> dict[str, str]:
    """Make ``work/input`` read-only and record its hashes, so a changed input shows up in the manifest."""
    base = task.work / "input"
    if not base.is_dir():
        return {}
    for path in [base, *base.rglob("*")]:
        mode = path.stat().st_mode
        path.chmod(mode & ~(stat.S_IWUSR | stat.S_IWGRP | stat.S_IWOTH))
    hashes = input_files(task)
    write_json(task.path("input.manifest.json"), hashes)
    return hashes


def _git_head(work: Path) -> str | None:
    if not (work / ".git").exists():
        return None
    result = subprocess.run(["git", "-C", str(work), "rev-parse", "HEAD"], text=True, encoding="utf-8",
                            errors="replace", capture_output=True, timeout=60, check=False)
    return result.stdout.strip() or None


def write_manifest(task: Task, run: int, files: dict[str, Path] | None = None) -> dict:
    """Hash what the run delivered once it settled: anything changed after this reads as a mismatch on fetch."""
    files = standard_result(task) if files is None else files
    hashes = {name: sha256(path) for name, path in files.items()}
    manifest = {"run": run, "files": hashes, "deliverables": sum(1 for name in hashes if name != "final.md"),
                "next_notes": next_notes(task.work / "report.md"), "head": _git_head(task.work)}
    wanted = read_json(task.path("input.manifest.json"))
    if wanted is not None:
        manifest["inputs_unchanged"] = input_files(task) == wanted
    started, accepted, ended = (number(task, f"{kind}.{run}") for kind in ("started", "accepted", "ended"))
    manifest["start_latency_s"] = round(accepted - started, 2) if started and accepted else None
    manifest["run_s"] = round(ended - started, 1) if started and ended else None
    manifest["hashed_at"] = time.time()
    for key, name in (("wait", f"wait.{run}.json"), ("lifecycle", f"lifecycle.{run}.json")):
        manifest[key] = read_json(task.path(name))
    manifest["model"] = (read_json(task.path("job.json"), {}) or {}).get("model")
    write_json(task.path("manifest.json"), manifest)
    return manifest


def end_state(runner_state: str, manifest: dict, *, cancelled: bool, next_notes_required: bool) -> str:
    """The run's ending state from what the runner saw and what it delivered.

    A cancel wins; ``done`` with nothing in ``report.md`` or ``out/`` is ``done_empty``; ``done`` whose report lacks
    ``## 다음에 알아야 할 것`` (or leaves it empty) is ``review`` when the task carries that rule."""
    if cancelled or runner_state == "cancelled":
        return "cancelled"
    if runner_state != "done":
        return runner_state if runner_state in ("stalled", "failed") else "failed"
    if not manifest.get("deliverables"):
        return "done_empty"
    if next_notes_required and not manifest.get("next_notes"):
        return "review"
    return "done"


def git_summary(task: Task) -> str:
    """``git.txt``: branch, worktree, commits since the base and the diff stat."""
    work, base = task.work, task.path("base")
    if not (work / ".git").exists() or not base.exists():
        return ""
    start = base.read_text(encoding="utf-8-sig").strip()

    def git(*args: str) -> str:
        return subprocess.run(["git", "-C", str(work), *args], capture_output=True, text=True, encoding="utf-8",
                              errors="replace", timeout=60, check=False).stdout

    stat_lines = git("diff", "--stat", f"{start}..HEAD").splitlines()[-20:]
    return (f"branch: {git('branch', '--show-current').strip()}\nworktree: {work}\n\n"
            f"{git('log', '--oneline', f'{start}..HEAD')}\n" + "\n".join(stat_lines) + "\n")


_SAFE_ID = re.compile(r"^O[TGS]-[A-Z0-9_-]{1,40}$")


def safe_id(task_id: str) -> str:
    if not _SAFE_ID.match(task_id or ""):
        raise ValueError(f"not a worker task id: {task_id!r}")
    return task_id
