"""Numeric fallback tiers and independent named-host slots (regression for #228)."""

import json

import pytest

from hermes_cli.worker import bidding, device, serve, settings, slots
from hermes_cli.worker.hub import Hub
from hermes_cli.worker.transport import Reply


def probe(**extra):
    return {"ready": True, "serve": True, "running": 0, "cores": 4, "load": 0,
            "free_gb": 50, "repo": True, **extra}


def test_numeric_tier_survives_config_and_beats_a_lower_fill_ratio():
    # Given tier 1 is busier but still available, and tier 2 is fast and empty.
    devices = settings.devices(raw={"first": {"host": "first", "cap": 2, "last": True},
                                    "last": {"host": "last", "last": 2, "fast": True}})
    # When an automatic bid runs.
    winner, _ = bidding.choose(devices, [probe(running=1), probe()], repo=None)
    # Then the first available numeric tier wins.
    assert [dev.last for dev in devices] == [1, 2]
    assert winner.name == "first"


@pytest.mark.parametrize("running, expected", [(0, "normal"), (1, "first"), (2, "last")])
def test_automatic_bid_uses_the_first_available_tier(running, expected):
    devices = [settings.Device("normal", "normal"), settings.Device("first", "first", last=1),
               settings.Device("last", "last", last=2, fast=True)]
    winner, _ = bidding.choose(devices, [probe(running=int(running > 0)),
                                        probe(running=int(running > 1)), probe()], repo=None)
    assert winner.name == expected


@pytest.mark.parametrize("failure", [{"ready": False}, {"serve": False}, {"free_gb": 0},
                                     {"repo": False}, {"error": "offline"}, {"running": 1}])
def test_manual_auto_false_still_obeys_every_readiness_gate(failure):
    dev = settings.Device("named", "named", auto=False)
    winner, _ = bidding.choose([dev], [probe(**failure)], repo="owner/repo", manual=True)
    assert winner is None


def test_manual_selection_ignores_fallback_priority():
    devices = [settings.Device("normal", "normal"), settings.Device("named", "named", last=2, fast=True)]
    winner, _ = bidding.choose(devices, [probe(), probe()], repo=None, manual=True)
    assert winner.name == "named"


def test_manual_host_still_refuses_repo_work_when_configured_no_repo():
    dev = settings.Device("named", "named", no_repo=True, auto=False)
    assert bidding.choose([dev], [probe()], repo="owner/repo", manual=True)[0] is None


def test_named_capacity_and_probe_counts_are_independent():
    dev = settings.devices(raw={"host": {"host": "local", "cap": 1, "named_cap": 3}})[0]
    winner, _ = bidding.choose([dev], [probe(running=4, bid_running=1, named_running=2)],
                               repo=None, manual=True)
    assert winner == dev
    assert bidding.choose([dev], [probe(running=3, bid_running=0, named_running=3)],
                          repo=None)[0] == dev


def test_reservations_count_and_persist_each_pool_separately(tmp_path):
    assert slots.reserve(tmp_path, "OT-A", 1)["action"] == "reserved"
    assert slots.reserve(tmp_path, "OT-B", 2, pool="named")["action"] == "reserved"
    assert slots.reserve(tmp_path, "OT-C", 2, pool="named")["action"] == "reserved"
    assert slots.reserve(tmp_path, "OT-D", 1)["action"] == "full"
    assert slots.reserve(tmp_path, "OT-E", 2, pool="named")["action"] == "full"
    assert slots.held(tmp_path, pool="bid") == 1
    assert slots.held(tmp_path, pool="named") == 2
    assert slots.cap(tmp_path) == 1
    assert slots.cap(tmp_path, pool="named") == 2


def test_serve_limits_launches_per_pool_and_keeps_followups_in_their_pool(tmp_path, monkeypatch):
    launched = []
    monkeypatch.setattr(device, "launch", lambda root, task, run: launched.append((task.id, run)))
    for tid, pool in [("OT-A", "bid"), ("OT-B", "named")]:
        slots.reserve(tmp_path, tid, 1, pool=pool)
        task = tmp_path / "tasks" / tid
        for name in ("request.1", "prompt.1", "prepared"):
            (task / name).write_text("{}")
    serve.serve_once(tmp_path)
    assert launched == [("OT-A", 1), ("OT-B", 1)]
    task = tmp_path / "tasks" / "OT-B"
    (task / "rc.1").write_text("0")
    (task / "request.2").write_text("{}")
    (task / "prompt.2").write_text("more")
    serve.serve_once(tmp_path)
    assert launched[-1] == ("OT-B", 2)


def test_serve_does_not_overlaunch_either_pool(tmp_path, monkeypatch):
    launched = []
    monkeypatch.setattr(device, "launch", lambda root, task, run: launched.append(task.id))
    for tid, pool in [("OT-A", "bid"), ("OT-B", "named"), ("OT-C", "bid"), ("OT-D", "named")]:
        slots.reserve(tmp_path, tid, 1, pool=pool)
        task = tmp_path / "tasks" / tid
        task.mkdir(exist_ok=True)
        (task / "slot_pool").write_text(pool)
        for name in ("request.1", "prompt.1", "prepared"):
            (task / name).write_text("{}")
    serve.serve_once(tmp_path)
    assert launched == ["OT-A", "OT-B"]


def test_ended_named_request_releases_only_its_pool(tmp_path):
    slots.reserve(tmp_path, "OT-A", 1)
    slots.reserve(tmp_path, "OT-B", 1, pool="named")
    task = tmp_path / "tasks" / "OT-B"
    (task / "request.1").write_text("{}")
    (task / "rc.1").write_text("0")
    assert slots.reserve(tmp_path, "OT-C", 1, pool="named")["action"] == "reserved"
    assert slots.reserve(tmp_path, "OT-D", 1)["action"] == "full"


def test_fixed_host_is_probed_before_reservation(tmp_path, monkeypatch):
    hub = Hub(tmp_path / "worker", out=lambda line: None)
    dev = settings.Device("named", "local", auto=False)
    monkeypatch.setattr(Hub, "devices", lambda self: [dev])
    monkeypatch.setattr(bidding, "probe_all", lambda *a, **k: [probe(ready=False)])
    monkeypatch.setattr(Hub, "_reserve", lambda *a: pytest.fail("reserved an unready host"))
    job = {"id": "OT-A", "fixed_host": "named", "runner": "command", "state": "queued"}
    assert hub.place(job) is None


@pytest.mark.parametrize("fixed, expected", [(None, ["reserve", "OT-A", "1"]),
                                           ("named", ["reserve", "OT-A", "3", "named"])])
def test_hub_sends_the_selected_pool_capacity(tmp_path, monkeypatch, fixed, expected):
    hub = Hub(tmp_path / "worker", out=lambda line: None)
    dev = settings.devices(raw={"named": {"host": "local", "cap": 1, "named_cap": 3}})[0]
    calls = []

    class Transport:
        def call(self, verb, **kwargs):
            calls.append(verb)
            return Reply(0, json.dumps({"action": "reserved"}).encode(), "")

    monkeypatch.setattr("hermes_cli.worker.hub.for_device", lambda *a: Transport())
    assert hub._reserve({"id": "OT-A", "fixed_host": fixed}, dev)
    assert calls == [expected]
