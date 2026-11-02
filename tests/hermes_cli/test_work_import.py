"""``lina work import --from <work.db | ledger.jsonl>``: the record is copied into the store idempotently, the source is
only read, and the copy is checked by row counts per table and by every item's latest record (lina#36)."""

import os
import sqlite3

import pytest

from hermes_cli.work import ledger, store
from tests.hermes_cli._work_env import FIXTURES, WorkEnv


@pytest.fixture
def work(tmp_path, monkeypatch):
    return WorkEnv(tmp_path, monkeypatch)


def _source(work):
    src = work.tmp / "ledger.jsonl"
    src.write_text((FIXTURES / "ledger.jsonl").read_text(encoding="utf-8"), encoding="utf-8")
    return src


def _counts():
    db = ledger.open_db()
    try:
        return store.counts(db)
    finally:
        db.close()


def _fingerprint(path):
    st = os.stat(path)
    return path.read_bytes(), st.st_mtime_ns


def test_import_from_jsonl_is_idempotent_and_reads_only(work):
    src = _source(work)
    before = _fingerprint(src)
    first = work.ok("import", "--from", str(src))
    assert "event" in first and "다름" not in first
    counts = _counts()
    assert (counts["area"], counts["goal"], counts["todo"], counts["event"]) == (1, 2, 2, 7)
    second = work.ok("import", "--from", str(src))
    assert _counts() == counts
    assert "새 줄 0개" in second
    assert _fingerprint(src) == before


def test_import_continues_after_new_source_lines(work):
    src = _source(work)
    work.ok("import", "--from", str(src))
    with open(src, "a", encoding="utf-8") as fh:
        fh.write('{"id": "M2", "kind": "메모", "title": "Second memo", "parent": "A1", "by": "alpha", '
                 '"state": "open", "created": 1799992800, "op": "add", "writer": "alpha", "links": [], "notes": []}\n')
    out = work.ok("import", "--from", str(src))
    assert "새 줄 1개" in out
    assert [i["id"] for i in work.json("--all")["items"]][-1] == "M2"


def test_import_from_a_work_db_copies_lines_runs_and_pairs(work):
    jsonl = _source(work)
    db_file = work.tmp / "old" / "work.db"
    db_file.parent.mkdir()
    work.ok("migrate", "--from", str(jsonl), "--to", str(db_file))
    with sqlite3.connect(db_file) as conn:
        conn.execute("INSERT INTO run (id, runner, title, agent_id, created_at, ended_at, exit_code, result_path) "
                     "VALUES ('OT-ALP1101000000', 'omo', 'old run', 'alpha', '2027-01-15T07:00:00Z', "
                     "'2027-01-15T08:00:00Z', 0, '/results/old')")
        conn.execute("INSERT INTO run_todo (run_id, todo_id, closes, added_by, added_at) "
                     "VALUES ('OT-ALP1101000000', 'T1', 1, 'alpha', '2027-01-15T07:00:00Z')")
        conn.execute("DROP TABLE outbox")
    before = _fingerprint(db_file)
    work.ok("import", "--from", str(db_file))
    work.ok("import", "--from", str(db_file))
    counts = _counts()
    assert (counts["event"], counts["run"], counts["run_todo"]) == (7, 1, 1)
    assert _fingerprint(db_file) == before
    assert {i["id"]: i["title"] for i in work.json("--all")["items"]}["T1"] == "Updated fixture task"


def test_import_refuses_a_store_that_holds_a_different_record(work):
    src = _source(work)
    work.ok("import", "--from", str(src))
    other = work.tmp / "other.jsonl"
    lines = src.read_text(encoding="utf-8").splitlines()
    lines[0] = lines[0].replace('"Dev"', '"Life"')
    other.write_text("\n".join(lines) + "\n", encoding="utf-8")
    counts = _counts()
    code, _, err = work.run("import", "--from", str(other))
    assert code != 0 and "다르다" in err
    assert _counts() == counts


def test_importing_a_store_s_own_export_adds_nothing(work):
    work.goal_and_todos(todos=1)
    exported = work.tmp / "export.jsonl"
    work.ok("export-jsonl", "--to", str(exported))
    counts = _counts()
    assert "새 줄 0개" in work.ok("import", "--from", str(exported))
    assert _counts() == counts
