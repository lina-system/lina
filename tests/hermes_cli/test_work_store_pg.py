"""Postgres work-store behavior contracts (lina#36), exercised against a real server."""

from pathlib import Path

import pytest

from hermes_cli import store_migrations
from hermes_cli.work import store, store_sqlite
from hermes_cli.work.store_pg import PgDb, PgError
from tests.hermes_cli import work_pg_fixture

pg_db = work_pg_fixture.pg_db


def test_migration_applies_once_when_repeated(pg_db: PgDb):
    # Given a database without the work migration.
    expected = [v for v, _ in store_migrations.pending(pg_db.run_sql)]
    # When the runner applies the migration twice and the SQL itself is rerun.
    first = store_migrations.apply(pg_db.run_sql)
    second = store_migrations.apply(pg_db.run_sql)
    pg_db.run_sql((store_migrations.MIGRATIONS / "0001_work.sql").read_text(encoding="utf-8"))
    # Then only one ledger entry exists and both paths are idempotent.
    assert first == expected
    assert second == []
    assert pg_db.execute("SELECT count(*) FROM memory.schema_migrations WHERE version = '0001'").fetchone() == (1,)


def test_agent_tables_are_forced_rls_when_helper_exists(pg_db: PgDb):
    # Given the store's first-start RLS helper, when present.
    if pg_db.execute("SELECT to_regprocedure('public.lina_apply_agent_rls(regclass)') IS NULL").fetchone() == (True,):
        pytest.skip("Cluster does not provide the store RLS helper")
    # When all work tables carrying agent_id are inspected.
    rows = pg_db.execute("SELECT c.relname, c.relrowsecurity, c.relforcerowsecurity, "
                         "EXISTS (SELECT 1 FROM pg_policy p WHERE p.polrelid=c.oid AND p.polname='lina_agent_rows') "
                         "FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace "
                         "JOIN pg_attribute a ON a.attrelid=c.oid WHERE n.nspname='work' "
                         "AND c.relkind='r' AND a.attname='agent_id' AND NOT a.attisdropped").fetchall()
    # Then every such table is forced through the named policy.
    assert {row[0] for row in rows} == {"area", "project", "goal", "todo", "run", "wait", "decision", "link", "event", "outbox"}
    assert all(row[1:] == (True, True, True) for row in rows)


def test_lina_app_sees_own_rows_when_agent_is_set(pg_db: PgDb):
    # Given two agents' areas and the application's RLS role.
    if pg_db.execute("SELECT to_regprocedure('public.lina_apply_agent_rls(regclass)') IS NULL").fetchone() == (True,):
        pytest.skip("Cluster does not provide the store RLS helper")
    pg_db.begin()
    try:
        pg_db.execute("INSERT INTO actor VALUES ('a', 'agent', 'A'), ('b', 'agent', 'B')")
        pg_db.execute("INSERT INTO area (id,title,agent_id,created_by) VALUES ('a','A','a','a'), ('b','B','b','b')")
        pg_db.run_sql("SET LOCAL ROLE lina_app; SET LOCAL lina.agent_id = 'a'")
        # When reading as agent a.
        rows = pg_db.execute("SELECT id, agent_id FROM area ORDER BY id").fetchall()
        # Then agent b's row is hidden.
        assert rows == [("a", "a")]
    finally:
        pg_db.rollback()


def test_text_roundtrips_when_parameters_contain_special_characters(pg_db: PgDb):
    # Given text including SQL syntax, psql syntax, Unicode and escaped characters.
    text = "quote ' double \"\n한글 🐘 \\backslash\n\\echo fake; DROP TABLE work.actor; -- ?"
    # When the parameter crosses psql in both SELECT and RETURNING results.
    pg_db.execute("CREATE TEMP TABLE roundtrip (value text)")
    row = pg_db.execute("INSERT INTO roundtrip VALUES (?) RETURNING value", (text,)).fetchone()
    # Then both statement types preserve the exact value.
    assert row == (text,)
    assert pg_db.execute("SELECT value, '?' FROM roundtrip").fetchall() == [(text, "?")]


def test_connection_recovers_when_sql_error_aborts_transaction(pg_db: PgDb):
    # Given an open transaction in the persistent process.
    pg_db.begin()
    pg_db.execute("CREATE TEMP TABLE lost_transaction (id int)")
    # When SQL fails, ON_ERROR_STOP terminates that connection.
    with pytest.raises(PgError, match="does not exist"):
        pg_db.execute("SELECT * FROM table_that_does_not_exist")
    # Then the next statement reconnects and the transaction's table is gone.
    assert pg_db.execute("SELECT to_regclass('pg_temp.lost_transaction')").fetchone() == (None,)


def test_store_matches_sqlite_when_records_are_appended(pg_db: PgDb, tmp_path: Path):
    # Given one narrative including links, a run, a wait and a decision.
    records = [
        {"id": "A1", "kind": "영역", "title": "Area", "by": "owner", "op": "add"},
        {"id": "G1", "kind": "목표", "parent": "A1", "title": "Goal", "by": "owner", "op": "add",
         "links": ["gh:o/r#1"], "promise": True, "stall": "1d"},
        {"id": "T1", "kind": "할 일", "parent": "G1", "title": "Todo '한글'\\\n", "by": "owner", "op": "add",
         "links": ["kanban:t_0123abcd", "omo:OT-ABC123"], "wait": {"on": "answer", "since": 1800000000},
         "decide": "Choose", "decide_choices": ["yes", "no"]},
    ]
    sqlite = store_sqlite.connect(tmp_path / "w.db", "owner", {}, create=True)
    try:
        # When the same records pass through the public store API on both backends.
        for db in (pg_db, sqlite):
            store.seed(db, {})
            for record in records:
                info = {"labels": [None for _ in record.get("links", [])], "projects": [],
                        "gh_repo": lambda spec: tuple(spec.split("/", 1)) if "/" in spec else ("", spec),
                        "at": "2027-01-15T08:00:00Z"}
                store.append(db, record, info)
        # Then ledger records, order, lines, counts and the derived status agree exactly.
        assert store.read_all(pg_db) == ({r["id"]: r for r in records}, ["A1", "G1", "T1"])
        assert store.read_all(pg_db) == store.read_all(sqlite)
        assert store.lines(pg_db) == store.lines(sqlite)
        assert store.counts(pg_db) == store.counts(sqlite)
        assert pg_db.execute("SELECT status FROM v_todo WHERE id = 'T1'").fetchone() == \
            sqlite.execute("SELECT status FROM v_todo WHERE id = 'T1'").fetchone() == ("waiting",)
    finally:
        sqlite.close()
