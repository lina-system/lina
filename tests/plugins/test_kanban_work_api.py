"""Work API (plugins/kanban/dashboard/work_api.py) on a scratch board through the kanban plugin router.

Every response is checked against the published schema docs/api/work-client.yaml.
"""

from __future__ import annotations

import importlib.util
import sys
import time
from pathlib import Path

import jsonschema
import pytest
from fastapi import FastAPI
from fastapi.testclient import TestClient

import hermes_yaml
from hermes_cli import kanban_db as kb
from hermes_cli import kanban_db_connect as kbc
from hermes_cli.kanban_goal_gates import run_card_gates

REPO = Path(__file__).resolve().parents[2]
WORK = "/api/plugins/kanban/work"
AGENT = "worker-a"


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


_SPEC = hermes_yaml.safe_load((REPO / "docs/api/work-client.yaml").read_text(encoding="utf-8"))
_COMPONENTS = _openapi_to_jsonschema(_SPEC["components"])


def _valid(schema_name: str, data):
    jsonschema.Draft7Validator(
        {"$ref": f"#/components/schemas/{schema_name}", "components": _COMPONENTS}).validate(data)
    return data


@pytest.fixture
def client(tmp_path, monkeypatch):
    home = tmp_path / ".hermes"
    home.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    kb.init_db()
    spec = importlib.util.spec_from_file_location(
        "hermes_dashboard_plugin_kanban_work_test", REPO / "plugins/kanban/dashboard/plugin_api.py")
    mod = importlib.util.module_from_spec(spec)
    monkeypatch.setitem(sys.modules, spec.name, mod)
    spec.loader.exec_module(mod)
    app = FastAPI()
    app.include_router(mod.router, prefix="/api/plugins/kanban")
    return TestClient(app)


@pytest.fixture
def conn(client):
    c = kbc.connect()
    yield c
    c.close()


def _card(conn, title, **kw):
    kw.setdefault("assignee", AGENT)
    return kb.create_task(conn, title=title, goal_mode=True, **kw)


def _goal(conn, work_ids, title="Synthetic goal"):
    return kb.create_task(conn, title=title, assignee="main", parents=work_ids)


def _running(conn, tid):
    assert kb.claim_task(conn, tid) is not None
    return tid


def _card_json(client, tid, schema="WorkCardDetail"):
    r = client.get(f"{WORK}/cards/{tid}")
    assert r.status_code == 200, r.text
    return _valid(schema, r.json())


def test_state_follows_each_column_and_counts_only_live_runs(client, conn, tmp_path):
    ws = tmp_path / "ws"
    ws.mkdir()
    ids = {name: _card(conn, name) for name in (
        "triage", "scheduled", "ready", "live", "stale", "gate-wait", "review", "blocked", "done")}
    triage = _card(conn, "triaged", triage=True)
    for tid in (ids["live"], ids["stale"], ids["gate-wait"], ids["review"], ids["blocked"], ids["done"]):
        _running(conn, tid)
    with kb.write_txn(conn):
        conn.execute("UPDATE tasks SET status = 'scheduled' WHERE id = ?", (ids["scheduled"],))
        old = int(time.time()) - 3600
        conn.execute("UPDATE task_runs SET claim_expires = ?, started_at = ?, last_heartbeat_at = NULL "
                     "WHERE task_id = ?", (old, old, ids["stale"]))
        conn.execute("UPDATE tasks SET workspace_path = ?, goal_gates = ? WHERE id = ?",
                     (str(ws), '[{"command": "exit 75"}]', ids["gate-wait"]))
    assert run_card_gates(conn, kb.get_task(conn, ids["gate-wait"])).waiting
    assert kb.request_review(conn, ids["review"], summary="ready for a look")
    assert kb.block_task(conn, ids["blocked"], reason="which colour?", kind="needs_input")
    assert kb.complete_task(conn, ids["done"], summary="shipped")
    goal = _goal(conn, [*ids.values(), triage])

    expected = {
        triage: ("planned", "triage"), ids["scheduled"]: ("planned", "scheduled"),
        ids["ready"]: ("planned", "ready"), ids["live"]: ("in_progress", None),
        ids["stale"]: ("stopped", "unresponsive"), ids["gate-wait"]: ("waiting", "gate"),
        ids["review"]: ("waiting", "review"), ids["blocked"]: ("stopped", "needs_input"),
        ids["done"]: ("done", None), goal: ("planned", "todo"),
    }
    for tid, state in expected.items():
        card = _card_json(client, tid)
        assert (card["state"], card["reason"]) == state, kb.get_task(conn, tid).title

    running = _valid("WorkCardList", client.get(f"{WORK}/cards", params={"state": "in_progress"}).json())
    assert [c["id"] for c in running["cards"]] == [ids["live"]]
    listed = _valid("WorkCardList", client.get(f"{WORK}/cards", params={"agent": AGENT}).json())
    assert {c["id"] for c in listed["cards"]} == set(ids.values()) | {triage}
    assert client.get(f"{WORK}/cards", params={"state": "running"}).status_code == 400


def test_todo_progress_counts_ticks_and_passing_prs_gate(client, conn, tmp_path):
    ws = tmp_path / "ws"
    ws.mkdir()
    body = ("## Goal\nShip widgets.\n\n## To-dos\n- acme/widgets#1\n- acme/widgets#2\n"
            "- [x] write the notes\n- [ ] tag the release\nplain note line\n\n## Outcome\n- [x] not a to-do\n")
    tid = _card(conn, "widgets", body=body, workspace_path=str(ws),
                goal_gates=["echo gate prs acme/widgets#1"])
    _goal(conn, [tid])
    assert run_card_gates(conn, kb.get_task(conn, tid)) is None

    card = _card_json(client, tid)
    assert card["todoProgress"] == {"done": 2, "total": 4}
    assert [(t["id"], t["kind"], t["done"]) for t in card["todos"]] == [
        ("acme/widgets#1", "issue", True), ("acme/widgets#2", "issue", False),
        ("check-3", "check", True), ("check-4", "check", False)]
    assert [(g["command"], g["passed"]) for g in card["gates"]] == [("echo gate prs acme/widgets#1", True)]


def test_detail_lists_worker_runs_and_a_plain_now_line(client, conn):
    tid = _card(conn, "runs")
    _goal(conn, [tid])
    kb.add_comment(conn, tid, AGENT, "started OT-SYN0000001 for acme/widgets#1")
    kb.add_comment(conn, tid, AGENT, "quick-issue: qi-42 opened; OT-SYN0000001 still going")
    kb.add_comment(conn, tid, AGENT, "**Now** running `lina worker run lw-7` " + "x" * 200)

    card = _card_json(client, tid)
    assert [(r["id"], r["kind"]) for r in card["workerRuns"]] == [
        ("OT-SYN0000001", "omo"), ("qi-42", "quick-issue"), ("lw-7", "lina-worker")]
    first, *_ = card["workerRuns"]
    assert first["firstSeenAt"] <= first["lastSeenAt"]
    assert card["now"].startswith("Now running lina worker run lw-7")
    assert len(card["now"]) <= 120
    assert len(card["comments"]) == 3


def test_goal_lists_work_cards_and_the_evidence_that_closed_them(client, conn):
    a, b = _card(conn, "part a"), _card(conn, "part b")
    goal = _goal(conn, [a, b], title="Two-part goal")
    _running(conn, a)
    assert kb.complete_task(conn, a, summary="merged acme/widgets#3", metadata={"prs": [3]})

    goals = _valid("GoalList", client.get(f"{WORK}/goals").json())["goals"]
    assert [g["id"] for g in goals] == [goal]
    one = _valid("WorkGoal", client.get(f"{WORK}/goals/{goal}").json())
    assert [c["id"] for c in one["workCards"]] == [a, b]
    assert [(e["cardId"], e["summary"], e["metadata"]) for e in one["evidence"]] == [
        (a, "merged acme/widgets#3", {"prs": [3]})]
    assert client.get(f"{WORK}/goals/{a}").status_code == 404
    assert client.get(f"{WORK}/cards/t_missing").status_code == 404


def test_inbox_lists_blocks_and_open_questions_and_answer_unblocks_only_needs_input(client, conn):
    needs_input, capability, asked, answered = (_card(conn, n) for n in ("needs", "cap", "asked", "answered"))
    _goal(conn, [needs_input, capability, asked, answered])
    for tid in (needs_input, capability):
        _running(conn, tid)
    assert kb.block_task(conn, needs_input, reason="pick a colour", kind="needs_input")
    assert kb.block_task(conn, capability, reason="no GPU here", kind="capability")
    kb.add_comment(conn, asked, AGENT, "질문: which colour?")
    kb.add_comment(conn, answered, AGENT, "Q: which size?")
    kb.add_comment(conn, answered, "owner", "large")

    inbox = _valid("Inbox", client.get(f"{WORK}/inbox").json())
    assert {(b["cardId"], b["blockKind"], b["blockReason"]) for b in inbox["blocked"]} == {
        (needs_input, "needs_input", "pick a colour"), (capability, "capability", "no GPU here")}
    assert [(q["cardId"], q["body"]) for q in inbox["questions"]] == [(asked, "질문: which colour?")]

    ok = _valid("AnswerResult", client.post(f"{WORK}/inbox/{needs_input}/answer", json={"text": "blue"}).json())
    assert (ok["unblocked"], ok["state"]) == (True, "planned")
    kept = _valid("AnswerResult", client.post(f"{WORK}/inbox/{capability}/answer", json={"text": "wait"}).json())
    assert (kept["unblocked"], kept["state"], kept["reason"]) == (False, "stopped", "capability")
    assert kb.get_task(conn, capability).status == "blocked"
    client.post(f"{WORK}/inbox/{asked}/answer", json={"text": "green"})
    inbox = _valid("Inbox", client.get(f"{WORK}/inbox").json())
    assert [b["cardId"] for b in inbox["blocked"]] == [capability]
    assert inbox["questions"] == []
    assert [c.author for c in kb.list_comments(conn, needs_input)] == ["owner"]
    assert client.post(f"{WORK}/inbox/{asked}/answer", json={"text": "  "}).status_code == 400
    assert client.post(f"{WORK}/inbox/{asked}/answer", json={"text": "me", "author": AGENT}).status_code == 400


def test_work_routes_sit_behind_the_dashboard_auth_gate(_isolate_hermes_home, monkeypatch):
    import hermes_state
    from hermes_constants import get_hermes_home
    from hermes_cli.web_server import _SESSION_HEADER_NAME, _SESSION_TOKEN, app

    monkeypatch.setattr(hermes_state, "DEFAULT_DB_PATH", get_hermes_home() / "state.db")
    anonymous = TestClient(app)
    assert anonymous.get(f"{WORK}/cards").status_code == 401
    assert anonymous.post(f"{WORK}/inbox/t_x/answer", json={"text": "hi"}).status_code == 401
    authed = TestClient(app)
    authed.headers[_SESSION_HEADER_NAME] = _SESSION_TOKEN
    r = authed.get(f"{WORK}/cards")
    assert r.status_code == 200, r.text
    assert _valid("WorkCardList", r.json()) == {"cards": []}
