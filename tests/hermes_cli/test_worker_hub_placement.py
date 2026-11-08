"""The hub hands a task to a device and cancels it only on the device's word (lina#35, review of #128 and #132): the
placement is recorded before the request is published, so a hub that stops in between resumes the same placement
instead of bidding again; a cancel that never reached the device is an error, not a cancelled task; a late end of an
earlier run never ends the run that is going now."""

import json
from dataclasses import replace

import pytest

from hermes_cli.worker import device, hub as hub_mod, serve
from hermes_cli.worker.hub import Hub, HubError
from hermes_cli.worker.task import write_manifest
from hermes_cli.worker.transport import Reply
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


def _submit(lane, host=None) -> str:
    lane["hub"].submit({"briefs": [fakes.brief(lane["tmp"], "SCENARIO:done one")], "mode": "ulw", "topic": "place",
                        "bot": "sion", "runner": "fakecli", "wait": None, "host": host})
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


def test_a_late_end_of_an_earlier_run_does_not_end_the_current_run(lane):
    task_id = _submit(lane)
    hub = lane["hub"]
    job = hub.store.load(task_id)
    job.update(runs=2, state="running")
    hub.store.save(job)
    assert hub.notify(task_id, "done", "1") == "earlier run"
    job = hub.store.load(task_id)
    assert (job["state"], job["runs"]) == ("running", 2)
    assert "done:1" in fakes.notified(job)
    assert not list(hub.store.notices.glob(f"{task_id}-*"))


@pytest.mark.parametrize("kind, run", [("cancelled", "0"), ("unknown", "x")])
def test_task_level_notices_still_end_the_task(lane, kind, run):
    """A cancel before any run started and an unreachable device are about the task, not a run: they always apply."""
    task_id = _submit(lane)
    hub = lane["hub"]
    assert hub.notify(task_id, kind, run) != "earlier run"
    assert hub.store.load(task_id)["state"] == kind


def test_unknown_named_device_is_refused_before_creating_a_task(lane):
    hub = lane["hub"]
    with pytest.raises(HubError) as error:
        _submit(lane, "missing")
    assert len(str(error.value).splitlines()) == 1
    assert "missing" in str(error.value) and "here" in str(error.value)
    assert hub.store.all() == []


def test_removed_named_device_fails_and_places_the_next_task_on_the_same_tick(lane):
    hub = lane["hub"]
    (here,) = hub.devices()
    devices = [replace(here, name="gone", cap=0), replace(here, cap=0)]
    lane["mp"].setattr(hub, "devices", lambda: devices)
    first, second = _submit(lane, "gone"), _submit(lane, "here")
    card_ends = []
    hub.store.update(first, card={"task": "card-1", "kind": "lina"})
    lane["mp"].setattr(hub_mod.kanban_link, "record_end", lambda *args: card_ends.append(args[-1]))
    devices[:] = [here]

    hub.tick()

    ended = hub.store.load(first)
    delivery = f"{first}:0:run.completed"
    assert ended["state"] == "failed"
    assert [(entry["delivery"], entry["end_state"]) for entry in ended["notified"]] == [(delivery, "failed")]
    assert hub.store.load(second)["state"] == "running"
    assert card_ends == [f"{ended['notice'].rstrip()}\n\ndelivery {delivery}\n"]
    reason = [line for line in ended["notice"].splitlines() if "gone" in line]
    assert len(reason) == 1
    (notice,) = hub.store.notices.glob(f"{first}-failed-0-*.md")
    assert notice.read_text() == card_ends[0]
    hub.tick()
    assert len(card_ends) == 1


def test_a_failing_lina_card_does_not_keep_the_end_from_the_next_card(lane):
    hub = lane["hub"]
    (here,) = hub.devices()
    devices = [replace(here, name="gone", cap=0), replace(here, cap=0)]
    lane["mp"].setattr(hub, "devices", lambda: devices)
    first = _submit(lane, "gone")
    cards = [{"task": "card-1", "kind": "lina"}, {"task": "card-2", "kind": "lina"}]
    hub.store.update(first, card=cards[0], card_links=cards)
    reached = []

    def record_end(card, *args):
        if card["task"] == "card-1":
            raise OSError("board is locked")
        reached.append(card["task"])

    lane["mp"].setattr(hub_mod.kanban_link, "record_end", record_end)
    devices[:] = [here]

    hub.tick()

    assert hub.store.load(first)["state"] == "failed"
    assert reached == ["card-2"]
    receipts = [(r["kind"], r["card"]["task"]) for r in hub.store.receipts_of(first)
                if r["kind"] in ("card_notice_failed", "card_noticed")]
    assert receipts == [("card_notice_failed", "card-1"), ("card_noticed", "card-2")]


def test_full_named_device_waits_without_blocking_another_device(lane):
    hub = lane["hub"]
    (here,) = hub.devices()
    devices = [replace(here, name="full", cap=0), replace(here, cap=0)]
    lane["mp"].setattr(hub, "devices", lambda: devices)
    first, second = _submit(lane, "full"), _submit(lane, "here")
    devices[:] = [devices[0], here]

    hub.tick()

    assert hub.store.load(first)["state"] == "queued"
    assert hub.store.load(first)["notified"] == []
    assert hub.store.load(second)["state"] == "running"
    assert hub.store.load(second)["host"] == "here"


def test_wait_reports_removed_named_device_failure(lane):
    hub = lane["hub"]
    (here,) = hub.devices()
    devices = [replace(here, name="gone", cap=0)]
    lane["mp"].setattr(hub, "devices", lambda: devices)
    now = hub_mod.time.time()
    started = now

    def remove_device_and_tick(seconds):
        nonlocal now
        now += seconds
        devices.clear()
        hub.tick()

    lane["mp"].setattr(hub_mod.time, "time", lambda: now)
    lane["mp"].setattr(hub_mod.time, "sleep", remove_device_and_tick)

    result = hub.submit({"briefs": [fakes.brief(lane["tmp"], "SCENARIO:done one")], "mode": "ulw",
                         "topic": "place", "bot": "sion", "runner": "fakecli", "host": "gone", "wait": 30})

    (job,) = hub.store.all()
    assert result == 0
    assert now - started < 30
    assert job["state"] == "failed" and job["host"] is None
    assert job["waiter"] is None and job["waiter_start"] is None
    assert lane["lines"][-1] == f"{job['id']} failed · {job['placement_error']}"
    assert "gone" in lane["lines"][-1] and len(lane["lines"][-1].splitlines()) == 1
    assert not list(hub.store.notices.glob(f"{job['id']}-*"))


def test_wait_fetches_result_after_tick_records_device_end(lane):
    hub = lane["hub"]

    def finish_device_and_tick(seconds):
        (job,) = hub.store.all()
        task = device.task_of(lane["root"], job["id"])
        task.work.mkdir(parents=True, exist_ok=True)
        (task.work / "report.md").write_text(fakes.REPORT_WITH_NOTES)
        write_manifest(task, 1)
        task.path("rc.1").write_text("0")
        task.path("state").write_text("done")
        hub.tick()
        assert hub.store.load(job["id"])["state"] == "done"
        assert not (hub.store.out_dir(job) / "report.md").exists()

    lane["mp"].setattr(hub_mod.time, "sleep", finish_device_and_tick)

    result = hub.submit({"briefs": [fakes.brief(lane["tmp"], "SCENARIO:done one")], "mode": "ulw",
                         "topic": "place", "bot": "sion", "runner": "fakecli", "host": "here", "wait": 30})

    (job,) = hub.store.all()
    out_dir = hub.store.out_dir(job)
    assert result == 0
    assert lane["lines"][-1] == f"{job['id']} done · result {out_dir}"
    assert (out_dir / "report.md").read_text() == fakes.REPORT_WITH_NOTES
    assert job["delivery"] == "ok"
    assert job["waiter"] is None and job["waiter_start"] is None
    assert not list(hub.store.notices.glob(f"{job['id']}-*"))
