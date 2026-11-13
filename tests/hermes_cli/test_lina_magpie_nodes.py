"""Nodes whose magpie model list or version differs from the hub's (lina#489): what a node's ``hello`` adds to
``capabilities.proxy``, and what the daily runner sync compares and lists. The magpie answering ``/v1/models`` is a
local HTTP endpoint and the ``magpie`` binary a script on PATH; the host's own magpie is out of reach."""

from __future__ import annotations

import json
import sys

import pytest

from hermes_cli import lina_magpie_nodes, lina_runner_sync
from hermes_cli.lina_runner_sync import NodeView
from tests.hermes_cli.test_lina_runner_sync import make_hub, release, run
from tests.hermes_cli.test_worker_proxy_health import Proxy, _closed

pytestmark = pytest.mark.real_model_path  # the conftest stubs the magpie reads this file makes for real

GATEWAY_KEY = "synthetic-gateway-key-489"
HUB = {"version": "0.1.3903", "names": ["claude/claude-opus-5-5", "codex/gpt-5.5"]}


@pytest.fixture
def endpoint():
    found = Proxy()
    yield found
    found.close()


@pytest.fixture
def magpie_bin(tmp_path, monkeypatch):
    """*make(printed)*: a ``magpie`` on PATH whose ``--version`` prints *printed*; None: no magpie at all."""
    monkeypatch.setenv("HOME", str(tmp_path / "home"))
    monkeypatch.setattr("hermes_cli.worker.proxy_service.magpie_state", lambda unit: "running")

    def make(printed: str | None) -> None:
        bin_dir = tmp_path / "bin"
        bin_dir.mkdir(exist_ok=True)
        if printed is not None:
            exe = bin_dir / "magpie"
            exe.write_text(f"#!{sys.executable}\nprint({printed!r})\n", encoding="utf-8")
            exe.chmod(0o755)
        monkeypatch.setenv("PATH", f"{bin_dir}:/usr/bin:/bin")

    return make


def hello_proxy(tmp_path, proxy_yaml: str) -> dict:
    from hermes_cli.worker import device
    base = tmp_path / "node"
    base.mkdir(exist_ok=True)
    (base / "config.yaml").write_text(f"devices:\n  workers:\n    me: {{host: local}}\nworker:\n  proxy: {proxy_yaml}\n",
                                      encoding="utf-8")
    code, out = device.dispatch(base / "worker", ["hello"], b"")
    assert code == 0, out
    return json.loads(out)


def test_hello_carries_the_version_and_names_of_the_nodes_own_magpie_and_no_key(tmp_path, endpoint, magpie_bin,
                                                                                monkeypatch):
    magpie_bin("magpie 0.1.3903")
    endpoint.key = GATEWAY_KEY
    monkeypatch.setenv("MAGPIE_KEY_489", GATEWAY_KEY)

    reply = hello_proxy(tmp_path, f"{{health: '{endpoint.base}/v1/models', key_env: MAGPIE_KEY_489}}")

    proxy = reply["capabilities"]["proxy"]
    assert proxy["version"] == "0.1.3903"
    assert proxy["names"] == ["p/m0", "p/m1"], "names only, without the node's hub/ link"
    assert GATEWAY_KEY not in json.dumps(reply)


@pytest.mark.parametrize("printed", [None, "", "magpie (devel)"], ids=["no-magpie", "prints-nothing", "no-number"])
def test_a_node_whose_magpie_gives_no_version_reports_none(tmp_path, endpoint, magpie_bin, printed):
    magpie_bin(printed)
    proxy = hello_proxy(tmp_path, f"{{health: '{endpoint.base}/v1/models'}}")["capabilities"]["proxy"]
    assert proxy["version"] is None


def test_a_node_whose_magpie_does_not_answer_reports_no_list_and_is_unknown_not_a_difference(tmp_path, magpie_bin):
    magpie_bin("magpie 0.1.3903")
    proxy = hello_proxy(tmp_path, f"{{health: '{_closed('/v1/models')}'}}")["capabilities"]["proxy"]
    assert proxy["names"] is None

    issues, notes = lina_magpie_nodes.lines({"magpie": lina_magpie_nodes.compare(
        tmp_path, [NodeView("build-a", True, proxy=proxy)], hub=HUB)})

    assert issues == []
    assert notes == ["- magpie: build-a model list unknown (the node's magpie gave no list)"]


def compared(proxy: dict | None, *, reachable: bool = True, hub: dict = HUB) -> tuple[list[str], list[str]]:
    record = {"magpie": lina_magpie_nodes.compare(None, [NodeView("build-a", reachable, proxy=proxy)], hub=hub)}
    return lina_magpie_nodes.lines(record)


def test_equal_lists_and_versions_list_nothing():
    assert compared({"names": list(reversed(HUB["names"])), "version": "0.1.3903"}) == ([], [])


def test_a_missing_model_lists_the_node_with_the_missing_name():
    issues, notes = compared({"names": ["claude/claude-opus-5-5"], "version": "0.1.3903"})
    assert issues == ["- magpie: build-a model list differs from the hub's (missing codex/gpt-5.5)"]
    assert notes == []


def test_a_different_version_lists_the_node_with_both_versions():
    issues, _ = compared({"names": HUB["names"], "version": "0.1.3800"})
    assert issues == ["- magpie: build-a version 0.1.3800, hub 0.1.3903"]


def test_a_node_that_does_not_answer_is_unknown_not_a_difference():
    assert compared(None, reachable=False) == ([], ["- magpie: build-a unknown (node did not answer)"])


@pytest.mark.parametrize("proxy, hub, why", [
    ({"names": HUB["names"], "version": None}, HUB, "the node's magpie gave no version"),
    ({"names": HUB["names"]}, HUB, "the node's magpie gave no version"),
    ({"names": HUB["names"], "version": "0.1.3903"}, {**HUB, "version": None}, "the hub's magpie gave no version"),
], ids=["node-none", "old-node-without-the-field", "hub-none"])
def test_a_missing_version_is_unknown_not_a_difference(proxy, hub, why):
    assert compared(proxy, hub=hub) == ([], [f"- magpie: build-a version unknown ({why})"])


def test_the_daily_sync_reads_the_hubs_magpie_and_lists_the_nodes_that_differ(tmp_path, monkeypatch, endpoint,
                                                                               magpie_bin):
    magpie_bin("magpie 0.1.3903")
    hub = make_hub(tmp_path, monkeypatch)
    (hub["root"] / "config.yaml").write_text(
        f"worker:\n  proxy: {{remote_id: '', health: '{endpoint.base}/v1/models'}}\n", encoding="utf-8")
    hub["http"].publish("1.0.70", release("1.0.70"))
    listed = ["hub/p/m0", "hub/p/m1"]  # the endpoint's ids; on the hub (remote_id "") they are read as they are
    views = {"same": {"names": listed, "version": "0.1.3903"}, "short": {"names": listed[:1], "version": "0.1.3903"},
             "older": {"names": listed, "version": "0.1.3800"}}
    hub["env"].nodes = lambda root: [NodeView(name, True, proxy={"health": 200, "models": 2, **view})
                                     for name, view in views.items()] + [NodeView("away", False, "ssh: timed out")]

    code, _ = run(hub)

    assert code == 0
    issues, notes = lina_runner_sync.issue_lines(hub["root"])
    assert [line for line in issues if line.startswith("- magpie:")] == [
        "- magpie: older version 0.1.3800, hub 0.1.3903",
        "- magpie: short model list differs from the hub's (missing hub/p/m1)"]
    assert "- magpie: away unknown (node did not answer)" in notes
    assert not [line for line in issues + notes if "magpie: same" in line]
