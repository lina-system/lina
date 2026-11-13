"""Reasons the bid passes a node apart from its slots (lina#454, lina#456): its magpie does not answer. The score order
is not touched by any of them."""

import json

import pytest

from hermes_cli import lina_devices
from hermes_cli.worker import bidding, protocol, transport
from hermes_cli.worker.hub import Hub
from hermes_cli.worker.settings import Device

MAGPIE_UP = {"health": 200, "models": 23}


def probe(running=0, load=0.0, cores=8, **extra):
    return {"ready": True, "serve": True, "running": running, "cores": cores, "load": load, "free_gb": 50,
            "repo": True, **extra}


def why(dev, found, **kw):
    return bidding.readiness(dev, found, repo=None, min_free_gb=5, **kw)


class FakeNode:
    """A node over a fake raw transport: its hello carries *capabilities*, its probe answers *found*. Every verb asked
    is kept in ``asked``."""

    def __init__(self, found: dict, capabilities: dict | None = None):
        self.found, self.capabilities, self.asked = found, capabilities or {}, []

    def call(self, verb, stdin=b"", timeout=0):
        self.asked.append(verb[0])
        if verb[0] == "hello":
            body = {"protocol": {"min": protocol.PROTOCOL_MIN, "max": protocol.PROTOCOL_MAX}, "instance_id": "i-1",
                    "serve": True, "capabilities": {"access_modes": ["read-only", "workspace-write", "full"],
                                                    **self.capabilities}}
            return transport.Reply(0, json.dumps(body).encode(), "")
        if verb[0] == "probe":
            return transport.Reply(0, json.dumps(self.found).encode(), "")
        return transport.Reply(1, b"{}", f"unexpected verb {verb[0]}")


@pytest.fixture
def fleet(tmp_path, monkeypatch):
    """``node-a`` (magpie down) and ``node-b`` (no magpie at all) behind fake transports, for the hub and ``lina devices``."""
    (tmp_path / "config.yaml").write_text(
        "devices:\n  workers:\n    node-a: {host: node-a, cap: 2}\n    node-b: {host: node-b, cap: 2}\n"
        "worker:\n  min_free_gb: 5\n", encoding="utf-8")
    nodes = {"node-a": FakeNode(probe(model_path="ok"), {"proxy": {"health": None, "models": 0}}),
             "node-b": FakeNode(probe(), {})}

    def for_device(dev, root):
        return transport.NegotiatedTransport(nodes[dev.name], dev.name)

    monkeypatch.setattr(transport, "for_device", for_device)
    monkeypatch.setattr(lina_devices, "for_device", for_device)
    monkeypatch.setattr(lina_devices, "omo_status", lambda dev: {"pinned": "1", "current": "1"})
    monkeypatch.setattr(lina_devices, "runner_status", lambda dev, root: [])
    return tmp_path, nodes


@pytest.mark.parametrize("health", [None, 500, 0])
@pytest.mark.parametrize("manual", [False, True])
def test_a_node_whose_reported_magpie_does_not_answer_is_passed(health, manual):
    dev = Device("node-a", "node-a", cap=2)
    assert why(dev, probe(model_path="ok", proxy={"health": health, "models": 0}), manual=manual) == \
        "magpie not answering"
    assert why(dev, probe(model_path="ok", proxy=MAGPIE_UP), manual=manual) is None



def test_a_node_without_an_magpie_is_not_passed_for_it():
    dev = Device("node-a", "node-a", cap=2)
    assert why(dev, probe(proxy=None)) is None
    assert why(dev, probe()) is None  # an older node, whose hello has no magpie


def test_the_bid_reads_magpie_from_the_hello_and_hosts_says_why(fleet):
    base, nodes = fleet
    devices = [Device("node-a", "node-a", cap=2), Device("node-b", "node-b", cap=2)]

    winner, rows = bidding.bid(devices, base / "worker", repo=None, runner="omo")

    assert winner.name == "node-b"
    assert rows[0]["why"] == "magpie not answering" and "why" not in rows[1]
    assert nodes["node-a"].asked == ["hello", "probe"], "one hello and one probe: nothing else is sent to the node"

    lines: list[str] = []
    assert Hub(base / "worker", out=lines.append).hosts() == 0
    assert lines[0].startswith("node-a ") and lines[0].endswith(" · magpie not answering")
    assert lines[1].startswith("node-b ") and lines[1].endswith(" · can take work")


def test_a_named_run_to_a_node_whose_magpie_does_not_answer_is_not_started(fleet):
    base, nodes = fleet
    hub = Hub(base / "worker", out=lambda line: None)
    job = {"id": "OT-T0000000001", "created": 1.0, "runner": "omo", "state": "new", "fixed_host": "node-a",
           "bot": "bot"}
    hub.store.save(job)

    assert hub.place(job) is None

    assert hub.store.load(job["id"])["state"] == "queued"
    queued = [r for r in hub.store.receipts_of(job["id"]) if r["kind"] == "queued"]
    assert [row["why"] for row in queued[0]["bids"]] == ["magpie not answering"]
    assert "reserve" not in nodes["node-a"].asked


def test_lina_devices_says_magpie_not_answering(fleet):
    base, _ = fleet
    lines: list[str] = []
    assert lina_devices.list_devices(base, as_json=True, out=lines.append) == 0
    rows = {row["name"]: row for row in json.loads(lines[0])}
    assert rows["node-a"]["ready"] is False and rows["node-a"]["why"] == "magpie not answering"
    assert rows["node-b"]["ready"] is True
