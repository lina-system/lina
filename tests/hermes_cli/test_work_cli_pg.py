"""``lina work`` end to end on the Postgres store (``work.store.backend: postgres``, lina#36): the copied record reads
back as the ledger's golden output, and a hand-off interrupted between record and worker resumes."""

import json

import pytest

from hermes_cli.work import run as runs
from tests.hermes_cli._work_env import FIXTURES, WorkEnv
from tests.hermes_cli import work_pg_fixture

pg_db = work_pg_fixture.pg_db


@pytest.fixture
def work(tmp_path, monkeypatch, pg_db):
    pg_db.run_sql("DROP SCHEMA IF EXISTS work CASCADE; DO $$ BEGIN IF to_regclass('memory.schema_migrations') "
                  "IS NOT NULL THEN DELETE FROM memory.schema_migrations WHERE version = '0001'; END IF; END $$")
    return WorkEnv(tmp_path, monkeypatch,
                   extra=f"  store: {{backend: postgres, psql: {json.dumps(pg_db.argv)}}}\n")


def test_golden_json_and_list_on_postgres(work):
    work.fixture()
    actual = work.json("--all")
    for item in actual["items"]:
        item.get("view", {}).pop("activity_at", None)
    assert actual == json.loads((FIXTURES / "work-json.txt").read_text(encoding="utf-8"))
    assert work.ok("list", "--all") == (FIXTURES / "work-list-all.txt").read_text(encoding="utf-8")


def test_interrupted_hand_off_resumes_on_postgres(work, monkeypatch):
    work.goal_and_todos(todos=2)
    brief = work.tmp / "brief.md"
    brief.write_text("Check both machines\n", encoding="utf-8")
    real = runs._call_worker

    def crash(*_args, **_kwargs):
        raise RuntimeError("the process died before the worker call")
    monkeypatch.setattr(runs, "_call_worker", crash)
    with pytest.raises(RuntimeError):
        work.run("run", "submit", "T1", "T2", "--brief", str(brief))
    jobs = work.root / "worker" / "hub" / "jobs"
    assert not jobs.is_dir() or not list(jobs.glob("*.json"))
    monkeypatch.setattr(runs, "_call_worker", real)
    work.ok("list")
    (job,) = list(jobs.glob("*.json"))
    from hermes_cli.work import ledger
    db = ledger.open_db()
    try:
        pairs = db.execute("SELECT todo_id FROM run_todo WHERE run_id = ? ORDER BY todo_id", (job.stem,)).fetchall()
        pending = db.execute("SELECT count(*) FROM outbox WHERE done_at IS NULL").fetchone()
    finally:
        db.close()
    assert pairs == [("T1",), ("T2",)] and pending == (0,)
