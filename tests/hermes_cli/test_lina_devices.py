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
    devices = [line for line in text if not line.startswith(" ")]  # runner rows (omo's block too) are indented
    assert "ssh unreachable" in devices[1] and "omo missing" in devices[2]


def test_check_exits_1_for_a_device_that_is_not_ready(base, probes):
    assert lina_devices.check("build-a", base=base, out=lambda line: None) == 0
    assert lina_devices.check("build-c", base=base, out=lambda line: None) == 1
    with pytest.raises(lina_devices.DevicesError, match="no device 'nope'"):
        lina_devices.check("nope", base=base)


@pytest.fixture
def mixed_devices(tmp_path, monkeypatch):
    (tmp_path / "config.yaml").write_text(
        "devices:\n  workers:\n    build: {host: build, cap: 2}\n"
        "    laptop: {host: laptop-ssh, role: person}\n", encoding="utf-8")
    asked = []

    def probe(dev, root, runner):
        asked.append(dev.name)
        return {"ssh": False, "error": "offline"}

    monkeypatch.setattr(lina_devices, "probe", probe)
    return tmp_path, asked


@pytest.mark.parametrize("as_json", [False, True])
def test_list_never_probes_person_devices(mixed_devices, as_json):
    base, asked = mixed_devices
    lines = []

    assert lina_devices.list_devices(base, as_json=as_json, out=lines.append) == 0

    assert asked == ["build"]
    if as_json:
        assert [row["name"] for row in json.loads(lines[0])] == ["build"]
    else:
        assert lines[0].startswith("build ")
        assert any("/api/lina/devices" in line for line in lines)
        assert not any(line.startswith("laptop ") for line in lines)


@pytest.mark.parametrize("run_smoke", [False, True])
def test_check_person_device_refuses_before_probe_or_smoke(mixed_devices, monkeypatch, run_smoke):
    base, asked = mixed_devices
    smoked = []
    monkeypatch.setattr(lina_devices, "smoke", lambda *a, **k: smoked.append(a) or {"state": "done", "id": "fake"})

    with pytest.raises(lina_devices.DevicesError, match="person device.*not a node"):
        lina_devices.check("laptop", base=base, run_smoke=run_smoke, out=lambda line: None)

    assert asked == [] and smoked == []


def test_add_and_remove_edit_the_list_lina_worker_bids_over(base):
    lina_devices.add("build-e", "build-e", base=base, cap=3, no_repo=True, manual=True, out=lambda line: None)
    added = next(dev for dev in settings.devices(base) if dev.name == "build-e")
    assert (added.host, added.cap, added.no_repo, added.auto) == ("build-e", 3, True, False)

    lina_devices.remove("build-b", base=base, out=lambda line: None)
    assert [dev.name for dev in settings.devices(base)] == ["build-a", "build-c", "build-d", "build-e"]
    with pytest.raises(lina_devices.DevicesError):
        lina_devices.add("bad.name", "x", base=base)


def test_person_saved_with_a_host_appears_in_settings_cli_and_api(tmp_path, monkeypatch):
    from fastapi.testclient import TestClient
    from hermes_cli import lina_board_devices, web_server
    from hermes_cli.lina_work_settings import set_path
    from hermes_cli.lina_work_settings_store import Store

    monkeypatch.setenv("LINA_HOME", str(tmp_path))
    Store(tmp_path).apply(
        lambda doc: set_path(doc, "devices.workers.laptop", {"host": "laptop-ssh", "role": "person"}),
        {"kind": "cli", "principal": "tester", "device": "local"})

    assert [(dev.name, dev.host, dev.role) for dev in settings.devices()] == [
        ("laptop", "laptop-ssh", "person")]
    assert lina_devices.device("laptop", tmp_path).host == "laptop-ssh"
    monkeypatch.setattr(lina_devices, "probe", lambda *args: {"ssh": False})
    lines: list[str] = []
    assert lina_devices.list_devices(as_json=True, out=lines.append) == 0
    assert json.loads(lines[0]) == [], "person devices are served by the API, not the CLI node list"

    # Keep the real view and config reader; only the remote status read is replaced.
    monkeypatch.setattr(lina_board_devices, "PERSON", lina_board_devices._Lazy(900))
    monkeypatch.setattr(lina_board_devices, "_spawn", lambda work: work())
    monkeypatch.setattr(lina_board_devices, "person_read", lambda *args: {
        "reachable": True, "checkedAt": 1, "versions": {}})
    with TestClient(web_server.app) as client:
        client.headers[web_server._SESSION_HEADER_NAME] = web_server._SESSION_TOKEN
        reply = client.get("/api/lina/devices")
    assert reply.status_code == 200
    assert [(row["name"], row["role"]) for row in reply.json()["person"]] == [("laptop", "person")]
    assert reply.json()["machines"] == []


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
          min_free_mem_gb: 0
          runners:
            fakecli: {json.dumps({"type": "command", "argv": argv, "access": "workspace-write"})}
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
