"""Reasons the bid passes a node apart from its slots (lina#456): it is cooling off after quick failures, or its available
memory is under the floor. A named run still starts on a cooling node; an older node whose probe has none of the new
fields is judged as before; the score order is unchanged."""

import json
import textwrap
import time

import pytest

from hermes_cli import lina_devices
from hermes_cli.worker import bidding, device, transport
from hermes_cli.worker.hub import Hub
from hermes_cli.worker.hub_store import Store
from hermes_cli.worker.settings import Device
from tests.hermes_cli.test_worker_bidding_skip_reasons import OCX_UP, FakeNode, probe

T0 = 1_000_000.0
COOLING = "cooling off after 2 quick failures"


def facts(root, found=None, *, now, node="node-a", descriptor=None):
    return bidding.with_node_facts(found or probe(), descriptor, node, root, now=now)


def why(dev, found, **kw):
    return bidding.readiness(dev, found, repo=None, min_free_gb=5, **kw)


@pytest.fixture
def root(tmp_path):
    (tmp_path / "config.yaml").write_text("worker:\n  min_free_gb: 5\n", encoding="utf-8")
    return tmp_path / "worker"


def ends(root, *items, node="node-a"):
    store = Store(root)
    for at, state, took in items:
        bidding.record_end(store, node, state, took, at=at)
    return store


def test_two_quick_failures_pass_the_node_in_the_bid_but_a_named_run_still_starts(root):
    ends(root, (T0, "failed", 30.0), (T0 + 600, "failed", 90.0))
    dev = Device("node-a", "node-a", cap=2)
    found = facts(root, now=T0 + 660)

    assert why(dev, found) == COOLING
    assert why(dev, found, manual=True) is None
    assert bidding.choose([dev], [found], repo=None, manual=True)[0] is dev


def test_a_done_run_clears_the_cool_off(root):
    ends(root, (T0, "failed", 30.0), (T0 + 60, "failed", 30.0))
    assert facts(root, now=T0 + 120).get("cooling") == 2
    ends(root, (T0 + 100, "done", 30.0))
    assert "cooling" not in facts(root, now=T0 + 120)
    ends(root, (T0 + 110, "failed", 30.0))
    assert "cooling" not in facts(root, now=T0 + 120), "the newest two are done and failed, not two failures"


def test_the_cool_off_ends_after_cooloff_s(root):
    ends(root, (T0, "failed", 30.0), (T0 + 60, "failed", 30.0))
    assert facts(root, now=T0 + 60 + 899).get("cooling") == 2
    assert "cooling" not in facts(root, now=T0 + 60 + 900)


@pytest.mark.parametrize("history", [
    [(T0, "failed", 30.0), (T0 + 60, "failed", 121.0)],  # not quick
    [(T0, "failed", None), (T0 + 60, "failed", 30.0)],  # length unknown: not quick
    [(T0, "failed", 30.0), (T0 + 1801, "failed", 30.0)],  # more than 30 minutes apart
    [(T0, "cancelled", 30.0), (T0 + 60, "failed", 30.0)],  # only failed counts
    [(T0, "failed", 30.0), (T0 + 60, "stalled", 30.0)],
    [(T0 + 60, "failed", 30.0)],  # one failure
])
def test_what_does_not_cool_a_node_off(root, history):
    ends(root, *history)
    assert "cooling" not in facts(root, now=T0 + 120)


def test_the_cool_off_numbers_are_settings(root):
    (root.parent / "config.yaml").write_text(textwrap.dedent("""\
        worker:
          cooloff_failures: 3
          cooloff_window_s: 60
          cooloff_quick_s: 10
          cooloff_s: 30
        """), encoding="utf-8")
    ends(root, (T0, "failed", 5.0), (T0 + 20, "failed", 5.0))
    assert "cooling" not in facts(root, now=T0 + 25), "3 failures are needed now"
    ends(root, (T0 + 40, "failed", 5.0))
    found = facts(root, now=T0 + 69)
    assert found.get("cooling") == 3 and why(Device("node-a", "node-a"), found) == \
        "cooling off after 3 quick failures"
    assert "cooling" not in facts(root, now=T0 + 70)


def test_settings_set_takes_the_cool_off_numbers_and_refuses_a_bad_one(tmp_path, monkeypatch):
    from hermes_cli.lina_work_settings import SettingsError, set_path
    from hermes_cli.lina_work_settings_store import Store as SettingsStore
    from hermes_cli.worker import settings

    base = tmp_path / "lina"
    base.mkdir()
    (base / "config.yaml").write_text("devices:\n  workers:\n    node-a: {host: node-a}\nworker:\n  min_free_gb: 5\n",
                                      encoding="utf-8")
    monkeypatch.setenv("LINA_HOME", str(base))
    store = SettingsStore(base)
    store.apply(lambda doc: set_path(doc, "worker.cooloff_s", 600), {"kind": "cli", "principal": "t", "device": "x"})
    assert settings.setting("cooloff_s", base) == 600 and settings.setting("min_free_gb", base) == 5
    with pytest.raises(SettingsError) as refused:
        store.apply(lambda doc: set_path(doc, "worker.cooloff_failures", 0),
                    {"kind": "cli", "principal": "t", "device": "x"})
    assert [e["path"] for e in refused.value.errors] == ["worker.cooloff_failures"]


@pytest.mark.parametrize("manual", [False, True])
def test_a_node_under_the_memory_floor_is_passed(root, manual):
    dev = Device("node-a", "node-a", cap=2)
    assert why(dev, facts(root, probe(mem_available_gb=3), now=T0), manual=manual) == "memory 3GB < 4GB"
    assert why(dev, facts(root, probe(mem_available_gb=4), now=T0), manual=manual) is None
    (root.parent / "config.yaml").write_text("worker:\n  min_free_mem_gb: 8\n", encoding="utf-8")
    assert why(dev, facts(root, probe(mem_available_gb=6), now=T0), manual=manual) == "memory 6GB < 8GB"


def test_probe_reports_available_memory_in_whole_gb(tmp_path, monkeypatch):
    (tmp_path / "config.yaml").write_text("worker:\n  min_free_gb: 0\n", encoding="utf-8")
    monkeypatch.setattr(device.psutil, "virtual_memory", lambda: type("Mem", (), {"available": int(6.7 * 2**30)})())
    assert device.probe(tmp_path / "worker")["mem_available_gb"] == 6


@pytest.mark.parametrize("manual", [False, True])
def test_an_older_node_without_the_new_fields_is_judged_as_before(root, manual):
    dev = Device("node-a", "node-a", cap=2)
    old = probe()
    assert why(dev, facts(root, old, now=T0), manual=manual) is None
    assert why(dev, facts(root, probe(running=2), now=T0), manual=manual) == "full"
    assert why(dev, facts(root, probe(free_gb=1), now=T0), manual=manual) == "disk 1GB < 5GB"


def test_the_score_order_is_unchanged_with_the_new_fields():
    healthy = {"ocx": OCX_UP, "mem_available_gb": 32, "mem_floor_gb": 4}
    idle = [Device("slow", "slow", cap=2), Device("fast", "fast", cap=2, fast=True), Device("spare", "spare", last=1)]
    winner, rows = bidding.choose(idle, [probe(load=0.0, **healthy), probe(load=7.0, **healthy),
                                         probe(**healthy)], repo=None)
    assert winner.name == "fast" and rows[2]["why"] == "only when the others are full"

    fuller = [Device("fast", "fast", cap=2, fast=True), Device("slow", "slow", cap=2)]
    assert bidding.choose(fuller, [probe(running=1, **healthy), probe(**healthy)], repo=None)[0].name == "slow"


@pytest.mark.parametrize("fast", [False, True])
def test_on_equal_fill_and_fast_the_lower_load_per_core_wins(fast):
    busy, calm = Device("node-a", "node-a", cap=2, fast=fast), Device("node-b", "node-b", cap=2, fast=fast)
    winner, rows = bidding.choose([busy, calm], [probe(running=1, load=6.0), probe(running=1, load=2.0)], repo=None)
    assert winner is calm and rows[0]["score"][2] == 0.75 and rows[1]["score"][2] == 0.25


def test_the_load_is_compared_per_core():
    small, big = Device("node-a", "node-a", cap=2), Device("node-b", "node-b", cap=2)
    winner, _ = bidding.choose([small, big], [probe(load=3.0, cores=2), probe(load=4.0, cores=8)], repo=None)
    assert winner is big, "4.0 on 8 cores (0.5) is lighter than 3.0 on 2 cores (1.5)"


@pytest.fixture
def fleet(tmp_path, monkeypatch):
    """``node-a`` (whose runs end through the hub) and ``node-b`` (low on memory) behind fake transports."""
    (tmp_path / "config.yaml").write_text(
        "devices:\n  workers:\n    node-a: {host: node-a, cap: 2}\n    node-b: {host: node-b, cap: 2}\n"
        "worker:\n  min_free_gb: 5\n", encoding="utf-8")
    nodes = {"node-a": FakeNode(probe(mem_available_gb=16)), "node-b": FakeNode(probe(mem_available_gb=2))}

    def for_device(dev, root):
        return transport.NegotiatedTransport(nodes[dev.name], dev.name)

    monkeypatch.setattr(transport, "for_device", for_device)
    monkeypatch.setattr(lina_devices, "for_device", for_device)
    monkeypatch.setattr(lina_devices, "omo_status", lambda dev: {"pinned": "1", "current": "1"})
    monkeypatch.setattr(lina_devices, "runner_status", lambda dev, root: [])
    monkeypatch.setattr(Hub, "_end_advance", lambda self, job, delivery: "sent")
    return tmp_path


def _ended(hub, task_id, state, run_s, run="1", manifest_run=None, started=None):
    """*task_id*'s run *run* ends on node-a; its fetched manifest says the node ran it *run_s* seconds (for run
    *manifest_run*, default *run*; *run_s* None: nothing fetched)."""
    job = {"id": task_id, "created": 1.0, "runner": "omo", "state": "running", "host": "node-a", "bot": "bot",
           "runs": int(run), "started": time.time() if started is None else started}
    hub.store.save(job)
    if run_s is not None:
        out = hub.store.out_dir(job)
        out.mkdir(parents=True, exist_ok=True)
        (out / "manifest.json").write_text(json.dumps({"run": int(manifest_run or run), "run_s": run_s}),
                                           encoding="utf-8")
    hub.notify(task_id, state, run)


def test_run_ends_reported_to_the_hub_cool_the_node_off_and_every_surface_says_why(fleet):
    hub = Hub(fleet / "worker", out=lambda line: None)
    _ended(hub, "OT-T0000000001", "failed", 20)
    _ended(hub, "OT-T0000000002", "failed", 20)

    lines: list[str] = []
    Hub(fleet / "worker", out=lines.append).hosts()
    assert lines[0].startswith("node-a ") and lines[0].endswith(
        " · can take work · cooling off after 2 quick failures (named runs still start)")
    assert lines[1].startswith("node-b ") and lines[1].endswith(" · memory 2GB < 4GB")

    out: list[str] = []
    lina_devices.list_devices(fleet, as_json=True, out=out.append)
    rows = {row["name"]: row for row in json.loads(out[0])}
    assert rows["node-a"]["why"] == COOLING and rows["node-b"]["why"] == "memory 2GB < 4GB"

    job = {"id": "OT-T0000000003", "created": 2.0, "runner": "omo", "state": "new", "bot": "bot"}
    hub.store.save(job)
    assert hub.place(job) is None
    queued = [r for r in hub.store.receipts_of(job["id"]) if r["kind"] == "queued"]
    assert [row["why"] for row in queued[0]["bids"]] == [COOLING, "memory 2GB < 4GB"]

    _ended(hub, "OT-T0000000004", "done", 20)
    assert "cooling" not in bidding.with_node_facts(probe(), None, "node-a", fleet / "worker")


def test_a_later_run_is_measured_by_its_manifest_too(fleet):
    hub = Hub(fleet / "worker", out=lambda line: None)
    _ended(hub, "OT-T0000000001", "failed", 20)
    _ended(hub, "OT-T0000000002", "failed", 20, run="2")
    assert hub.store.load_node("node-a")["ends"][-1]["took"] == 20.0
    assert bidding.with_node_facts(probe(), None, "node-a", fleet / "worker").get("cooling") == 2


def test_a_late_end_report_still_counts_the_node_s_run_time(fleet):
    hub = Hub(fleet / "worker", out=lambda line: None)
    hours_ago = time.time() - 3 * 3600
    _ended(hub, "OT-T0000000001", "failed", 20, started=hours_ago)
    _ended(hub, "OT-T0000000002", "failed", 20, started=hours_ago)
    assert [end["took"] for end in hub.store.load_node("node-a")["ends"]] == [20.0, 20.0]
    assert bidding.with_node_facts(probe(), None, "node-a", fleet / "worker").get("cooling") == 2


def test_a_long_run_reported_at_once_is_not_a_quick_failure(fleet):
    hub = Hub(fleet / "worker", out=lambda line: None)
    _ended(hub, "OT-T0000000001", "failed", 300)
    _ended(hub, "OT-T0000000002", "failed", 300)
    assert [end["took"] for end in hub.store.load_node("node-a")["ends"]] == [300.0, 300.0]
    assert "cooling" not in bidding.with_node_facts(probe(), None, "node-a", fleet / "worker")


def test_an_end_without_that_run_s_manifest_has_no_length(fleet):
    hub = Hub(fleet / "worker", out=lambda line: None)
    _ended(hub, "OT-T0000000001", "failed", None)  # nothing fetched (a waiter, a failed fetch)
    _ended(hub, "OT-T0000000002", "failed", 20, run="2", manifest_run="1")  # an earlier run's manifest
    assert [end["took"] for end in hub.store.load_node("node-a")["ends"]] == [None, None]
    assert "cooling" not in bidding.with_node_facts(probe(), None, "node-a", fleet / "worker")


def test_enough_ends_are_kept_for_a_threshold_above_ten(root):
    (root.parent / "config.yaml").write_text("worker:\n  cooloff_failures: 11\n", encoding="utf-8")
    ends(root, *((T0 + i, "failed", 5.0) for i in range(11)))
    assert facts(root, now=T0 + 20).get("cooling") == 11

    (root / "hub" / "nodes" / "node-a.json").unlink()
    ends(root, *((T0 + i, "failed", 5.0) for i in range(10)))
    assert "cooling" not in facts(root, now=T0 + 20)
