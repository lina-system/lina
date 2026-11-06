"""Store-owned mappings make shared ancestors independent of import order."""

from contextlib import closing
import json

import pytest

from hermes_cli.migrate_memory_work import _destination_db, migrate_work
from hermes_cli.work import store, store_sqlite
from hermes_cli import migrate_memory_work_receipts as receipts
from tests.hermes_cli.test_migrate_memory_work import build_copied_work_fixture
from tests.hermes_cli.test_migrate_memory_work_ancestry import target
from tests.hermes_cli.work_pg_fixture import pg_db

__all__ = ["pg_db", "target"]


@pytest.fixture(autouse=True)
def isolated_mapping_receipt(target):
    with closing(_destination_db()) as db:
        db.execute("DELETE FROM setting WHERE key='memory_migration_receipts'")


def shared_source(path):
    build_copied_work_fixture(path)
    with closing(store_sqlite.connect(path, "owner", {})) as db:
        db.execute("UPDATE todo SET title='other task' WHERE id='T2'")
    return path


@pytest.mark.parametrize("order", [("alpha", "beta"), ("beta", "alpha")])
def test_single_import_orders_promote_only_shared_ancestors(target, tmp_path, order):
    source = shared_source(tmp_path / "shared.db")
    names = {"alpha": "renamed-a", "beta": "different-b"}
    for profile in order:
        report = migrate_work(source, target, profile, names[profile])
        assert report["ok"]
    with closing(_destination_db()) as db:
        assert db.execute("SELECT id,agent_id FROM area").fetchall() == [("A1", "different-b")]
        assert db.execute("SELECT id,agent_id FROM goal").fetchall() == [("G1", "different-b")]
        assert db.execute("SELECT id,agent_id FROM todo ORDER BY id").fetchall() == [
            ("T1", "renamed-a"), ("T2", "different-b")]
        before = store.dump(db)
    for profile in order:
        assert migrate_work(source, target, profile, names[profile])["inserted"] == 0
    with closing(_destination_db()) as db:
        assert store.dump(db) == before


def test_promotion_refuses_changed_recorded_ancestor(target, tmp_path):
    source = shared_source(tmp_path / "shared.db")
    migrate_work(source, target, "alpha", "renamed-a")
    with closing(_destination_db()) as db:
        db.execute("UPDATE goal SET title='locally changed' WHERE id='G1'")
        before = store.dump(db)
    with pytest.raises(ValueError, match="collision"):
        migrate_work(source, target, "beta", "different-b")
    with closing(_destination_db()) as db:
        assert store.dump(db) == before


def test_profile_mapping_cannot_be_reassigned(target, tmp_path):
    source = shared_source(tmp_path / "shared.db")
    migrate_work(source, target, "alpha", "renamed-a")
    with closing(_destination_db()) as db:
        before = store.dump(db)
    with pytest.raises(ValueError, match="already mapped"):
        migrate_work(source, target, "alpha", "another-name")
    with closing(_destination_db()) as db:
        assert store.dump(db) == before


def test_unreceipted_existing_ancestor_is_never_promoted(target, tmp_path):
    from hermes_cli.work.cmd_store import import_selected_rows
    from hermes_cli.migrate_memory_work_rows import KINDS, rows

    source = shared_source(tmp_path / "shared.db")
    with closing(store_sqlite.connect(source, "owner", {})) as src:
        original = rows(src)
    with closing(_destination_db()) as db, store.transaction(db):
        import_selected_rows(db, {kind: original[kind] if kind == "area" else [] for kind in KINDS})
    migrate_work(source, target, "alpha", "renamed-a")
    with closing(_destination_db()) as db:
        before = store.dump(db)
    with pytest.raises(ValueError, match="collision"):
        migrate_work(source, target, "beta", "different-b")
    with closing(_destination_db()) as db:
        assert store.dump(db) == before


def test_unmapped_owner_and_changed_source_are_not_silently_promoted(target, tmp_path):
    source = shared_source(tmp_path / "shared.db")
    migrate_work(source, target, "alpha", "arbitrary-name")
    with closing(_destination_db()) as db:
        assert db.execute("SELECT agent_id FROM area").fetchone() == ("beta",)
        saved = json.loads(db.execute("SELECT value FROM setting WHERE key=?", (receipts.RECEIPT_KEY,)).fetchone()[0])
        assert saved["profiles"] == {"alpha": "arbitrary-name"}
        assert {row["owner"] for row in saved["ancestors"].values()} == {"beta"}
        before = store.dump(db)
    with closing(store_sqlite.connect(source, "owner", {})) as db:
        db.execute("UPDATE goal SET title='changed source' WHERE id='G1'")
    with pytest.raises(ValueError, match="ancestor collision"):
        migrate_work(source, target, "beta", "other-arbitrary-name")
    with closing(_destination_db()) as db:
        assert store.dump(db) == before
