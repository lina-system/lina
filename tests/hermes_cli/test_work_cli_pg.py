"""``lina work`` end to end on the Postgres store (``work.store.backend: postgres``, lina#36): the copied record reads
back as the ledger's golden output, and a hand-off interrupted between record and worker resumes."""

import json

import pytest

from hermes_cli import store_migrations
from hermes_cli.work import run as runs
from tests.hermes_cli import work_pg_fixture
from tests.hermes_cli._work_env import FIXTURES, WorkEnv

pg_db = work_pg_fixture.pg_db
WORK_MIGRATION = next(p for p in store_migrations.migration_files() if p.name.endswith("_work.sql"))


@pytest.fixture
def work(tmp_path, monkeypatch, pg_db):
    # Drop the work schema and its ledger row, so the next connect runs the work migration again. The version is
    # read from the file name: the migration's number is the runner's, not this test's.
    version = WORK_MIGRATION.name.split("_", 1)[0]
    pg_db.run_sql("DROP SCHEMA IF EXISTS work CASCADE; DO $$ BEGIN IF to_regclass('memory.schema_migrations') "
                  f"IS NOT NULL THEN DELETE FROM memory.schema_migrations WHERE version = '{version}'; END IF; END $$")
    return WorkEnv(tmp_path, monkeypatch,
                   extra=f"  store: {{backend: postgres, psql: {json.dumps(pg_db.argv)}}}\n")


def test_golden_json_and_list_on_postgres(work):
    work.fixture()
    actual = work.json("--all")
    for item in actual["items"]:
        item.get("view", {}).pop("activity_at", None)
    assert actual == json.loads((FIXTURES / "work-json.txt").read_text(encoding="utf-8"))
    assert work.ok("list", "--all") == (FIXTURES / "work-list-all.txt").read_text(encoding="utf-8")


def test_import_from_a_work_db_keeps_its_projects_on_postgres(work):
    """lina#480 on Postgres: a work.db whose project table no longer matches the paths its lines name."""
    import sqlite3
    jsonl = work.tmp / "ledger.jsonl"
    lines = (FIXTURES / "ledger.jsonl").read_text(encoding="utf-8").splitlines()
    moved = lines[1].replace('"op": "add"', '"op": "edit"').replace("1_Projects/Fixture", "1_Goals/Fixture")
    jsonl.write_text("\n".join(lines + [moved]) + "\n", encoding="utf-8")
    db_file = work.tmp / "work.db"
    work.ok("migrate", "--from", str(jsonl), "--to", str(db_file))
    with sqlite3.connect(db_file) as conn:
        conn.execute("DELETE FROM project WHERE para_path = '1_Projects/Fixture'")
        conn.execute("UPDATE project SET para_path = replace(para_path, '1_Projects/', '1_Goals/')")
        projects = conn.execute("SELECT id, para_path FROM project ORDER BY id").fetchall()
    assert "새 줄 8개" in work.ok("import", "--from", str(db_file))
    assert "새 줄 0개" in work.ok("import", "--from", str(db_file))
    from hermes_cli.work import ledger
    db = ledger.open_db()
    try:
        assert [tuple(r) for r in db.execute("SELECT id, para_path FROM project ORDER BY id").fetchall()] == projects
    finally:
        db.close()


def test_import_refuses_a_work_db_with_an_unsafe_column_name_on_postgres(work):
    """lina#504 review: a work.db column name with a quote, a newline and a psql meta-command never reaches psql."""
    import sqlite3
    jsonl = work.tmp / "ledger.jsonl"
    jsonl.write_text((FIXTURES / "ledger.jsonl").read_text(encoding="utf-8"), encoding="utf-8")
    db_file = work.tmp / "work.db"
    work.ok("migrate", "--from", str(jsonl), "--to", str(db_file))
    marker = work.tmp / "ran"
    name = f'x"\n\\! touch {marker}\n'
    with sqlite3.connect(db_file) as conn:
        conn.execute('ALTER TABLE area ADD COLUMN "' + name.replace('"', '""') + '" TEXT')
    code, _, err = work.run("import", "--from", str(db_file))
    assert code != 0 and "area" in err
    assert not marker.exists()
    from hermes_cli.work import ledger, store
    db = ledger.open_db()
    try:
        counts = store.counts(db)
    finally:
        db.close()
    assert [counts[k] for k in ("area", "goal", "todo", "event")] == [0, 0, 0, 0]


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
