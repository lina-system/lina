"""A node probes a runner the way its run builds it (lina#337): its own ``worker.runners`` entry, else the hub's spec.

The node here has its own LINA root and ``config.yaml``, apart from the hub's; its ``_device`` verbs run in this process
as ``lina worker _device`` runs them over ssh. Nothing starts a runner: placement ends at the published request.
"""

import json
import sys
import textwrap

import pytest

from hermes_cli.worker import device, hub as hub_mod, serve, settings, transport
from hermes_cli.worker.file_node import FileNodeTransport
from hermes_cli.worker.hub import Hub
from hermes_cli.worker.transport import NegotiatedTransport, Reply
from tests.hermes_cli import _worker_fakes as fakes

PRESENT = sys.executable
MISSING = "lina-337-no-such-agent"


def _acp(binary: str) -> str:
    return json.dumps({"type": "acp", "argv": [binary]})


class Node:
    """A node's ``_device`` verbs on its own LINA root. ``old``: a node from before lina#337, whose hello does not
    offer the runner spec and whose probe never read stdin."""

    def __init__(self, root, old: bool = False):
        self.root, self.old, self.probe_stdin = root, old, []

    def call(self, verb, stdin=b"", timeout=120):
        if verb[0] == "probe":
            self.probe_stdin.append(stdin)
            stdin = b"" if self.old else stdin
        code, out = device.dispatch(self.root, verb, stdin)
        if self.old and verb[0] == "hello":
            hello = json.loads(out)
            hello["capabilities"].pop("probe_runner_spec", None)
            out = (json.dumps(hello) + "\n").encode()
        return Reply(code, out)


@pytest.fixture
def field(tmp_path, monkeypatch):
    """A hub whose only device is the node ``box``; *make(hub_runners, node_runners, old)* writes both configs."""
    monkeypatch.setattr(device, "launch", lambda root, task, run: None)

    def make(hub_runners: str, node_runners: str = "{}", old: bool = False) -> dict:
        hub_base, node_base = tmp_path / "hub", tmp_path / "node"
        hub_base.mkdir()
        node_base.mkdir()
        (hub_base / "config.yaml").write_text(textwrap.dedent(f"""\
            devices:
              workers:
                box: {{host: box, cap: 2}}
            worker:
              min_free_gb: 0
              min_free_mem_gb: 0
              runners: {{{hub_runners}}}
            """), encoding="utf-8")
        (node_base / "config.yaml").write_text(f"worker:\n  min_free_gb: 0\n  runners: {node_runners}\n",
                                               encoding="utf-8")
        node = Node(node_base / "worker", old=old)
        serve.serve_once(node.root)

        def for_device(dev, worker_root):
            assert dev.name == "box"
            return NegotiatedTransport(node, dev.name)

        monkeypatch.setattr(transport, "for_device", for_device)
        monkeypatch.setattr(hub_mod, "for_device", for_device)
        lines: list[str] = []
        return {"hub": Hub(hub_base / "worker", out=lines.append), "lines": lines, "node": node}

    return make


def _hosts_row(field: dict, runner: str = "acp") -> str:
    field["lines"].clear()
    field["hub"].hosts(runner)
    (row,) = [line for line in field["lines"] if line.startswith("box ")]
    return row


def test_a_hub_only_runner_is_ready_on_a_node_with_its_binary_and_placed_with_host(field, tmp_path):
    lane = field(f"acp: {_acp(PRESENT)}")
    assert "can take work" in _hosts_row(lane)

    lane["hub"].submit({"briefs": [fakes.brief(tmp_path, "hub-only runner")], "mode": "ulw", "topic": "acp",
                        "bot": "sion", "runner": "acp", "host": "box", "wait": None})
    (job,) = lane["hub"].store.all()
    assert (job["host"], job["state"]) == ("box", "running")
    task_dir = settings.tasks_dir(lane["node"].root) / job["id"]
    assert (task_dir / "request.1").is_file()
    assert json.loads((task_dir / "job.json").read_text())["runner_spec"] == json.loads(_acp(PRESENT))
    assert json.loads(lane["node"].probe_stdin[-1]) == json.loads(_acp(PRESENT))


def test_the_nodes_own_entry_wins_over_the_hub_spec(field):
    lane = field(f"acp: {_acp(PRESENT)}", node_runners=f"{{acp: {_acp(MISSING)}}}")
    assert _hosts_row(lane).endswith(f" · {MISSING} not found on PATH")


def test_a_node_without_the_binary_says_the_binary_is_missing_not_no_runner(field):
    lane = field(f"acp: {_acp(MISSING)}")
    row = _hosts_row(lane)
    assert row.endswith(f" · {MISSING} not found on PATH") and "no runner" not in row


def test_an_older_node_answers_as_before_and_the_bid_table_says_why(field):
    lane = field(f"acp: {_acp(PRESENT)}", old=True)
    assert _hosts_row(lane).endswith(
        " · no runner 'acp' (configured: omo); box does not take the hub's runner spec (update the node)")
    assert lane["node"].probe_stdin == [b""]


def test_a_file_node_takes_a_hub_omo_runner_and_still_refuses_any_other_type(tmp_path, monkeypatch):
    base = tmp_path / "lina"
    base.mkdir()
    (base / "config.yaml").write_text("worker:\n  min_free_gb: 0\n", encoding="utf-8")
    (dev,) = settings.devices(raw={"nas": {"host": "nas-host", "kind": "file", "omo": "~/.bun/bin/omo"}})
    monkeypatch.setattr(FileNodeTransport, "facts", lambda self, timeout=60: (
        Reply(0, b""), {"ready": True, "omo": "/node/omo", "model_path": "ok", "cores": "4", "load": "0", "free_kb": ""}))
    node = FileNodeTransport(dev, base / "worker")

    def probe(name: str, spec: dict) -> dict:
        return node.call(["probe", "", name], json.dumps(spec).encode()).json()

    assert probe("omo-hub", {"type": "omo"})["ready"] is True
    refused = probe("acp", json.loads(_acp(PRESENT)))
    assert (refused["ready"], refused["why"]) == (False, "a file node runs omo only (runner acp is acp)")
