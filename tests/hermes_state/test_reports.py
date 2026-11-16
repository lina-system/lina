"""Reports as a store record (lina#263, ``hermes_state_reports.py``, migration ``0025_report.sql``): a report has a
title, a one-line summary, a body, its author agent, an optional card and goal, its times and its versions; an
edit keeps the old version readable; row-level security keeps every agent to its own reports.

These run on the real Postgres from tests/_fixtures/memory_store.py.
"""

from __future__ import annotations

import uuid
from contextlib import closing

import psycopg
import pytest

import hermes_state_reports as reports
from hermes_cli.store_migrations import MIGRATIONS_DIR, apply
from tests._fixtures.memory_store import _initdb_sql, _with_db

MIGRATION = "0025_report.sql"


@pytest.fixture
def alpha():
    with closing(reports.agent_connection("alpha")) as conn:
        yield conn


def _write(conn, **changes):
    fields = {"agent": "alpha", "title": "Disk check", "summary": "Two disks are near full; one needs a swap.",
              "body": "## Disks\n\n| disk | used |\n| --- | --- |\n| a | 91% |\n", **changes}
    return reports.save(conn, **fields)


def test_a_report_keeps_every_version_readable_and_its_newest_is_the_body(alpha):
    first = _write(alpha, card="t_card1", goal="g_goal1", session_id="s1")
    second = _write(alpha, report_id=first.report_id, title="Disk check (done)", summary="Both disks swapped.",
                    body="All disks are below 50%.")

    assert first.version == 1 and second.version == 2 and second.report_id == first.report_id
    assert reports.REPORT_ID.fullmatch(first.report_id)
    current = reports.get(alpha, first.report_id)
    assert (current.title, current.summary, current.body) == ("Disk check (done)", "Both disks swapped.",
                                                              "All disks are below 50%.")
    assert (current.agent, current.card, current.goal) == ("alpha", "t_card1", "g_goal1")
    assert current.updated_at >= current.created_at
    old = reports.get(alpha, first.report_id, version=1)
    assert (old.version, old.title, old.summary) == (1, "Disk check", "Two disks are near full; one needs a swap.")
    assert "| a | 91% |" in old.body
    assert [(v["version"], v["title"]) for v in reports.versions(alpha, first.report_id)] == [
        (1, "Disk check"), (2, "Disk check (done)")]


@pytest.mark.parametrize("changes,message", [
    ({"title": "  "}, "title"),
    ({"summary": ""}, "summary"),
    ({"summary": "x" * (reports.SUMMARY_MAX + 1)}, "summary"),
    ({"body": "\n"}, "body"),
    ({"report_id": "rep_000000000000"}, "no report"),
    ({"report_id": "../etc"}, "not a report id"),
])
def test_a_report_that_cannot_be_written_as_asked_is_refused(alpha, changes, message):
    with pytest.raises(reports.ReportError, match=message):
        _write(alpha, **changes)


def test_a_summary_is_kept_as_one_line(alpha):
    report = _write(alpha, summary="Two disks\nare near   full.")
    assert report.summary == "Two disks are near full."


def test_another_agent_neither_reads_nor_edits_a_report_and_no_agent_deletes_a_version(alpha):
    report = _write(alpha)
    with closing(reports.agent_connection("beta")) as beta:
        assert reports.get(beta, report.report_id) is None
        assert reports.listing(beta) == []
        with pytest.raises(reports.ReportError, match="no report"):
            _write(beta, agent="beta", report_id=report.report_id)
    with pytest.raises(psycopg.errors.InsufficientPrivilege):
        alpha.execute("DELETE FROM reports.report_version WHERE report_id = %s", (report.report_id,))
    with closing(reports.owner_connection()) as owner:
        assert [r.report_id for r in reports.listing(owner)] == [report.report_id]


def test_the_list_filters_by_card_and_goal_and_leaves_private_reports_out_unless_asked(alpha):
    on_card = _write(alpha, card="t_one", goal="g_one")
    _write(alpha, card="t_two", goal="g_one")
    private = _write(alpha, card="t_one", private=True)

    assert [r.report_id for r in reports.listing(alpha, card="t_one")] == [on_card.report_id]
    assert {r.report_id for r in reports.listing(alpha, card="t_one", include_private=True)} == {
        on_card.report_id, private.report_id}
    assert [r.report_id for r in reports.listing(alpha, card="t_one", goal="g_one")] == [on_card.report_id]
    assert len(reports.listing(alpha, goal="g_one")) == 2
    still = _write(alpha, report_id=private.report_id, private=False)
    assert still.private, "a report once private stays private"


@pytest.fixture
def fresh_db(_memory_store):
    owner = _memory_store.superuser_dsn()
    name = f"lina_report_{uuid.uuid4().hex[:10]}"
    with psycopg.connect(_with_db(owner, "postgres"), autocommit=True) as admin:
        admin.execute(f"CREATE DATABASE {name}")
    dsn = _with_db(owner, name)
    with psycopg.connect(dsn, autocommit=True) as conn:
        conn.execute(_initdb_sql())
    yield dsn
    with psycopg.connect(_with_db(owner, "postgres"), autocommit=True) as admin:
        admin.execute(f"DROP DATABASE IF EXISTS {name} WITH (FORCE)")


def test_the_migration_applies_once_runs_again_unchanged_and_gives_both_tables_agent_rows(fresh_db):
    def run(conn):
        def sql(text: str) -> str:
            cur = conn.execute(text)
            return "\n".join("|".join(str(v) for v in row) for row in cur.fetchall()) if cur.description else ""
        return sql

    with psycopg.connect(fresh_db, autocommit=True) as conn:
        assert MIGRATION in apply(run(conn), out=lambda _line: None)
        assert apply(run(conn), out=lambda _line: None) == []
        conn.execute((MIGRATIONS_DIR / MIGRATION).read_text(encoding="utf-8"))
        policies = conn.execute("SELECT c.relname, c.relforcerowsecurity FROM pg_policy p "
                                "JOIN pg_class c ON c.oid = p.polrelid JOIN pg_namespace n ON n.oid = c.relnamespace "
                                "WHERE n.nspname = 'reports' AND p.polname = 'lina_agent_rows' ORDER BY 1").fetchall()
    assert policies == [("report", True), ("report_version", True)]
