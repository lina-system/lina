"""A task whose device left ``devices.workers`` or does not answer (lina#413): ``cancel`` ends it on the hub and says
the device could not be told, the tick ends a placement on a gone device once instead of retrying it every minute,
and a device added back never gets the old request. A failed hello is reported as the hello failure, and only a node
that does not list the run's access mode is told to update."""

import json
from dataclasses import replace

import pytest

from hermes_cli.worker import bidding, device, hub as hub_mod, protocol, serve
from hermes_cli.worker.hub import Hub, HubError
from hermes_cli.worker.transport import NegotiatedTransport, Reply
from tests.hermes_cli import _worker_fakes as fakes


class HubStopped(BaseException):
    """The hub process ends here (no handler of the hub catches it)."""


@pytest.fixture
def lane(tmp_path, monkeypatch):
    base, fake = tmp_path / "lina", tmp_path / "fake"
    argv = fakes.install_fake_cli(fake)
    monkeypatch.setenv("FAKE_CLI_LOG", str(fake / "cli.jsonl"))
    fakes.write_config(base, f"fakecli: {json.dumps({'type': 'command', 'argv': argv, 'access': 'workspace-write'})}\n")
    launched: list[tuple[str, int]] = []
    monkeypatch.setattr(device, "launch", lambda root, task, run: launched.append((task.id, run)))
    root = base / "worker"
    serve.serve_once(root)
    lines: list[str] = []
    return {"root": root, "hub": Hub(root, out=lines.append), "lines": lines, "launched": launched, "tmp": tmp_path,
            "mp": monkeypatch}


def _submit(lane) -> str:
    lane["hub"].submit({"briefs": [fakes.brief(lane["tmp"], "SCENARIO:done one")], "mode": "ulw", "topic": "place",
                        "bot": "sion", "runner": "fakecli", "wait": None, "host": None})
    return lane["hub"].store.all()[-1]["id"]


def _placing_then_gone(lane) -> tuple[str, list, object]:
    """A task left ``placing`` on ``here`` (the hub stopped before ``request.1``), then ``here`` leaves the list."""
    real_put = hub_mod.put

    def put(transport, task_id, name, data):
        if name == "prompt.1":
            raise HubStopped(name)
        real_put(transport, task_id, name, data)

    lane["mp"].setattr(hub_mod, "put", put)
    with pytest.raises(HubStopped):
        _submit(lane)
    lane["mp"].undo()
    lane["mp"].setattr(device, "launch", lambda root, task, run: lane["launched"].append((task.id, run)))
    hub = lane["hub"]
    (job,) = hub.store.all()
    assert (job["host"], job["state"]) == ("here", "placing")
    (here,) = hub.devices()
    devices: list = []
    lane["mp"].setattr(hub, "devices", lambda: devices)
    return job["id"], devices, here


def _back_and_served(lane, devices: list, here, task_id: str) -> None:
    """The device is added back, the tick runs, and the device serves what it holds: no request may reach it."""
    devices[:] = [replace(here)]
    lane["hub"].tick()
    serve.serve_once(lane["root"])
    task_dir = lane["root"] / "tasks" / task_id
    assert not list(task_dir.glob("request.*"))
    assert lane["launched"] == []


def test_cancel_of_a_placing_task_on_a_gone_device_ends_it_on_the_hub(lane):
    task_id, devices, here = _placing_then_gone(lane)
    hub = lane["hub"]
    contacted: list[str] = []
    lane["mp"].setattr(hub_mod, "for_device", lambda dev, root: contacted.append(dev.name))

    assert hub.cancel(task_id, "handed out elsewhere") == 0

    job = hub.store.load(task_id)
    assert job["state"] == "cancelled" and contacted == []
    (said,) = [line for line in lane["lines"] if task_id in line]
    assert "here could not be told" in said and len(said.splitlines()) == 1
    (receipt,) = [r for r in hub.store.receipts_of(task_id) if r["kind"] == "cancel_not_delivered"]
    assert receipt["host"] == "here" and "devices.workers" in receipt["error"]
    kinds = [r["kind"] for r in hub.store.receipts_of(task_id)]
    hub.tick()
    assert hub.store.load(task_id)["state"] == "cancelled"
    assert [r["kind"] for r in hub.store.receipts_of(task_id)] == kinds
    lane["mp"].undo()
    lane["mp"].setattr(hub, "devices", lambda: devices)
    lane["mp"].setattr(device, "launch", lambda root, task, run: lane["launched"].append((task.id, run)))
    _back_and_served(lane, devices, here, task_id)
    assert hub.store.load(task_id)["state"] == "cancelled"


def test_cancel_on_a_device_that_does_not_answer_ends_on_the_hub_and_says_so(lane):
    task_id = _submit(lane)
    hub = lane["hub"]
    assert hub.store.load(task_id)["state"] == "running"

    class Unreachable:
        def call(self, verb, stdin=b"", timeout=120):
            return Reply(255, b"", "ssh: connect to host port 22:\nConnection refused")

    lane["mp"].setattr(Hub, "transport", lambda self, job: Unreachable())
    assert hub.cancel(task_id, "not needed") == 0

    job = hub.store.load(task_id)
    assert job["state"] == "cancelled"
    (said,) = [line for line in lane["lines"] if "could not be told" in line]
    assert "Connection refused" in said and "may remain" in said and len(said.splitlines()) == 1
    (receipt,) = [r for r in hub.store.receipts_of(task_id) if r["kind"] == "cancel_not_delivered"]
    assert receipt["run"] == "1" and "Connection refused" in receipt["error"]
    assert [e["end_state"] for e in job["notified"]] == ["cancelled"]


def test_the_tick_ends_a_placement_on_a_gone_device_once_and_a_device_back_gets_no_request(lane):
    task_id, devices, here = _placing_then_gone(lane)
    hub = lane["hub"]

    hub.tick()

    job = hub.store.load(task_id)
    assert job["state"] == "failed" and "left devices.workers" in job["placement_error"]
    assert [line for line in job["notice"].splitlines() if "left devices.workers" in line]
    kinds = [r["kind"] for r in hub.store.receipts_of(task_id)]
    assert kinds.count("placement_dropped") == 1
    hub.tick()
    assert [r["kind"] for r in hub.store.receipts_of(task_id)] == kinds
    _back_and_served(lane, devices, here, task_id)
    assert hub.store.load(task_id)["state"] == "failed"


def _hello(reply: Reply):
    """A contact whose ``hello`` answers *reply*; every other verb would be a run reaching the device."""
    class Raw:
        def call(self, verb, stdin=b"", timeout=120):
            assert verb == ["hello"], f"{verb[0]} sent after the hello"
            return reply

    return lambda dev, root: NegotiatedTransport(Raw(), dev.name)


def test_a_failed_hello_is_reported_as_such_not_as_an_old_node(lane):
    hub = lane["hub"]
    (here,) = hub.devices()
    lane["mp"].setattr(hub_mod, "for_device", _hello(Reply(255, b"", "ssh: connect to host: Connection timed out")))
    with pytest.raises(HubError) as error:
        hub._publish({"id": "T-1", "access": "workspace-write"}, here)
    assert "worker hello failed" in str(error.value) and "Connection timed out" in str(error.value)
    assert "update the node" not in str(error.value)


def test_a_node_without_the_mode_is_still_told_to_update(lane):
    hub = lane["hub"]
    (here,) = hub.devices()
    hello = {"protocol": {"min": protocol.PROTOCOL_MIN, "max": protocol.PROTOCOL_MAX},
             "capabilities": {"access_modes": ["read-only"]}}
    lane["mp"].setattr(hub_mod, "for_device", _hello(Reply(0, json.dumps(hello).encode())))
    with pytest.raises(HubError, match=r"does not enforce access workspace-write \(update the node\)"):
        hub._publish({"id": "T-1", "access": "workspace-write"}, here)


def test_missing_access_tells_no_descriptor_from_a_missing_mode():
    assert "update the node" in bidding.missing_access({"capabilities": {"access_modes": ["read-only"]}},
                                                       "workspace-write")
    for empty in (None, {}):
        why = bidding.missing_access(empty, "workspace-write")
        assert why and "update the node" not in why
    assert bidding.missing_access({"capabilities": {"access_modes": ["workspace-write"]}}, "workspace-write") is None
