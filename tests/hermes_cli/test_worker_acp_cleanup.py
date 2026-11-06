"""ACP teardown must outlive broken buffered pipes (PR 286 review)."""

import contextlib
import json
import os
import signal
import subprocess
import sys

import pytest

from hermes_cli.worker.runners.acp import AcpRunner, _Session
from hermes_cli.worker.runners.base import Handle, Task

pytestmark = pytest.mark.platforms("posix")

EARLY_CLOSE = r'''
import json, os, signal, sys
signal.signal(signal.SIGTERM, signal.SIG_IGN)
os.close(0)
print(json.dumps({"jsonrpc":"2.0", "id":1, "result":{}}), flush=True)
signal.pause()
'''


def test_closed_input_agent_is_reaped_and_terminal_state_is_published(tmp_path):
    task = Task("broken-input", tmp_path)
    task.work.mkdir()
    handle = Handle(task, 1, {"access": "workspace-write"})
    runner = AcpRunner("fake", {"argv": [sys.executable]})
    session = _Session(runner, handle)
    session.session_id = "session-one"
    with subprocess.Popen([sys.executable, "-u", "-c", EARLY_CLOSE], stdin=subprocess.PIPE,
                          stdout=subprocess.PIPE, start_new_session=True) as proc:
        session.proc = proc
        try:
            # The stdout frame is emitted only after fd 0 is closed; no timing race.
            assert json.loads(proc.stdout.readline())["id"] == 1
            session.run("hello", {})
            assert proc.poll() is not None
            with pytest.raises(ProcessLookupError):
                os.killpg(proc.pid, 0)
            assert json.loads(task.path("acp.1.json").read_text(encoding="utf-8-sig"))["state"] in ("failed", "done")
        finally:
            with contextlib.suppress(ProcessLookupError):
                os.killpg(proc.pid, signal.SIGKILL)
            proc.wait(timeout=5)
            with contextlib.suppress(OSError, ValueError):
                proc.stdin.close()
            session.selector.close()


def test_terminal_state_is_published_even_when_cleanup_raises(tmp_path, monkeypatch):
    task = Task("cleanup-error", tmp_path)
    task.work.mkdir()
    session = _Session(AcpRunner("fake", {"argv": [sys.executable]}), Handle(task, 1, {"access": "full"}))
    with subprocess.Popen([sys.executable, "-c", "pass"], stdout=subprocess.PIPE) as proc:
        session.proc = proc
        monkeypatch.setattr(session, "open", lambda previous: "")
        monkeypatch.setattr(session, "turns", lambda prompt: "done")

        def broken_cleanup():
            raise OSError("teardown failed")

        monkeypatch.setattr(session, "cleanup", broken_cleanup)
        try:
            with pytest.raises(OSError, match="teardown failed"):
                session.run("hello", {})
            assert json.loads(task.path("acp.1.json").read_text(encoding="utf-8-sig"))["state"] == "done"
        finally:
            session.selector.close()
