"""The usage, quota and model screens read magpie as well as OCX (lina#588): ``worker.proxy`` of the LINA root picks
the proxies read; magpie is a local fake gateway on 127.0.0.1 serving synthetic recordings of ``/v1/models``,
``/v1/magpie/usage`` and ``/v1/magpie/quotas`` (no real names, keys or addresses). OCX stays the default and reads
exactly as before; a magpie that does not answer is shown as unknown, never as zero."""

from __future__ import annotations

import argparse
import contextlib
import json
import socket
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import pytest
from fastapi.testclient import TestClient

from hermes_cli import lina_board_quota, lina_magpie, lina_ocx
from hermes_cli.subcommands.models import build_models_parser
from hermes_cli.worker import proxy as proxies
from hermes_cli.worker.runners.base import RunnerError
from tests.hermes_cli._lina_ocx_fakes import FIXTURES as OCX_FIXTURES, installed_root
from tests.hermes_cli.test_lina_board_api import _valid

MAGPIE = Path(__file__).parent / "fixtures" / "magpie"
NOW = 1_794_500_000.0


def _fixture(name: str):
    return json.loads((MAGPIE / name).read_text(encoding="utf-8"))


class FakeMagpie:
    """magpie's gateway on a free loopback port: each GET path answers its recording; every request is kept."""

    def __init__(self):
        self.paths: list[str] = []
        self.status = 200
        routes = {"/v1/models": "models.json", "/v1/magpie/usage": "usage_today.json",
                  "/v1/magpie/quotas": "quotas.json"}
        fake = self

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                fake.paths.append(self.path)
                name = routes.get(self.path.split("?")[0])
                code = fake.status if name else 404
                body = (MAGPIE / name).read_bytes() if name and code == 200 else b'{"error": "no"}'
                self.send_response(code)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *args):
                pass

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.url = f"http://127.0.0.1:{self.server.server_address[1]}"
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def close(self):
        self.server.shutdown()
        self.server.server_close()


@pytest.fixture
def magpie():
    fake = FakeMagpie()
    yield fake
    fake.close()


@pytest.fixture
def closed_port():
    with contextlib.closing(socket.socket()) as sock:  # bound, never listening: a connection is refused
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
        yield f"http://127.0.0.1:{port}"


@pytest.fixture
def root(tmp_path, monkeypatch):
    root = installed_root(tmp_path / "lina")
    monkeypatch.setattr(lina_ocx, "lina_root", lambda: root)
    monkeypatch.setattr(lina_ocx, "own_proxy_running", lambda root, port: True)
    monkeypatch.setattr(lina_board_quota, "_CACHE", {})
    return root


def _proxy(root: Path, section: str) -> None:
    (root / "config.yaml").write_text(f"worker:\n  proxy: {section}\n", encoding="utf-8")


def _models(*argv) -> int:
    parser = argparse.ArgumentParser()
    build_models_parser(parser.add_subparsers())
    args = parser.parse_args(["models", *argv])
    with pytest.raises(SystemExit) as exc:
        args.func(args)
    return exc.value.code


@pytest.fixture
def client():
    from hermes_cli import web_server
    test_client = TestClient(web_server.app)
    test_client.headers[web_server._SESSION_HEADER_NAME] = web_server._SESSION_TOKEN
    return test_client


# --- the settings ---------------------------------------------------------------------------------------------------

def test_the_proxies_read_default_to_the_kind_and_name_only_known_kinds():
    assert proxies.of({}).reads() == ("ocx",)
    assert proxies.of({"proxy": {"kind": "magpie"}}).reads() == ("magpie",)
    both = proxies.of({"proxy": {"read": ["ocx", "magpie"], "url": "http://127.0.0.1:9", "key_env": "MAGPIE_KEY"}})
    assert (both.kind, both.reads(), both.url, both.key_env) == ("ocx", ("ocx", "magpie"), "http://127.0.0.1:9",
                                                                  "MAGPIE_KEY")
    for wrong in ({"read": ["ocx", "ocx"]}, {"read": ["other"]}, {"read": []}, {"url": "127.0.0.1:9"},
                  {"key_env": "not a name"}):
        with pytest.raises(RunnerError, match="worker.proxy"):
            proxies.of({"proxy": wrong})


def test_the_gateway_is_the_url_else_the_health_origin_else_magpie_s_default():
    assert lina_magpie.base_url(proxies.Proxy("magpie", url="http://127.0.0.1:7/")) == "http://127.0.0.1:7"
    assert lina_magpie.base_url(proxies.Proxy("magpie", health="http://127.0.0.1:8/healthz")) == "http://127.0.0.1:8"
    assert lina_magpie.base_url(proxies.Proxy("magpie")) == lina_magpie.DEFAULT_URL


# --- lina models ----------------------------------------------------------------------------------------------------

@pytest.mark.platforms("posix")
def test_a_magpie_node_lists_magpie_s_models_by_their_lina_names(root, magpie, capsys):
    _proxy(root, f"{{kind: magpie, url: '{magpie.url}'}}")
    assert _models() == 0
    lines = capsys.readouterr().out.splitlines()
    assert lines[0].split() == ["PROVIDER", "MODEL", "CONTEXT", "STATE"]
    assert [line.split()[:2] if not line.startswith(" ") else ["", line.split()[0]] for line in lines[1:]] == [
        ["claude", "claude/claude-opus-5-5"], ["codex", "codex/gpt-6-astra"], ["claude", "claude/claude-fable-5"],
        ["", "someplace/odd-model"]]  # an unknown provider stays raw; an entry without an id is left out
    assert magpie.paths == ["/v1/models"]  # one read, nothing else asked of magpie


@pytest.mark.platforms("posix")
def test_ocx_and_magpie_show_side_by_side_labelled_by_kind(root, magpie, capsys):
    assert _models() == 0
    ocx_alone = capsys.readouterr().out.splitlines()
    _proxy(root, f"{{kind: ocx, read: [ocx, magpie], url: '{magpie.url}'}}")
    assert _models() == 0
    lines = capsys.readouterr().out.splitlines()
    split = lines.index("[magpie]")
    assert lines[0] == "[ocx]" and lines[1:split] == ocx_alone  # OCX's table as it is alone
    assert "claude/claude-opus-5-5" in "\n".join(lines[split:])
    assert _models("--json") == 0
    parts = json.loads(capsys.readouterr().out)
    assert [(part["kind"], part["data"] is not None) for part in parts] == [("ocx", True), ("magpie", True)]


@pytest.mark.platforms("posix")
def test_an_unreachable_magpie_is_unknown_never_zero(root, closed_port, capsys, client):
    _proxy(root, f"{{kind: magpie, url: '{closed_port}'}}")
    assert _models("usage") == 1
    out = capsys.readouterr().out
    assert out.startswith("unknown: magpie GET /v1/magpie/usage: not reachable") and " 0 " not in out
    assert client.get("/api/lina/usage?range=today").status_code == 503
    assert client.get("/api/lina/models").status_code == 503

    _proxy(root, f"{{kind: ocx, read: [ocx, magpie], url: '{closed_port}'}}")
    assert _models("usage") == 0
    lines = capsys.readouterr().out.splitlines()
    assert lines[lines.index("[magpie]") + 1].startswith("unknown: ")
    body = client.get("/api/lina/models/usage").json()
    assert body["proxies"][0]["data"] == json.loads((OCX_FIXTURES / "usage.json").read_text())
    assert body["proxies"][1]["data"] is None and body["proxies"][1]["unknown"].startswith("magpie GET")


@pytest.mark.platforms("posix")
def test_a_magpie_that_answers_an_error_is_unknown(root, magpie, client):
    magpie.status = 500
    _proxy(root, f"{{kind: magpie, url: '{magpie.url}'}}")
    response = client.get("/api/lina/models/accounts")
    assert response.status_code == 503 and "HTTP 500" in response.json()["detail"]


# --- usage ----------------------------------------------------------------------------------------------------------

@pytest.mark.platforms("posix")
def test_magpie_usage_is_served_per_caller_key_in_ocx_s_row_shape(root, magpie, client):
    _proxy(root, f"{{kind: magpie, url: '{magpie.url}'}}")
    usage = _valid("UsageView", client.get("/api/lina/usage?range=today").json())
    assert magpie.paths == ["/v1/magpie/usage?period=today"]
    assert usage["proxy"] == "magpie" and usage["range"] == "today"
    assert (usage["requests"], usage["tokens"], usage["costUsd"]) == (
        7, {"total": 6600, "input": 1200, "output": 300, "cacheRead": 5000}, 0.42)
    machines = {row["machine"]: row for row in usage["byMachine"]}
    assert list(machines) == ["node-a", "node-b"]
    assert machines["node-a"]["tokens"] == {"total": 5250, "input": 900, "output": 250, "cacheRead": 4000}
    # node-b's recording leaves out output and cache counts: unknown, not 0
    assert machines["node-b"]["requests"] == 2 and machines["node-b"]["tokens"] == {
        "total": None, "input": 300, "output": None, "cacheRead": None}
    assert [(row["provider"], row["requests"]) for row in usage["byProvider"]] == [("claude", 4), ("codex", 3)]
    accounts = [row["account"] for row in usage["byAccount"]]
    assert accounts[1] == "work" and accounts[0].startswith("#") and "@" not in json.dumps(usage)


@pytest.mark.platforms("posix")
def test_with_both_kinds_usage_and_model_state_carry_each_proxy(root, magpie, client, monkeypatch):
    from hermes_cli import lina_models
    ocx_usage = {"range": "today", "generatedAt": None, "requests": 1, "tokens": {"total": 2, "input": 1, "output": 1,
                                                                                   "cacheRead": 0},
                 "costUsd": None, "byModel": [], "byProvider": [], "byAccount": [], "byMachine": []}
    monkeypatch.setattr(lina_models, "usage", lambda range_, root=None: dict(ocx_usage))
    monkeypatch.setattr(lina_models, "models_state", lambda root=None: {"enabled": 0, "disabled": 0,
                                                                        "enabledModels": [], "machines": []})
    _proxy(root, f"{{kind: ocx, read: [ocx, magpie], url: '{magpie.url}'}}")
    usage = _valid("UsageView", client.get("/api/lina/usage?range=today").json())
    assert usage["proxy"] == "ocx" and usage["requests"] == 1
    assert [(part["kind"], part["data"]["requests"]) for part in usage["proxies"]] == [("ocx", 1), ("magpie", 7)]
    state = _valid("ModelsState", client.get("/api/lina/models/state").json())
    magpie_state = state["proxies"][1]["data"]
    assert magpie_state["enabled"] == 4 and {"provider": "claude", "model": "claude/claude-opus-5-5"} in \
        magpie_state["enabledModels"]


# --- quota ----------------------------------------------------------------------------------------------------------

def test_magpie_quota_cards_become_ocx_accounts_with_unknown_windows_left_out():
    cards = lina_magpie.accounts(_fixture("quotas.json"))
    assert [card["provider"] for card in cards] == ["claude", "codex", "grok"]  # a key's balance has no window
    claude, codex, grok = cards
    assert claude["quota"] == {"fiveHourPercent": 12, "fiveHourResetAt": "2026-11-12T15:00:00Z", "weeklyPercent": 40,
                               "weeklyResetAt": "2026-11-15T00:00:00Z",
                               "customWindows": [{"label": "7 days · Fable", "percent": 5, "resetAt": None}]}
    # read deferred: the windows magpie kept are shown; one with neither used nor remaining is unknown, not 0
    assert codex["health"] == "deferred" and codex["quota"] == {"fiveHourPercent": 30, "fiveHourResetAt": None}
    assert grok["quota"] == {"quotaUnavailable": True}
    assert "@" not in json.dumps(cards)


@pytest.mark.platforms("posix")
def test_the_board_quota_reads_magpie_s_kept_quotas_in_the_quota_row_shape(root, magpie, monkeypatch):
    _proxy(root, f"{{kind: magpie, url: '{magpie.url}'}}")
    data = lina_board_quota.view(root, NOW)
    assert magpie.paths == ["/v1/magpie/quotas?kept=1"]  # what magpie last read; no vendor is asked
    services = {service["service"]: service for service in data["quota"]}
    assert list(services) == ["OpenAI", "Claude", "Grok"]
    claude = services["Claude"]["accounts"][0]
    assert [(w["key"], w["percent"], w["left"]) for w in claude["windows"]] == [
        ("weekly", 40.0, 60.0), ("fiveHour", 12.0, 88.0), ("custom:7 days · Fable", 5.0, 95.0)]
    assert claude["mainWindow"] == "weekly" and services["Claude"]["percent"] == 40.0
    grok = services["Grok"]["accounts"][0]
    assert grok["windows"] == [] and grok["unavailable"] is True and services["Grok"]["percent"] is None
    assert "proxy" not in services["Claude"]  # one proxy read: no label
    assert (root / "board" / "quota.jsonl").is_file()


@pytest.mark.platforms("posix")
def test_with_both_kinds_the_quota_view_labels_each_service_and_a_dead_magpie_is_an_error(root, magpie, closed_port,
                                                                                          monkeypatch):
    from hermes_cli import lina_models
    accounts = {"accounts": [{"provider": "anthropic", "id": "k", "quota": {"weeklyPercent": 10}}], "notes": []}
    monkeypatch.setattr(lina_models, "query", lambda view, root=None: accounts)
    _proxy(root, f"{{kind: ocx, read: [ocx, magpie], url: '{magpie.url}'}}")
    data = lina_board_quota.view(root, NOW, [])
    assert [(service["proxy"], service["service"]) for service in data["quota"]] == [
        ("ocx", "Claude"), ("magpie", "OpenAI"), ("magpie", "Claude"), ("magpie", "Grok")]
    assert data["missing"] == ["Gemini", "Antigravity", "Cursor"]

    monkeypatch.setattr(lina_board_quota, "_CACHE", {})
    _proxy(root, f"{{kind: ocx, read: [ocx, magpie], url: '{closed_port}'}}")
    errors: list[dict] = []
    data = lina_board_quota.view(root, NOW, errors)
    assert [service["service"] for service in data["quota"]] == ["Claude"]
    assert data["missing"] is None  # unknown while magpie is unread
    assert errors[0]["source"] == "quota-magpie" and errors[0]["message"].startswith("unknown: ")
    with pytest.raises(lina_magpie.MagpieError):
        _proxy(root, f"{{kind: magpie, url: '{closed_port}'}}")
        lina_board_quota.view(root, NOW, [])
