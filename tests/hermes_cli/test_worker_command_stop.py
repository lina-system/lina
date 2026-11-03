"""``CommandRunner.stop`` ends the whole CLI tree, not just its wrapper shell (review of lina#35): the CLI runs under
``/bin/sh`` in its own process group, and a CLI that ignores SIGTERM outlives the shell. Stopping must escalate until
the group is empty, or the run is finalized while the agent keeps working."""

from __future__ import annotations

import os
import signal
import subprocess
import sys
import time
from pathlib import Path
from types import SimpleNamespace

import pytest

from hermes_cli.worker.runners import command as command_runner
from hermes_cli.worker.runners.command import _WRAP, CommandRunner

pytestmark = pytest.mark.skipif(sys.platform == "win32", reason="POSIX process groups")

_STUBBORN = "import signal, time; signal.signal(signal.SIGTERM, signal.SIG_IGN); open({ready!r}, 'w').close(); time.sleep(120)"


def _group_gone(pgid: int) -> bool:
    try:
        os.killpg(pgid, 0)
    except ProcessLookupError:
        return True
    return False


def test_stop_escalates_until_a_cli_that_ignores_sigterm_is_gone(tmp_path: Path, monkeypatch):
    ready = tmp_path / "ready"
    rc_file = tmp_path / "exit.1.1"
    proc = subprocess.Popen(["/bin/sh", "-c", _WRAP, "sh", str(rc_file), sys.executable, "-c",
                             _STUBBORN.format(ready=str(ready))], start_new_session=True)
    deadline = time.monotonic() + 30
    while not ready.exists():
        assert time.monotonic() < deadline, "the CLI did not start"
        time.sleep(0.05)
    try:
        os.kill(proc.pid, signal.SIGTERM)  # the shell goes on SIGTERM; the CLI under it ignores it
        proc.wait(timeout=10)
        assert not _group_gone(proc.pid), "the CLI should still be running under the dead shell"
        runner = CommandRunner("fakecli", {"type": "command", "argv": ["true"]})
        runner.stop(SimpleNamespace(data={"pid": proc.pid}))
        assert _group_gone(proc.pid)
    finally:
        try:
            os.killpg(proc.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass


def test_a_group_with_no_process_left_is_not_alive():
    proc = subprocess.Popen(["/bin/sh", "-c", "exit 0"], start_new_session=True)
    proc.wait(timeout=10)
    assert not command_runner._group_alive(proc.pid)
