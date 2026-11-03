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


def _receipts(work: WorkEnv) -> Path:
    from hermes_cli.worker import settings
    from hermes_cli.worker.hub_store import Store
    return Store(settings.root(work.root)).receipts


def _replace_work(work: WorkEnv, rows: int) -> None:
    from hermes_cli.work import ledger
    (work.work / "work.db").unlink()
    work.clock[0] += 3600  # a new store is made later: its first row is not the old one's
    work.goal_and_todos(todos=2)
    n = 0
    while True:
        db = ledger.open_db()
        try:
            if db.execute("SELECT max(id) FROM event").fetchone()[0] >= rows:
                return
        finally:
            db.close()
        work.ok("note", "T1", f"새 기록 {n}")
        n += 1


def _replace_worker(work: WorkEnv, rows: int) -> None:
    _receipts(work).unlink()  # a recreated file: same line lengths, so the old offset still ends a line
    for n in range(rows + 1):
        _receipt(work, f"OT-BET{n:04d}", "queued")


def _replace_kanban(work: WorkEnv, rows: int) -> None:
    from hermes_cli.kanban_db import kanban_db_path
    path = Path(kanban_db_path())
    for extra in ("", "-wal", "-shm"):
        Path(f"{path}{extra}").unlink(missing_ok=True)
    for n in range(rows + 1):
        _card(f"새 보드 카드 {n}")


def _kanban_max() -> int:
    from hermes_cli import kanban_db_connect as kbc
    conn = kbc.connect()
    try:
        return int(conn.execute("SELECT COALESCE(max(id), 0) FROM task_events").fetchone()[0])
    finally:
        conn.close()


@pytest.mark.parametrize("store", ["work", "worker", "kanban"])
def test_a_cursor_from_a_replaced_store_that_grew_past_it_gets_one_resync(work, client, pool, store):
    from hermes_cli.work import ledger
    for n in range(3):
        work.ok("note", "T1", f"메모 {n}")
        _receipt(work, f"OT-ALP{n:04d}", "queued")
        _card(f"카드 {n}")
    with client.websocket_connect(_url()) as ws:
        old = _next(ws, pool)["cursor"]
    db = ledger.open_db()
    try:
        rows = {"work": int(db.execute("SELECT max(id) FROM event").fetchone()[0]),
                "worker": len(_receipts(work).read_text(encoding="utf-8").splitlines()), "kanban": _kanban_max()}[store]
    finally:
        db.close()
    {"work": _replace_work, "worker": _replace_worker, "kanban": _replace_kanban}[store](work, rows)

    with client.websocket_connect(_url(old)) as ws:
        assert _next(ws, pool)["resync"] is True
        _receipt(work, "OT-ALP0009", "run_started")
        assert _events(_next(ws, pool)) == [("worker", "run_started", "OT-ALP0009")]


def test_a_cursor_of_the_old_format_gets_one_resync(work, client, pool):
    import base64
    old = "c1." + base64.urlsafe_b64encode(b"1.0.0").decode().rstrip("=")
    with client.websocket_connect(_url(old)) as ws:
        assert _next(ws, pool)["resync"] is True
        work.ok("comment", "T1", "봐 줘")
        assert _events(_next(ws, pool)) == [("work", "comment", "T1")]


def test_a_kanban_cursor_before_a_selectively_pruned_gap_gets_a_resync_and_one_after_it_does_not(work, client, pool):
    from hermes_cli import kanban_db
    from hermes_cli import kanban_db_connect as kbc
    _card("진행 카드")  # an active task: its event stays and keeps the lowest id
    with client.websocket_connect(_url()) as ws:
        before_gap = _next(ws, pool)["cursor"]
    done = _card("끝난 카드")
    conn = kbc.connect()
    try:
        assert kanban_db.complete_task(conn, done, result="끝", fire_lifecycle_hook=False)
    finally:
        conn.close()
    with client.websocket_connect(_url()) as ws:
        after_gap = _next(ws, pool)["cursor"]
    later = _card("다음 카드")
    conn = kbc.connect()
    try:
        with conn:
            conn.execute("UPDATE task_events SET created_at = created_at - 100000000 WHERE task_id = ?", (done,))
        assert kanban_db.gc_events(conn, older_than_seconds=3600) >= 2  # the done task's old events, by gc's own rule
        low = conn.execute("SELECT min(id) FROM task_events").fetchone()[0]
    finally:
        conn.close()
    assert low == 1  # the lowest id did not move: only the gap tells

    with client.websocket_connect(_url(before_gap)) as ws:
        assert _next(ws, pool)["resync"] is True
    with client.websocket_connect(_url(after_gap)) as ws:
        replay = _next(ws, pool)
    assert "resync" not in replay and ("kanban", "created", later) in _events(replay)


@pytest.mark.parametrize(("receipt", "sent"), [
    *[({"kind": kind}, True) for kind in ("submitted", "placed", "queued", "run_started", "start_failed_requeued",
                                          "follow_up", "answer", "exec_end", "delivery", "verdict")],
    *[({"kind": kind}, False) for kind in ("card_linked", "card_link_failed", "card_noticed", "card_notice_failed",
                                           "award", "publish_failed_resumes", "fetch_failed", "group_notified",
                                           "cancel_requested", "steer")],
    ({"kind": "notified", "state": "human"}, True),
    ({"kind": "notified", "state": "done"}, False),
    ({"kind": "notified", "state": "cancelled"}, False),
    ({"kind": "a_kind_added_later"}, True),
])
def test_every_receipt_is_streamed_except_the_noise_and_unknown_kinds_are_streamed(receipt, sent):
    from hermes_cli.lina_board_events import streamed
    assert streamed({"task": "OT-ALP0001", **receipt}) is sent


@pytest.fixture
def hub(work, tmp_path, monkeypatch):
    """The real hub with one local device whose runs are never launched (the placement is what the board shows)."""
    import json
    import textwrap

    from hermes_cli.worker import device, serve, settings
    from hermes_cli.worker.hub import Hub
    from tests.hermes_cli import _worker_fakes as fakes
    argv = fakes.install_fake_cli(tmp_path / "fake")
    work.config(extra=textwrap.dedent(f"""\
        devices:
          workers:
            here: {{host: local, cap: 2}}
        worker:
          min_free_gb: 0
          runners:
            fakecli: {json.dumps({"type": "command", "argv": argv})}
        """))
    monkeypatch.setattr(device, "launch", lambda *_: None)
    root = settings.root(work.root)
    serve.serve_once(root)
    return Hub(root, out=lambda *_: None)


def _submit(hub, work: WorkEnv, host: str | None = "here") -> str:
    """A brief handed to the hub; with *host* it goes to that device without a bid (the fixed test clock makes the
    device's serve heartbeat look stale to the bidder)."""
    from tests.hermes_cli import _worker_fakes as fakes
    hub.submit({"briefs": [fakes.brief(work.tmp, "# 디스크 점검\n")], "mode": "ulw", "topic": "disk", "bot": "alpha",
                "runner": "fakecli", "wait": None, "host": host})
    return hub.store.all()[-1]["id"]


def _worker_events_until(ws, pool, last: str) -> list[tuple[str, str, str]]:
    seen: list[tuple[str, str, str]] = []
    while not seen or seen[-1][1] != last:
        seen += [e for e in _events(_next(ws, pool)) if e[0] == "worker"]
    return seen


def test_a_placement_and_a_verdict_through_the_hub_reach_the_socket(work, client, pool, hub):
    with client.websocket_connect(_url()) as ws:
        _next(ws, pool)
        task = _submit(hub, work)
        assert _worker_events_until(ws, pool, "run_started") == [
            ("worker", "submitted", task), ("worker", "placed", task), ("worker", "run_started", task)]
        hub.verdict(task, True)
        assert _events(_next(ws, pool)) == [("worker", "verdict", task)]


def test_a_queued_run_cancelled_through_the_hub_reaches_the_socket_as_its_end(work, client, pool, hub, monkeypatch):
    from hermes_cli.worker import hub as hub_mod
    monkeypatch.setattr(hub_mod.bidding, "bid", lambda *_a, **_k: (None, []))  # no device free: the run waits
    with client.websocket_connect(_url()) as ws:
        _next(ws, pool)
        task = _submit(hub, work, host=None)
        assert _worker_events_until(ws, pool, "queued") == [("worker", "submitted", task), ("worker", "queued", task)]
        hub.cancel(task, "필요 없어졌다")
        assert _events(_next(ws, pool)) == [("worker", "exec_end", task)]


def test_the_stream_opens_with_the_work_store_down_and_picks_it_up_with_one_resync(work, client, pool, monkeypatch):
    import sqlite3

    from hermes_cli import lina_board_events as events
    down, real = [True], events.Stream._work_bounds

    def bounds(self):
        if down[0]:
            raise sqlite3.OperationalError("the work store is down")
        return real(self)
    monkeypatch.setattr(events.Stream, "_work_bounds", bounds)
    with client.websocket_connect(_url()) as ws:
        assert _next(ws, pool)["events"] == []
        _receipt(work, "OT-ALP0001", "run_started")
        assert _events(_next(ws, pool)) == [("worker", "run_started", "OT-ALP0001")]
        down[0] = False
        assert _next(ws, pool)["resync"] is True  # what the store wrote while down is not known: one refetch
        work.ok("comment", "T1", "봐 줘")
        assert _events(_next(ws, pool)) == [("work", "comment", "T1")]


def test_a_run_waiting_for_a_person_and_the_answer_each_reach_the_socket(work, client, pool):
    from hermes_cli.worker import settings
    from hermes_cli.worker.hub import HUMAN_NOTICE_AFTER_S, Hub
    from hermes_cli.worker.hub_store import Store
    worker_root = settings.root(work.root)
    store = Store(worker_root)
    store.save({"id": "OT-ALP0001", "bot": "alpha", "topic": "t", "mode": "ulw", "state": "running", "runs": 1,
                "host": "here", "created": NOW - 600, "started": NOW - 500, "prompt": "ulw # 디스크 점검\n",
                "human_since": NOW - HUMAN_NOTICE_AFTER_S - 1})
    with client.websocket_connect(_url()) as ws:
        _next(ws, pool)
        Hub(worker_root, out=lambda *_: None)._human_notice(store.load("OT-ALP0001"), "1", NOW)  # the run asks a person
        asked = _next(ws, pool)
        assert _events(asked) == [("worker", "notified", "OT-ALP0001")]
        store.receipt("OT-ALP0001", "answer", run=1, response={"action": "answer", "steps": 1})  # as hub.send does
        answered = _next(ws, pool)
        assert _events(answered) == [("worker", "answer", "OT-ALP0001")]


def test_a_live_stream_resyncs_when_kanban_rows_after_its_position_are_pruned(work, client, pool):
    from hermes_cli import kanban_db
    from hermes_cli import kanban_db_connect as kbc
    active, done = _card("진행 카드"), _card("끝난 카드")
    conn = kbc.connect()
    try:
        assert kanban_db.complete_task(conn, done, result="끝", fire_lifecycle_hook=False)
    finally:
        conn.close()
    with client.websocket_connect(_url()) as ws:
        _next(ws, pool)
        conn = kbc.connect()
        try:
            with conn:  # one commit, so the poll sees both rows and the prune at once
                for task, at in ((done, 1), (active, 2)):  # the done task's event is old, the active one's new
                    conn.execute("INSERT INTO task_events (task_id, kind, payload, created_at) VALUES (?, 'commented', "
                                 "'{}', ?)", (task, at if task == done else int(NOW)))
                gone = conn.execute(  # gc_events' own rule: old events of done or archived tasks
                    "DELETE FROM task_events WHERE created_at < ? AND kind != 'decomposed' AND task_id IN "
                    "(SELECT id FROM tasks WHERE status IN ('done', 'archived'))", (int(NOW) - 3600,)).rowcount
        finally:
            conn.close()
        assert gone >= 1
        assert _next(ws, pool)["resync"] is True
        later = _card("다음 카드")
        after = _next(ws, pool)
        assert "resync" not in after and ("kanban", "commented", active) not in _events(after)
        assert ("kanban", "created", later) in _events(after)


def test_the_stream_is_behind_the_dashboard_socket_credential(work, client, pool):
    with pytest.raises(WebSocketDisconnect), client.websocket_connect("/api/lina/events?token=wrong") as ws:
        ws.receive_json()
    with client.websocket_connect(_url()) as ws:
        assert _next(ws, pool)["events"] == []
