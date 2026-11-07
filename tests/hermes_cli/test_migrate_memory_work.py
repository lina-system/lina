"""Copied profile ledger accounting and collision-safe migration."""

import json
from contextlib import closing

import pytest

from hermes_cli.migrate_memory_work import migrate_work
from hermes_cli.work import env, store, store_sqlite
from hermes_cli.work.cmd_store import _import_line


@pytest.fixture
def work_env(tmp_path, monkeypatch):
    previous = env._CURRENT[:]
    config = {"work": {"store": {"backend": "sqlite"}, "agents": {"alpha": "Alpha"}}}
    monkeypatch.setattr(env, "_root_config", lambda base: config)
    env.bind(env.Env(tmp_path, config, "en", "owner", "Owner", {"alpha": "Alpha"}))
    yield tmp_path
    env._CURRENT[:] = previous


def _append(db, number, **fields):
    kind = {"A": "영역", "G": "목표", "T": "할일"}[fields["id"][0]]
    row = {"op": "add", "state": "open", "created": 1700000000, "kind": kind, **fields}
    _import_line(db, number, json.dumps(row), row)


def build_copied_work_fixture(source):
    """Build a copied SQLite fixture reusable by the combined migration CLI."""
    with closing(store_sqlite.connect(source, "owner", {}, create=True)) as db:
        with store.transaction(db):
            _append(db, 1, id="A1", by="beta", title="private area")
            _append(db, 2, id="G1", by="beta", parent="A1", title="private goal")
            _append(db, 3, id="T1", by="alpha", parent="G1", title="selected task")
            _append(db, 4, id="T2", by="beta", parent="G1", title="EXCLUDED\x00BODY\x00MARKER", writer="alpha")
            db.execute("INSERT INTO run (id, runner, title, agent_id, created_at) "
                       "VALUES ('run-alpha', 'omo', 'selected run', 'alpha', '2026-01-01')")
            db.execute("INSERT INTO run (id, runner, title, agent_id, created_at) "
                       "VALUES ('run-beta', 'omo', 'private run', 'beta', '2026-01-01')")
            db.execute("INSERT INTO run_todo (run_id, todo_id, closes, added_by, added_at) "
                       "VALUES ('run-alpha', 'T1', 1, 'alpha', '2026-01-01')")
    return source


def test_copied_profile_import_accounts_for_ancestry_without_body_leaks(work_env):
    # Given a real copied ledger with shared ancestry and another profile's task.
    source = build_copied_work_fixture(work_env / "copied.db")
    before = source.read_bytes(), source.stat().st_mtime_ns
    destination = work_env / "destination"

    # When migrating the selected profile under a new agent identity.
    report = migrate_work(source, destination, "alpha", "new-alpha")

    # Then ancestors import unchanged, no source changes, and excluded bodies stay out.
    assert report["status"] == "imported" and report["ok"] is True
    assert report["copied_path"] == str(source)
    assert report["whole_source_counts"]["event"] == 4
    assert report["selected_ids"] == ["T1"]
    assert report["selected_counts"]["todo"] == 1
    assert report["selected_counts"]["run"] == 1
    assert report["selected_counts"]["run_todo"] == 1
    assert report["excluded_run_ids"] == ["run-beta"]
    assert report["selected_event_rows"] == 3 and report["excluded_event_rows"] == 1
    assert report["whole_source_rows"] == report["source_rows"] + report["excluded_rows"]
    assert report["source_rows"] == report["matched"] + report["inserted"]
    assert [row["id"] for row in report["required_ancestors"]] == ["A1", "G1"]
    assert [row["id"] for row in report["excluded_items"]] == ["T2"]
    assert report["migrated_counts"] == report["selected_counts"]
    assert report["destination_manifest"] == report["source_manifest"]
    assert "EXCLUDED\\u0000BODY\\u0000MARKER" not in json.dumps(report)
    assert (source.read_bytes(), source.stat().st_mtime_ns) == before
    assert (destination / "work" / "work.db").exists()


def test_existing_importer_remapping_agent_overwrites_global_item(work_env):
    # Given an original import already projected in the destination store.
    with closing(store_sqlite.connect(work_env / "destination.db", "owner", {}, create=True)) as db:
        with store.transaction(db):
            _append(db, 1, id="T1", by="alpha", title="original")

        # When the same item is imported with only the agent identity remapped.
        with store.transaction(db):
            _append(db, 1, id="T1", by="new-alpha", title="copied")

        # Then the original row is replaced, rather than a second identity added.
        assert db.execute("SELECT id, agent_id, title FROM todo").fetchall() == [
            ("T1", "new-alpha", "copied")]
        assert db.execute("SELECT count(*) FROM event").fetchone()[0] == 2


def test_online_work_copy_remains_reusable_after_readonly_inventory(work_env):
    import sqlite3

    from hermes_cli.migrate_memory_copy import snapshot

    home = work_env / "source"
    home.mkdir()
    with closing(sqlite3.connect(home / "state.db")) as conn:
        conn.execute("CREATE TABLE fixture(id INTEGER)")
    ledger = build_copied_work_fixture(work_env / "ledger.db")
    copied = snapshot(home, "default", work_env / "copy", work_db=ledger)

    migrate_work(copied.work_db, work_env / "destination", "alpha", "new-alpha")

    repeated = snapshot(home, "default", work_env / "copy", work_db=ledger)
    assert repeated.receipt == copied.receipt
    assert not copied.work_db.with_name("work.db-wal").exists()
    assert not copied.work_db.with_name("work.db-shm").exists()
