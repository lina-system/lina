"""The readers of the work record outside ``lina work`` take a to-do line saying ``작업`` or ``할 일`` as one to-do
kind (lina#505): the app's item list and detail, the app's writes, the reply pushes, the sign-in question and
``hermes kanban plan``. Each test imports a record whose T1 says ``할 일`` (an older line) and T2 says ``작업``."""

from __future__ import annotations

import argparse
import json

import pytest
from fastapi.testclient import TestClient

from hermes_cli import lina_ocx
from tests.hermes_cli._work_env import NOW, WorkEnv

AREA = {"id": "A1", "kind": "영역", "title": "Dev", "parent": "", "by": "alpha"}
GOAL = {"id": "G1", "kind": "목표", "title": "빌드 기기 정리", "parent": "A1", "by": "alpha", "check": "false",
        "done": "빌드 기기가 모두 가동된다", "basis": [{"text": "기기 정리해 줘", "at": NOW - 7200, "src": "fixture"}]}


def _todo(i: str, word: str) -> dict:
    return {"id": i, "kind": word, "title": f"기기 {i} 점검", "parent": "G1", "by": "alpha", "check": "false",
            "done": "기기가 답한다", "why": "작업이 멈추지 않게"}


@pytest.fixture
def work(tmp_path, monkeypatch):
    env = WorkEnv(tmp_path, monkeypatch)
    monkeypatch.setattr(lina_ocx, "lina_root", lambda: env.root)
    src = tmp_path / "mixed.jsonl"
    base = {"state": "open", "created": NOW - 7200, "progress_at": NOW - 3600, "op": "add", "writer": "alpha",
            "links": [], "notes": []}
    rows = (AREA, GOAL, _todo("T1", "할 일"), _todo("T2", "작업"))
    src.write_text("".join(json.dumps(dict(base, **r), ensure_ascii=False) + "\n" for r in rows), encoding="utf-8")
    env.ok("import", "--from", str(src))
    return env


@pytest.fixture
def client():
    from hermes_cli import web_server
    test_client = TestClient(web_server.app)
    test_client.headers[web_server._SESSION_HEADER_NAME] = web_server._SESSION_TOKEN
    return test_client


def _record() -> dict:
    from hermes_cli.work import env, ledger
    env.reset()
    try:
        recs, _ = ledger.read_all()
        return {i: dict(r) for i, r in recs.items()}
    finally:
        ledger.close()


def test_the_app_lists_and_details_todos_of_both_words(work, client):
    listing = client.get("/api/lina/work/items")
    assert listing.status_code == 200, listing.text
    assert {row["id"]: row["kind"] for row in listing.json()["items"]} == {"G1": "goal", "T1": "todo", "T2": "todo"}
    assert {i: client.get(f"/api/lina/work/items/{i}").status_code for i in ("T1", "T2")} == {"T1": 200, "T2": 200}
    goal = client.get("/api/lina/work/items/G1").json()
    assert [kid["id"] for kid in goal["children"]] == ["T1", "T2"]


def test_the_app_comments_on_todos_of_both_words(work, client):
    for i in ("T1", "T2"):
        response = client.post(f"/api/lina/work/items/{i}/comments", json={"text": "로그도 남겨 줘"})
        assert response.status_code == 200, (i, response.text)
    assert [len(_record()[i]["comments"]) for i in ("T1", "T2")] == [1, 1]


def test_replies_on_todos_of_both_words_are_pushed(work):
    from hermes_cli.lina_push_watch import reply_keys
    for i in ("T1", "T2"):
        work.ok("comment", i, "로그도 남겨 줘")
        work.ok("reply", i, "c1", "로그를 남겼다", "--result", "applied")
    assert reply_keys(work.root) == {"reply:T1:c1", "reply:T2:c1"}


def test_the_sign_in_question_is_a_new_todo_line_in_the_new_word(work):
    from hermes_cli.lina_sign_in import ask_person
    from hermes_cli.work import ledger, store
    asked = ask_person(work.root, "hub", "codex", "two_step")
    assert asked == "T3"
    assert ask_person(work.root, "hub", "codex", "two_step") == "T3"
    db = ledger.open_db()
    try:
        kinds = [json.loads(line)["kind"] for _, line in store.lines(db) if json.loads(line).get("id") == "T3"]
    finally:
        db.close()
    assert kinds == ["작업"]


def test_a_plan_takes_an_existing_todo_of_either_word_and_a_rerun_adds_nothing(work, monkeypatch, capsys):
    from hermes_cli import kanban_db as kb
    from hermes_cli.kanban import kanban_command
    from hermes_cli.kanban_parser import build_parser
    for profile in ("alpha", "beta"):
        (work.root / "profiles" / profile).mkdir(parents=True)
        (work.root / "profiles" / profile / "config.yaml").write_text("{}\n", encoding="utf-8")
    monkeypatch.setenv("HERMES_PROFILE", "alpha")
    kb._INITIALIZED_PATHS.clear()
    kb.init_db()
    new = {"title": "기기 3 점검", "why": "작업이 멈추지 않게", "done": "기기가 답한다", "check": "false"}
    card = {"key": "other", "title": "Card other", "assignee": "beta", "todos": ["[ ] other work"],
            "ledger_todos": ["T1", "T2", new], "outcome": "Done.", "verification": "Checked.", "stop_when": "Done.",
            "worker": "omo", "gates": ["hermes kanban gate prs"], "max_turns": 5, "max_runtime": "1h"}
    plan = work.tmp / "plan.json"
    plan.write_text(json.dumps({"key": "mixed-plan", "goal": {"title": "Machines tidy", "assignee": "alpha",
                                                              "ledger_goal": "G1"}, "cards": [card]}),
                    encoding="utf-8")
    parser = argparse.ArgumentParser()
    build_parser(parser.add_subparsers())
    for _ in range(2):
        capsys.readouterr()
        assert kanban_command(parser.parse_args(["kanban", "plan", str(plan), "--json"])) == 0, capsys.readouterr()
    titles = [r["title"] for r in _record().values() if r["kind"] in ("작업", "할 일")]
    assert sorted(titles) == ["기기 3 점검", "기기 T1 점검", "기기 T2 점검"]
