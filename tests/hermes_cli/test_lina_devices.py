"""``lina devices`` (lina-system/lina#37): the device list ``lina worker`` bids over, with readiness.

Readiness comes from fake probes (no ssh, no real device). The smoke test runs one real task through ``lina worker``
on a local-transport device with a ``command`` runner (the fake CLI of the worker tests): the hub, the device code and
the run are the real ones, the run starts on a thread instead of its own process.
"""

from __future__ import annotations

import json
import threading
import textwrap

import pytest

from hermes_cli import lina_devices
from hermes_cli.worker import serve, settings
from tests.hermes_cli import _worker_fakes as fakes
from tests.waiting import wait_until

PIN = {"pinned": "1.2.3", "current": "1.2.3"}


@pytest.fixture
def base(tmp_path):
    root = tmp_path / "lina"
    root.mkdir()
    (root / "config.yaml").write_text(textwrap.dedent("""\
        devices:
          workers:
            build-a: {host: build-a, cap: 2}
            build-b: {host: build-b}
            build-c: {host: build-c}
            build-d: {host: build-d}
        worker:
          min_free_gb: 5
        """), encoding="utf-8")
    return root


@pytest.fixture
def probes(monkeypatch):
    found = {
        "build-a": {"ssh": True, "ready": True, "serve": True, "free_gb": 80, "running": 1},
        "build-b": {"ssh": False, "error": "ssh: connect to host build-b: No route to host"},
        "build-c": {"ssh": True, "ready": True, "serve": True, "free_gb": 80, "running": 0},
        "build-d": {"ssh": True, "ready": True, "serve": True, "free_gb": 2, "running": 0},
    }
    omo = {"build-a": PIN, "build-c": {"pinned": "1.2.3", "current": "1.2.0"}, "build-d": PIN}
    asked: list[str] = []
    monkeypatch.setattr(lina_devices, "probe", lambda dev, root, runner: found[dev.name])
    monkeypatch.setattr(lina_devices, "omo_status", lambda dev: asked.append(dev.name) or omo[dev.name])
    monkeypatch.setattr(lina_devices, "runner_status", lambda dev, root: [])
    return asked


def test_list_shows_each_device_with_ssh_omo_disk_and_cap(base, probes):
    lines: list[str] = []
    assert lina_devices.list_devices(base, as_json=True, out=lines.append) == 0
    rows = {row["name"]: row for row in json.loads(lines[0])}

    assert rows["build-a"]["ready"] is True and rows["build-a"]["cap"] == 2 and rows["build-a"]["omo"] is True
    assert rows["build-b"]["ssh"] is False and "unreachable" in rows["build-b"]["why"]
    assert rows["build-c"]["omo"] is False and "pinned omo 1.2.3 not current" in rows["build-c"]["why"]
    assert rows["build-d"]["ready"] is False and "disk 2GB < 5GB" in rows["build-d"]["why"]
    assert "build-b" not in probes, "lina omo status is not asked over an ssh that does not connect"

    text: list[str] = []
    lina_devices.list_devices(base, out=text.append)
    assert text[0].split()[:2] == ["build-a", "build-a"] and text[0].endswith("ready")
    assert "ssh unreachable" in text[1] and "omo missing" in text[2]


def test_check_exits_1_for_a_device_that_is_not_ready(base, probes):
    assert lina_devices.check("build-a", base=base, out=lambda line: None) == 0
    assert lina_devices.check("build-c", base=base, out=lambda line: None) == 1
    with pytest.raises(lina_devices.DevicesError, match="no device 'nope'"):
        lina_devices.check("nope", base=base)


def test_add_and_remove_edit_the_list_lina_worker_bids_over(base):
    lina_devices.add("build-e", "build-e", base=base, cap=3, no_repo=True, manual=True, out=lambda line: None)
    added = next(dev for dev in settings.devices(base) if dev.name == "build-e")
    assert (added.host, added.cap, added.no_repo, added.auto) == ("build-e", 3, True, False)

    lina_devices.remove("build-b", base=base, out=lambda line: None)
    assert [dev.name for dev in settings.devices(base)] == ["build-a", "build-c", "build-d", "build-e"]
    with pytest.raises(lina_devices.DevicesError):
        lina_devices.add("bad.name", "x", base=base)


@pytest.fixture
def local_device(tmp_path, monkeypatch):
    """A LINA root whose only device is this machine, running a ``command`` runner, and its serve loop."""
    root = tmp_path / "lina"
    argv = fakes.install_fake_cli(tmp_path / "fake")
    monkeypatch.setenv("FAKE_CLI_LOG", str(tmp_path / "fake" / "cli.jsonl"))
    root.mkdir()
    (root / "config.yaml").write_text(textwrap.dedent(f"""\
        devices:
          workers:
            here: {{host: local, cap: 1}}
        worker:
          default_runner: fakecli
          poll_s: 0.05
          min_free_gb: 0
          runners:
            fakecli: {json.dumps({"type": "command", "argv": argv})}
        """), encoding="utf-8")
    launcher = fakes.ThreadLauncher().install(monkeypatch)
    stop = threading.Event()

    def serve_loop():
        while not stop.is_set():
            serve.serve_once(root / "worker")
            stop.wait(0.05)

    loop = threading.Thread(target=serve_loop, daemon=True)
    loop.start()
    # Readiness needs the serve heartbeat; the loop's first tick writes it on its own thread.
    wait_until(lambda: serve.heartbeat_age(root / "worker") is not None, what="the serve loop's first heartbeat")
    yield root
    stop.set()
    loop.join(10)
    launcher.join()


def test_run_smoke_runs_one_real_task_on_the_device_and_ends_done(local_device):
    lines: list[str] = []
    assert lina_devices.check("here", base=local_device, run_smoke=True, timeout=60, as_json=True,
                              out=lines.append) == 0
    row = json.loads(lines[0])
    assert row["ready"] is True and row["omo"] is None
    assert row["smoke"]["state"] == "done" and row["smoke"]["id"].startswith("OT-")

    job = json.loads((local_device / "worker" / "hub" / "jobs" / f"{row['smoke']['id']}.json").read_text())
    assert job["host"] == "here" and job["runner"] == "fakecli" and job["dry_notify"] is True
