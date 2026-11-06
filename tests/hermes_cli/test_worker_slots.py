"""A work device's slots and run processes (lina#35, review of #128): a reserved or waiting task holds a slot, so a
batch never lands on a full device and ``serve`` launches no more than the cap; a task cancelled before ``serve``
picks it up is never prepared or started; an ended run process is reaped and a run is alive only while that same
process (PID and birth) runs."""

import json
import os
import sys

import psutil
import pytest

from hermes_cli.worker import device, serve
from hermes_cli.worker.hub import Hub, HubError
from hermes_cli.worker.runners.base import Task
from hermes_platform.resolver.core import Resolution
from tests.hermes_cli import _worker_fakes as fakes
from tests.waiting import wait_until


@pytest.fixture
def lane(tmp_path, monkeypatch):
    """A hub with one local ``cap: 1`` device whose launches are recorded, not run."""
    base, fake = tmp_path / "lina", tmp_path / "fake"
    argv = fakes.install_fake_cli(fake)
    monkeypatch.setenv("FAKE_CLI_LOG", str(fake / "cli.jsonl"))
    fakes.write_config(base, f"fakecli: {json.dumps({'type': 'command', 'argv': argv, 'access': 'workspace-write'})}\n")
    config = base / "config.yaml"
    config.write_text(config.read_text().replace("here: {host: local, cap: 2}", "here: {host: local, cap: 1}"))
    launched: list[tuple[str, int]] = []
    monkeypatch.setattr(device, "launch", lambda root, task, run: launched.append((task.id, run)))
    root = base / "worker"
    serve.serve_once(root)
    lines: list[str] = []
    return {"root": root, "hub": Hub(root, out=lines.append), "lines": lines, "launched": launched, "tmp": tmp_path}


def _submit(lane, *texts) -> list[str]:
    briefs = [fakes.brief(lane["tmp"], text, name=f"brief{i}.md") for i, text in enumerate(texts)]
    before = len(lane["lines"])
    lane["hub"].submit({"briefs": briefs, "mode": "ulw", "topic": "slots", "bot": "sion", "runner": "fakecli",
                        "wait": None})
    return [line.split()[0] for line in lane["lines"][before:] if line.startswith("OT-")]


def test_a_batch_does_not_overfill_a_cap_1_device(lane):
    first, second = _submit(lane, "SCENARIO:done one", "SCENARIO:done two")
    hub = lane["hub"]
    assert hub.store.load(first)["host"] == "here"
    assert hub.store.load(second)["state"] == "queued"
    serve.serve_once(lane["root"])
    assert lane["launched"] == [(first, 1)]


def test_serve_launches_no_more_than_the_cap(lane):
    """Requests that reached the device anyway (a hub that skipped reservation) wait for a free slot."""
    _submit(lane, "SCENARIO:done one")
    other = lane["root"] / "tasks" / "OT-SIO1102120009"
    for name in ("prompt.1", "request.1", "job.json"):
        (other / name).parent.mkdir(parents=True, exist_ok=True)
        (other / name).write_text("{}")
    serve.serve_once(lane["root"])
    serve.serve_once(lane["root"])
    assert len(lane["launched"]) == 1
    finished = lane["root"] / "tasks" / lane["launched"][0][0] / "rc.1"
    finished.write_text("0\n")
    serve.serve_once(lane["root"])
    assert len(lane["launched"]) == 2


def test_a_task_cancelled_before_serve_is_never_started(lane):
    (task_id,) = _submit(lane, "SCENARIO:done one")
    hub = lane["hub"]
    assert hub.cancel(task_id, "changed my mind") == 0
    serve.serve_once(lane["root"])
    task_dir = lane["root"] / "tasks" / task_id
    assert lane["launched"] == []
    assert not (task_dir / "prepared").exists() and not (task_dir / "handle.json").exists()
    assert (task_dir / "state").read_text().strip() == "cancelled"
    assert hub.store.load(task_id)["notified"] == ["cancelled:1"]


def test_a_run_cancelled_after_launch_opens_no_session(lane):
    (task_id,) = _submit(lane, "SCENARIO:done one")
    serve.serve_once(lane["root"])
    task_dir = lane["root"] / "tasks" / task_id
    (task_dir / "cancelled").write_text("stop\n")
    assert device.run_task(lane["root"], task_id, 1) == "cancelled"
    assert not (task_dir / "handle.json").exists()
    assert not (lane["tmp"] / "fake" / "cli.jsonl").exists()


def _ended(lane, task_id):
    serve.serve_once(lane["root"])
    (lane["root"] / "tasks" / task_id / "rc.1").write_text("0\n")


def test_two_sends_before_the_next_pass_make_one_run(lane):
    """Codex's case on #128: two follow-ups between a run's end and the device's next pass."""
    (task_id,) = _submit(lane, "SCENARIO:done one")
    _ended(lane, task_id)
    hub, task_dir = lane["hub"], lane["root"] / "tasks" / task_id
    assert hub.send(task_id, "first follow-up") == 0
    assert hub.send(task_id, "second follow-up") == 0
    serve.serve_once(lane["root"])
    serve.serve_once(lane["root"])
    assert lane["launched"] == [(task_id, 1), (task_id, 2)]
    assert not (task_dir / "request.3").exists()
    prompt = (task_dir / "prompt.2").read_text()
    assert "first follow-up" in prompt and "second follow-up" in prompt
    with pytest.raises(HubError, match="starting or running"):
        hub.send(task_id, "third follow-up")
    assert not (task_dir / "request.3").exists()


def test_serve_never_starts_a_second_run_of_a_task(lane):
    """Requests already on disk (written before the device serialized follow-ups) still run one at a time."""
    (task_id,) = _submit(lane, "SCENARIO:done one")
    _ended(lane, task_id)
    task_dir = lane["root"] / "tasks" / task_id
    for run in (2, 3):
        (task_dir / f"prompt.{run}").write_text("ulw more")
        (task_dir / f"request.{run}").write_text("{}")
    serve.serve_once(lane["root"])
    serve.serve_once(lane["root"])
    assert lane["launched"] == [(task_id, 1), (task_id, 2)]
    (task_dir / "rc.2").write_text("0\n")
    serve.serve_once(lane["root"])
    assert lane["launched"][-1] == (task_id, 3)


@pytest.mark.platforms("posix")
def test_an_ended_run_is_reaped_and_no_longer_alive(tmp_path, monkeypatch):
    task = Task("OT-SIO1102120010", tmp_path / "tasks" / "OT-SIO1102120010")
    task.dir.mkdir(parents=True)
    monkeypatch.setattr(device, "run_argv", lambda task, run: [sys.executable, "-c", "pass"])
    monkeypatch.setattr(device, "locate_command", lambda name: Resolution("missing"))
    device.launch(tmp_path, task, 1)
    record = json.loads(task.path("pid.1").read_text())
    pid = record["pid"] if isinstance(record, dict) else record
    wait_until(lambda: not device.alive(task, 1), what="the ended run reads as not alive")
    device.reap()
    assert not psutil.pid_exists(pid)


def test_a_reused_pid_is_not_the_run(tmp_path):
    task = Task("OT-SIO1102120011", tmp_path)
    task.path("pid.1").write_text(json.dumps({"pid": os.getpid(), "birth": 1.0}))
    assert device.alive(task, 1) is False
