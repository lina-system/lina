"""``ocx.external`` (lina#232): ``lina models`` and the client API read an OCX run outside LINA, read-only.

The OCX is a fake CLI that prints ``tests/fixtures/ocx_external``: synthetic data in the exact shape of OCX 2.97.0's
``--json`` output (key names, nesting, nulls, ms vs s epochs), with invented values around one fixed time. It logs every
argv it is run with, so a test sees exactly what reached it. The per-machine usage files are answered for every
``--api-key-id``; with ``LINA_TEST_HOLD`` such a read waits until that file exists, with ``LINA_TEST_KEY_FAIL`` it
fails. The machine rows come from a background fan-out of 30-day reads (lina#273).
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
from tests.waiting import wait_until

pytestmark = pytest.mark.platforms("posix")

FIXTURES = Path(__file__).resolve().parents[1] / "fixtures" / "ocx_external"
VIEW_FIXTURES = {"models live --json": "models-live.json", "account list --json": "account-list.json",
                 "account list --quota --json": "account-list-quota.json", "status --json": "status.json",
                 "link status --json": "link-status.json", "access key list --json": "access-key-list.json",
                 "usage --range today --json": "usage-today.json", "usage --range 7d --json": "usage-7d.json",
                 "usage --range 30d --json": "usage-30d.json"}
FIXTURE_AT = 1700000000  # the fixed time the synthetic fixtures are built around

_FAKE_OCX = '''#!{python}
import json, os, sys, time
args = sys.argv[1:]
with open({log!r}, "a") as fh:
    fh.write(json.dumps({{"argv": args, "home": os.environ.get("OPENCODEX_HOME"),
                         "caller": os.environ.get("LINA_TEST_CALLER")}}) + "\\n")
name = {fixtures!r}.get(" ".join(args))
if len(args) == 6 and args[:2] == ["usage", "--range"] and args[3] == "--api-key-id":
    name = "usage-30d-one-machine-key.json" if args[2] == "30d" else "usage-7d-one-machine-key.json"
    hold = os.environ.get("LINA_TEST_HOLD")
    deadline = time.monotonic() + 30  # a test that never releases fails instead of hanging
    while hold and not os.path.exists(hold):
        if time.monotonic() > deadline:
            sys.exit(3)
        time.sleep(0.01)
    if os.environ.get("LINA_TEST_KEY_FAIL"):
        print("usage aggregate failed", file=sys.stderr)
        sys.exit(1)
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
    monkeypatch.setattr(lina_models, "_MACHINES", {})
    monkeypatch.setenv("LINA_TEST_CALLER", "kept")
    fake.root = root
    yield fake
    lina_models._join(root, timeout=60)  # no fan-out outlives its test


def _machine_keys() -> list[dict]:
    return [key for key in _fixture("access-key-list.json")["keys"] if key["name"].startswith("link:")]


def _expected_machine_row(range_: str) -> dict:
    """The per-key 30-day fixture's buckets summed over the dates of the range's unfiltered fixture."""
    dates = {day["date"] for day in _fixture(f"usage-{range_}.json")["days"]}
    days = [day for day in _fixture("usage-30d-one-machine-key.json")["days"] if day["date"] in dates]
    models = [model for day in days for model in day["models"]]
    costs = [day["estimatedCostUsd"] for day in days if "estimatedCostUsd" in day]
    return {"requests": sum(day["requests"] for day in days),
            "tokens": {"total": sum(day["totalTokens"] for day in days),
                       "input": sum(model["inputTokens"] for model in models),
                       "output": sum(model["outputTokens"] for model in models),
                       "cacheRead": sum(model["cacheReadInputTokens"] for model in models)},
            "costUsd": sum(costs) if costs else None}


def _key_reads(ocx) -> list[str]:
    return [argv for argv in ocx.argvs() if "--api-key-id" in argv]


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


@pytest.mark.parametrize("key_id", ["00000000-0000-4000-8000-000000000001", "Abc123"])
def test_an_access_key_id_of_ocx_s_real_shape_is_read(ocx, key_id):
    argv = ("usage", "--range", "7d", "--api-key-id", key_id, "--json")
    lina_models.run(argv)
    assert ocx.calls()[-1]["argv"] == list(argv)  # the id stays one argument: no split, no extra flag


@pytest.mark.parametrize("key_id", ["-x", "--refresh", "a b", "a=b", "", "a" * 65])
def test_an_access_key_id_that_could_be_another_argument_never_reaches_the_cli(ocx, key_id):
    with pytest.raises(lina_ocx.OcxError, match="read-only"):
        lina_models.run(("usage", "--range", "7d", "--api-key-id", key_id, "--json"))
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
    lina_models._join()
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
    assert _valid("UsageView", client.get("/api/lina/usage?range=7d").json())["byMachine"] == []  # none finished
    lina_models._join()
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
    assert [row["machine"] for row in usage["byMachine"]] == ["machine-a", "machine-b", "machine-c", "machine-d"]
    assert [row["tokens"]["total"] for row in usage["byMachine"]] == [16000] * 4  # the client-<n> keys are no machines
    assert sorted(_key_reads(ocx)) == sorted(f"usage --range 30d --api-key-id {key['id']} --json"
                                             for key in _machine_keys())
    assert "agent" not in json.dumps(usage).lower()

    calls = len(ocx.calls())
    again = client.get("/api/lina/usage?range=7d", headers={"If-None-Match": response.headers["etag"]})
    assert again.status_code == 304 and len(ocx.calls()) == calls
    assert client.get("/api/lina/usage?range=1y").status_code == 422
    today = _valid("UsageView", client.get("/api/lina/usage").json())
    assert today["range"] == "today" and "usage --range today --json" in ocx.argvs()


def test_machine_rows_are_summed_from_one_30_day_read_over_each_range_s_dates(ocx):
    lina_models.usage("today")
    lina_models._join()
    per_key = _fixture("usage-30d-one-machine-key.json")["summary"]
    for range_ in lina_models.RANGES:
        rows = lina_models.usage(range_)["byMachine"]
        assert [row["machine"] for row in rows] == [key["name"][5:] for key in _machine_keys()]
        expected = _expected_machine_row(range_)
        assert [{k: v for k, v in row.items() if k != "machine"} for row in rows] == [expected] * len(rows), range_
    assert _expected_machine_row("30d") == lina_models._totals(per_key)  # the 30-day row is the read's summary
    today, week = _expected_machine_row("today"), _expected_machine_row("7d")
    assert today["costUsd"] is None and 0 < today["requests"] < week["requests"] < per_key["requests"]
    assert {argv.split(" --api-key-id")[0] for argv in _key_reads(ocx)} == {"usage --range 30d"}
    assert len(_key_reads(ocx)) == len(_machine_keys())


def test_app_polling_reads_each_machine_key_once_per_15_minutes_and_idle_reads_none(ocx, client, monkeypatch):
    clock = [10_000.0]
    monkeypatch.setattr(lina_models, "_clock", lambda: clock[0])
    spawns: list[tuple[float, str]] = []
    for minute in range(60):
        clock[0] = 10_000.0 + 60 * minute
        seen = len(_key_reads(ocx))
        for range_ in lina_models.RANGES:
            assert client.get(f"/api/lina/usage?range={range_}").status_code == 200
        lina_models._join()
        spawns += [(clock[0], argv) for argv in _key_reads(ocx)[seen:]]
    times = [at for at, _ in spawns]
    keys = len(_machine_keys())
    assert max(sum(start <= at < start + 900 for at in times) for start in times) <= min(keys, 12)
    assert max(sum(start <= at < start + 600 for at in times) for start in times) <= 36
    assert all(argv.startswith("usage --range 30d --api-key-id ") for _, argv in spawns)
    assert sorted(set(times)) == [10_000.0 + 900 * n for n in range(4)] and len(spawns) == 4 * keys

    seen = len(ocx.calls())
    for minute in range(30):  # the screen is closed: no reader, no fan-out
        clock[0] += 60
        lina_models._join()
    assert len(ocx.calls()) == seen


def test_a_usage_read_never_waits_for_the_machine_fan_out(ocx, client, monkeypatch, tmp_path):
    clock = [10_000.0]
    monkeypatch.setattr(lina_models, "_clock", lambda: clock[0])
    release = tmp_path / "release"
    monkeypatch.setenv("LINA_TEST_HOLD", str(release))

    def read_all() -> list[list[dict]]:
        views = []
        for range_ in lina_models.RANGES:
            response = client.get(f"/api/lina/usage?range={range_}")
            assert response.status_code == 200, range_
            views.append(_valid("UsageView", response.json())["byMachine"])
        return views

    def held(count: int) -> None:  # the fan-out sits in its first key's read, which the fake CLI holds
        wait_until(lambda: len(_key_reads(ocx)) == count, what="the held per-key read")

    try:
        assert read_all() == [[], [], []]
        held(1)
        assert read_all() == [[], [], []]  # answered while the per-key read is still held
        assert len(_key_reads(ocx)) == 1 and ocx.root in lina_models._MACHINES
        release.touch()
        lina_models._join()
        rows = read_all()
        assert [len(view) for view in rows] == [len(_machine_keys())] * 3

        release.unlink()
        clock[0] += 900
        read_all()  # starts the next fan-out
        held(len(_machine_keys()) + 1)
        assert read_all() == rows  # the last finished fan-out's rows while the next one is held
        assert len(_key_reads(ocx)) == len(_machine_keys()) + 1
    finally:
        release.touch()
        lina_models._join(timeout=60)
    assert read_all() == rows


def test_a_failed_fan_out_keeps_the_rows_and_the_next_waits_15_minutes(ocx, monkeypatch, caplog):
    clock = [10_000.0]
    monkeypatch.setattr(lina_models, "_clock", lambda: clock[0])
    lina_models.usage("7d")
    lina_models._join()
    rows = lina_models.usage("7d")["byMachine"]
    assert len(rows) == len(_machine_keys())

    monkeypatch.setenv("LINA_TEST_KEY_FAIL", "1")
    clock[0] += 900
    lina_models.usage("7d")
    lina_models._join()
    failed = len(_key_reads(ocx))
    assert failed == len(_machine_keys()) + 1  # the fan-out stopped at its first failing read
    assert lina_models.usage("7d")["byMachine"] == rows
    (record,) = [r for r in caplog.records if r.name == "hermes_cli.lina_models"]
    assert not any(key["id"] in record.getMessage() for key in _machine_keys())

    monkeypatch.delenv("LINA_TEST_KEY_FAIL")
    clock[0] += 899
    lina_models.usage("7d")
    lina_models._join()
    assert len(_key_reads(ocx)) == failed  # no retry before 15 minutes
    clock[0] += 1
    lina_models.usage("7d")
    lina_models._join()
    assert len(_key_reads(ocx)) == failed + len(_machine_keys())
    assert lina_models.usage("7d")["byMachine"] == rows


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

