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
REVIEWER = "worker-b"


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
    # The work record (lina#666) in the scratch sqlite store, never the platform's postgres.
    (home / "config.yaml").write_text(
        f"desk: {{path: {tmp_path}}}\nwork:\n  locale: ko\n  owner: {{id: owner, name: owner}}\n"
        f"  agents: {{alpha: alpha}}\n  session_queue: {tmp_path / 'queue'}\n  store: {{backend: sqlite}}\n",
        encoding="utf-8")
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


def _record(*args: str) -> str:
    """One ``lina work`` command against the scratch record; returns its output."""
    import contextlib
    import io

    from hermes_cli.work import env, main
    out = io.StringIO()
    env.reset()
    try:
        with contextlib.redirect_stdout(out):
            main(list(args))
    finally:
        env.reset()
    return out.getvalue()


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
        ids["review"]: ("waiting", "review"), ids["blocked"]: ("stopped", "decision"),
        ids["done"]: ("done", None), goal: ("planned", "todo"),
    }
    for tid, state in expected.items():
        card = _card_json(client, tid)
        assert (card["state"], card["reason"]) == state, kb.get_task(conn, tid).title
    stops = {tid: (c["waitingOn"], c["retry"]) for tid, c in ((t, _card_json(client, t)) for t in expected)}
    assert stops[ids["blocked"]] == ("owner", "none") and stops[ids["scheduled"]] == (None, "scheduled")
    assert stops[ids["stale"]] == stops[ids["live"]] == (None, None)

    running = _valid("WorkCardList", client.get(f"{WORK}/cards", params={"state": "in_progress"}).json())
    assert [c["id"] for c in running["cards"]] == [ids["live"]]
    listed = _valid("WorkCardList", client.get(f"{WORK}/cards", params={"agent": AGENT}).json())
    assert {c["id"] for c in listed["cards"]} == set(ids.values()) | {triage}
    assert client.get(f"{WORK}/cards", params={"state": "running"}).status_code == 400


def test_a_reviewed_card_is_listed_under_the_agent_that_did_it(client, conn):
    """lina#290: ``request_review`` reassigns the card to the reviewer, so the list, its ``agent`` filter and the
    card's ``agent`` stay on the implementer, and the reviewer is sent on its own field."""
    reviewed, approved = _card(conn, "in review"), _card(conn, "reviewed then done")
    _goal(conn, [reviewed, approved])
    for tid in (reviewed, approved):
        _running(conn, tid)  # the run's profile is the card's assignee, so AGENT did it
        assert kb.request_review(conn, tid, reviewer=REVIEWER)
        assert kb.claim_review_task(conn, tid, claimer=REVIEWER)  # a review run: the reviewer
    assert kb.complete_task(conn, approved, summary="merged acme/widgets#7", force=True)

    listed = _valid("WorkCardList", client.get(f"{WORK}/cards", params={"agent": AGENT}).json())
    assert {c["id"] for c in listed["cards"]} == {reviewed, approved}
    assert {(c["agent"], c["reviewer"]) for c in listed["cards"]} == {(AGENT, REVIEWER)}
    assert [c["state"] for c in listed["cards"]] == ["in_progress", "done"]
    assert client.get(f"{WORK}/cards", params={"agent": REVIEWER}).json()["cards"] == []
    detail = _card_json(client, reviewed)
    assert (detail["agent"], detail["reviewer"]) == (AGENT, REVIEWER)


def test_a_reviewed_goal_card_and_a_blocked_reviewed_card_carry_the_implementer(client, conn):
    """lina#290: the goal row and the inbox rows carry the implementer too, the reviewer beside it."""
    part = _card(conn, "part")
    goal = _goal(conn, [part])
    _running(conn, part)
    assert kb.complete_task(conn, part, summary="merged acme/widgets#8")  # the goal card becomes ready
    assert kb.claim_task(conn, goal) is not None
    assert kb.request_review(conn, goal, reviewer=REVIEWER)
    assert kb.claim_review_task(conn, goal, claimer=REVIEWER)

    row = _valid("WorkGoal", client.get(f"{WORK}/goals/{goal}").json())
    assert (row["agent"], row["reviewer"]) == ("main", REVIEWER)

    blocked = _card(conn, "blocked")
    _goal(conn, [blocked])
    _running(conn, blocked)
    assert kb.request_review(conn, blocked, reviewer=REVIEWER)
    assert kb.claim_review_task(conn, blocked, claimer=REVIEWER)
    assert kb.block_task(conn, blocked, reason="pick a colour", kind="needs_input")
    kb.add_comment(conn, blocked, REVIEWER, "질문: which colour?")
    inbox = _valid("Inbox", client.get(f"{WORK}/inbox").json())
    item = next(b for b in inbox["blocked"] if b["cardId"] == blocked)
    question = next(q for q in inbox["questions"] if q["cardId"] == blocked)
    assert (item["agent"], item["reviewer"]) == (AGENT, REVIEWER)
    assert (question["agent"], question["reviewer"]) == (AGENT, REVIEWER)


def test_a_goal_mode_card_and_a_todos_card_are_listed_without_a_goal_card(client, conn):
    """lina#290: a goal-mode card registered on its own and a plain card with a ``## To-dos`` section are work
    cards, though no goal card sits above either."""
    lone = _card(conn, "no goal card above it")
    todos = kb.create_task(conn, title="opened by the decomposer", assignee=AGENT,
                           body="## Goal\n- ship\n\n## To-dos\n- [ ] rotate the disks")
    _running(conn, lone)

    running = _valid("WorkCardList", client.get(f"{WORK}/cards", params={"state": "in_progress"}).json())
    assert [c["id"] for c in running["cards"]] == [lone]
    listed = {c["id"] for c in client.get(f"{WORK}/cards", params={"agent": AGENT}).json()["cards"]}
    assert listed == {lone, todos}

    assert kb.complete_task(conn, lone, summary="rotated both disks", force=True)
    done = _valid("WorkCardList", client.get(f"{WORK}/cards", params={"state": "done"}).json())
    assert [c["id"] for c in done["cards"]] == [lone]


def test_successor_run_does_not_inherit_waiting_gate_state(client, conn, tmp_path):
    ws = tmp_path / "ws"
    ws.mkdir()
    tid = _card(conn, "successor", workspace_path=str(ws), goal_gates=["exit 75"])
    _goal(conn, [tid])
    first = kb.claim_task(conn, tid)
    assert first is not None
    assert run_card_gates(conn, first).waiting
    waiting = _card_json(client, tid)
    assert (waiting["state"], waiting["reason"]) == ("waiting", "gate")
    assert kb.reclaim_task(conn, tid)

    successor = kb.claim_task(conn, tid)
    assert successor is not None
    assert successor.current_run_id != first.current_run_id

    detail = _card_json(client, tid)
    response = client.get(f"{WORK}/cards", params={"state": "in_progress"})
    assert response.status_code == 200, response.text
    running = _valid("WorkCardList", response.json())
    assert (detail["state"], detail["reason"]) == ("in_progress", None)
    assert [c["id"] for c in running["cards"]] == [tid]
    assert client.get(f"{WORK}/cards", params={"state": "waiting"}).json()["cards"] == []


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


def test_list_rows_carry_the_end_and_last_heartbeat_the_detail_has(client, conn):
    """lina#577: the list row alone tells when a done card ended and when a stopped card last beat."""
    done, stale, todo = _card(conn, "done"), _card(conn, "stale"), _card(conn, "todo")
    for tid in (done, stale):
        _running(conn, tid)
    assert kb.complete_task(conn, done, summary="shipped")
    beat = int(time.time()) - 3600
    with kb.write_txn(conn):
        conn.execute("UPDATE task_runs SET claim_expires = ?, started_at = ?, last_heartbeat_at = ? WHERE task_id = ?",
                     (beat, beat - 60, beat, stale))
        conn.execute("UPDATE tasks SET last_heartbeat_at = ? WHERE id = ?", (beat, stale))

    rows = {c["id"]: c for c in _valid("WorkCardList", client.get(f"{WORK}/cards").json())["cards"]}
    ended = kb.get_task(conn, done).completed_at
    assert ended is not None and rows[done]["endedAt"] == ended
    assert (rows[stale]["state"], rows[stale]["endedAt"], rows[todo]["endedAt"]) == ("stopped", None, None)
    assert rows[stale]["lastHeartbeatAt"] == _card_json(client, stale)["lastHeartbeatAt"] == beat
    assert rows[todo]["lastHeartbeatAt"] is None
    for tid in (done, stale, todo):
        detail = _card_json(client, tid)
        assert (detail["endedAt"], detail["lastHeartbeatAt"]) == (rows[tid]["endedAt"], rows[tid]["lastHeartbeatAt"])


def test_hermes_list_rows_carry_the_end_and_last_heartbeat_the_detail_has(client):
    """lina#577 on the Hermes board: a shown card's heartbeat comes from its events (the CLI has no heartbeat
    column), and a done card served from its ``list`` row alone (``detail: false``) ends at that row's
    ``completed_at``."""
    from plugins.kanban.dashboard import work_api_hermes
    now = int(time.time())

    def show(task_id, status, **extra):
        task = {"id": task_id, "title": task_id, "body": "", "assignee": AGENT, "status": status,
                "created_at": now - 9000, "started_at": now - 7200, "completed_at": extra.pop("completed_at", None)}
        return {"task": task, "parents": [], "children": [], "comments": [], "events": [], "runs": [], **extra}

    shows = [
        show("t_done", "done", completed_at=now - 600,
             events=[{"kind": "heartbeat", "payload": None, "created_at": now - 900, "run_id": 1}]),
        show("t_old", "done", completed_at=now - 90 * 86400, detail=False),
        show("t_stuck", "blocked", events=[
            {"kind": "heartbeat", "payload": {"note": "halfway"}, "created_at": now - 300, "run_id": 2},
            {"kind": "blocked", "payload": {"reason": "which colour?"}, "created_at": now - 200, "run_id": 2}]),
    ]
    rows = {c["id"]: c for c in _valid("WorkCardList", work_api_hermes.cards(shows, now, None, None))["cards"]}
    assert {i: (r["endedAt"], r["lastHeartbeatAt"]) for i, r in rows.items()} == {
        "hermes:t_done": (now - 600, now - 900), "hermes:t_old": (now - 90 * 86400, None),
        "hermes:t_stuck": (None, now - 300)}
    assert rows["hermes:t_old"]["detail"] is False and rows["hermes:t_stuck"]["state"] == "stopped"
    for card_id, row in rows.items():
        detail = _valid("WorkCardDetail", work_api_hermes.card(shows, card_id, now))
        assert (detail["endedAt"], detail["lastHeartbeatAt"]) == (row["endedAt"], row["lastHeartbeatAt"])


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
    answer_events = kb.list_events(conn, needs_input)
    comment = [event for event in answer_events if event.kind == "commented"][-1]
    assert comment.payload["source"] == {"surface": "app"}
    assert comment.payload["comment_id"] == ok["commentId"]
    unblocked = [event for event in answer_events if event.kind == "unblocked"][-1]
    assert (unblocked.payload["by"], unblocked.payload["reason"]) == ("owner", "blue")
    kept = _valid("AnswerResult", client.post(f"{WORK}/inbox/{capability}/answer", json={"text": "wait"}).json())
    assert (kept["unblocked"], kept["state"], kept["reason"]) == (False, "stopped", "feedback")
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


def test_board_query_resolves_only_existing_boards(client, conn):
    _card(conn, "on the default board")
    assert client.get(f"{WORK}/cards", params={"board": "default"}).status_code == 200
    assert client.get(f"{WORK}/cards", params={"board": "no-such-board"}).status_code == 404
    assert client.get(f"{WORK}/cards", params={"board": "../etc"}).status_code == 400
    kb.create_board("side")
    assert client.get(f"{WORK}/cards", params={"board": "side"}).status_code == 200


def test_a_dependency_block_classified_in_place_waits_on_the_owner(client, conn):
    """lina#629: the failure breaker parks a card ``blocked`` untyped; classifying it ``dependency`` in place
    leaves it ``blocked``, which only an operator resumes (the dispatcher wakes ``todo`` dependency cards and
    ``needs_input`` waits), so it must not advertise ``retry: scheduled``."""
    tid = _card(conn, "breaker parked")
    with kb.write_txn(conn):
        conn.execute("UPDATE tasks SET status = 'blocked', block_kind = NULL, current_run_id = NULL WHERE id = ?", (tid,))
    assert kb.block_task(conn, tid, reason="waits for the import card", kind="dependency")
    assert kb.get_task(conn, tid).status == "blocked"
    card = _card_json(client, tid)
    assert (card["state"], card["reason"], card["waitingOn"], card["retry"]) == (
        "stopped", "blocked_by_item", "owner", "none")


def test_a_card_carries_the_goal_title_of_the_record_work_that_links_it(client, conn):
    """lina#666: a record to-do linking ``kanban:<card>`` gives that card its goal's title as ``goalTitle``; a card no
    record item links has null, in the list and in the detail."""
    linked = _card(conn, "a card the record links")
    unlinked = _card(conn, "a card nothing links")
    _record("add", "목표", "--by", "alpha", "--area", "Dev", "--title", "빌드 기기 정리", "--basis", "기기 정리해 줘",
            "--done", "빌드 기기가 모두 가동된다", "--check", "false")
    _record("add", "할 일", "--by", "alpha", "--goal", "G1", "--title", "기기 1 점검", "--why", "멈추지 않게",
            "--done", "기기가 답한다", "--check", "false", "--link", f"kanban:{linked}")

    listed = _valid("WorkCardList", client.get(f"{WORK}/cards").json())
    assert {c["id"]: c["goalTitle"] for c in listed["cards"]} == {linked: "빌드 기기 정리", unlinked: None}
    assert "errors" not in listed
    assert _card_json(client, linked)["goalTitle"] == "빌드 기기 정리"


def test_an_unreadable_record_leaves_goal_titles_null_and_still_serves_the_cards(client, conn, monkeypatch):
    """lina#666: the record failing to read costs only ``goalTitle`` (null) and names itself in ``errors``."""
    from hermes_cli.work import ledger
    tid = _card(conn, "served without its goal")

    def unreadable():
        raise RuntimeError("store is down")
    monkeypatch.setattr(ledger, "read_all", unreadable)

    r = client.get(f"{WORK}/cards")
    assert r.status_code == 200, r.text
    listed = _valid("WorkCardList", r.json())
    assert [(c["id"], c["goalTitle"]) for c in listed["cards"]] == [(tid, None)]
    assert [e["source"] for e in listed["errors"]] == ["work"]


def test_a_blocked_card_without_goal_or_todos_is_served_stopped_and_triage_planned(client, conn):
    """lina#664: a blocked card is work whatever its body (stopped, with its reason, waitingOn and retry), and so is a
    triage card (planned); a ready card with neither goal mode nor To-dos is still not work."""
    blocked = kb.create_task(conn, title="order gate", assignee=AGENT, body="plain body")
    assert kb.block_task(conn, blocked, reason="which vendor?", kind="needs_input")
    triage = kb.create_task(conn, title="remote control idea", assignee=AGENT, triage=True)
    kb.create_task(conn, title="plain ready card", assignee=AGENT)

    listed = _valid("WorkCardList", client.get(f"{WORK}/cards").json())
    rows = {c["id"]: (c["state"], c["reason"], c["waitingOn"], c["retry"]) for c in listed["cards"]}
    assert rows == {blocked: ("stopped", "decision", "owner", "none"), triage: ("planned", "triage", None, None)}


def test_a_stopped_row_asks_the_owner_with_the_first_korean_sentence_of_its_block_reason(client, conn):
    """lina#690: a card stopped on the owner carries ``ask``: the first sentence of its block reason without card ids,
    run ids or URLs, at most 60 characters; null for an English-only reason and on a stop nobody has to answer. The
    detail carries the row's."""
    long = "배포 키를 새로 받을지 지금 키를 그대로 쓸지 오늘 안에 정해 주세요 그래야 다음 배포를 예정대로 열 수 있고 알림도 멈추지 않아요"
    reasons = {
        "stripped": ("새 배포 키를 받을지 정해 주세요 (카드 t_492a3297, OT-SIO1114051903, https://example.com/runs/7). "
                     "아니면 다음 주로 미뤄도 돼요.", "needs_input"),
        "cut": (long, "needs_input"),
        "inbox line": ("agent-a · 갈림길 · hermes:t_0000a001 캐시를 둘지 버릴지 골라 주세요? 급하지 않아요.", None),
        "english": ("Pick the banner colour for card t_0000a001. See https://example.com/a.", "needs_input"),
        "no text": ("   ", "needs_input"),
    }
    ids = {}
    for name, (reason, kind) in reasons.items():
        ids[name] = kb.create_task(conn, title=name, assignee=AGENT, body="plain body")
        assert kb.block_task(conn, ids[name], reason=reason, kind=kind)
    waits = kb.create_task(conn, title="waits on a card", assignee=AGENT, body="## To-dos\n- [ ] import\n")
    assert kb.block_task(conn, waits, reason="가져오기 카드를 기다려요.", kind="needs_input",
                         wait_for={"kind": "card", "task_id": ids["cut"]})

    rows = {c["id"]: c for c in _valid("WorkCardList", client.get(f"{WORK}/cards").json())["cards"]}
    asks = {name: rows[tid]["ask"] for name, tid in ids.items()}
    assert asks == {"stripped": "새 배포 키를 받을지 정해 주세요.", "cut": long[:59].rstrip() + "…",
                    "inbox line": "캐시를 둘지 버릴지 골라 주세요?", "english": None, "no text": None}
    assert len(asks["cut"]) <= 60
    assert (rows[waits]["waitingOn"], rows[waits]["ask"]) == (None, None)
    for tid in [*ids.values(), waits]:
        assert _card_json(client, tid)["ask"] == rows[tid]["ask"]
