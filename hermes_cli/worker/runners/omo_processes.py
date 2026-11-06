"""Task-owned omo commands and durable terminal cleanup.

The caller unloads the session before cleanup so its in-memory monitor registry
cannot overwrite the edited manifest. Signals target individual proven PIDs,
never a process group that may also contain the shared daemon or foreign work.
"""

from __future__ import annotations

import json
import getpass
import os
import shlex
import signal
import subprocess
import time
from dataclasses import dataclass
from pathlib import Path
from typing import TypedDict

from hermes_cli.worker.runners.base import Task

WATCH_BINS = frozenset(("sh", "bash", "dash", "zsh", "sleep", "tail", "grep", "egrep", "fgrep",
                        "awk", "gawk", "mawk", "sed", "cat", "head", "tee", "cut", "tr", "timeout",
                        "stdbuf", "inotifywait", "watch", "test", "[", "true"))
TERM_WAIT_S = 5.0
KILL_WAIT_S = 2.0


class ProcessReceipt(TypedDict):
    pid: int
    pgid: int
    cmd: str


class RemovedMonitor(TypedDict):
    id: str | None
    description: str | None


class StopReceipt(TypedDict, total=False):
    killed: list[ProcessReceipt]
    left: list[ProcessReceipt]
    manifest_removed: list[RemovedMonitor]
    backup: str
    manifest_error: str
    errors: list[str]


@dataclass(frozen=True)
class Process:
    pid: int
    ppid: int
    pgid: int
    state: str
    argv: tuple[str, ...]
    cwd: str | None
    identity: str
    monitor: bool = False

    def receipt(self) -> ProcessReceipt:
        return {"pid": self.pid, "pgid": self.pgid, "cmd": shlex.join(self.argv)}


def _linux_process_table() -> dict[int, Process]:
    rows = {}
    for entry in Path("/proc").iterdir():
        if not entry.name.isdigit():
            continue
        try:
            fields = (entry / "stat").read_text(encoding="utf-8-sig").rsplit(")", 1)[1].split()
            argv = tuple(x.decode(errors="replace") for x in (entry / "cmdline").read_bytes().split(b"\0") if x)
            try:
                cwd = str((entry / "cwd").resolve(strict=True))
            except OSError:
                cwd = None
            try:
                monitor = any(x.startswith(b"SENPI_MONITOR_ID=") and x.split(b"=", 1)[1]
                              for x in (entry / "environ").read_bytes().split(b"\0"))
            except OSError:
                monitor = False
            pid = int(entry.name)
            rows[pid] = Process(pid, int(fields[1]), int(fields[2]), fields[0], argv, cwd, fields[19], monitor)
        except (OSError, ValueError, IndexError):
            continue  # Process exited between the directory snapshot and its record.
    return rows


def _portable_process_table() -> dict[int, Process]:
    """Read portable ps and lsof output; also exercised with fake output on Linux."""
    rows = {}
    output = subprocess.run(["/bin/ps", "-axww", "-o", "pid,ppid,pgid,stat,lstart,command"],
                            capture_output=True, text=True, errors="replace", timeout=20, check=False)
    cwds = {}
    user = str(os.getuid()) if hasattr(os, "getuid") else getpass.getuser()
    cwd_output = subprocess.run(["/usr/sbin/lsof", "-a", "-d", "cwd", "-u", user, "-Fpn"],
                                capture_output=True, text=True, errors="replace", timeout=30, check=False)
    pid = None
    for line in cwd_output.stdout.splitlines():
        if line.startswith("p"):
            pid = int(line[1:]) if line[1:].isdigit() else None
        if line.startswith("n") and pid is not None:
            cwds[pid] = line[1:]
    for line in output.stdout.splitlines():
        fields = line.split(None, 9)
        if len(fields) != 10 or not fields[0].isdigit():
            continue
        try:
            pid, parent, group = map(int, fields[:3])
            argv = tuple(shlex.split(fields[9]))
        except ValueError:
            continue
        # lstart survives exec; the command text does not. PID is the table key.
        birth = " ".join(fields[4:9])
        rows[pid] = Process(pid, parent, group, fields[3], argv, cwds.get(pid), birth)
    return rows


def _process_table() -> dict[int, Process]:
    return _linux_process_table() if Path("/proc").is_dir() else _portable_process_table()


def _terminal_manifest(task: Task) -> tuple[Path | None, dict]:
    try:
        session = json.loads(task.path("session.json").read_text(encoding="utf-8-sig"))
        sid, session_path = session.get("durableSessionId"), session.get("sessionPath")
        if not sid or not session_path or Path(sid).name != sid:
            return None, {}
        path = Path(session_path).parent / "extensions" / "terminal" / f"{sid}.json"
        return path, json.loads(path.read_text(encoding="utf-8-sig"))
    except (OSError, ValueError):
        return None, {}


def _is_runtime(process: Process) -> bool:
    """Inspect executable/script paths, never arbitrary command arguments."""
    if not process.argv:
        return False
    executable = Path(process.argv[0]).name
    paths = [process.argv[0]]
    if executable in ("node", "bun", "python", "python3") and len(process.argv) > 1:
        if not process.argv[1].startswith("-"):
            paths.append(process.argv[1])
    for path in paths:
        parts = Path(path).parts
        if any(tuple(parts[i:i + len(mark)]) == mark
               for mark in ((".omo", "agent", "runtime"), ("node_modules", "omo-ai"),
                            ("@code-yeongyu", "senpi"), (".omo-app",), ("omo-runtime",))
               for i in range(len(parts))):
            return True
    return False


def _owned(task: Task, rows: dict[int, Process], manifest: dict) -> tuple[list[Process], set[int]]:
    root = task.dir.resolve()
    protected, cursor = set(), os.getpid()
    while cursor > 1 and cursor not in protected and cursor in rows:
        protected.add(cursor)
        cursor = rows[cursor].ppid
    runtimes = {pid for pid, row in rows.items() if _is_runtime(row)}
    anchors, monitor_groups = set(), set()
    for record in [*(manifest.get("monitors") or []), *(manifest.get("backgroundSessions") or [])]:
        runtime = record.get("runtime") or {}
        pid, group = runtime.get("pid"), runtime.get("processGroupId")
        row = rows.get(pid)
        argv = runtime.get("argv") or []
        if row and pid > 1 and group == row.pgid and (not argv or tuple(argv) == row.argv[:len(argv)]):
            anchors.add(pid)
            if record.get("runtimeKind") == "command":
                monitor_groups.add(group)
    found = []
    for pid, row in rows.items():
        if pid in protected or pid in runtimes or row.state.startswith("Z") or not row.cwd or not row.argv:
            continue
        if not Path(row.cwd).resolve().is_relative_to(root):
            continue
        cursor, seen = pid, set()
        while cursor > 1 and cursor in rows and cursor not in seen:
            seen.add(cursor)
            if cursor in anchors or cursor in runtimes:
                found.append(row)
                break
            cursor = rows[cursor].ppid
    return found, monitor_groups


def live_work_processes(task: Task) -> list[ProcessReceipt]:
    """Live task commands, including sibling worktrees but excluding monitor primitives."""
    _, manifest = _terminal_manifest(task)
    rows, groups = _owned(task, _process_table(), manifest)
    return [row.receipt() for row in rows
            if not ((row.monitor or row.pgid in groups) and Path(row.argv[0]).name.lstrip("-") in WATCH_BINS)]


def _drop_command_monitors(task: Task, path: Path | None, manifest: dict, receipt: StopReceipt) -> None:
    if path is None:
        return
    removed = [m for m in manifest.get("monitors") or [] if m.get("runtimeKind") == "command"]
    if not removed:
        return
    backup = task.path(f"terminal-manifest.cancel.{time.time_ns()}.json")
    temporary = path.with_name(f".{path.name}.{os.getpid()}.tmp")
    try:
        backup.write_bytes(path.read_bytes())
        receipt["backup"] = backup.name
        manifest["monitors"] = [m for m in manifest["monitors"] if m.get("runtimeKind") != "command"]
        if not manifest["monitors"] and not manifest.get("backgroundSessions"):
            path.unlink()
        else:
            with temporary.open("x", encoding="utf-8") as stream:
                os.chmod(temporary, 0o600)
                json.dump(manifest, stream, indent=2)
            temporary.replace(path)
        receipt["manifest_removed"] = [{"id": m.get("monitorId"), "description": m.get("description")}
                                       for m in removed]
    except OSError as error:
        receipt["manifest_error"] = str(error)
    finally:
        temporary.unlink(missing_ok=True)


def stop_task_processes(task: Task) -> StopReceipt:
    """Remove restartable command monitors and TERM/KILL only proven task PIDs.

    Keep process identities across reparenting, so killing a shell does not hide
    its already-proven children. Re-read identity immediately before each signal.
    """
    receipt: StopReceipt = {"killed": [], "left": [], "manifest_removed": [], "errors": []}
    path, manifest = _terminal_manifest(task)
    initial, _ = _owned(task, _process_table(), manifest)
    tracked = {row.pid: row for row in initial}
    _drop_command_monitors(task, path, manifest.copy(), receipt)
    for sig, duration in ((signal.SIGTERM, TERM_WAIT_S), (getattr(signal, "SIGKILL", signal.SIGTERM), KILL_WAIT_S)):
        sent = set()
        deadline = time.monotonic() + duration
        while True:
            rows = _process_table()
            discovered, _ = _owned(task, rows, manifest)
            for row in discovered:
                tracked.setdefault(row.pid, row)
            remaining = [row for pid, row in tracked.items() if pid in rows
                         and rows[pid].identity == row.identity and not rows[pid].state.startswith("Z")]
            if not remaining:
                break
            for row in remaining:
                if row.pid in sent:
                    continue
                current = _process_table().get(row.pid)
                if not current or current.identity != row.identity or current.pgid != row.pgid:
                    continue
                try:
                    os.kill(row.pid, sig)
                    sent.add(row.pid)
                except ProcessLookupError:
                    continue
                except PermissionError as error:
                    receipt["errors"].append(f"{row.pid}: {error}")
                    sent.add(row.pid)
            if time.monotonic() >= deadline:
                break
            time.sleep(min(0.05, max(0, deadline - time.monotonic())))
        if not remaining:
            break
    rows = _process_table()
    for pid, row in tracked.items():
        current = rows.get(pid)
        key = "left" if current and current.identity == row.identity and not current.state.startswith("Z") else "killed"
        receipt[key].append(row.receipt())
    return receipt
