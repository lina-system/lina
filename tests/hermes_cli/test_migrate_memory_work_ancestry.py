"""Real-backend FK closure and exact-subset rerun contracts."""

from contextlib import closing

import pytest

from hermes_cli.migrate_memory_work import _destination_db, migrate_work
from hermes_cli.work import env, ledger, store, store_sqlite
from tests.hermes_cli.test_migrate_memory_work import build_copied_work_fixture
from tests.hermes_cli.work_pg_fixture import pg_db


@pytest.fixture(params=["sqlite", "postgres"])
def target(request, tmp_path, monkeypatch):
    previous = env._CURRENT[:]
    home = tmp_path / "destination"
    home.mkdir()
    backend = {"backend": request.param}
    if request.param == "postgres":
        backend["psql"] = request.getfixturevalue("pg_db").argv
    config = {"work": {"store": backend}}
    monkeypatch.setattr(env, "_root_config", lambda base: config)
    env.bind(env.Env(home, config, "en", "owner", "Owner", {}))
    with closing(ledger.open_db()) as db:
        if request.param == "postgres":
            db.execute("TRUNCATE area, project, goal, todo, run, event, link, wait, decision, run_todo CASCADE")
    yield home
    env._CURRENT[:] = previous


def ancestry_source(path):
    build_copied_work_fixture(path)
    with closing(store_sqlite.connect(path, "owner", {})) as db:
        with store.transaction(db):
            db.execute("UPDATE todo SET title = 'ancestor task' WHERE id = 'T2'")
            db.execute("UPDATE todo SET close_run_id = 'run-beta' WHERE id = 'T1'")
            db.execute("UPDATE run SET goal_id = 'G1' WHERE id = 'run-beta'")
            db.execute("UPDATE todo SET close_run_id = 'run-alpha' WHERE id = 'T2'")
            db.execute("INSERT INTO run_todo (run_id, todo_id, added_by) "
                       "VALUES ('run-alpha', 'T2', 'beta')")
            store.run_event(db, "run-beta", "run_started", "beta", {})
            # Forward parent and a cycle through an otherwise excluded task's event.
            db.execute("UPDATE event SET parent_id = 4 WHERE id = 3")
            db.execute("UPDATE event SET parent_id = 3 WHERE id = 4")
    return path


def test_import_closes_foreign_ancestry_and_preserves_ownership(target, tmp_path):
    # Given valid cross-profile references, including a forward event cycle.
    source = ancestry_source(tmp_path / "copied.db")
    # When the copied subset is imported through the real configured backend.
    report = migrate_work(source, target, "alpha", "new-alpha")
    # Then all FK targets exist without transferring ancestor ownership.
    assert report["source_manifest"] == report["destination_manifest"]
    assert {(r["kind"], r["id"], r["profile"]) for r in report["required_ancestors"]} == {
        ("area", "A1", "beta"), ("goal", "G1", "beta"),
        ("todo", "T2", "beta"), ("run", "run-beta", "beta"), ("event", 4, "beta")}
    assert report["excluded_run_ids"] == []
    with closing(_destination_db()) as db:
        assert db.execute("SELECT id, agent_id, close_run_id FROM todo ORDER BY id").fetchall() == [
            ("T1", "new-alpha", "run-beta"), ("T2", "beta", "run-alpha")]
        assert db.execute("SELECT id, agent_id FROM run ORDER BY id").fetchall() == [
            ("run-alpha", "new-alpha"), ("run-beta", "beta")]
        assert db.execute("SELECT id, parent_id, agent_id FROM event WHERE id IN (3, 4) ORDER BY id").fetchall() == [
            (3, 4, "new-alpha"), (4, 3, "beta")]
        assert db.execute("SELECT count(*) FROM run_todo").fetchone() == (2,)
    repeated = migrate_work(source, target, "alpha", "new-alpha")
    assert repeated["inserted"] == 0 and repeated["matched"] == report["inserted"]


@pytest.mark.parametrize("parent_id", [3, 4])
def test_event_parent_closure_handles_cycles_without_relation_edges(target, tmp_path, parent_id):
    # Given either a self-reference or a cycle across profile-owned tasks.
    source = build_copied_work_fixture(tmp_path / "copied.db")
    with closing(store_sqlite.connect(source, "owner", {})) as db:
        with store.transaction(db):
            db.execute("UPDATE todo SET title = 'ancestor task' WHERE id = 'T2'")
            db.execute("UPDATE event SET parent_id = ? WHERE id = 3", (parent_id,))
            db.execute("UPDATE event SET parent_id = 3 WHERE id = 4")
    # When only selected history supplies the cross-profile dependency.
    report = migrate_work(source, target, "alpha", "new-alpha")
    # Then self-parenting and parent-only ancestry both preserve the exact graph.
    with closing(_destination_db()) as db:
        assert db.execute("SELECT parent_id FROM event WHERE id = 3").fetchone() == (parent_id,)
        ancestors = {(r["kind"], r["id"]) for r in report["required_ancestors"]}
        assert (("todo", "T2") in ancestors) == (parent_id == 4)
        if parent_id == 4:
            assert db.execute("SELECT agent_id FROM todo WHERE id = 'T2'").fetchone() == ("beta",)
            assert ("event", 4) in ancestors


def test_relation_owner_selects_foreign_item_without_primary_seed(target, tmp_path):
    # Given an alpha-owned relation on an otherwise unselected beta task.
    source = build_copied_work_fixture(tmp_path / "copied.db")
    with closing(store_sqlite.connect(source, "owner", {})) as db:
        with store.transaction(db):
            db.execute("UPDATE todo SET title = 'ancestor task' WHERE id = 'T2'")
            store.upsert(db, "wait", 50, {"todo_id": "T2", "reason": "review", "on_text": "review",
                                         "since": store.EPOCH, "agent_id": "alpha"})
    # When relation ownership is the only selection seed for the foreign task.
    report = migrate_work(source, target, "alpha", "new-alpha")
    # Then the relation is remapped but its required task retains beta ownership.
    with closing(_destination_db()) as db:
        assert db.execute("SELECT agent_id FROM wait WHERE id = 50").fetchone() == ("new-alpha",)
        assert db.execute("SELECT agent_id FROM todo WHERE id = 'T2'").fetchone() == ("beta",)
    assert ("todo", "T2", "beta") in {
        (r["kind"], r["id"], r["profile"]) for r in report["required_ancestors"]}


def test_exact_subset_rerun_ignores_new_live_events(target, tmp_path):
    # Given an imported subset followed by a real live event write.
    source = build_copied_work_fixture(tmp_path / "copied.db")
    first = migrate_work(source, target, "alpha", "new-alpha")
    with closing(_destination_db()) as db:
        store.run_event(db, "run-alpha", "run_activity", "new-alpha", {"live": True})
        before = store.dump(db)
    # When the identical copied source is replayed.
    repeated = migrate_work(source, target, "alpha", "new-alpha")
    # Then only the imported subset is matched and the live row is untouched.
    assert repeated["inserted"] == 0
    assert repeated["matched"] == first["inserted"]
    assert repeated["source_manifest"] == repeated["destination_manifest"]
    with closing(_destination_db()) as db:
        assert store.dump(db) == before


def test_changed_imported_event_refuses_after_live_append(target, tmp_path):
    # Given live progress plus corruption of an imported event.
    source = build_copied_work_fixture(tmp_path / "copied.db")
    migrate_work(source, target, "alpha", "new-alpha")
    with closing(_destination_db()) as db:
        store.run_event(db, "run-alpha", "run_activity", "new-alpha", {"live": True})
        db.execute("UPDATE event SET body = 'changed' WHERE id = 3")
        before = store.dump(db)
    # When replaying the exact source subset.
    with pytest.raises(ValueError, match="work ID collision"):
        migrate_work(source, target, "alpha", "new-alpha")
    # Then content changes are refused atomically, despite legitimate live rows.
    with closing(_destination_db()) as db:
        assert store.dump(db) == before


def test_missing_parent_event_refuses_before_writing(target, tmp_path):
    # Given a damaged copy whose selected event references a missing parent.
    source = build_copied_work_fixture(tmp_path / "copied.db")
    with closing(store_sqlite.connect(source, "owner", {})) as db:
        db.execute("PRAGMA foreign_keys = OFF")
        db.execute("UPDATE event SET parent_id = 999 WHERE id = 3")
    with closing(_destination_db()) as db:
        before = store.dump(db)
    # When selecting the damaged copied graph.
    with pytest.raises(ValueError, match="missing work ancestor: event/999"):
        migrate_work(source, target, "alpha", "new-alpha")
    # Then no destination writer has run.
    with closing(_destination_db()) as db:
        assert store.dump(db) == before
