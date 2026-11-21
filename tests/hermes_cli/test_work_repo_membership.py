"""Repository membership survives task edits and existing work stores."""

import json
import sqlite3
from contextlib import closing
from pathlib import Path

import pytest

from hermes_cli import store_migrations
from hermes_cli.work import env, ledger
from hermes_cli.work.store_pg import PgError
from tests.hermes_cli import work_pg_fixture
from tests.hermes_cli._work_env import WorkEnv

pg_db = work_pg_fixture.pg_db


@pytest.mark.parametrize("backend", ["sqlite", "postgres"])
def test_repo_membership_is_a_task_fk_and_survives_edits(tmp_path, monkeypatch, request, backend):
    extra = ""
    if backend == "postgres":
        pg = request.getfixturevalue("pg_db")
        work_pg_fixture.reset_work_schema(pg)
        extra = f"  store: {{backend: postgres, psql: {json.dumps(pg.argv)}}}\n"
    work = WorkEnv(tmp_path, monkeypatch, extra=extra)
    work.goal_and_todos()

    with closing(ledger.open_db()) as db:
        for name in ("album", "archive"):
            db.execute("INSERT INTO repo (id, area_id, name, forge_url) VALUES (?, ?, ?, ?)",
                       (f"A1/{name}", "A1", name, f"https://forge.example/{name}"))
        with pytest.raises((sqlite3.IntegrityError, PgError), match=r"(?i)foreign key"):
            db.execute("INSERT INTO todo_repo (todo_id, repo_id) VALUES (?, ?)", ("T1", "A1/missing"))
        with pytest.raises((sqlite3.IntegrityError, PgError), match=r"(?i)foreign key"):
            db.execute("INSERT INTO todo_repo (todo_id, repo_id) VALUES (?, ?)", ("T999", "A1/album"))

    task = work.ok("add", "할 일", "--by", "beta", "--goal", "G1", "--title", "Review notes",
                   "--why", "Keep the notes current", "--done", "The notes are current", "--check", "false",
                   "--omo", "next", "--repo", "A1/album").split()[0]
    work.ok("edit", "T1", "--repo", "A1/archive")
    work.ok("edit", task, "--title", "Review the latest notes")
    code, _, err = work.run("edit", task, "--repo", "A1/missing")
    assert code != 0 and "A1/missing" in err

    items = {item["id"]: item for item in work.json("--all")["items"]}
    assert items[task]["repo_id"] == "A1/album"
    with closing(ledger.open_db()) as db:
        assert db.execute("SELECT todo_id, repo_id FROM todo_repo ORDER BY todo_id").fetchall() == [
            ("T1", "A1/archive"), (task, "A1/album"),
        ]
        with pytest.raises((sqlite3.IntegrityError, PgError), match=r"(?i)(unique|duplicate)"):
            db.execute("INSERT INTO todo_repo (todo_id, repo_id) VALUES (?, ?)", (task, "A1/archive"))
    work.ok("edit", task, "--repo", "")
    with closing(ledger.open_db()) as db:
        assert db.execute("SELECT repo_id FROM todo_repo WHERE todo_id = ?", (task,)).fetchone() is None


def test_postgres_migration_preserves_existing_tasks_and_shares_registry(pg_db):
    work_pg_fixture.reset_work_schema(pg_db)
    initial_schema = store_migrations.MIGRATIONS_DIR / "0004_work.sql"
    pg_db.run_sql(store_migrations.migration_script(initial_schema))
    pg_db.run_sql("""
        INSERT INTO work.actor VALUES ('alpha', 'agent', 'Alpha');
        INSERT INTO work.area (id, title, agent_id, created_by) VALUES ('A1', 'Notes', 'alpha', 'alpha');
        INSERT INTO work.todo (id, title, agent_id, created_by, created_at)
        VALUES ('T1', 'Review the notes', 'alpha', 'alpha', now()),
               ('T2', 'Review the archive', 'alpha', 'alpha', now());
    """)

    store_migrations.apply(pg_db.run_sql)
    assert pg_db.execute("SELECT id, title FROM work.todo ORDER BY id").fetchall() == [
        ("T1", "Review the notes"), ("T2", "Review the archive"),
    ]
    assert pg_db.execute("SELECT todo_id FROM work.todo_repo").fetchall() == []
    pg_db.run_sql("SET ROLE lina_app")
    try:
        pg_db.run_sql("""
            INSERT INTO work.repo (id, area_id, name, forge_url)
            VALUES ('A1/album', 'A1', 'album', 'https://forge.example/album');
        """)
        assert pg_db.execute("SELECT id FROM work.repo").fetchall() == [("A1/album",)]
        assert pg_db.execute("SELECT row_security_active('work.repo')").fetchone() == (False,)
        pg_db.run_sql("""
            SET lina.agent_id = 'alpha';
            INSERT INTO work.todo_repo (todo_id, repo_id) VALUES ('T1', 'A1/album');
        """)
        assert pg_db.execute("SELECT todo_id FROM work.todo_repo").fetchall() == [("T1",)]
        pg_db.run_sql("SET lina.agent_id = 'beta'")
        assert pg_db.execute("SELECT id FROM work.repo").fetchall() == [("A1/album",)]
        assert pg_db.execute("SELECT todo_id FROM work.todo_repo").fetchall() == []
        with pytest.raises(PgError, match="row-level security"):
            pg_db.run_sql("INSERT INTO work.todo_repo (todo_id, repo_id) VALUES ('T2', 'A1/album')")
    finally:
        pg_db.run_sql("RESET ROLE; RESET lina.agent_id")


def test_legacy_sqlite_tasks_survive_import_and_in_place_upgrade(tmp_path, monkeypatch):
    def activate(work: WorkEnv) -> None:
        monkeypatch.setenv("HOME", str(work.home))
        monkeypatch.setenv("HERMES_HOME", str(work.root))
        monkeypatch.setattr(Path, "home", lambda: work.home)
        env.reset()

    source = WorkEnv(tmp_path / "source", monkeypatch)
    source.goal_and_todos()
    legacy = source.work / "work.db"
    with closing(sqlite3.connect(legacy)) as conn, conn:
        conn.execute("PRAGMA foreign_keys = ON")
        conn.execute("DROP TABLE IF EXISTS todo_repo")
        conn.execute("DROP TABLE IF EXISTS repo")
        before = conn.execute("SELECT id, title, goal_id, agent_id FROM todo").fetchall()

    target = WorkEnv(tmp_path / "target", monkeypatch)
    target.ok("import", "--from", str(legacy))
    with closing(ledger.open_db()) as db:
        assert db.execute("SELECT id, title, goal_id, agent_id FROM todo").fetchall() == before
        assert db.execute("SELECT todo_id FROM todo_repo").fetchall() == []
    with closing(sqlite3.connect(legacy)) as conn:
        assert conn.execute("SELECT 1 FROM sqlite_master WHERE name = 'todo_repo'").fetchone() is None

    activate(source)
    source.ok("edit", "T1", "--title", "Review the notes")
    with closing(ledger.open_db()) as db:
        assert db.execute("SELECT title, goal_id, agent_id FROM todo WHERE id = 'T1'").fetchone() == (
            "Review the notes", before[0][2], before[0][3],
        )
        with pytest.raises(sqlite3.IntegrityError, match="FOREIGN KEY"):
            db.execute("INSERT INTO todo_repo (todo_id, repo_id) VALUES ('T1', 'A1/missing')")
        db.execute("INSERT INTO repo (id, area_id, name, forge_url) VALUES (?, ?, ?, ?)",
                   ("A1/album", "A1", "album", "https://forge.example/album"))
    source.ok("edit", "T1", "--repo", "A1/album")

    activate(target)
    target.ok("import", "--from", str(legacy))
    with closing(ledger.open_db()) as db:
        assert db.execute("SELECT todo_id, repo_id FROM todo_repo").fetchall() == [("T1", "A1/album")]
        assert db.execute("SELECT id, forge_url FROM repo").fetchall() == [
            ("A1/album", "https://forge.example/album"),
        ]

    activate(source)
    source.ok("edit", "T1", "--repo", "")
    activate(target)
    with closing(ledger.open_db()) as db:
        db.execute("UPDATE repo SET forge_url = 'https://forge.example/conflict'")
    assert target.run("import", "--from", str(legacy))[0] != 0
    with closing(ledger.open_db()) as db:
        assert db.execute("SELECT todo_id, repo_id FROM todo_repo").fetchall() == [("T1", "A1/album")]
        db.execute("UPDATE repo SET forge_url = 'https://forge.example/album'")
    target.ok("import", "--from", str(legacy))
    with closing(ledger.open_db()) as db:
        assert db.execute("SELECT todo_id FROM todo_repo").fetchall() == []
