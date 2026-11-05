"""Task-owned process and durable monitor contracts for #228."""

import importlib
import json
import os
import selectors
import signal
import subprocess
import sys
from pathlib import Path

import pytest

from hermes_cli.worker.runners.base import Task


@pytest.fixture
def api():
    return importlib.import_module("hermes_cli.worker.runners.omo_processes")


@pytest.fixture
def task(tmp_path):
    task = Task("OT-PROCESSES", tmp_path)
    task.work.mkdir()
    return task


def manifest(task, monitors=(), backgrounds=()):
    folder = task.dir / "sessions"
    target = folder / "extensions" / "terminal" / "session.json"
    target.parent.mkdir(parents=True, exist_ok=True)
    task.path("session.json").write_text(json.dumps({"sessionPath": str(folder / "session.jsonl"),
                                                   "durableSessionId": "session"}))
    target.write_text(json.dumps({"version": 1, "monitors": list(monitors),
                                  "backgroundSessions": list(backgrounds), "extra": "kept"}))
    return target


def proc(api, pid, parent, group, cwd, argv=("sleep", "100"), monitor=False, state="S"):
    return api.Process(pid, parent, group, state, tuple(argv), str(cwd), str(pid), monitor)


def test_scope_ancestry_and_argv_literals(api, task, monkeypatch):
    root = proc(api, 10, 1, 10, "/", ("node", "/opt/node_modules/omo-ai/cli.js"))
    sibling = task.dir / "other-worktree"
    sibling.mkdir()
    rows = {10: root,
            11: proc(api, 11, 10, 10, task.work),
            12: proc(api, 12, 10, 12, sibling),
            13: proc(api, 13, 1, 13, task.work),
            14: proc(api, 14, 10, 14, str(task.dir) + "-other"),
            15: proc(api, 15, 1, 15, "/", ("echo", "/opt/node_modules/omo-ai/cli.js")),
            16: proc(api, 16, 15, 16, task.work),
            17: proc(api, 17, 10, 17, task.work, state="Z")}
    monkeypatch.setattr(api, "_process_table", lambda: rows)
    assert {p["pid"] for p in api.live_work_processes(task)} == {11, 12}


@pytest.mark.parametrize("recorded", [False, True])
def test_only_monitor_primitives_are_excluded(api, task, monkeypatch, recorded):
    manifest(task, [{"runtimeKind": "command", "runtime": {"pid": 21, "processGroupId": 21}}]
             if recorded else [])
    rows = {10: proc(api, 10, 1, 10, "/", ("node", "/opt/node_modules/omo-ai/cli.js")),
            21: proc(api, 21, 10, 21, task.work, ("bash",), not recorded),
            22: proc(api, 22, 21, 21, task.work, ("sleep", "10"), not recorded),
            23: proc(api, 23, 21, 21, task.work, ("python", "build.py"), not recorded),
            24: proc(api, 24, 10, 24, task.work, ("sleep", "10"))}
    monkeypatch.setattr(api, "_process_table", lambda: rows)
    assert {p["pid"] for p in api.live_work_processes(task)} == {23, 24}


def test_recorded_runtime_owns_detached_terminal(api, task, monkeypatch):
    manifest(task, backgrounds=[{"runtime": {"pid": 21, "processGroupId": 21, "argv": ["bash"]}}])
    rows = {21: proc(api, 21, 1, 21, task.work, ("bash",)),
            22: proc(api, 22, 21, 21, task.work),
            23: proc(api, 23, 1, 21, task.work)}
    monkeypatch.setattr(api, "_process_table", lambda: rows)
    assert {p["pid"] for p in api.live_work_processes(task)} == {21, 22}
    rows[21] = proc(api, 21, 1, 25, task.work, ("bash",))
    assert api.live_work_processes(task) == []


def test_portable_ps_lsof_parsing(api, task, monkeypatch):
    calls = []

    def run(argv, **kwargs):
        calls.append(argv)
        text = (" PID PPID PGID STAT COMMAND\n"
                "10 1 10 S node /opt/node_modules/omo-ai/cli.js\n"
                "11 10 11 S /usr/bin/python 'build file.py'\n"
                "broken row\n12 bad 12 S sleep 10\n") if argv[0] == "/bin/ps" else (
                    f"p11\nn{task.work}\npbad\nn/ignored\n")
        return subprocess.CompletedProcess(argv, 0, text, "")

    monkeypatch.setattr(api.subprocess, "run", run)
    rows = api._portable_process_table()
    assert rows[11].cwd == str(task.work)
    assert rows[11].argv == ("/usr/bin/python", "build file.py")
    assert set(rows) == {10, 11}
    assert calls[0][1:] == ["-axww", "-o", "pid,ppid,pgid,stat,command"]
    assert calls[1][1:] == ["-a", "-d", "cwd", "-u", str(os.getuid()), "-Fpn"]


def test_cancel_preserves_noncommand_records_and_backup(api, task, monkeypatch):
    command = {"monitorId": "command", "runtimeKind": "command", "suspended": True}
    others = [{"monitorId": "rpc", "runtimeKind": "rpc"}, {"runtimeKind": "file"}]
    target = manifest(task, [command, *others], [{"id": "background"}])
    original = target.read_bytes()
    monkeypatch.setattr(api, "_process_table", dict)
    receipt = api.stop_task_processes(task)
    saved = json.loads(target.read_text())
    assert saved["monitors"] == others
    assert saved["backgroundSessions"] == [{"id": "background"}]
    assert saved["extra"] == "kept"
    assert receipt["manifest_removed"] == [{"id": "command", "description": None}]
    assert task.path(receipt["backup"]).read_bytes() == original
    assert api.stop_task_processes(task)["manifest_removed"] == []


def test_cancel_empty_manifest_is_removed(api, task, monkeypatch):
    target = manifest(task, [{"runtimeKind": "command"}])
    monkeypatch.setattr(api, "_process_table", dict)
    assert api.stop_task_processes(task)["left"] == []
    assert not target.exists()


@pytest.mark.platforms("linux")
def test_live_exact_cleanup_preserves_owner_and_stray(api, task, monkeypatch):
    """Pipe readiness precedes scanning; the owner reaps the child before exiting."""
    read_fd, write_fd = os.pipe()
    runtime = task.dir / ".omo" / "agent" / "runtime" / "fixture" / "python"
    runtime.parent.mkdir(parents=True)
    runtime.symlink_to(sys.executable)
    # The test runner may itself be below the real shared omo daemon. Bound
    # runtime identity to the fixture so the independently launched stray is foreign.
    is_runtime = api._is_runtime
    monkeypatch.setattr(api, "_is_runtime", lambda row: bool(row.argv and row.argv[0] == str(runtime))
                        and is_runtime(row))
    owner_code = (
        "import os,signal,subprocess,sys; signal.signal(signal.SIGTERM,signal.SIG_IGN); "
        "p=subprocess.Popen([sys.argv[2],'-c',"
        "'import os,signal,sys; signal.signal(signal.SIGTERM,signal.SIG_IGN); '"
        "'os.write(int(sys.argv[1]),str(os.getpid()).encode()+bytes([10])); signal.pause()',sys.argv[1]],"
        "pass_fds=(int(sys.argv[1]),)); p.wait(); signal.pause()"
    )
    owner = subprocess.Popen([str(runtime), "-c", owner_code, str(write_fd), sys.executable], cwd=task.work,
                             pass_fds=(write_fd,), start_new_session=True)
    stray = subprocess.Popen([sys.executable, "-c", "import signal; signal.pause()"], cwd=task.work,
                             start_new_session=True)
    child = None
    os.close(write_fd)
    try:
        with selectors.DefaultSelector() as selector:
            selector.register(read_fd, selectors.EVENT_READ)
            assert selector.select(timeout=10), "child never became ready"
            child = int(os.read(read_fd, 64).strip())
        manifest(task, backgrounds=[{"runtime": {"pid": child, "processGroupId": owner.pid,
                                                 "argv": [sys.executable]}}])
        assert {p["pid"] for p in api.live_work_processes(task)} == {child}
        receipt = api.stop_task_processes(task)
        assert receipt["left"] == []
        assert {p["pid"] for p in receipt["killed"]} == {child}
        assert owner.poll() is None
        assert stray.poll() is None
        assert not any(p["pid"] == child for p in api.live_work_processes(task))
    finally:
        os.close(read_fd)
        for pid in (child, owner.pid, stray.pid):
            if pid is not None:
                try:
                    os.kill(pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
        owner.wait(timeout=10)
        stray.wait(timeout=10)
