"""``ocx.external`` (lina#232): ``lina models`` and the client API read an OCX run outside LINA, read-only.

The OCX is a fake CLI that prints ``tests/fixtures/ocx_external``: synthetic data in the exact shape of OCX 2.97.0's
``--json`` output (key names, nesting, nulls, ms vs s epochs), with invented values around one fixed time. It logs every
argv it is run with, so a test sees exactly what reached it. The per-machine usage file is answered for every
``--api-key-id``.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

import pytest
from fastapi.testclient import TestClient

from hermes_cli import lina_board_quota, lina_models, lina_ocx, lina_ocx_takeover_refresh, lina_services
from hermes_cli.subcommands.models import build_models_parser
from tests.hermes_cli.test_lina_board_api import _valid

pytestmark = pytest.mark.platforms("posix")

FIXTURES = Path(__file__).resolve().parents[1] / "fixtures" / "ocx_external"
VIEW_FIXTURES = {"models live --json": "models-live.json", "account list --json": "account-list.json",
                 "account list --quota --json": "account-list-quota.json", "status --json": "status.json",
                 "link status --json": "link-status.json", "access key list --json": "access-key-list.json",
                 "usage --range today --json": "usage-today.json", "usage --range 7d --json": "usage-7d.json",
                 "usage --range 30d --json": "usage-30d.json"}
FIXTURE_AT = 1700000000  # the fixed time the synthetic fixtures are built around

_FAKE_OCX = '''#!{python}
import json, os, sys
args = sys.argv[1:]
with open({log!r}, "a") as fh:
    fh.write(json.dumps({{"argv": args, "home": os.environ.get("OPENCODEX_HOME"),
                         "caller": os.environ.get("LINA_TEST_CALLER")}}) + "\\n")
name = {fixtures!r}.get(" ".join(args))
if len(args) == 6 and args[:2] == ["usage", "--range"] and args[3] == "--api-key-id":
    name = "usage-7d-one-machine-key.json"
if name is None:
    print("unknown command: " + " ".join(args), file=sys.stderr)
    sys.exit(2)
data = json.load(open(os.path.join({fixture_dir!r}, name)))
if name == "status.json" and os.environ.get("LINA_TEST_OCX_DOWN"):
    data["proxy"]["running"] = False
print(json.dumps(data))
'''


def _fixture(name):
    return json.loads((FIXTURES / name).read_text(encoding="utf-8"))


class Ocx:
    def __init__(self, tmp_path: Path):
        self.log = tmp_path / "calls.jsonl"
        self.cli = tmp_path / "bin" / "ocx"
        self.data = tmp_path / "ocx-data"
        self.cli.parent.mkdir()
        self.data.mkdir()
        self.cli.write_text(_FAKE_OCX.format(python=sys.executable, log=str(self.log), fixtures=VIEW_FIXTURES,
                                             fixture_dir=str(FIXTURES)), encoding="utf-8")
        self.cli.chmod(0o755)

    def calls(self) -> list[dict]:
        return [json.loads(line) for line in self.log.read_text().splitlines()] if self.log.exists() else []

    def argvs(self) -> list[str]:
        return [" ".join(call["argv"]) for call in self.calls()]


@pytest.fixture
def ocx(tmp_path, monkeypatch):
    fake = Ocx(tmp_path)
    root = tmp_path / "lina"
    root.mkdir()
    (root / "config.yaml").write_text(f"ocx:\n  external:\n    cli: {fake.cli}\n    data_dir: {fake.data}\n",
                                      encoding="utf-8")
    monkeypatch.setattr(lina_ocx, "lina_root", lambda: root)
    monkeypatch.setattr(lina_ocx, "own_proxy_running", lambda *a: pytest.fail("the own-proxy check ran"))
    monkeypatch.setattr(lina_models, "_CACHE", {})
    monkeypatch.setenv("LINA_TEST_CALLER", "kept")
    fake.root = root
    return fake


@pytest.fixture
def client():
    from hermes_cli import web_server
    test_client = TestClient(web_server.app)
    test_client.headers[web_server._SESSION_HEADER_NAME] = web_server._SESSION_TOKEN
    return test_client


def _models(*argv) -> int:
    parser = argparse.ArgumentParser()
    build_models_parser(parser.add_subparsers())
    args = parser.parse_args(["models", *argv])
    with pytest.raises(SystemExit) as exc:
        args.func(args)
    return exc.value.code


@pytest.mark.parametrize(("argv", "fixture", "ran"), [
    (["--json"], "models-live.json", "models live --json"),
    (["accounts", "--json"], "account-list.json", "account list --json"),
    (["quota", "--json"], "account-list-quota.json", "account list --quota --json"),
    (["status", "--json"], "status.json", "status --json"),
    (["usage", "--json"], "usage-30d.json", "usage --range 30d --json")])
def test_lina_models_reads_the_external_cli_with_its_data_dir(ocx, capsys, argv, fixture, ran):
    assert _models(*argv) == 0
    assert json.loads(capsys.readouterr().out) == _fixture(fixture)
    assert ocx.argvs()[-1] == ran
    assert {(call["home"], call["caller"]) for call in ocx.calls()} == {(str(ocx.data), "kept")}


@pytest.mark.parametrize("argv", [("account", "list", "--quota", "--refresh", "--json"), ("account", "refresh", "openai"),
                                  ("account", "switch", "openai", "--id", "x"), ("account", "login", "openai"),
                                  ("start", "--port", "10100"), ("stop",), ("usage", "--range", "1y", "--json"),
                                  ("usage", "--range", "7d", "--api-key-id", "--refresh", "--json"),
                                  ("link", "remove", "machine-a"), ("models", "disable", "gpt-x")])
def test_an_argv_outside_the_read_list_never_reaches_the_cli(ocx, argv):
    with pytest.raises(lina_ocx.OcxError, match="read-only"):
        lina_models.run(argv)
    assert ocx.calls() == []


@pytest.mark.parametrize("argv", [["install"], ["upgrade"], ["rollback"], ["refresh"], ["takeover", "plan", "--from", "x"],
                                  ["takeover", "switch", "--from", "x"]])
def test_write_verbs_of_lina_models_refuse_without_spawning(ocx, capsys, monkeypatch, argv):
    for name in ("run", "Popen", "call", "check_call", "check_output"):
        monkeypatch.setattr(f"subprocess.{name}", lambda *a, **kw: pytest.fail("a process was spawned"))
    assert _models(*argv) == 1
    out = capsys.readouterr().out.strip().splitlines()
    assert len(out) == 1 and "ocx.external" in out[0] and f"lina models {argv[0]}" in out[0]


@pytest.mark.parametrize("verb", ["start", "stop", "restart"])
def test_lina_services_refuses_ocx_verbs_without_spawning(ocx, capsys, monkeypatch, verb):
    for name in ("run", "Popen"):
        monkeypatch.setattr(f"subprocess.{name}", lambda *a, **kw: pytest.fail("a process was spawned"))
    assert lina_services.act(verb, "ocx") == 1
    assert capsys.readouterr().out.strip() == (f"OCX: `lina services {verb} ocx` is refused: ocx.external is set, "
                                               "so LINA only reads the OCX run outside it")


def test_each_argv_is_read_once_a_minute(ocx, monkeypatch):
    clock = [1000.0]
    monkeypatch.setattr(lina_models, "_clock", lambda: clock[0])
    for _ in range(3):
        lina_models.query("accounts")
        lina_models.usage("7d")
    assert ocx.argvs().count("status --json") == 1
    assert ocx.argvs().count("account list --json") == 1
    assert ocx.argvs().count("usage --range 7d --json") == 1
    clock[0] += 61
    lina_models.query("accounts")
    assert ocx.argvs().count("status --json") == ocx.argvs().count("account list --json") == 2


def test_a_read_fails_while_the_external_proxy_is_not_running(ocx, monkeypatch):
    monkeypatch.setenv("LINA_TEST_OCX_DOWN", "1")
    with pytest.raises(lina_ocx.OcxError, match="not running"):
        lina_models.query("accounts")
    assert ocx.argvs() == ["status --json"]


def test_usage_serves_model_provider_account_and_machine_and_no_agent(ocx, client):
    response = client.get("/api/lina/usage?range=7d")
    assert response.status_code == 200
    usage = _valid("UsageView", response.json())
    fixture = _fixture("usage-7d.json")
    assert (usage["requests"], usage["tokens"]["total"], usage["costUsd"]) == (300, 135000, 18.0)
    assert [row["costUsd"] for row in usage["byModel"]] == [2.25, None, 6.75, 9.0]  # OCX leaves unpriced rows out
    assert [(row["model"], row["provider"], row["tokens"]["cacheRead"]) for row in usage["byModel"]] == [
        (row["model"], row["provider"], row["cacheReadInputTokens"]) for row in fixture["models"]]
    assert [row["provider"] for row in usage["byProvider"]] == [row["provider"] for row in fixture["providers"]]
    assert [row["account"] for row in usage["byAccount"]] == [row["accountLogLabel"] for row in fixture["accounts"]]
    keys = _fixture("access-key-list.json")["keys"]
    assert [row["machine"] for row in usage["byMachine"]] == ["machine-a", "machine-b", "machine-c", "machine-d"]
    assert [row["tokens"]["total"] for row in usage["byMachine"]] == [45000] * 4  # the client-<n> keys are no machines
    machine_calls = [argv for argv in ocx.argvs() if "--api-key-id" in argv]
    assert sorted(machine_calls) == sorted(f"usage --range 7d --api-key-id {key['id']} --json" for key in keys
                                           if key["name"].startswith("link:"))
    assert "agent" not in json.dumps(usage).lower()

    calls = len(ocx.calls())
    again = client.get("/api/lina/usage?range=7d", headers={"If-None-Match": response.headers["etag"]})
    assert again.status_code == 304 and len(ocx.calls()) == calls
    assert client.get("/api/lina/usage?range=1y").status_code == 422
    today = _valid("UsageView", client.get("/api/lina/usage").json())
    assert today["range"] == "today" and "usage --range today --json" in ocx.argvs()


def test_quota_reads_the_cached_quota_and_names_state_left_and_missing(ocx, client):
    view = _valid("QuotaView", client.get("/api/lina/quota").json())
    assert view["errors"] == [] and view["missing"] == ["Grok", "Antigravity"]
    assert "account list --quota --json" in ocx.argvs()
    assert not any("--refresh" in argv for argv in ocx.argvs())
    services = {service["service"]: service for service in view["quota"]}
    assert [len(services[name]["accounts"]) for name in ("OpenAI", "Claude")] == [3, 2]
    assert [acct["windows"] for acct in services["Gemini"]["accounts"]] == [[]]  # the google API key
    assert [(acct["windows"], acct["state"]) for acct in services["Cursor"]["accounts"]] == [([], "reauth")]
    assert [(acct["active"], acct["state"]) for acct in services["Claude"]["accounts"]] == [(True, "ok"), (False, "ok")]
    weekly = next(w for w in services["Claude"]["accounts"][0]["windows"] if w["key"] == "weekly")
    assert (weekly["percent"], weekly["left"]) == (40, 60)
    assert "@" not in json.dumps(view)


def test_quota_state_left_and_missing_rules():
    accounts = _fixture("account-list-quota.json")["accounts"]
    assert lina_board_quota.missing([a for a in accounts if a["provider"] not in ("cursor", "google")]) == [
        "Grok", "Gemini", "Antigravity", "Cursor"]
    paused = dict(accounts[0], paused=True, quota={"weeklyPercent": 120})
    (service,) = lina_board_quota.quota_view([paused], [], 0)
    account = service["accounts"][0]
    assert (account["state"], account["windows"][0]["left"]) == ("paused", 0)
    assert lina_board_quota.service_of("google") == "Gemini"
    assert lina_board_quota.service_of("google-antigravity") == "Antigravity"


def test_models_state_counts_models_and_tells_each_link_s_freshness(ocx, client):
    state = lina_models.models_state(now=FIXTURE_AT)
    assert (state["enabled"], state["disabled"], len(state["enabledModels"])) == (8, 2, 8)
    assert [(m["machine"], m["state"], m["ok"]) for m in state["machines"]] == [
        ("machine-a", "connected", True), ("machine-b", "connected", True), ("machine-c", "failed", False),
        ("machine-d", "connected", False)]  # machine-d last reported an hour ago
    assert not any(m["ok"] for m in lina_models.models_state(now=FIXTURE_AT + 700)["machines"])

    response = client.get("/api/lina/models/state")
    assert response.status_code == 200
    _valid("ModelsState", response.json())
    again = client.get("/api/lina/models/state", headers={"If-None-Match": response.headers["etag"]})
    assert again.status_code == 304


def test_api_answers_503_while_the_external_proxy_is_down(ocx, client, monkeypatch):
    monkeypatch.setenv("LINA_TEST_OCX_DOWN", "1")
    for path in ("/api/lina/usage", "/api/lina/models/state", "/api/lina/models/accounts"):
        response = client.get(path)
        assert response.status_code == 503 and "not running" in response.json()["detail"], path


def test_the_credit_refresh_cron_entry_stays_quiet_with_an_external_ocx(ocx, monkeypatch):
    monkeypatch.setattr("subprocess.run", lambda *a, **kw: pytest.fail("a process was spawned"))
    lines: list[str] = []
    assert lina_ocx_takeover_refresh.refresh(root=ocx.root, out=lines.append) == 0
    assert lines == [] and ocx.calls() == []

