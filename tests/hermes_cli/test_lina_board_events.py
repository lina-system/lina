"""``GET /api/lina/events`` (lina#150): one WebSocket for the work record, ``lina worker`` run state changes and kanban
task events, with a resumable cursor, over a scratch LINA root. Messages validate against
``docs/api/board-client.yaml`` (``ChangeMessage``); cursors are taken from the server only (they are opaque)."""

from __future__ import annotations

import time
from concurrent.futures import ThreadPoolExecutor
from concurrent.futures import TimeoutError as FutureTimeout
from pathlib import Path

import jsonschema
import pytest
from fastapi.testclient import TestClient
from starlette.websockets import WebSocketDisconnect

import hermes_yaml
from hermes_cli import lina_ocx
from tests.hermes_cli._work_env import NOW, WorkEnv

pytestmark = pytest.mark.platforms("posix")

REPO = Path(__file__).resolve().parents[2]
WITHIN_S = 2.0


def _jsonschema(node):
    if isinstance(node, list):
        return [_jsonschema(n) for n in node]
    if not isinstance(node, dict):
        return node
    out = {k: _jsonschema(v) for k, v in node.items() if k != "nullable"}
    if node.get("nullable") and "type" in out:
        out["type"] = [out["type"], "null"]
    elif node.get("nullable"):
        out = {"anyOf": [out, {"type": "null"}]}
    return out


_COMPONENTS = _jsonschema(
    hermes_yaml.safe_load((REPO / "docs/api/board-client.yaml").read_text(encoding="utf-8"))["components"])


def _valid(schema_name: str, data):
    jsonschema.Draft7Validator({"$ref": f"#/components/schemas/{schema_name}", "components": _COMPONENTS}).validate(data)
    return data


@pytest.fixture
def work(tmp_path, monkeypatch):
    env = WorkEnv(tmp_path, monkeypatch)
    monkeypatch.setattr(lina_ocx, "lina_root", lambda: env.root)
    env.goal_and_todos(todos=2)
    return env


@pytest.fixture
def client():
    from hermes_cli import web_server
    test_client = TestClient(web_server.app)
    test_client.headers[web_server._SESSION_HEADER_NAME] = web_server._SESSION_TOKEN
    return test_client


@pytest.fixture
def pool():
    with ThreadPoolExecutor(max_workers=1) as executor:
        yield executor


def _url(since: str | None = None) -> str:
    from hermes_cli import web_server
    return f"/api/lina/events?token={web_server._SESSION_TOKEN}" + (f"&since={since}" if since is not None else "")


def _next(ws, pool, within: float = WITHIN_S) -> dict:
    """The next message, or a failure when none comes in *within* seconds."""
    try:
        return _valid("ChangeMessage", pool.submit(ws.receive_json).result(timeout=within))
    except FutureTimeout:
        pytest.fail(f"no message within {within}s")


def _events(message: dict) -> list[tuple[str, str, str]]:
    return [(e["source"], e["kind"], e["id"]) for e in message["events"]]


def _receipt(work: WorkEnv, task: str, kind: str) -> None:
    from hermes_cli.worker import settings
    from hermes_cli.worker.hub_store import Store
    Store(settings.root(work.root)).receipt(task, kind, run=1)


def _card(title: str) -> str:
    from hermes_cli import kanban_db
    from hermes_cli import kanban_db_connect as kbc
    conn = kbc.connect()
    try:
        return kanban_db.create_task(conn, title=title)
    finally:
        conn.close()


def test_the_first_message_is_the_tail_and_a_write_through_lina_work_arrives_within_two_seconds(work, client, pool):
    with client.websocket_connect(_url()) as ws:
        first = _next(ws, pool)
        assert first["events"] == [] and first["cursor"]

        work.ok("comment", "T1", "1번부터 봐 줘")
        written = time.monotonic()
        cli = _next(ws, pool)
        assert time.monotonic() - written < WITHIN_S
        assert _events(cli) == [("work", "comment", "T1")]
        assert cli["events"][0]["at"] == NOW and set(cli["events"][0]) == {"source", "kind", "id", "at"}

        assert client.post("/api/lina/work/items/T2/comments", json={"text": "로그도 남겨 줘"}).status_code == 200
        written = time.monotonic()
        api = _next(ws, pool)
        assert time.monotonic() - written < WITHIN_S
        assert _events(api) == [("work", "comment", "T2")] and api["cursor"] != cli["cursor"]


def test_worker_run_state_changes_and_kanban_task_events_arrive_on_the_same_socket(work, client, pool):
    with client.websocket_connect(_url()) as ws:
        _next(ws, pool)
        _receipt(work, "OT-ALP0001", "steer")  # not a state change
        _receipt(work, "OT-ALP0001", "run_started")
        assert _events(_next(ws, pool)) == [("worker", "run_started", "OT-ALP0001")]
        card = _card("기기 점검 카드")
        assert ("kanban", "created", card) in _events(_next(ws, pool))


def test_since_replays_what_a_reconnecting_client_missed_from_all_three_sources(work, client, pool):
    with client.websocket_connect(_url()) as ws:
        cursor = _next(ws, pool)["cursor"]
    work.ok("comment", "T1", "다시 봐 줘")
    work.ok("note", "T2", "기기 2를 켰다")
    _receipt(work, "OT-ALP0002", "exec_end")
    card = _card("백업 카드")

    with client.websocket_connect(_url(cursor)) as ws:
        missed = _next(ws, pool)
    work_events = [e for e in _events(missed) if e[0] == "work"]  # the checker may add its own lines (a goal stage)
    assert work_events.index(("work", "comment", "T1")) < work_events.index(("work", "note", "T2"))
    assert ("worker", "exec_end", "OT-ALP0002") in _events(missed)
    assert ("kanban", "created", card) in _events(missed)

    with client.websocket_connect(_url(missed["cursor"])) as ws:
        assert _next(ws, pool) == {"cursor": missed["cursor"], "events": []}


def test_an_unreadable_or_unkept_since_gets_one_resync_and_the_stream_goes_on(work, client, pool):
    with client.websocket_connect(_url("not-a-cursor")) as ws:
        resync = _next(ws, pool)
        assert resync["resync"] is True
        work.ok("comment", "T1", "봐 줘")
        assert _events(_next(ws, pool)) == [("work", "comment", "T1")]

    for n in range(3):
        work.ok("note", "T1", f"메모 {n}")
    with client.websocket_connect(_url()) as ws:
        later = _next(ws, pool)["cursor"]
    (work.work / "work.db").unlink()  # the record is reset: the cursor points past what it holds now
    work.goal_and_todos(todos=1)
    with client.websocket_connect(_url(later)) as ws:
        assert _next(ws, pool)["resync"] is True


def test_a_kanban_cursor_before_pruned_events_gets_a_resync(work, client, pool):
    from hermes_cli import kanban_db_connect as kbc
    _card("첫 카드")
    with client.websocket_connect(_url()) as ws:
        cursor = _next(ws, pool)["cursor"]
    for n in range(3):
        _card(f"카드 {n}")
    conn = kbc.connect()
    try:
        first = conn.execute("SELECT min(id) FROM task_events WHERE id > (SELECT min(id) FROM task_events)").fetchone()[0]
        with conn:
            conn.execute("DELETE FROM task_events WHERE id <= ?", (first + 1,))
    finally:
        conn.close()
    with client.websocket_connect(_url(cursor)) as ws:
        assert _next(ws, pool)["resync"] is True


def test_the_stream_is_behind_the_dashboard_socket_credential(work, client, pool):
    with pytest.raises(WebSocketDisconnect), client.websocket_connect("/api/lina/events?token=wrong") as ws:
        ws.receive_json()
    with client.websocket_connect(_url()) as ws:
        assert _next(ws, pool)["events"] == []
