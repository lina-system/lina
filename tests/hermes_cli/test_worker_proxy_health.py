"""Magpie model-list health, hello reports and bid readiness agree (lina#586)."""

import json
import socket
import textwrap
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest

from hermes_cli import lina_devices
from hermes_cli.worker import bidding, device, protocol, transport
from hermes_cli.worker.hub import Hub
from hermes_cli.worker.settings import Device
from hermes_cli.worker.transport import Reply
from tests.hermes_cli.test_worker_bidding_skip_reasons import FakeNode, probe

pytestmark = pytest.mark.real_model_path  # the conftest stub answers every model path 200: this file asks a real one



class Proxy:
    """A local endpoint answering every path with ``status`` and, when ``models`` is not None, a ``/v1/models``
    body listing that many models; with ``key`` set, a request without ``Authorization: Bearer <key>`` gets 401."""

    def __init__(self):
        self.status, self.models, self.key = 200, 2, None
        endpoint = self

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                denied = endpoint.key is not None and self.headers.get("Authorization") != f"Bearer {endpoint.key}"
                body = b"" if endpoint.models is None or denied else json.dumps(
                    {"object": "list", "data": [{"id": f"hub/p/m{n}"} for n in range(endpoint.models)]}).encode()
                self.send_response(401 if denied else endpoint.status)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *args):
                pass

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.base = f"http://127.0.0.1:{self.server.server_address[1]}"
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def close(self):
        self.server.shutdown()
        self.server.server_close()


@pytest.fixture
def endpoint():
    found = Proxy()
    yield found
    found.close()


def _closed(path: str) -> str:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    return f"http://127.0.0.1:{port}{path}"


@pytest.fixture
def node(tmp_path, monkeypatch):
    """*make(proxy_yaml, model_check)*: a node's LINA root whose ``worker.proxy`` is *proxy_yaml* (None: absent) and
    whose ``host: local`` device asks *model_check*; returns a ``call(verb)`` that runs its ``_device`` verbs."""

    def make(proxy_yaml: str | None = None, model_check: str | None = None):
        base = tmp_path / "node"
        base.mkdir(exist_ok=True)
        entry = f"me: {{host: local{', model_check: ' + model_check if model_check else ''}}}"
        text = f"devices:\n  workers:\n    {entry}\nworker:\n  default_runner: claude\n"
        if proxy_yaml:
            text += textwrap.indent(f"proxy: {proxy_yaml}\n", "  ")
        (base / "config.yaml").write_text(text, encoding="utf-8")
        root = base / "worker"

        def call(*verb):
            code, out = device.dispatch(root, list(verb), b"")
            assert code == 0, out
            return json.loads(out)

        return call

    return make



def test_a_magpie_node_is_up_on_200_with_models(node, endpoint, monkeypatch):
    monkeypatch.setattr("hermes_cli.worker.proxy_service.magpie_state", lambda unit: f"running ({unit})")
    url = f"{endpoint.base}/v1/models"
    call = node(f"{{health: '{url}'}}")
    assert call("probe", "", "claude")["model_path"] == "ok"
    assert call("hello")["capabilities"]["proxy"] == {
        "url": url, "health": 200, "models": 2, "remote_id": "hub",
        "service": {"name": "magpie", "state": "running (magpie)"}}


@pytest.mark.parametrize("status", [404, 503])
def test_a_magpie_node_that_answers_an_error_is_down_with_kind_and_address(node, endpoint, status):
    endpoint.status, endpoint.models = status, None
    url = f"{endpoint.base}/v1/models"
    call = node(f"{{health: '{url}'}}")
    assert call("probe", "", "claude")["model_path_why"] == f"model path does not answer (magpie {url}: HTTP {status})"
    assert call("hello")["capabilities"]["proxy"]["health"] == status


def test_a_magpie_node_that_answers_nothing_is_down(node):
    url = _closed("/v1/models")
    call = node(f"{{health: '{url}'}}")
    assert call("probe", "", "claude")["model_path_why"] == f"model path does not answer (magpie {url}: HTTP none)"
    proxy = call("hello")["capabilities"]["proxy"]
    assert {key: proxy[key] for key in ("health", "models")} == {"health": None, "models": 0}


def test_a_magpie_node_with_an_empty_model_list_is_down(node, endpoint):
    endpoint.models = 0
    url = f"{endpoint.base}/v1/models"
    call = node(f"{{health: '{url}'}}")
    assert call("probe", "", "claude")["model_path_why"] == \
        f"model path lists no model (magpie {url}: HTTP 200, 0 models)"


GATEWAY_KEY = "synthetic-gateway-key-625"


def test_a_magpie_node_sends_the_key_its_key_env_names_and_shows_it_nowhere(node, endpoint, monkeypatch, caplog,
                                                                           tmp_path):
    """A gateway that answers only with a key (lina#625): with ``key_env`` set and the variable present the check
    passes; without the variable it is 401 with the kind and address. The key is in no output."""
    monkeypatch.setattr("hermes_cli.worker.proxy_service.magpie_state", lambda unit: "running")
    endpoint.key = GATEWAY_KEY
    url = f"{endpoint.base}/v1/models"
    call = node(f"{{health: '{url}', key_env: LINA_TEST_GATEWAY_KEY}}")

    monkeypatch.delenv("LINA_TEST_GATEWAY_KEY", raising=False)
    down = call("probe", "", "claude")
    assert down["model_path_why"] == f"model path does not answer (magpie {url}: HTTP 401)"

    monkeypatch.setenv("LINA_TEST_GATEWAY_KEY", GATEWAY_KEY)
    with caplog.at_level("DEBUG"):
        up, hello = call("probe", "", "claude"), call("hello")
        check = device.proxy_check(tmp_path / "node" / "worker")
    assert up["model_path"] == "ok"
    assert check == {"url": url, "health": 200, "models": 2}
    assert bidding.proxy_text(probe(proxy=hello["capabilities"]["proxy"])) == "magpie 200, 2 models"
    for shown in (json.dumps(down), json.dumps(up), json.dumps(hello), json.dumps(check), caplog.text):
        assert GATEWAY_KEY not in shown


def test_a_magpie_node_without_key_env_sends_no_authorization(node, endpoint, monkeypatch):
    monkeypatch.setenv("LINA_TEST_GATEWAY_KEY", GATEWAY_KEY)  # present, but no key_env names it
    endpoint.key = GATEWAY_KEY
    url = f"{endpoint.base}/v1/models"
    call = node(f"{{health: '{url}'}}")
    assert call("probe", "", "claude")["model_path_why"] == f"model path does not answer (magpie {url}: HTTP 401)"


def test_a_magpie_node_asks_its_local_v1_models_by_default_and_not_the_model_state(node, tmp_path):
    node("{}", model_check="http://127.0.0.1:1/healthz")
    root = tmp_path / "node" / "worker"
    assert device.model_check_url(root) == device.settings.MODEL_CHECK == "http://127.0.0.1:3425/v1/models"


@pytest.mark.parametrize("kind", [None, "{}"])
def test_which_runners_need_the_model_path_on_either_kind(kind):
    worker = {"proxy": {"kind": "magpie"}} if kind else {}
    assert device.needs_model_path(worker, "omo", {"type": "omo"}) is True
    assert device.needs_model_path(worker, "claude", None) is True  # pinned, signs in through the proxy
    assert device.needs_model_path(worker, "grok", {"type": "acp", "argv": ["grok"]}) is False  # own credentials
    assert device.needs_model_path(worker, "dsh", {"type": "acp", "argv": ["dsh"]}) is False  # sign-in unknown


def test_a_runner_that_needs_no_model_path_is_not_asked_on_a_magpie_node(node):
    call = node(f"{{health: '{_closed('/v1/models')}'}}")
    found = call("probe", "", "grok")
    assert "model_path" not in found and "model_path_why" not in found


MAGPIE_UP = {"health": 200, "models": 23}


@pytest.mark.parametrize(("found", "reason"), [
    (probe(model_path="ok", proxy=MAGPIE_UP), None),
    (probe(model_path="ok", proxy={**MAGPIE_UP, "health": None, "models": 0}), "magpie not answering"),
    (probe(model_path="ok", proxy={**MAGPIE_UP, "models": 0}), "magpie not answering"),
    (probe(proxy={**MAGPIE_UP, "health": None}), None),  # a runner that does not reach models through it
    (probe(), None),  # no proxy at all
])
def test_the_bid_requires_health_and_models_only_for_proxy_runners(found, reason):
    assert bidding.readiness(Device("n", "n"), found, repo=None, min_free_gb=5) == reason


def test_with_node_facts_carries_the_reported_proxy(tmp_path):
    (tmp_path / "config.yaml").write_text("worker:\n  min_free_gb: 5\n", encoding="utf-8")
    caps = {"proxy": MAGPIE_UP}
    found = bidding.with_node_facts(probe(), {"capabilities": caps}, "n", tmp_path / "worker")
    assert found["proxy"] == MAGPIE_UP


@pytest.mark.parametrize(("found", "text"), [
    (probe(proxy=MAGPIE_UP), "magpie 200, 23 models"),
    (probe(), "proxy none"),
])
def test_the_proxy_text_hosts_and_devices_show(found, text):
    assert bidding.proxy_text(found) == text


@pytest.fixture
def fleet(tmp_path, monkeypatch):
    """A magpie node whose list is empty, a healthy magpie node, a node with no proxy."""
    (tmp_path / "config.yaml").write_text(
        "devices:\n  workers:\n" + "".join(f"    {n}: {{host: {n}, cap: 2}}\n" for n in ("m-empty", "m-up", "none"))
        + "worker:\n  min_free_gb: 5\n", encoding="utf-8")
    nodes = {"m-empty": FakeNode(probe(model_path="ok"), {"proxy": {**MAGPIE_UP, "models": 0}}),
             "m-up": FakeNode(probe(model_path="ok"), {"proxy": MAGPIE_UP}),
             "none": FakeNode(probe(), {})}

    def for_device(dev, root):
        return transport.NegotiatedTransport(nodes[dev.name], dev.name)

    monkeypatch.setattr(transport, "for_device", for_device)
    monkeypatch.setattr(lina_devices, "for_device", for_device)
    monkeypatch.setattr(lina_devices, "runner_status", lambda dev, root: [])
    return tmp_path


def test_hosts_and_devices_show_the_proxy_and_agree_with_the_bid(fleet):
    lines: list[str] = []
    assert Hub(fleet / "worker", out=lines.append).hosts() == 0
    hosts = {line.split()[0]: line for line in lines[:3]}
    assert " · magpie 200, 0 models · magpie not answering" in hosts["m-empty"]
    assert hosts["m-up"].endswith(" · magpie 200, 23 models · can take work")
    assert hosts["none"].endswith(" · proxy none · can take work")

    out: list[str] = []
    assert lina_devices.list_devices(fleet, as_json=True, out=out.append) == 0
    rows = {row["name"]: row for row in json.loads(out[0])}
    assert (rows["m-empty"]["ready"], rows["m-empty"]["why"]) == (False, "magpie not answering")
    assert rows["m-up"]["proxy"] == MAGPIE_UP and rows["m-up"]["ready"] is True
    assert rows["none"]["proxy"] is None
    text: list[str] = []
    assert lina_devices.list_devices(fleet, out=text.append) == 0
    assert any(line.startswith("m-empty ") and "magpie 200, 0 models" in line for line in text)
