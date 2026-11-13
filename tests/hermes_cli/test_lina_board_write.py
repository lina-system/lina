"""The owner's writes and read marks through the client API (lina#149) over a scratch work record: each route runs the
``lina work`` command the CLI runs, says the action came from the app, tells the owning agent, refuses bad input with a
4xx that writes nothing, is rate-limited per client, and every response validates against
``docs/api/board-client.yaml``."""

from __future__ import annotations

import itertools
from pathlib import Path

import jsonschema
import pytest
from fastapi.testclient import TestClient

import hermes_yaml
from hermes_cli import lina_board, lina_deps
from tests.hermes_cli._work_env import NOW, WorkEnv

pytestmark = pytest.mark.platforms("posix")

REPO = Path(__file__).resolve().parents[2]
_CLIENTS = itertools.count()


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
    monkeypatch.setattr(lina_deps, "lina_root", lambda: env.root)
    monkeypatch.setattr(lina_board, "devices_read", lambda root, now, errors: [])
    monkeypatch.setattr(lina_board, "quota_read", lambda root, now: [])
    (env.root / "worker" / "hub" / "jobs").mkdir(parents=True)
    env.goal_and_todos(todos=2)
    return env


def _client(**kwargs) -> TestClient:
    """A signed-in client with an address of its own (the write limit counts per client)."""
    from hermes_cli import web_server
    test_client = TestClient(web_server.app, client=(f"client-{next(_CLIENTS)}", 50000), **kwargs)
    test_client.headers[web_server._SESSION_HEADER_NAME] = web_server._SESSION_TOKEN
    return test_client


@pytest.fixture
def client():
    return _client()


def _lines(work: WorkEnv) -> int:
    from hermes_cli.work import ledger
    db = ledger.open_db()
    try:
        return db.execute("SELECT count(*) FROM event").fetchone()[0]
    finally:
        db.close()


def _record(work: WorkEnv, item_id: str) -> dict:
    return next(row for row in work.json("--all")["items"] if row["id"] == item_id)


def _act(client, path: str, body=None, status: int = 200) -> dict:
    response = client.post(path, json=body) if body is not None else client.post(path)
    assert response.status_code == status, response.text
    return _valid("ActionResult", response.json()) if status == 200 else response.json()


def test_done_and_undo_run_the_owner_s_marks_from_the_app_and_tell_the_agent(work, client):
    from hermes_cli.work.cmd_comments import OWNER_UNDO_LIMIT
    before = _lines(work)
    refused = _act(client, "/api/lina/work/items/G1/done", {}, 409)
    assert "T1" in refused["detail"] and _lines(work) == before

    done = _act(client, "/api/lina/work/items/T1/done")
    assert done == {"ok": True, "id": "T1", "state": "done", "reason": None, "cid": None,
                    "undoUntil": NOW + OWNER_UNDO_LIMIT}
    assert _record(work, "T1")["owner_done"]["via"] == "app"
    assert "T1" in work.notices("alpha")

    undone = _act(client, "/api/lina/work/items/T1/undo")
    assert (undone["state"], undone["undoUntil"]) == ("planned", None)
    assert _record(work, "T1")["owner_undo"]["via"] == "app"

    goal = _act(client, "/api/lina/work/items/G1/done", {"withOpen": True})
    assert goal["state"] == "done" and _record(work, "G1")["owner_done"]["with"] == ["T1", "T2"]
    work.clock[0] += OWNER_UNDO_LIMIT + 1
    before = _lines(work)
    assert "10" in _act(client, "/api/lina/work/items/G1/undo", status=409)["detail"]
    assert _lines(work) == before


def test_comments_answers_and_retries_are_the_cli_s_comments_from_the_app(work, client):
    first = _act(client, "/api/lina/work/items/T1/comments", {"text": "  1번 기기부터 봐 줘  "})
    assert (first["cid"], first["state"]) == ("c1", "planned")
    row = _record(work, "T1")["comments"][0]
    assert (row["by"], row["text"], row["via"]) == ("owner", "1번 기기부터 봐 줘", "app")
    assert "c1" in work.notices("alpha")

    before = _lines(work)
    assert _act(client, "/api/lina/work/items/T1/comments", {"text": "줄이자", "answer": True}, 409)["detail"]
    assert _lines(work) == before
    work.ok("wait", "T1", "--decide", "어느 기기부터 볼까?")
    answer = _act(client, "/api/lina/work/items/T1/comments", {"text": "1번", "answer": True})
    assert (answer["cid"], answer["state"], answer["reason"]) == ("c2", "waiting", "decision")
    assert _record(work, "T1")["comments"][1]["kind"] == "answer"

    again = _act(client, "/api/lina/work/items/T2/retry")
    retry = _record(work, "T2")["comments"][0]
    assert (again["cid"], retry["by"], retry["via"]) == ("c1", "owner", "app") and retry["text"]
    assert "T2 c1" in work.notices("alpha")


def test_quick_add_hands_one_line_to_the_chosen_agent(work, client):
    added = _act(client, "/api/lina/work/add", {"agent": "beta", "text": "백업 확인해 줘", "due": "2027-01-20"})
    memo = _record(work, added["id"])
    assert (memo["kind"], memo["by"], memo["from"], memo["via"], memo["due"]) == ("메모", "beta", "owner", "app",
                                                                                 "2027-01-20")
    assert [b["text"] for b in memo["basis"]] == ["백업 확인해 줘"]
    assert added["id"] in work.notices("beta")


@pytest.mark.parametrize(("path", "body", "status"), [
    ("/api/lina/work/items/T99/done", {}, 404),
    ("/api/lina/work/items/A1/comments", {"text": "x"}, 404),
    ("/api/lina/work/items/T1/comments", {"text": "   "}, 400),
    ("/api/lina/work/items/T1/comments", {"text": "x" * 2001}, 400),
    ("/api/lina/work/items/T1/comments", {"text": "x", "due": "2027-01-20"}, 400),
    ("/api/lina/work/items/T1/done", {"due": "2027-01-20"}, 400),
    ("/api/lina/work/add", {"agent": "beta", "text": "x", "due": "next week"}, 400),
    ("/api/lina/work/add", {"agent": "nobody", "text": "x"}, 400),
    ("/api/lina/work/add", {"agent": "beta"}, 400),
    ("/api/lina/work/items/T1/comments", {"text": None}, 400),
    ("/api/lina/work/items/T1/comments", {"text": 5}, 400),
    ("/api/lina/work/items/T1/comments", {"text": ["x"]}, 400),
    ("/api/lina/work/add", {"agent": "beta", "text": None}, 400),
    ("/api/lina/work/add", {"agent": None, "text": "x"}, 400),
    ("/api/lina/work/add", {"agent": "beta", "text": 7}, 400),
    ("/api/lina/work/add", {"agent": "beta", "text": ["x"]}, 400),
])
def test_bad_input_is_refused_with_one_line_and_writes_nothing(work, client, path, body, status):
    before = _lines(work)
    response = client.post(path, json=body)
    assert response.status_code == status
    assert isinstance(response.json()["detail"], str)
    assert _lines(work) == before and work.notices("alpha") == work.notices("beta") == ""


def _pending_hand_off(work: WorkEnv, monkeypatch, todo: str) -> str:
    """A ``work run submit`` that crashed before the worker call, so the next command resumes it first."""
    from hermes_cli.work import ledger
    from hermes_cli.work import run as runs
    brief = work.root / f"brief-{todo}.md"
    brief.write_text("Check both machines and write out/report.txt\n", encoding="utf-8")
    real = runs._call_worker
    monkeypatch.setattr(runs, "_call_worker", lambda *_a, **_k: (_ for _ in ()).throw(RuntimeError("died")))
    with pytest.raises(RuntimeError):
        work.run("run", "submit", todo, "--brief", str(brief))
    monkeypatch.setattr(runs, "_call_worker", real)
    db = ledger.open_db()
    try:
        return db.execute("SELECT run_id FROM outbox WHERE done_at IS NULL").fetchone()[0]
    finally:
        db.close()


def test_a_hand_off_resumed_first_does_not_stand_in_for_the_new_memo_or_comment(work, client, monkeypatch):
    from hermes_cli.work import ledger

    def resumed(run_id: str) -> bool:
        db = ledger.open_db()
        try:
            return db.execute("SELECT done_at FROM outbox WHERE run_id = ?", (run_id,)).fetchone()[0] is not None
        finally:
            db.close()

    run_id = _pending_hand_off(work, monkeypatch, "T1")
    added = _act(client, "/api/lina/work/add", {"agent": "beta", "text": "백업 확인해 줘"})
    assert resumed(run_id) and added["id"] != run_id
    assert _record(work, added["id"])["kind"] == "메모"

    run_id = _pending_hand_off(work, monkeypatch, "T2")
    said = _act(client, "/api/lina/work/items/T1/comments", {"text": "1번 기기부터 봐 줘"})
    assert resumed(run_id) and (said["id"], said["cid"]) == ("T1", "c1")


def test_writes_are_behind_the_auth_gate_and_limited_per_client(work, client):
    from hermes_cli import web_server
    assert TestClient(web_server.app).post("/api/lina/work/items/T1/done").status_code == 401
    codes = [client.post("/api/lina/notices/read", json={}).status_code for _ in range(31)]
    assert codes == [400] * 30 + [429]
    before = _lines(work)
    assert client.post("/api/lina/work/items/T1/done").status_code == 429 and _lines(work) == before
    assert _client().post("/api/lina/notices/read", json={}).status_code == 400  # another client has its own limit


def test_read_marks_are_kept_per_owner_and_shown_on_the_board(work, client):
    work.ok("wait", "T1", "--decide", "색을 고를까?")
    work.ok("add", "할 일", "--by", "owner", "--from", "alpha", "--title", "회의 자료 보기", "--why", "기록용",
            "--done", "확인했다")

    def board(test_client=client):
        return _valid("BoardSnapshot", test_client.get("/api/lina/board").json())
    notes = board()["notices"]
    assert len(notes) == 2 and board()["counts"]["unread"] == 2 and not any(n["read"] for n in notes)

    marked = _valid("ReadResult", client.post("/api/lina/notices/read", json={"keys": [notes[0]["key"]]}).json())
    assert marked == {"ok": True, "unread": 1}
    seen = board(_client())
    assert {n["key"]: n["read"] for n in seen["notices"]} == {notes[0]["key"]: True, notes[1]["key"]: False}
    assert seen["counts"]["unread"] == 1

    for body in ({"keys": ["todo:T9:1"]}, {"keys": [notes[1]["key"]], "all": True}, {"all": False}):
        assert client.post("/api/lina/notices/read", json=body).status_code == 400
    assert _valid("ReadResult", client.post("/api/lina/notices/read", json={"all": True}).json())["unread"] == 0
    assert board()["counts"]["unread"] == 0
