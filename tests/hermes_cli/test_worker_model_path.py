"""A work node's model path, asked on the node (lina#416): a node with no model path answered ready and took work,
and its smoke run ended ``provider_error``. The node now reports its model path in ``probe`` and ``hello`` (the same
``model_check`` default a file node asks), the bid passes it with that reason, ``lina worker hosts`` and
``lina devices`` say it, and a ``--host`` submit there is refused with the same line. A node whose model path answers
bids as before, and an older node whose probe/hello has no such field is judged as before.
"""

import json
import textwrap
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest

from hermes_cli import lina_devices
from hermes_cli.worker import bidding, device, hub as hub_mod, serve, transport
from hermes_cli.worker.hub import Hub, HubError
from hermes_cli.worker.settings import Device
from hermes_cli.worker.transport import NegotiatedTransport, Reply
from tests.hermes_cli import _worker_fakes as fakes
from tests.hermes_cli.test_worker_bidding_skip_reasons import probe

pytestmark = pytest.mark.real_model_path  # the conftest stub answers every model path 200: this file asks a real one


class ModelPath:
    """The endpoint the node's ``model_check`` names: it answers ``status``, and a closed server answers nothing."""

    def __init__(self):
        self.status = 200
        endpoint = self

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(endpoint.status)
                self.end_headers()

            def log_message(self, *args):
                pass

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.url = f"http://127.0.0.1:{self.server.server_address[1]}/healthz"
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def close(self) -> None:
        if self.server is not None:
            self.server.shutdown()
            self.server.server_close()
            self.server = None


@pytest.fixture
def model_path():
    endpoint = ModelPath()
    yield endpoint
    endpoint.close()


class Node:
    """A node's ``_device`` verbs on its own LINA root, as ``lina worker _device`` runs them over ssh."""

    def __init__(self, root):
        self.root = root

    def call(self, verb, stdin=b"", timeout=120):
        code, out = device.dispatch(self.root, verb, stdin)
        return Reply(code, out)


@pytest.fixture
def field(tmp_path, monkeypatch):
    """*make(model_check)*: the hub ``box`` and a node with its own LINA root whose ``model_check`` is *model_check*."""

    def make(model_check: str) -> dict:
        hub_base, node_base = tmp_path / "hub", tmp_path / "node"
        hub_base.mkdir()
        node_base.mkdir()
        runner = json.dumps({"type": "command", "argv": fakes.install_fake_cli(tmp_path / "fake"),
                             "access": "workspace-write"})
        (hub_base / "config.yaml").write_text(textwrap.dedent(f"""\
            devices:
              workers:
                box: {{host: box, cap: 2}}
            worker:
              min_free_gb: 0
              min_free_mem_gb: 0
              default_runner: fakecli
              runners:
                fakecli: {runner}
            """), encoding="utf-8")
        (node_base / "config.yaml").write_text(textwrap.dedent(f"""\
            devices:
              workers:
                me: {{host: local, model_check: {model_check}}}
            worker:
              min_free_gb: 0
              runners:
                fakecli: {runner}
            """), encoding="utf-8")
        node = Node(node_base / "worker")
        serve.serve_once(node.root)

        def for_device(dev, worker_root):
            assert dev.name == "box"
            return NegotiatedTransport(node, dev.name)

        monkeypatch.setattr(transport, "for_device", for_device)
        monkeypatch.setattr(hub_mod, "for_device", for_device)
        monkeypatch.setattr(lina_devices, "for_device", for_device)
        # The node reports a healthy OCX, the #416 shape: its OCX answers while its model path does not, so only
        # the model path decides and the answer does not depend on an OCX running on the host of this test.
        monkeypatch.setattr("hermes_cli.lina_runner_sync_node.ocx_report",
                            lambda root: {"external": False, "version": "1.0.0", "health": 200})
        monkeypatch.setattr(lina_devices, "omo_status", lambda dev: {"pinned": "1", "current": "1"})
        monkeypatch.setattr(lina_devices, "runner_status", lambda dev, root: [])
        lines: list[str] = []
        return {"hub": Hub(hub_base / "worker", out=lines.append), "hub_base": hub_base, "node": node,
                "lines": lines}

    return make


def _bid(lane: dict):
    (dev,) = lane["hub"].devices()
    return bidding.bid([dev], lane["hub"].worker_root, repo=None, runner="fakecli")


def _hosts_line(lane: dict) -> str:
    lane["lines"].clear()
    lane["hub"].hosts()
    (line,) = [row for row in lane["lines"] if row.startswith("box ")]
    return line


def _devices_row(lane: dict) -> dict:
    lines: list[str] = []
    assert lina_devices.list_devices(lane["hub_base"], as_json=True, out=lines.append) == 0
    return json.loads(lines[0])[0]


def test_the_node_reports_its_model_path_in_probe_and_hello(field, model_path):
    lane = field(model_path.url)
    node = lane["node"]

    answered = json.loads(node.call(["probe", "", "fakecli"]).out)
    assert answered["model_path"] == "ok" and "model_path_why" not in answered
    assert json.loads(node.call(["hello"]).out)["capabilities"]["model_path"] == "ok"

    model_path.status = 503
    reason = f"model path does not answer ({model_path.url}: HTTP 503)"
    assert json.loads(node.call(["probe", "", "fakecli"]).out)["model_path_why"] == reason
    assert json.loads(node.call(["hello"]).out)["capabilities"]["model_path_why"] == reason


def test_a_node_whose_model_path_answers_is_bid_on_as_before(field, model_path):
    lane = field(model_path.url)

    winner, rows = _bid(lane)

    assert winner is not None and winner.name == "box"
    assert "why" not in rows[0]
    assert _hosts_line(lane).endswith(" · can take work")
    assert _devices_row(lane)["ready"] is True


def test_a_node_whose_model_path_does_not_answer_is_passed_with_that_reason(field, model_path):
    model_path.status = 503
    lane = field(model_path.url)
    reason = f"model path does not answer ({model_path.url}: HTTP 503)"

    winner, rows = _bid(lane)

    assert winner is None and rows[0]["why"] == reason
    assert _hosts_line(lane).endswith(f" · {reason}")
    row = _devices_row(lane)
    assert row["ready"] is False and row["why"] == reason


def test_a_model_path_that_answers_nothing_at_all_is_named_with_its_url(field, model_path):
    lane = field(model_path.url)
    model_path.close()

    _, rows = _bid(lane)

    assert rows[0]["why"] == f"model path does not answer ({model_path.url}: HTTP none)"


def test_a_named_submit_to_a_node_whose_model_path_does_not_answer_is_refused(field, model_path):
    lane = field(model_path.url)
    brief = fakes.brief(lane["hub_base"], "write out/ok.txt")
    opts = {"briefs": [brief], "mode": "ulw", "topic": "model path", "bot": "sion", "host": "box", "wait": None}
    assert lane["hub"].submit(dict(opts)) == 0, "an answering model path is placed as before"

    model_path.status = 503
    with pytest.raises(HubError, match="model path does not answer"):
        lane["hub"].submit(dict(opts))


def test_a_model_path_passes_a_named_run_too_and_the_probe_row_says_it():
    dev = Device("node-a", "node-a", cap=2)
    reason = "model path does not answer (http://127.0.0.1:10100/healthz: HTTP none)"
    found = probe(model_path="down", model_path_why=reason)
    for manual in (False, True):
        assert bidding.readiness(dev, found, repo=None, min_free_gb=5, manual=manual) == reason


def test_a_node_whose_hello_alone_carries_the_model_path_is_passed_too(tmp_path):
    """The reason comes from the hello the same contact asks first when its probe reply has no answer of its own."""
    (tmp_path / "config.yaml").write_text("worker:\n  min_free_gb: 5\n", encoding="utf-8")
    reason = "model path does not answer (http://127.0.0.1:10100/healthz: HTTP none)"
    found = bidding.with_node_facts(probe(), {"capabilities": {"model_path": "down", "model_path_why": reason}},
                                    "node-a", tmp_path / "worker")
    assert bidding.readiness(Device("node-a", "node-a"), found, repo=None, min_free_gb=5) == reason


def test_an_older_node_without_the_field_is_bid_on_as_before():
    dev = Device("node-a", "node-a", cap=2)
    assert bidding.readiness(dev, probe(), repo=None, min_free_gb=5) is None
    assert bidding.readiness(dev, probe(model_path="ok"), repo=None, min_free_gb=5) is None
