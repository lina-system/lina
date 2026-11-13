"""``GET /api/lina/board``, ``/work/due``, ``/devices`` and ``/quota`` (lina#39) over a scratch LINA root: a work
record in the sqlite store, hub job files of ``lina worker``, a fake device probe and a fake magpie account read.

Ported from the board's ``tests/test_board_api.py``: the sidebar-count test is rewritten as "every count is the length
of the list it labels" (the sidebar HTML is not served); the reply-shape test is rewritten as validation against
``docs/api/board-client.yaml`` (``ai_summary`` is dropped: the LLM text cache is not served).
"""

from __future__ import annotations

import datetime
import json
import sqlite3
from pathlib import Path

import jsonschema
import pytest
from fastapi.testclient import TestClient

import hermes_yaml
from hermes_cli import lina_deps
from tests.hermes_cli._work_env import NOW, WorkEnv

pytestmark = pytest.mark.platforms("posix")

REPO = Path(__file__).resolve().parents[2]
ROUTES = {"/api/lina/board": "BoardSnapshot", "/api/lina/work/due": "DueView", "/api/lina/devices": "DevicesView",
          "/api/lina/quota": "QuotaView"}
DEVICES = "devices:\n  workers:\n    build-a: {host: build-a, cap: 2}\n    here: {host: local, cap: 1}\n"
READY = {"runner": "omo", "ready": True, "serve": True, "running": 1, "cores": 8, "load": 0.5, "free_gb": 100,
         "repo": True}


def _openapi_to_jsonschema(node):
    """OpenAPI 3.0 ``nullable: true`` -> JSON Schema ``type: [t, "null"]``."""
    if isinstance(node, list):
        return [_openapi_to_jsonschema(n) for n in node]
    if not isinstance(node, dict):
        return node
    out = {k: _openapi_to_jsonschema(v) for k, v in node.items() if k != "nullable"}
    if node.get("nullable") and "type" in out:
        out["type"] = [out["type"], "null"]
    return out


_COMPONENTS = _openapi_to_jsonschema(
    hermes_yaml.safe_load((REPO / "docs/api/board-client.yaml").read_text(encoding="utf-8"))["components"])


def _valid(schema_name: str, data):
    jsonschema.Draft7Validator({"$ref": f"#/components/schemas/{schema_name}", "components": _COMPONENTS}).validate(data)
    return data


class Fakes:
    """What the board reads beyond the record: hub jobs and receipts, the live view per run, probes, magpie accounts."""

    def __init__(self, work: WorkEnv, monkeypatch):
        self.work = work
        self.hub = work.root / "worker" / "hub"
        (self.hub / "jobs").mkdir(parents=True)
        self.live: dict[str, dict] = {}
        self.probes = [{"error": "ssh: connect timed out"}, dict(READY)]
        self.accounts: list[dict] = []
        self.magpie_error: str | None = None
        monkeypatch.setattr(lina_deps, "lina_root", lambda: work.root)
        from hermes_cli.worker import hub
        monkeypatch.setattr(hub.Hub, "meta", lambda _hub, job, live=False: self.live.get(job["id"], {"state": "running"}))
        from hermes_cli import lina_board_devices, lina_devices
        names = ["build-a", "here"]
        monkeypatch.setattr(lina_board_devices, "for_device",
                            lambda dev, root: _Contact(lambda: self.probes[names.index(dev.name)]))
        monkeypatch.setattr(lina_devices, "omo_status", lambda dev: {"current": None})
        monkeypatch.setattr(lina_board_devices, "_spawn", lambda work: work())
        lina_board_devices.OMO.reset()
        lina_board_devices.PERSON.reset()
        from hermes_cli import lina_magpie
        monkeypatch.setattr(lina_magpie, "quota_accounts", self._query)

    def _query(self, proxy):
        if self.magpie_error:
            from hermes_cli.lina_magpie import MagpieError
            raise MagpieError(self.magpie_error)
        return [dict(a) for a in self.accounts]

    def job(self, tid: str, *, agent: str = "alpha", state: str = "running", heading: str = "", ended: float | None = None,
            created: float = NOW - 3600, **extra) -> None:
        job = {"id": tid, "bot": agent, "topic": f"topic-{tid}", "mode": "ulw", "state": state, "created": created,
               "started": created + 60 if state != "queued" else None, "host": "here" if state != "queued" else None,
               "prompt": f"ulw # {heading}\nbody\n" if heading else "ulw do it\n", "verdict": None, "card": None,
               "todo": None, **extra}
        (self.hub / "jobs" / f"{tid}.json").write_text(json.dumps(job), encoding="utf-8")
        if ended is not None:
            with (self.hub / "receipts.jsonl").open("a", encoding="utf-8") as fh:
                fh.write(json.dumps({"at": "", "ts": ended, "task": tid, "kind": "exec_end", "state": state}) + "\n")


class _Contact:
    """A node's negotiated contact: ``hello`` (no runner rows) and then ``probe``; a probe with an ``error`` is a
    node ssh could not reach."""

    def __init__(self, probe):
        self.probe, self.descriptor = probe, None

    def call(self, verb, stdin=b"", timeout=120):
        from hermes_cli.worker.transport import Reply
        probe = self.probe()
        if "error" in probe:
            return Reply(255, b"", probe["error"])
        self.descriptor = {"protocol": {"min": 1, "max": 1}, "capabilities": {"runners": []}}
        return Reply(0, json.dumps(probe).encode())


@pytest.fixture
def work(tmp_path, monkeypatch):
    env = WorkEnv(tmp_path, monkeypatch)
    env.config(extra=DEVICES)
    return env


@pytest.fixture
def fakes(work, monkeypatch):
    return Fakes(work, monkeypatch)


@pytest.fixture
def client():
    from hermes_cli import web_server
    test_client = TestClient(web_server.app)
    test_client.headers[web_server._SESSION_HEADER_NAME] = web_server._SESSION_TOKEN
    return test_client


def _today(offset: int = 0) -> str:
    """The owner's calendar day at the fixture clock with no ``work.timezone`` set: the host's zone, which is what
    the board reads by default (``work.notify.owner_today``), not UTC (lina#512)."""
    return (datetime.datetime.fromtimestamp(NOW).astimezone().date()
            + datetime.timedelta(days=offset)).isoformat()


def _record(work: WorkEnv) -> None:
    """G1 (alpha) with T1 due today and T2; the owner's T3 (overdue) and T4 (waiting); a decision on T1 and an
    answered one on G1."""
    work.goal_and_todos(todos=2)
    work.ok("edit", "T1", "--due", _today())
    for title, due in (("백업 설정 확인", _today(-3)), ("회의 자료 보기", "")):
        work.ok("add", "할 일", "--by", "owner", "--from", "alpha", "--title", title, "--why", "기록용",
                "--done", "확인했다", *(["--due", due] if due else []))
    work.ok("wait", "T4", "--on", "회의가 잡히면", "--hold")
    work.ok("wait", "T1", "--decide", "색을 고를까?")
    work.ok("wait", "G1", "--decide", "범위를 줄일까?")
    work.ok("comment", "G1", "줄이자", "--answer")


def _jobs(fakes: Fakes) -> None:
    fakes.job("OT-ALP0001", heading="기기 점검 스크립트")
    fakes.live["OT-ALP0001"] = {"state": "running", "live": {"dash": "working", "last": "running the **tests** now"}}
    fakes.job("OT-ALP0002", state="done", ended=NOW - 600)
    fakes.job("OT-ALP0003", state="failed", ended=NOW - 900)
    fakes.job("OT-ALP0004", state="done", verdict="accepted", ended=NOW - 3600)
    fakes.job("OT-ALP0005", card={"task": "t_card"})
    fakes.job("OT-ALP0006", state="done", verdict="accepted", created=NOW - 5 * 86400, ended=NOW - 4 * 86400)
    fakes.job("OT-BET0007", agent="beta", state="queued")
    fakes.job("OT-BET0008", agent="beta")
    fakes.live["OT-BET0008"] = {"state": "running", "live": {"dash": "human", "why": "asked which colour"}}


def _account(**quota) -> dict:
    return {"provider": "openai", "type": "codex", "id": "someone@example.com", "plan": "pro", "paused": False,
            "quota": quota}


def test_board_serves_every_part_with_counts_equal_to_its_lists(work, fakes, client):
    _record(work)
    _jobs(fakes)
    fakes.accounts = [_account(weeklyPercent=40, weeklyResetAt=(NOW + 3 * 86400) * 1000), {"provider": "anthropic",
                                                                                          "id": "k", "quota": {}}]

    board = _valid("BoardSnapshot", client.get("/api/lina/board").json())

    assert board["errors"] == []
    cards = {card["id"]: (card["state"], card["reason"]) for card in board["cards"]}
    assert cards == {"OT-ALP0001": ("in_progress", None), "OT-ALP0002": ("waiting", "review"),
                     "OT-ALP0003": ("stopped", "failed"), "OT-ALP0004": ("done", None),
                     "OT-BET0007": ("planned", "queued"), "OT-BET0008": ("waiting", "decision")}
    first = next(card for card in board["cards"] if card["id"] == "OT-ALP0001")
    assert (first["title"], first["now"]) == ("기기 점검 스크립트", "running the tests now")
    assert [row["target"] for row in board["inbox"]["decide"]] == ["T1", "G1"]
    assert [row["answered"] for row in board["inbox"]["decide"]] == [False, True]
    assert [(row["id"], row["waiting"]) for row in board["inbox"]["mine"]] == [("T3", False), ("T4", True)]
    assert board["inbox"]["review"] == ["OT-BET0008"]  # OT-ALP0002 waits on alpha's verdict (lina#295)
    assert {name: [row["id"] for row in rows] for name, rows in board["due"].items()} == {
        "overdue": ["T3"], "today": ["T1"], "week": [], "later": []}
    assert [(m["name"], m["state"], m["why"]) for m in board["machines"]] == [("build-a", "down", "unreachable"),
                                                                              ("here", "ok", None)]
    assert [(s["service"], s["percent"], [a["number"] for a in s["accounts"]]) for s in board["quota"]] == [("OpenAI", 40.0,
                                                                                                      [1]), ("Claude", None, [1])]
    assert "someone@example.com" not in json.dumps(board)

    counts, inbox = board["counts"], board["inbox"]
    assert counts["inbox"] == len(inbox["decide"]) + len(inbox["review"]) + len(inbox["mine"]) == 5
    assert counts["running"] == sum(card["state"] == "in_progress" for card in board["cards"]) == 1
    assert counts["stopped"] == sum(card["state"] == "stopped" for card in board["cards"]) == 1
    assert counts["done"] == sum(card["state"] == "done" for card in board["cards"]) == 1
    assert counts["due"] == sum(len(rows) for rows in board["due"].values()) == 2
    assert counts["agents"] == {"alpha": 2, "beta": 0}
    assert sorted(note["kind"] for note in board["notices"]) == ["ask", "ask", "review", "todo", "todo"]
    assert any("결정 필요: 색을 고를까?" in note["line"] for note in board["notices"])


def test_a_run_waiting_on_an_agent_s_verdict_is_that_agent_s_not_the_owner_s(work, fakes, client):
    """lina#295: a finished run with no verdict is the agent's to judge; a run asking a question is the owner's."""
    _record(work)
    fakes.job("OT-ALP0002", state="done", ended=NOW - 600)
    fakes.job("OT-BET0008", agent="beta")
    fakes.live["OT-BET0008"] = {"state": "running", "live": {"dash": "human", "why": "asked which colour"}}

    board = _valid("BoardSnapshot", client.get("/api/lina/board").json())

    rows = {card["id"]: card["waitingOn"] for card in board["cards"]}
    assert rows == {"OT-ALP0002": "agent:alpha", "OT-BET0008": None}
    counts, inbox = board["counts"], board["inbox"]
    assert inbox["review"] == ["OT-BET0008"]
    assert [note["target"] for note in board["notices"] if note["kind"] == "review"] == ["OT-BET0008"]
    assert counts["inbox"] == len(inbox["decide"]) + len(inbox["mine"]) + 1
    assert counts["agents"] == {"alpha": 1, "beta": 0}


@pytest.mark.parametrize("path", list(ROUTES))
def test_each_route_validates_and_answers_304_until_its_data_changes(work, fakes, client, path):
    _record(work)
    _jobs(fakes)
    fakes.accounts = [_account(weeklyPercent=40)]

    first = client.get(path)
    assert first.status_code == 200
    _valid(ROUTES[path], first.json())
    work.clock[0] += 1  # a later snapshot time alone is no change
    again = client.get(path, headers={"If-None-Match": first.headers["etag"]})
    assert again.status_code == 304 and again.headers["etag"] == first.headers["etag"]

    work.ok("edit", "T2", "--due", _today(1))
    fakes.probes[1]["load"] = 3.5
    fakes.accounts = [_account(weeklyPercent=60)]
    work.clock[0] += 600
    changed = client.get(path, headers={"If-None-Match": first.headers["etag"]})
    assert changed.status_code == 200 and changed.headers["etag"] != first.headers["etag"]


def test_a_source_that_fails_is_an_error_and_its_counts_are_null(work, fakes, client):
    (work.work / "work.db").mkdir(parents=True)  # the record cannot be opened
    (fakes.hub / "jobs" / "OT-BROKEN.json").write_text("{not json", encoding="utf-8")
    fakes.magpie_error = "LINA's magpie is not running on port 10111. Run `lina services start magpie`."

    board = _valid("BoardSnapshot", client.get("/api/lina/board").json())

    assert sorted({error["source"] for error in board["errors"]}) == ["quota", "work", "worker"]
    counts = board["counts"]
    assert {key: counts[key] for key in ("inbox", "running", "stopped", "due", "done", "agents")} == dict.fromkeys(
        ("inbox", "running", "stopped", "due", "done", "agents"))
    assert [m["name"] for m in board["machines"]] == ["build-a", "here"]  # devices still read
    due = _valid("DueView", client.get("/api/lina/work/due").json())
    assert due["count"] is None and due["errors"][0]["source"] == "work"


def test_due_groups_follow_the_owner_s_time_zone(work, fakes, client):
    """2027-01-15 (Fri) 20:00 UTC is 2027-01-16 (Sat) 05:00 in Seoul: one instant, two calendar days, and the groups
    follow ``work.timezone`` (lina#512: an unset zone is the host's, never UTC, so both cases name their zone)."""
    work.clock[0] = datetime.datetime(2027, 1, 15, 20, tzinfo=datetime.UTC).timestamp()
    work.goal_and_todos(todos=3)
    for item, due in (("T1", "2027-01-15"), ("T2", "2027-01-16"), ("T3", "2027-01-18")):
        work.ok("edit", item, "--due", due)

    def groups():
        due = _valid("DueView", client.get("/api/lina/work/due").json())
        return due["today"], {name: [row["id"] for row in rows] for name, rows in due["due"].items() if rows}
    work.config(extra="  timezone: UTC\n" + DEVICES)
    assert groups() == ("2027-01-15", {"today": ["T1"], "week": ["T2"], "later": ["T3"]})
    work.config(extra="  timezone: Asia/Seoul\n" + DEVICES)
    assert groups() == ("2027-01-16", {"overdue": ["T1"], "today": ["T2"], "later": ["T3"]})


def test_quota_rate_and_warning_follow_the_read_history(work, fakes, client):
    reset = NOW + 3 * 3600
    for step, percent in enumerate((40, 45, 50, 55)):
        work.clock[0] = NOW + step * 600
        fakes.accounts = [_account(weeklyPercent=percent, weeklyResetAt=reset, fiveHourPercent=5)]
        quota = _valid("QuotaView", client.get("/api/lina/quota").json())["quota"]
    weekly = next(w for w in quota[0]["accounts"][0]["windows"] if w["key"] == "weekly")
    assert weekly["ratePerHour"] == pytest.approx(30.0)
    assert weekly["warn"] is True and quota[0]["accounts"][0]["warn"] is True
    assert quota[0]["ratePerHour"] == pytest.approx(30.0)
    assert (work.root / "board" / "quota.jsonl").is_file()


def test_devices_show_readiness_without_metrics_and_usage_with_beszel(work, fakes, client, tmp_path):
    plain = _valid("DevicesView", client.get("/api/lina/devices").json())["machines"]
    assert [(m["name"], m["state"], m["cpu"], m["load"]) for m in plain] == [("build-a", "down", None, None),
                                                                            ("here", "ok", None, 0.5)]
    db = tmp_path / "beszel.db"
    with sqlite3.connect(db) as conn:
        conn.execute("CREATE TABLE systems (id TEXT, name TEXT, status TEXT, updated TEXT, info TEXT)")
        conn.execute("CREATE TABLE system_stats (system TEXT, stats TEXT, type TEXT, created TEXT)")
        stamp = datetime.datetime.fromtimestamp(NOW - 30, datetime.UTC).strftime("%Y-%m-%d %H:%M:%S.000Z")
        conn.execute("INSERT INTO systems VALUES ('s1', 'here', 'up', ?, ?)",
                     (stamp, json.dumps({"cpu": 12.5, "mp": 95.0, "dp": 40.0, "dt": 50})))
    work.config(extra=DEVICES + f"  metrics: {{beszel: {db}}}\n")
    here = next(m for m in _valid("DevicesView", client.get("/api/lina/devices").json())["machines"]
                if m["name"] == "here")
    assert (here["state"], here["why"], here["cpu"], here["memory"], here["metricsAt"]) == ("warn", "memory", 12.5,
                                                                                           95.0, NOW - 30)


def test_no_five_hour_window_is_collected_served_or_warned_on(work, fakes, client):
    """lina#654: an account near the end of its five-hour window and low on its weekly one raises no warning, and
    neither the read log nor the reply holds a five-hour window, magpie's own (``5 hours``) or a family's."""
    reset = NOW + 3600
    for step, five in enumerate((60, 75, 90, 99)):
        work.clock[0] = NOW + step * 600
        fakes.accounts = [_account(weeklyPercent=10, weeklyResetAt=NOW + 5 * 86400, fiveHourPercent=five,
                                   fiveHourResetAt=reset,
                                   customWindows=[{"label": "Gemini · 5 hours", "percent": five, "resetAt": reset}])]
        quota = _valid("QuotaView", client.get("/api/lina/quota").json())["quota"]

    account = quota[0]["accounts"][0]
    assert [w["key"] for w in account["windows"]] == ["weekly"]
    assert account["warn"] is False and quota[0]["percent"] == 10.0
    logged = [json.loads(line) for line in (work.root / "board" / "quota.jsonl").read_text().splitlines()]
    assert {row["window"] for row in logged} == {"weekly"}


def test_every_service_has_a_total_from_its_main_window_never_a_five_hour_one(work, fakes, client):
    """lina#662: the totals the old board shows on 11-13 from magpie's cards: Antigravity by its Gemini 7-day
    window, Cursor by its month's Total; OpenAI, Claude and Grok by their weekly window."""
    from hermes_cli import lina_magpie
    cards = [
        {"provider": "codex", "name": "a", "windows": [{"name": "5 hours", "used": 70}, {"name": "7 days", "used": 4}]},
        {"provider": "claude", "name": "b", "windows": [{"name": "7 days", "used": 49.4},
                                                      {"name": "7 days · Fable", "used": 5}]},
        {"provider": "grok", "name": "c", "windows": [{"name": "7 days", "used": 0}]},
        {"provider": "antigravity", "name": "d", "windows": [
            {"name": "Gemini · 5 hours", "used": 90}, {"name": "Gemini · 7 days", "used": 47.7},
            {"name": "Claude & GPT · 7 days", "used": 12}]},
        {"provider": "cursor", "name": "e", "windows": [{"name": "Total", "used": 0}, {"name": "Cursor Models", "used": 0},
                                                      {"name": "Other Models", "used": 3}]},
    ]
    fakes.accounts = [lina_magpie.account(card) for card in cards]

    quota = _valid("QuotaView", client.get("/api/lina/quota").json())["quota"]

    assert {s["service"]: (s["percent"], s["accounts"][0]["mainWindow"]) for s in quota} == {
        "OpenAI": (4.0, "weekly"), "Claude": (49.4, "weekly"), "Grok": (0.0, "weekly"),
        "Antigravity": (47.7, "custom:Gemini · 7 days"), "Cursor": (0.0, "custom:Total")}


def test_every_route_is_behind_the_dashboard_auth_gate(fakes, client):
    from hermes_cli import web_server
    anonymous = TestClient(web_server.app)
    assert {path: anonymous.get(path).status_code for path in ROUTES} == dict.fromkeys(ROUTES, 401)
    assert {path: client.get(path).status_code for path in ROUTES} == dict.fromkeys(ROUTES, 200)


def test_run_states_are_the_work_api_s_five_and_both_schemas_agree():
    """A worker run's state is one of lina#112's five (``work_api.WORK_STATES``, the one list), and the two client
    schemas carry that same enum, so the run cards and the work cards cannot drift apart."""
    from hermes_cli.worker.board import work_state
    from plugins.kanban.dashboard.work_api import WORK_STATES

    jobs = [{"state": s, "verdict": v} for s in ("new", "queued", "running", "done", "review", "failed", "stalled",
                                                  "unknown", "done_empty", "cancelled")
            for v in (None, "accepted", "rejected")]
    seen = {work_state(j)[0] for j in jobs} | {work_state({"state": "running"}, {"dash": "human"})[0]}
    assert seen == set(WORK_STATES)
    for name in ("board-client.yaml", "work-client.yaml"):
        spec = hermes_yaml.safe_load((REPO / "docs" / "api" / name).read_text(encoding="utf-8"))
        assert tuple(spec["components"]["schemas"]["WorkState"]["enum"]) == WORK_STATES, name
