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


@pytest.mark.parametrize("with_child", [False, True])
def test_python_kernel_is_not_work_but_its_child_is(api, task, monkeypatch, with_child):
    kernel = task.dir / "home/.omo/agent/runtime/id/node_modules/@code-yeongyu/senpi-codemode/src/kernels/py/prelude.py"
    rows = {10: proc(api, 10, 1, 10, "/", ("node", "/opt/node_modules/omo-ai/cli.js")),
            11: proc(api, 11, 10, 11, task.work, ("python3", "-u", str(kernel)))}
    if with_child:
        rows[12] = proc(api, 12, 11, 12, task.work, ("pytest", "-q"))
    monkeypatch.setattr(api, "_process_table", lambda: rows)
    assert {row["pid"] for row in api.live_work_processes(task)} == ({12} if with_child else set())


@pytest.mark.parametrize("interpreter,options", [
    ("python", ("-B",)),
    ("python3", ("-u", "-B", "-X", "dev", "-W", "ignore")),
    ("/usr/bin/python3.14", ("-uB", "-Xdev", "-Wignore")),
    ("python3.13", ("--",)),
    ("node", ("--no-warnings",)),
    ("node", ("--require", "./bootstrap.js", "--import=./loader.mjs")),
    ("bun", ("--smol",)),
    ("bun", ("--preload", "./bootstrap.js", "--",)),
])
def test_runtime_interpreter_options_reach_only_script(api, task, interpreter, options):
    script = "/opt/.omo/agent/runtime/id/kernel.py"
    assert api._is_runtime(proc(api, 11, 1, 11, task.work, (interpreter, *options, script)))


@pytest.mark.parametrize("argv", [
    ("python3", "-c", "/opt/.omo/agent/runtime/id/code"),
    ("python3", "-m", "/opt/.omo/agent/runtime/id/module"),
    ("python3", "-W", "/opt/.omo/agent/runtime/id/warning", "work.py"),
    ("python3", "-X", "/opt/.omo/agent/runtime/id/option", "work.py"),
    ("python3", "-u", "work.py", "/opt/.omo/agent/runtime/id/argument"),
    ("python3", "-", "/opt/.omo/agent/runtime/id/argument"),
    ("python3", "--unknown", "/opt/.omo/agent/runtime/id/argument"),
    ("python3", "-X"),
    ("python3", "--"),
    ("python3.bad", "/opt/.omo/agent/runtime/id/kernel.py"),
    ("node", "--eval", "/opt/.omo/agent/runtime/id/code"),
    ("node", "-p", "/opt/.omo/agent/runtime/id/code"),
    ("node", "--require", "/opt/.omo/agent/runtime/id/loader.js", "work.js"),
    ("bun", "--preload", "/opt/.omo/agent/runtime/id/loader.js", "work.js"),
    ("bun", "--smol", "work.js", "/opt/.omo/agent/runtime/id/argument"),
])
def test_runtime_interpreter_arguments_are_not_paths(api, task, argv):
    assert not api._is_runtime(proc(api, 11, 1, 11, task.work, argv))


@pytest.mark.parametrize("foreign_inside", [False, True])
def test_foreign_cwd_is_never_resolved_before_ownership(api, task, monkeypatch, foreign_inside):
    foreign = task.dir / "foreign" if foreign_inside else task.dir.parent / "foreign"
    outside = task.dir.parent / f"{task.dir.name}-other"
    rows = {10: proc(api, 10, 1, 10, "/", ("node", "/opt/node_modules/omo-ai/cli.js")),
            11: proc(api, 11, 10, 11, task.work),
            12: proc(api, 12, 1, 12, foreign),
            13: proc(api, 13, 10, 13, outside)}
    resolve = Path.resolve
    resolved = []

    def guarded(path, *args, **kwargs):
        assert path not in (foreign, outside), "unowned or out-of-task cwd was resolved"
        resolved.append(path)
        return resolve(path, *args, **kwargs)

    monkeypatch.setattr(Path, "resolve", guarded)
    monkeypatch.setattr(api, "_process_table", lambda: rows)
    assert {row["pid"] for row in api.live_work_processes(task)} == {11}
    assert task.work in resolved


@pytest.mark.parametrize("error", [FileNotFoundError, PermissionError])
def test_unresolvable_owned_cwd_is_not_owned(api, task, monkeypatch, error):
    missing = task.dir / "unavailable"
    rows = {10: proc(api, 10, 1, 10, "/", ("node", "/opt/node_modules/omo-ai/cli.js")),
            11: proc(api, 11, 10, 11, task.work),
            12: proc(api, 12, 10, 12, missing)}
    resolve = Path.resolve

    def guarded(path, *args, **kwargs):
        if path == missing:
            raise error("cwd unavailable")
        return resolve(path, *args, **kwargs)

    monkeypatch.setattr(Path, "resolve", guarded)
    monkeypatch.setattr(api, "_process_table", lambda: rows)
    assert {row["pid"] for row in api.live_work_processes(task)} == {11}


@pytest.mark.platforms("posix")
def test_linux_census_reads_cwd_link_without_resolving_target(api, tmp_path, monkeypatch):
    entry = tmp_path / "proc" / "21"
    entry.mkdir(parents=True)
    (entry / "stat").write_text("21 (bash) " + " ".join(["S", "1", "21", *(["0"] * 17)]))
    (entry / "cmdline").write_bytes(b"bash\0")
    (entry / "environ").write_bytes(b"")
    foreign = tmp_path / "foreign-home"
    (entry / "cwd").symlink_to(foreign)
    iterdir, resolve = Path.iterdir, Path.resolve

    def entries(path):
        return iter([entry]) if path == Path("/proc") else iterdir(path)

    def guarded(path, *args, **kwargs):
        assert path not in (entry / "cwd", foreign), "census resolved a cwd target"
        return resolve(path, *args, **kwargs)

    monkeypatch.setattr(Path, "iterdir", entries)
    monkeypatch.setattr(Path, "resolve", guarded)
    assert api._linux_process_table()[21].cwd == str(foreign)


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
        text = (" PID PPID PGID STAT STARTED COMMAND\n"
                "10 1 10 S Fri Nov  6 00:00:00 2026 node /opt/node_modules/omo-ai/cli.js\n"
                "11 10 11 S Fri Nov  6 00:00:01 2026 /usr/bin/python 'build file.py'\n"
                "broken row\n12 bad 12 S sleep 10\n") if argv[0] == "/bin/ps" else (
                    f"p11\nn{task.work}\npbad\nn/ignored\n")
        return subprocess.CompletedProcess(argv, 0, text, "")

    monkeypatch.setattr(api.subprocess, "run", run)
    rows = api._portable_process_table()
    assert rows[11].cwd == str(task.work)
    assert rows[11].argv == ("/usr/bin/python", "build file.py")
    assert set(rows) == {10, 11}
    assert calls[0][1:] == ["-axww", "-o", "pid,ppid,pgid,stat,lstart,command"]
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


@pytest.mark.parametrize("exits", [False, True])
def test_portable_exec_keeps_identity_until_process_is_gone(api, task, monkeypatch, exits):
    manifest(task, backgrounds=[{"runtime": {"pid": 21, "processGroupId": 21}}])
    scans, alive, signals = [0], [True], []

    def run(argv, **kwargs):
        text = f"p21\nn{task.work}\n"
        if argv[0] == "/bin/ps":
            scans[0] += 1
            command = "/bin/bash build.sh" if scans[0] == 1 else "/usr/bin/python build.py"
            birth = "Fri Nov  6 00:00:00 2026 " if "lstart" in argv[-1] else ""
            text = f"21 1 21 S {birth}{command}\n" if alive[0] else ""
        return subprocess.CompletedProcess(argv, 0, text, "")

    def kill(pid, sig):
        signals.append((pid, sig))
        if exits and sig == signal.SIGKILL:
            alive[0] = False

    monkeypatch.setattr(api.subprocess, "run", run)
    monkeypatch.setattr(api, "_process_table", api._portable_process_table)
    monkeypatch.setattr(api.os, "kill", kill)
    monkeypatch.setattr(api, "TERM_WAIT_S", 0)
    monkeypatch.setattr(api, "KILL_WAIT_S", 0)
    receipt = api.stop_task_processes(task)
    assert signals == [(21, signal.SIGTERM), (21, signal.SIGKILL)]
    assert [row["pid"] for row in receipt["killed"]] == ([21] if exits else [])
    assert [row["pid"] for row in receipt["left"]] == ([] if exits else [21])


def test_portable_reused_pid_has_a_different_birth_identity(api, task, monkeypatch):
    births = iter(("Fri Nov 6 00:00:00 2026", "Fri Nov 6 00:01:00 2026"))

    def run(argv, **kwargs):
        text = f"p21\nn{task.work}\n"
        if argv[0] == "/bin/ps":
            birth = next(births)
            column = f"{birth} " if "lstart" in argv[-1] else ""
            text = f"21 1 21 S {column}/bin/bash build.sh\n"
        return subprocess.CompletedProcess(argv, 0, text, "")

    monkeypatch.setattr(api.subprocess, "run", run)
    first = api._portable_process_table()[21]
    replacement = api._portable_process_table()[21]
    assert first.identity != replacement.identity


@pytest.mark.platforms("linux")
def test_live_exact_cleanup_preserves_owner_and_stray(api, task, monkeypatch):
    """Pipe readiness precedes scanning; the owner reaps the child before exiting."""
    read_fd, write_fd = os.pipe()
    runtime = task.dir / ".omo" / "agent" / "runtime" / "fixture" / "python"
    runtime.parent.mkdir(parents=True)
    runtime.symlink_to(sys.executable)
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
        # Read real process records only for this fixture's three known PIDs.
        iterdir = Path.iterdir

        def entries(path):
            return iter(path / str(pid) for pid in (owner.pid, child, stray.pid)) if path == Path("/proc") else iterdir(path)

        monkeypatch.setattr(Path, "iterdir", entries)
        monkeypatch.setattr(api, "_process_table", api._linux_process_table)
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
