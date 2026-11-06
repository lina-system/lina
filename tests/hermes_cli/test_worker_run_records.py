"""An ended run must not leave a reusable PID record (#228)."""

from hermes_cli.worker import device
from hermes_cli.worker.runners.base import Task
from types import SimpleNamespace
import json


def test_finish_removes_pid_before_reporting_end(tmp_path, monkeypatch):
    task = Task("OT-RECORD", tmp_path / "task")
    task.dir.mkdir()
    task.work.mkdir()
    task.path("pid.1").write_text('{"pid": 123, "birth": 1}')
    observed = []
    monkeypatch.setattr(device, "report_end", lambda *args: observed.append(task.path("pid.1").exists()))
    assert device.finish_run(tmp_path / "worker", task, 1, "failed") == "failed"
    assert observed == [False]
    assert not task.path("pid.1").exists()


def test_fast_finished_run_never_publishes_a_late_pid(tmp_path, monkeypatch):
    task = Task("OT-FAST", tmp_path / "task")
    task.dir.mkdir()
    monkeypatch.setattr(device, "locate_command", lambda name: SimpleNamespace(found=False))

    def spawn(*args, **kwargs):
        task.path("rc.1").write_text("0")
        return SimpleNamespace(pid=123)

    monkeypatch.setattr(device.subprocess, "Popen", spawn)
    monkeypatch.setattr(device.psutil, "Process", lambda pid: None)
    monkeypatch.setattr(device, "process_birth", lambda proc: 1)
    monkeypatch.setattr(device, "_CHILDREN", [])
    device.launch(tmp_path / "worker", task, 1)
    assert not task.path("pid.1").exists()


def test_probe_reports_no_memory_cap_without_systemd(tmp_path, monkeypatch):
    monkeypatch.setattr(device, "locate_command", lambda name: SimpleNamespace(found=False))
    monkeypatch.setattr(device.runners, "get", lambda *args, **kwargs: SimpleNamespace(available=lambda: (True, "fake")))
    probe = device.probe(tmp_path / "worker")
    assert probe["memory_max"] is None


def test_device_wire_keeps_named_reservation_outside_bid_capacity(tmp_path, monkeypatch):
    worker = tmp_path / "worker"
    monkeypatch.setattr(device.runners, "get", lambda *args, **kwargs: SimpleNamespace(available=lambda: (True, "fake")))
    assert json.loads(device.dispatch(worker, ["reserve", "OT-BID", "1"])[1])["action"] == "reserved"
    named = json.loads(device.dispatch(worker, ["reserve", "OT-NAMED", "1", "named"])[1])
    assert named["action"] == "reserved"
    probe = device.probe(worker)
    assert (probe["running"], probe["bid_running"], probe["named_running"]) == (2, 1, 1)
