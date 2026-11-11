"""``GET /api/lina/work/items`` reads the store's views in one round trip to Postgres (lina#584).

Each ``execute`` on the Postgres store is one round trip to ``psql``; the list's seven reads were most of its time on
the hub. They now go as one statement, and give the same rows, in the same order, as the reads one by one.
"""

import json

import pytest

from hermes_cli import lina_board_items, lina_ocx, store_migrations
from tests.hermes_cli import work_pg_fixture
from tests.hermes_cli._work_env import WorkEnv

pytestmark = pytest.mark.platforms("posix")

pg_db = work_pg_fixture.pg_db
WORK_MIGRATION = next(p for p in store_migrations.migration_files() if p.name.endswith("_work.sql"))


class Counted:
    def __init__(self, db, dialect: str):
        self.db, self.dialect, self.calls = db, dialect, 0

    def execute(self, sql, params=()):
        self.calls += 1
        return self.db.execute(sql, params)


@pytest.fixture
def work(tmp_path, monkeypatch, pg_db):
    version = WORK_MIGRATION.name.split("_", 1)[0]
    pg_db.run_sql("DROP SCHEMA IF EXISTS work CASCADE; DO $$ BEGIN IF to_regclass('memory.schema_migrations') "
                  f"IS NOT NULL THEN DELETE FROM memory.schema_migrations WHERE version = '{version}'; END IF; END $$")
    env = WorkEnv(tmp_path, monkeypatch, extra=f"  store: {{backend: postgres, psql: {json.dumps(pg_db.argv)}}}\n")
    monkeypatch.setattr(lina_ocx, "lina_root", lambda: env.root)
    env.goal_and_todos(todos=3)
    brief = tmp_path / "brief.md"
    brief.write_text("Check the machine\n", encoding="utf-8")
    for todo in ("T2", "T1"):
        env.ok("run", "submit", todo, "--brief", str(brief))
    env.ok("wait", "T3", "--decide", "어느 기기부터 볼까?", "--choice", "1번", "--choice", "2번")
    return env


def test_the_store_views_come_back_in_one_round_trip_with_the_same_rows(work):
    from hermes_cli.work import env, ledger
    env.bind(env.load(work.root))
    try:
        one, each = Counted(ledger.db(), "postgres"), Counted(ledger.db(), "one by one")
        together, apart = lina_board_items._store(one), lina_board_items._store(each)
    finally:
        ledger.close()
        env.reset()

    assert one.calls == 1 and each.calls > 1
    assert together == apart
    assert all(together[name] for name in ("todo", "goal", "runs", "run_todo", "asks", "goal_events"))
    assert len(together["runs"]) == 2
    assert [run["id"] for run in together["runs"]] == [
        run["id"] for run in sorted(together["runs"], key=lambda run: (run["created_at"], run["id"]))]
