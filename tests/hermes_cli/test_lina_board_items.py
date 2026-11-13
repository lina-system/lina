"""``GET /api/lina/work/items`` and ``/api/lina/work/items/{id}`` (lina#166) over a scratch work record in the sqlite
store: every response validates against ``docs/api/board-client.yaml``, and state comes from the store's views."""

from __future__ import annotations

import datetime
import json
from pathlib import Path

import jsonschema
import pytest
from fastapi.testclient import TestClient

import hermes_yaml
from hermes_cli import lina_deps
from tests.hermes_cli._work_env import NOW, WorkEnv

pytestmark = pytest.mark.platforms("posix")

REPO = Path(__file__).resolve().parents[2]


def _jsonschema(node):
    """OpenAPI 3.0 ``nullable: true`` -> JSON Schema: ``type: [t, "null"]``, or ``anyOf`` with null on an ``allOf``."""
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
    env.brief = tmp_path / "brief.md"
    env.brief.write_text("Check the machine and write out/report.txt\n", encoding="utf-8")
    return env


@pytest.fixture
def client():
    from hermes_cli import web_server
    test_client = TestClient(web_server.app)
    test_client.headers[web_server._SESSION_HEADER_NAME] = web_server._SESSION_TOKEN
    return test_client


def _list(client, **params) -> dict:
    response = client.get("/api/lina/work/items", params=params)
    assert response.status_code == 200, response.text
    return _valid("ItemList", response.json())


def _detail(client, item_id: str) -> dict:
    response = client.get(f"/api/lina/work/items/{item_id}")
    assert response.status_code == 200, response.text
    return _valid("ItemDetail", response.json())


def _view_states(work: WorkEnv) -> dict:
    from hermes_cli.work import ledger
    db = ledger.open_db()
    try:
        return dict(db.execute("SELECT id, status FROM v_todo UNION ALL SELECT id, status FROM v_goal").fetchall())
    finally:
        db.close()


def _job(work: WorkEnv, run_id: str, **fields) -> None:
    path = work.root / "worker" / "hub" / "jobs" / f"{run_id}.json"
    job = json.loads(path.read_text(encoding="utf-8"))
    job.update(fields)
    path.write_text(json.dumps(job), encoding="utf-8")
    work.ok("run-sync")


def _record(work: WorkEnv) -> None:
    """G1 (alpha, area Dev) with T1, T2 and a dropped T3; the owner's own T4 that beta asked for; T1 closed by its
    check; an area A1 and a memo M1 that are never served."""
    work.goal_and_todos(todos=3)
    work.ok("drop", "T3", "--quote", "그건 빼자")
    work.ok("add", "할 일", "--by", "owner", "--from", "beta", "--title", "회의 자료 보기", "--why", "기록용",
            "--done", "확인했다")
    work.ok("add", "메모", "--by", "alpha", "--area", "Dev", "--title", "기기 목록", "--basis", "나중에 볼 것")
    work.ok("edit", "T1", "--check", "true")


def test_list_serves_open_and_recently_closed_goals_and_todos_with_state_from_the_store(work, client):
    _record(work)

    listing = _list(client)

    assert listing["errors"] == []
    assert listing["owner"] == {"id": "owner", "name": "주인"}
    assert listing["agents"] == [{"id": "alpha", "name": "알파"}, {"id": "beta", "name": "베타"}]
    rows = {row["id"]: row for row in listing["items"]}
    assert list(rows) == ["G1", "T1", "T2", "T4"]
    views = _view_states(work)
    assert {i: row["state"] for i, row in rows.items()} == {i: views[i] for i in rows}
    assert (rows["T1"]["state"], rows["T1"]["closedAt"]) == ("done", NOW)
    assert (rows["T4"]["mine"], rows["T4"]["agent"], rows["T4"]["parent"], rows["T4"]["area"]) == (True, "beta", None,
                                                                                                     None)
    assert (rows["T2"]["mine"], rows["T2"]["agent"], rows["T2"]["parent"], rows["T2"]["area"]) == (False, "alpha", "G1",
                                                                                                     "Dev")
    added_by = {row["id"]: row["added_by"] for row in work.json("--all")["items"]}
    assert {i: row["addedBy"] for i, row in rows.items()} == {i: added_by[i] for i in rows}
    goal = _detail(client, "G1")
    assert goal["todos"] == rows["G1"]["todos"] == {"done": 1, "total": 2}
    assert goal["todos"]["total"] == len([kid for kid in goal["children"] if kid["kind"] == "todo"])
    assert goal["todos"]["done"] == len([kid for kid in goal["children"] if kid["state"] == "done"])

    assert [row["id"] for row in _list(client, agent="beta")["items"]] == ["T4"]
    assert [row["id"] for row in _list(client, agent="alpha")["items"]] == ["G1", "T1", "T2"]
    work.clock[0] += 4 * 86400
    assert [row["id"] for row in _list(client)["items"]] == ["G1", "T2", "T4"]


def test_a_todo_is_in_progress_only_while_a_live_run_is_on_it(work, client):
    work.goal_and_todos(todos=2)
    work.ok("note", "T1", "기기 1 점검을 시작했다")
    assert _detail(client, "T1")["state"] == "planned"

    work.ok("run", "submit", "T1", "--brief", str(work.brief))
    (run_id,) = [p.stem for p in (work.root / "worker" / "hub" / "jobs").glob("*.json")]
    started = int(datetime.datetime.now(datetime.UTC).timestamp()) - 60  # the store's views read the real clock
    _job(work, run_id, state="running", started=started, host="here")

    todo, goal = _detail(client, "T1"), _detail(client, "G1")
    assert (todo["state"], todo["reason"], todo["stateSince"], todo["runCount"]) == ("in_progress", None, started, 1)
    assert [(run["id"], run["state"], run["startedAt"], run["endedAt"]) for run in todo["runs"]] == [
        (run_id, "in_progress", started, None)]
    assert (goal["state"], goal["runCount"], [run["id"] for run in goal["runs"]]) == ("in_progress", 1, [run_id])
    assert _detail(client, "T2")["state"] == "planned"

    _job(work, run_id, state="done", delivery="ok", ended=started + 30)
    todo = _detail(client, "T1")
    assert (todo["state"], todo["reason"], todo["stateSince"]) == ("waiting", "review", started + 30)
    assert todo["state"] == _view_states(work)["T1"]


def test_detail_carries_the_owner_s_words_threads_decision_links_and_owner_done_mark(work, client):
    from hermes_cli.work.cmd_comments import OWNER_UNDO_LIMIT
    work.goal_and_todos(todos=2)
    work.ok("comment", "T1", "1번 기기부터 봐 줘")
    work.ok("reply", "T1", "c1", "1번부터 봤다", "--result", "applied", "--by", "alpha")
    work.ok("comment", "T1", "로그도 남겨 줘", "--by", "beta")
    work.ok("wait", "T2", "--decide", "어느 기기부터 볼까?", "--choice", "1번", "--choice", "2번", "--recommend", "1번")
    work.ok("link", "T1", "gh:lina-system/lina#166", "https://example.com/build", "kanban:t_0123abcd")

    goal, t1, t2 = _detail(client, "G1"), _detail(client, "T1"), _detail(client, "T2")

    assert [(b["text"], b["at"]) for b in goal["basis"]] == [("기기 정리해 줘", None)]
    assert (t1["why"], t1["doneCondition"]) == ("작업이 멈추지 않게", "기기가 답한다")
    assert [(c["cid"], c["by"], c["at"], c["text"], c["status"], c["kind"]) for c in t1["comments"]] == [
        ("c1", "owner", NOW, "1번 기기부터 봐 줘", "resolved", "comment"),
        ("c2", "beta", NOW, "로그도 남겨 줘", "open", "comment")]
    assert t1["comments"][0]["replies"] == [{"by": "alpha", "at": NOW, "text": "1번부터 봤다", "result": "applied"}]
    assert t1["commentCount"] == len(t1["comments"]) == 2
    assert [(link["kind"], link["url"]) for link in t1["links"]] == [
        ("gh_ref", "https://github.com/lina-system/lina/issues/166"), ("url", "https://example.com/build"),
        ("kanban", None)]
    assert t1["workCards"] == goal["workCards"] == ["t_0123abcd"]
    assert t2["decision"] == {"text": "어느 기기부터 볼까?", "kind": "ask", "choices": ["1번", "2번"],
                              "recommended": "1번", "cid": None, "at": NOW, "answered": False}
    assert (t2["state"], t2["reason"]) == ("waiting", "decision")
    assert t1["decision"] is None and goal["ownerDone"] is None

    work.clock[0] += 60
    work.ok("owner-done", "G1", "--with-open")
    goal, t1 = _detail(client, "G1"), _detail(client, "T1")
    assert goal["ownerDone"] == {"at": NOW + 60, "undoUntil": NOW + 60 + OWNER_UNDO_LIMIT, "with": ["T1", "T2"]}
    assert t1["ownerDone"] == {"at": NOW + 60, "undoUntil": NOW + 60 + OWNER_UNDO_LIMIT, "with": []}
    assert (goal["state"], goal["closedAt"], goal["stateSince"]) == ("done", NOW + 60, NOW + 60)


def test_an_unknown_id_an_area_a_memo_and_a_dropped_item_answer_404(work, client):
    _record(work)
    assert {"A1", "M1", "T3"} <= {row["id"] for row in work.json("--all")["items"]}
    assert {i: client.get(f"/api/lina/work/items/{i}").status_code for i in ("T99", "A1", "M1", "T3", "t2")} == {
        "T99": 404, "A1": 404, "M1": 404, "T3": 404, "t2": 200}


@pytest.mark.parametrize("path", ["/api/lina/work/items", "/api/lina/work/items/G1"])
def test_each_route_answers_304_until_its_data_changes_and_sits_behind_the_auth_gate(work, client, path):
    from hermes_cli import web_server
    work.goal_and_todos()
    assert TestClient(web_server.app).get(path).status_code == 401

    first = client.get(path)
    assert first.status_code == 200
    work.clock[0] += 1  # a later snapshot time alone is no change
    again = client.get(path, headers={"If-None-Match": first.headers["etag"]})
    assert again.status_code == 304 and again.headers["etag"] == first.headers["etag"]

    work.ok("comment", "T1", "다시 봐 줘")
    changed = client.get(path, headers={"If-None-Match": first.headers["etag"]})
    assert changed.status_code == 200 and changed.headers["etag"] != first.headers["etag"]


def test_a_record_that_fails_to_read_is_an_error_and_the_list_is_empty(work, client):
    (work.work / "work.db").mkdir(parents=True)  # the record cannot be opened

    listing = _list(client)

    assert [error["source"] for error in listing["errors"]] == ["work"]
    assert (listing["owner"], listing["agents"], listing["items"]) == (None, [], [])
    response = client.get("/api/lina/work/items/G1")
    assert response.status_code == 503 and response.json()["detail"]
