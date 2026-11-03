"""The ``submit --wait`` process is identified by ``(pid, start time)``, never a bare PID (review of lina#35): when the
waiter dies and its PID is reused, an unrelated process must not count as the waiter, or the end notice is recorded as
handled and never delivered. ``_wait`` clears the waiter on every way out, an error included."""

from __future__ import annotations

import json
import os
import subprocess
import sys

import pytest

from hermes_cli.worker import device, hub as hub_mod, serve
from hermes_cli.worker.hub import Hub
from tests.hermes_cli import _worker_fakes as fakes


@pytest.fixture
def lane(tmp_path, monkeypatch):
    base, fake = tmp_path / "lina", tmp_path / "fake"
    argv = fakes.install_fake_cli(fake)
    monkeypatch.setenv("FAKE_CLI_LOG", str(fake / "cli.jsonl"))
    fakes.write_config(base, f"fakecli: {json.dumps({'type': 'command', 'argv': argv})}\n")
    monkeypatch.setattr(device, "launch", lambda root, task, run: None)
    root = base / "worker"
    serve.serve_once(root)
    lines: list[str] = []
    return {"hub": Hub(root, out=lines.append), "lines": lines, "tmp": tmp_path}


def _job(lane) -> dict:
    lane["hub"].submit({"briefs": [fakes.brief(lane["tmp"], "SCENARIO:done one")], "mode": "ulw", "topic": "wait",
                        "bot": "sion", "runner": "fakecli", "wait": None})
    return lane["hub"].store.all()[-1]


def test_a_live_waiter_registered_with_its_start_time_is_the_waiter(lane):
    job = _job(lane)
    job.update(waiter=os.getpid(), waiter_start=hub_mod._start_time(os.getpid()))
    assert lane["hub"]._waiter_alive(job)


def test_a_reused_pid_is_not_the_waiter(lane):
    job = _job(lane)
    other = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(30)"])
    try:
        # the waiter registered with pid P and its start time; P now belongs to another process (different start)
        job.update(waiter=other.pid, waiter_start=(hub_mod._start_time(other.pid) or 0) - 1)
        assert not lane["hub"]._waiter_alive(job)
    finally:
        other.kill()
        other.wait()


def test_a_waiter_recorded_without_a_start_time_is_not_trusted(lane):
    job = _job(lane)
    job.update(waiter=os.getpid())
    job.pop("waiter_start", None)
    assert not lane["hub"]._waiter_alive(job)


def test_a_reused_pid_does_not_swallow_the_end_notice(lane, monkeypatch):
    job = _job(lane)
    lane["hub"].store.update(job["id"], waiter=os.getpid(), waiter_start=-1)  # this pid, but not the waiter's start
    delivered = []
    monkeypatch.setattr(Hub, "_notice_body", lambda self, j, kind, run: ("ended", lane["tmp"]))
    monkeypatch.setattr(Hub, "_deliver", lambda self, j, text, key: delivered.append(key) or "sent")
    assert lane["hub"].notify(job["id"], "done", "1") == "sent"
    assert delivered == ["done-1"]


def test_wait_clears_the_waiter_when_it_ends_with_an_error(lane, monkeypatch):
    job = _job(lane)
    lane["hub"].store.update(job["id"], waiter=os.getpid(), waiter_start=hub_mod._start_time(os.getpid()))
    monkeypatch.setattr(hub_mod.time, "sleep", lambda s: None)

    def broken(self, task_id):
        raise KeyboardInterrupt

    monkeypatch.setattr(Hub, "need", broken)
    with pytest.raises(KeyboardInterrupt):
        lane["hub"]._wait(job["id"], 30)
    saved = lane["hub"].store.load(job["id"])
    assert saved.get("waiter") is None and saved.get("waiter_start") is None
