"""Models CLI and authenticated API read the same synthetic magpie gateway (lina#32)."""
import argparse
import json

import pytest
from fastapi.testclient import TestClient

from hermes_cli import lina_deps
from hermes_cli.subcommands.models import build_models_parser
from hermes_cli import lina_magpie

DATA = {
    "models_live.json": [{"provider": "claude", "id": "claude/m-a", "raw": "hub/claude/m-a",
                          "contextWindow": 8192, "disabled": False}],
    "account_list.json": {"accounts": [], "notes": []},
    "status.json": {"proxy": {"running": True, "pid": None, "health": {"message": "magpie: 1 models"}},
                    "listen": {"port": 3425}},
    "usage.json": {"range": "30d", "summary": {"requests": 7, "totalTokens": 6600, "estimatedCostUsd": 0.42}},
}


@pytest.fixture
def root(tmp_path, monkeypatch):
    root = tmp_path / "lina"
    root.mkdir()
    monkeypatch.setattr(lina_deps, "lina_root", lambda: root)
    running = {"yes": True}

    def get(proxy, path):
        if not running["yes"]:
            raise lina_magpie.MagpieError("magpie is not reachable")
        return {"/v1/models": {"data": [{"id": "hub/claude/m-a", "context_length": 8192}]},
                "/v1/magpie/quotas?kept=1": {"data": []},
                "/v1/magpie/usage?period=30d": {"calls": 7, "input": 1200, "output": 300,
                    "cache_read": 5000, "cache_write": 100, "cost": 0.42}}[path]

    monkeypatch.setattr(lina_magpie, "get", get)
    return running


def _fixture(name):
    return DATA[name]


def _models(*argv) -> int:
    parser = argparse.ArgumentParser()
    build_models_parser(parser.add_subparsers())
    args = parser.parse_args(["models", *argv])
    with pytest.raises(SystemExit) as exc:
        args.func(args)
    return exc.value.code


@pytest.mark.platforms("posix")
def test_models_prints_a_table_of_the_live_catalog(root, capsys):
    assert _models() == 0
    lines = capsys.readouterr().out.splitlines()
    assert lines[0].split() == ["PROVIDER", "MODEL", "CONTEXT", "STATE"]
    assert [line.split()[1] for line in lines[1:]] == [m["id"] for m in _fixture("models_live.json")]


@pytest.mark.platforms("posix")
@pytest.mark.parametrize(("argv", "fixture"), [(["--json"], "models_live.json"),
                                               (["accounts", "--json"], "account_list.json"),
                                               (["status", "--json"], "status.json"),
                                               (["usage", "--json"], "usage.json")])
def test_json_passes_magpie_data_through(root, capsys, argv, fixture):
    assert _models(*argv) == 0
    assert json.loads(capsys.readouterr().out) == _fixture(fixture)


@pytest.mark.platforms("posix")
def test_accounts_table_lists_each_account_and_magpie_notes(root, capsys):
    assert _models("accounts") == 0
    lines = capsys.readouterr().out.splitlines()
    data = _fixture("account_list.json")
    assert lines[0].split()[:2] == ["PROVIDER", "ID"]
    assert [line.split()[1] for line in lines[1:1 + len(data["accounts"])]] == [a["id"] for a in data["accounts"]]
    assert lines[1 + len(data["accounts"]):] == data["notes"]



@pytest.fixture
def client():
    from hermes_cli import web_server
    test_client = TestClient(web_server.app)
    test_client.headers[web_server._SESSION_HEADER_NAME] = web_server._SESSION_TOKEN
    return test_client


@pytest.mark.platforms("posix")
@pytest.mark.parametrize(("path", "view", "fixture"), [("/api/lina/models", "list", "models_live.json"),
                                                       ("/api/lina/models/accounts", "accounts", "account_list.json"),
                                                       ("/api/lina/models/status", "status", "status.json"),
                                                       ("/api/lina/models/usage", "usage", "usage.json")])
def test_api_serves_the_same_data_with_an_etag(root, client, path, view, fixture):
    first = client.get(path)
    assert first.status_code == 200
    assert first.json() == {"view": view, "data": _fixture(fixture)}
    again = client.get(path, headers={"If-None-Match": first.headers["etag"]})
    assert again.status_code == 304 and again.headers["etag"] == first.headers["etag"]


def test_api_is_read_only_authenticated_and_503_while_magpie_is_down(root, client):
    from hermes_cli import web_server
    assert TestClient(web_server.app).get("/api/lina/models").status_code == 401
    assert client.post("/api/lina/models").status_code == 405
    root["yes"] = False
    response = client.get("/api/lina/models/accounts")
    assert response.status_code == 503 and "magpie is not reachable" in response.json()["detail"]
