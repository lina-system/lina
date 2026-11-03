"""Deterministic command answer/state and exit-publication races (lina#199)."""

from concurrent.futures import ThreadPoolExecutor
import os
from pathlib import Path
import subprocess
from threading import Event
import time

import pytest

from hermes_cli.worker.runners.base import Handle, Task
from hermes_cli.worker.runners.command import CommandRunner, _WRAP

pytestmark = pytest.mark.platforms("posix")


def test_state_does_not_finish_an_old_step_after_answer(tmp_path: Path, monkeypatch):
    task = Task("answer-race", tmp_path)
    task.work.mkdir()
    gate = task.path("release")
    os.mkfifo(gate)
    runner = CommandRunner("cli", {"argv": ["/bin/sh", "-c", 'read release < "$1"', "sh", str(gate)]})
    handle = Handle(task, 1, {"runner": "cli", "step": 1, "since": time.time()})
    handle.save()
    task.path("exit.1.1").write_text("0\n", encoding="utf-8")
    (task.work / "question.md").write_text("Which colour?\n", encoding="utf-8")
    loaded, answered = Event(), Event()
    original_load = Handle.load

    def pause_after_load(task, run=None):
        snapshot = original_load(task, run)
        loaded.set()
        assert answered.wait(10), "answer did not publish its next step"
        return snapshot

    monkeypatch.setattr(Handle, "load", pause_after_load)
    with ThreadPoolExecutor(max_workers=1) as pool:
        state = pool.submit(runner.state, handle)
        try:
            try:
                assert loaded.wait(10), "state did not load step 1"
                assert runner.answer(original_load(task), "blue")["action"] == "answer"
                assert original_load(task).data["step"] == 2
                assert not (task.work / "question.md").exists()
            finally:
                answered.set()
            assert state.result(timeout=10) in ("running", "waiting_human")
        finally:
            runner.stop(original_load(task))


@pytest.mark.parametrize("empty_exit", [False, True], ids=["not-created", "empty"])
def test_state_does_not_fail_when_exit_is_published_during_observation(tmp_path: Path, monkeypatch, empty_exit):
    task = Task("exit-race", tmp_path)
    task.work.mkdir()
    rc_file = task.path("exit.1.1")
    runner = CommandRunner("cli", {"argv": ["/bin/sh"]})
    with subprocess.Popen(["/bin/sh", "-c", _WRAP, "sh", str(rc_file),
                           "/bin/sh", "-c", "read release"], stdin=subprocess.PIPE) as proc:
        handle = Handle(task, 1, {"runner": "cli", "step": 1, "pid": proc.pid, "since": time.time()})
        handle.save()
        if empty_exit:
            rc_file.write_text("", encoding="utf-8")
        original_exit_code = runner._exit_code

        def finish_after_exit_read(handle):
            code = original_exit_code(handle)
            proc.communicate(b"continue\n", timeout=10)
            return code

        monkeypatch.setattr(runner, "_exit_code", finish_after_exit_read)
        assert runner.state(handle) == "running"
        monkeypatch.setattr(runner, "_exit_code", original_exit_code)
        assert runner.state(handle) == "done"
