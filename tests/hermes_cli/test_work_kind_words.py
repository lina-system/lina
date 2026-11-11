"""A to-do line says ``작업`` (the old record since 11-06) or ``할 일`` (older lines); every reader of ``lina work``
takes both as the one to-do kind, new lines say ``작업``, and ``lina work json`` keeps printing ``할 일`` (lina#505).

Each test imports a record mixing both words into a SQLite work.db and reads it back through one reader."""

import datetime
import json

import pytest

from hermes_cli.work import ledger, store
from tests.hermes_cli._work_env import NOW, WorkEnv

AREA = {"id": "A1", "kind": "영역", "title": "Dev", "parent": "", "by": "alpha"}
GOAL = {"id": "G1", "kind": "목표", "title": "빌드 기기 정리", "parent": "A1", "by": "alpha", "check": "false",
        "done": "빌드 기기가 모두 가동된다", "basis": [{"text": "기기 정리해 줘", "at": NOW - 7200, "src": "fixture"}]}


def _todo(i: str, word: str, **extra) -> dict:
    return dict({"id": i, "kind": word, "title": f"기기 {i} 점검", "parent": "G1", "by": "alpha", "check": "false",
                 "done": "기기가 답한다", "why": "작업이 멈추지 않게"}, **extra)


@pytest.fixture
def work(tmp_path, monkeypatch):
    return WorkEnv(tmp_path, monkeypatch)


def _import(work, *todos: dict) -> None:
    src = work.tmp / "mixed.jsonl"
    base = {"state": "open", "created": NOW - 7200, "progress_at": NOW - 3600, "op": "add", "writer": "alpha",
            "links": [], "notes": []}
    src.write_text("".join(json.dumps(dict(base, **r), ensure_ascii=False) + "\n" for r in (AREA, GOAL, *todos)),
                   encoding="utf-8")
    work.ok("import", "--from", str(src))
    assert (work.work / "work.db").is_file()


def _raw_lines(i: str) -> list[dict]:
    db = ledger.open_db()
    try:
        return [row for row in (json.loads(line) for _, line in store.lines(db)) if row.get("id") == i]
    finally:
        db.close()


def _read_all() -> tuple[dict, list]:
    try:
        return ledger.read_all()
    finally:
        ledger.close()


def test_check_closes_both_words_and_writes_the_new_word(work):
    _import(work, _todo("T1", "할 일", check="true"), _todo("T2", "작업", check="true"))
    work.ok("check", "--quiet")
    recs, _ = _read_all()
    assert (recs["T1"]["state"], recs["T2"]["state"]) == ("done", "done")
    assert [row["kind"] for row in _raw_lines("T1")] == ["할 일", "작업"]


def test_list_shows_both_words_as_the_todo_kind(work):
    _import(work, _todo("T1", "할 일"), _todo("T2", "작업"))
    rows = [line for line in work.ok("list", "--kind", "todo").splitlines() if line[:2] in ("T1", "T2")]
    assert [line.split()[:2] for line in rows] == [["T1", "작업"], ["T2", "작업"]]


def test_board_lists_the_owner_todos_of_both_words(work):
    from hermes_cli.work import board
    _import(work, _todo("T1", "할 일", by="owner", **{"from": "alpha"}),
            _todo("T2", "작업", by="owner", **{"from": "alpha"}))
    work.ok("list")
    recs, order = _read_all()
    assert sorted(item["id"] for item, _ in board.mine(recs, order, datetime.date(2027, 1, 15))) == ["T1", "T2"]


def test_run_hands_off_todos_of_both_words(work):
    brief = work.tmp / "brief.md"
    brief.write_text("Check both machines\n", encoding="utf-8")
    _import(work, _todo("T1", "할 일"), _todo("T2", "작업"))
    work.ok("run", "submit", "T1", "T2", "--brief", str(brief))
    db = ledger.open_db()
    try:
        assert sorted(r[0] for r in db.execute("SELECT todo_id FROM run_todo").fetchall()) == ["T1", "T2"]
    finally:
        db.close()


def test_notify_warns_about_past_due_todos_of_both_words(work):
    from hermes_cli.work.notify import due_alerts
    _import(work, _todo("T1", "할 일", due="2020-01-01"), _todo("T2", "작업", due="2020-01-01"))
    work.ok("list")
    recs, _ = _read_all()
    assert sorted(r["id"] for r, c in due_alerts(list(recs.values())) if c["status"] == "due_over") == ["T1", "T2"]


def test_json_prints_the_kind_the_dashboard_reads_for_both_words(work):
    _import(work, _todo("T1", "할 일"), _todo("T2", "작업"))
    kinds = {item["id"]: item["kind"] for item in work.json("--all")["items"]}
    assert (kinds["T1"], kinds["T2"]) == ("할 일", "할 일")
