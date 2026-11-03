"""Copied work migration through the real PostgreSQL writer."""

import json
from contextlib import closing

import pytest

from hermes_cli.migrate_memory_work import migrate_work
from hermes_cli.work import env, ledger, store, store_sqlite
from hermes_cli.work.cmd_store import _import_line
from tests.hermes_cli import work_pg_fixture
from tests.hermes_cli.test_migrate_memory_work import build_copied_work_fixture

pg_db = work_pg_fixture.pg_db


@pytest.fixture
def destination(tmp_path, monkeypatch, pg_db):
    previous = env._CURRENT[:]
    home = tmp_path / "destination"
    home.mkdir()
    config = {"work": {"store": {"backend": "postgres", "psql": pg_db.argv},
                       "agents": {"alpha": "Alpha", "beta": "Beta"}}}
    monkeypatch.setattr(env, "_root_config", lambda base: config if base == home else {})
    env.bind(env.Env(home, config, "en", "owner", "Owner", {}))
    with closing(ledger.open_db()) as db:
        db.execute("TRUNCATE area, project, goal, todo, run, event, link, wait, decision, run_todo CASCADE")
    yield home
    env._CURRENT[:] = previous


def rich_source(path):
    build_copied_work_fixture(path)
    with closing(store_sqlite.connect(path, "owner", {})) as db:
        with store.transaction(db):
            goal = store.latest(db, "G1")
            goal.update(op="edit", projects=["Projects/shared"])
            _import_line(db, 5, json.dumps(goal), goal)
            store.upsert(db, "wait", 20, {"goal_id": "G1", "reason": "review", "on_text": "ancestor-only",
                                         "since": store.EPOCH, "agent_id": "beta"})
            store.upsert(db, "link", 20, {"goal_id": "G1", "kind": "url", "target": "https://example.test/private",
                                         "added_by": "beta", "added_at": store.EPOCH, "agent_id": "beta"})
            task = store.latest(db, "T1")
            task.update(op="wait", writer="beta", links=["https://example.test/task", "omo:run-beta"],
                        wait={"on": "review", "since": 1700000001}, decide="Choose", decide_choices=["yes", "no"])
            _import_line(db, 6, json.dumps(task), task)
            own_goal = {"id": "G2", "by": "alpha", "parent": "A1", "title": "owned goal",
                        "op": "add", "created": 1700000000}
            _import_line(db, 7, json.dumps(own_goal), own_goal)
            db.execute("UPDATE run SET goal_id = 'G1' WHERE id = 'run-alpha'")
            store.run_event(db, "run-alpha", "run_started", "beta", {"detail": "started"})
            store.upsert(db, "wait", 10, {"run_id": "run-alpha", "reason": "review", "on_text": "review",
                                         "since": store.EPOCH, "agent_id": "alpha"})
            store.upsert(db, "decision", 10, {"run_id": "run-alpha", "kind": "question", "prompt": "Run?",
                                             "asked_by": "beta", "asked_at": store.EPOCH, "agent_id": "alpha",
                                             "answer": "Yes", "answered_by": "beta", "answered_at": store.EPOCH})
            store.upsert(db, "link", 10, {"run_id": "run-alpha", "kind": "url", "target": "https://example.test/run",
                                         "added_by": "beta", "added_at": store.EPOCH, "agent_id": "alpha"})
    return path


def test_selected_graph_preserves_ancestors_relations_and_repeat(destination, tmp_path):
    # Given a copied ledger with mixed ownership and relations on items and runs.
    source = rich_source(tmp_path / "copy.db")
    before = source.read_bytes(), source.stat().st_mtime_ns
    # When importing and repeating into the actual configured PostgreSQL backend.
    first = migrate_work(source, destination, "alpha", "new-alpha")
    assert first["ok"] and first["status"] == "imported"
    second = migrate_work(source, destination, "alpha", "new-alpha")
    # Then every selected source row has exactly one destination row and receipt.
    assert second["ok"] and second["inserted"] == 0
    assert second["matched"] == first["inserted"]
    assert first["source_manifest"] == first["destination_manifest"]
    assert first["source_rows"] == first["inserted"] + first["matched"]
    assert {row["kind"] for row in first["source_manifest"]} == {
        "area", "project", "goal", "todo", "run", "run_todo", "wait", "decision", "event", "link"}
    for kind, count in first["selected_counts"].items():
        assert count == first["inserted_counts"][kind] + first["matched_counts"][kind]
    assert {row["id"] for row in first["required_ancestors"]} == {"A1", "P1", "G1"}
    with closing(ledger.open_db()) as db:
        assert db.execute("SELECT id, agent_id FROM todo").fetchall() == [("T1", "new-alpha")]
        assert db.execute("SELECT id, agent_id FROM goal ORDER BY id").fetchall() == [
            ("G1", "beta"), ("G2", "new-alpha")]
        assert db.execute("SELECT agent_id FROM project").fetchone() == ("beta",)
        assert db.execute("SELECT id FROM run").fetchall() == [("run-alpha",)]
        assert db.execute("SELECT id FROM wait WHERE goal_id = 'G1'").fetchall() == []
        assert db.execute("SELECT id FROM link WHERE goal_id = 'G1'").fetchall() == []
        assert db.execute("SELECT run_id, todo_id, added_by FROM run_todo").fetchall() == [
            ("run-alpha", "T1", "alpha")]
        assert db.execute("SELECT agent_id, actor_id FROM event WHERE run_id IS NOT NULL").fetchall() == [
            ("new-alpha", "beta")]
        assert store.latest(db, "T1")["writer"] == "beta"
        assert db.execute("SELECT answer, answered_by FROM decision WHERE run_id = 'run-alpha'").fetchone() == (
            "Yes", "beta")
        for kind in ("wait", "decision", "link"):
            assert db.execute(f"SELECT agent_id FROM {kind} WHERE run_id = 'run-alpha'").fetchone() == ("new-alpha",)
    assert (source.read_bytes(), source.stat().st_mtime_ns) == before


@pytest.mark.parametrize("kind", ["todo", "run", "event", "project", "wait", "decision", "link", "run_todo"])
def test_conflicting_ids_refuse_without_writes(destination, tmp_path, kind):
    # Given an imported graph with one independently changed destination row.
    source = rich_source(tmp_path / "copy.db")
    assert migrate_work(source, destination, "alpha", "new-alpha")["ok"]
    columns = {"todo": "title", "run": "title", "event": "body", "project": "title",
               "wait": "on_text", "decision": "prompt", "link": "target", "run_todo": "added_at"}
    with closing(ledger.open_db()) as db:
        db.execute(f"UPDATE {kind} SET {columns[kind]} = ?", ("2000-01-01" if kind == "run_todo" else "changed",))
        before = store.dump(db)
        # When replaying the copied graph.
        with pytest.raises(ValueError, match="collision"):
            migrate_work(source, destination, "alpha", "new-alpha")
        # Then the entire destination remains byte-for-byte equivalent by row.
        assert store.dump(db) == before


def test_collision_preflight_never_calls_a_writer(destination, tmp_path, monkeypatch):
    # Given a last-kind collision with earlier new rows still absent.
    source = rich_source(tmp_path / "copy.db")
    assert migrate_work(source, destination, "alpha", "new-alpha")["ok"]
    with closing(ledger.open_db()) as db:
        db.execute("UPDATE event SET body = 'different' WHERE id = (SELECT max(id) FROM event)")
        before = store.dump(db)
        from hermes_cli import migrate_memory_work

        def forbidden_writer(*args):
            pytest.fail("preflight invoked a writer before rejecting the collision")

        monkeypatch.setattr(migrate_memory_work, "import_selected_rows", forbidden_writer)
        # When the source is replayed, it refuses before the writer seam.
        with pytest.raises(ValueError, match="collision"):
            migrate_work(source, destination, "alpha", "new-alpha")
        assert store.dump(db) == before


def test_writer_failure_rolls_back_entire_graph(destination, tmp_path, monkeypatch):
    # Given a fresh destination and a real writer that fails after writing rows.
    source = rich_source(tmp_path / "copy.db")
    from hermes_cli.work import cmd_store
    original = cmd_store._copy_runs

    def fail_after_runs(db, runs, pairs):
        original(db, runs, pairs)
        raise ValueError("injected failure after real writer")

    monkeypatch.setattr(cmd_store, "_copy_runs", fail_after_runs)
    with closing(ledger.open_db()) as db:
        before = store.dump(db)
        # When importing fails in the transaction, every table rolls back.
        with pytest.raises(ValueError, match="injected failure"):
            migrate_work(source, destination, "alpha", "new-alpha")
        assert store.dump(db) == before
