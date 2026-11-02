"""The hub hands a task to a device and cancels it only on the device's word (lina#35, review of #128 and #132): the
placement is recorded before the request is published, so a hub that stops in between resumes the same placement
instead of bidding again; a cancel that never reached the device is an error, not a cancelled task."""

import json

import pytest

from hermes_cli.worker import device, hub as hub_mod, serve
from hermes_cli.worker.hub import Hub, HubError
from hermes_cli.worker.transport import Reply
from tests.hermes_cli import _worker_fakes as fakes


class HubStopped(BaseException):
    """The hub process ends here (no handler of the hub catches it)."""


@pytest.fixture
def lane(tmp_path, monkeypatch):
    base, fake = tmp_path / "lina", tmp_path / "fake"
    argv = fakes.install_fake_cli(fake)
    monkeypatch.setenv("FAKE_CLI_LOG", str(fake / "cli.jsonl"))
    fakes.write_config(base, f"fakecli: {json.dumps({'type': 'command', 'argv': argv})}\n")
    launched: list[tuple[str, int]] = []
    monkeypatch.setattr(device, "launch", lambda root, task, run: launched.append((task.id, run)))
    root = base / "worker"
    serve.serve_once(root)
    lines: list[str] = []
    return {"root": root, "hub": Hub(root, out=lines.append), "lines": lines, "launched": launched, "tmp": tmp_path,
            "mp": monkeypatch}


def _submit(lane) -> str:
    lane["hub"].submit({"briefs": [fakes.brief(lane["tmp"], "SCENARIO:done one")], "mode": "ulw", "topic": "place",
                        "bot": "sion", "runner": "fakecli", "wait": None})
    return lane["hub"].store.all()[-1]["id"]


def _stop_hub_at(lane, rel: str) -> None:
    real_put = hub_mod.put

    def put(transport, task_id, name, data):
        if name == rel:
            if rel == "request.1":
                real_put(transport, task_id, name, data)
            raise HubStopped(name)
        real_put(transport, task_id, name, data)

    lane["mp"].setattr(hub_mod, "put", put)


def _no_second_bid(lane) -> None:
    def bid(*args, **kwargs):
        raise AssertionError("the task was bid out again")

    lane["mp"].setattr(hub_mod.bidding, "bid", bid)


@pytest.mark.parametrize("stops_after", ["request.1", "prompt.1"])
def test_a_hub_that_stops_mid_placement_resumes_it_on_the_same_device(lane, stops_after):
    _stop_hub_at(lane, stops_after)
    with pytest.raises(HubStopped):
        _submit(lane)
    hub = lane["hub"]
    (job,) = hub.store.all()
    assert (job["host"], job["runs"], job["state"]) == ("here", 1, "placing")

    lane["mp"].undo()
    _no_second_bid(lane)
    lane["mp"].setattr(device, "launch", lambda root, task, run: lane["launched"].append((task.id, run)))
    assert hub.resume_placement(job["id"]) == "here"
    assert hub.store.load(job["id"])["state"] == "running"
    serve.serve_once(lane["root"])
    task_dir = lane["root"] / "tasks" / job["id"]
    assert sorted(p.name for p in task_dir.glob("request.*")) == ["request.1"]
    assert lane["launched"] == [(job["id"], 1)]


def test_a_cancel_that_never_reached_the_device_is_not_a_cancel(lane):
    task_id = _submit(lane)
    hub = lane["hub"]

    class Unreachable:
        def call(self, verb, stdin=b"", timeout=120):
            return Reply(255, b"", "ssh: connect to host port 22: Connection refused")

    lane["mp"].setattr(Hub, "transport", lambda self, job: Unreachable())
    with pytest.raises(HubError, match="did not reach"):
        hub.cancel(task_id, "not needed")
    job = hub.store.load(task_id)
    assert job["state"] == "running" and job["notified"] == []
    assert not list(hub.store.notices.glob(f"{task_id}-*"))
