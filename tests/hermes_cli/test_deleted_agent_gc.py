"""Deleted profiles retain their shared message text for a full grace period."""

from datetime import UTC, datetime, timedelta
from pathlib import Path
import shutil
import zipfile

import psycopg
import pytest

from hermes_cli import profiles
from hermes_cli.profiles_archive import export_profile, import_profile
from hermes_cli.subcommands.store import _collect_text
from hermes_state import SessionDB


@pytest.fixture
def profile_home(tmp_path, monkeypatch, memory_store_sql):
    home = tmp_path / ".hermes"
    home.mkdir()
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setenv("LINA_HOME", str(home))
    for name in ("_cleanup_gateway_service", "_stop_profile_backends",
                 "_maybe_register_gateway_service", "_maybe_unregister_gateway_service",
                 "_notify_multiplexer", "seed_profile_skills"):
        monkeypatch.setattr(profiles, name, lambda *args, **kwargs: None)
    monkeypatch.setattr(profiles, "_purge_identity", lambda _name: True)
    # Initialize the real test store before creating profiles.
    memory_store_sql("SELECT 1")
    return home


def _history(name):
    home = profiles.create_profile(name, no_alias=True, no_skills=True)
    with SessionDB(home / "state.db") as db:
        db.create_session(session_id="history", source="cli")
        db.append_message("history", "user", "Synthetic retention fixture.")
    return home


def _clock(monkeypatch, instant):
    from hermes_cli import store_gc
    monkeypatch.setattr(store_gc, "_now", lambda: instant)


@pytest.mark.parametrize(("days", "remaining"), [(29, 1), (30, 1), (31, 0)])
def test_deleted_history_uses_deletion_age(profile_home, memory_store_sql, monkeypatch, days, remaining):
    _history("retired")
    _history("active")
    memory_store_sql("UPDATE memory.messages SET observed_at = '2000-01-01'::timestamptz")
    deleted = datetime(2030, 1, 1, tzinfo=UTC)
    _clock(monkeypatch, deleted)
    profiles.delete_profile("retired", yes=True)
    assert memory_store_sql("SELECT deleted_at FROM memory.agents WHERE agent_id = 'retired'") == [(deleted,)]
    _clock(monkeypatch, deleted + timedelta(days=days))

    _collect_text(30)

    assert memory_store_sql("SELECT count(*) FROM memory.messages WHERE agent_id = 'retired'") == [(remaining,)]
    assert memory_store_sql("SELECT count(*) FROM memory.messages WHERE agent_id = 'active'") == [(1,)]


@pytest.mark.parametrize("recovery", ["import", "create", "snapshot", "zip"])
def test_recovering_name_cancels_collection(profile_home, tmp_path, memory_store_sql, monkeypatch, recovery):
    original = _history("retired")
    snapshot = tmp_path / "snapshot"
    shutil.copytree(original, snapshot)
    archive = export_profile("retired", str(tmp_path / "backup.tar.gz"))
    deleted = datetime(2030, 1, 1, tzinfo=UTC)
    _clock(monkeypatch, deleted)
    profiles.delete_profile("retired", yes=True)
    _clock(monkeypatch, deleted + timedelta(days=1))
    if recovery == "import":
        restored = import_profile(str(archive))
        assert profiles.profile_exists("retired")
        with SessionDB(restored / "state.db") as db:
            assert len(db.get_messages_as_conversation("history")) == 1
    elif recovery == "create":
        profiles.create_profile("retired", no_alias=True, no_skills=True)
    else:
        from hermes_cli.backup_restore import _import_db_member, _safe_restore_db
        original.mkdir()
        if recovery == "snapshot":
            assert _safe_restore_db(snapshot / "state.db", original / "state.db")
        else:
            with zipfile.ZipFile(tmp_path / "backup.zip", "w") as bundle:
                bundle.write(snapshot / "state.db", "state.db")
            with zipfile.ZipFile(tmp_path / "backup.zip") as bundle:
                _import_db_member(bundle, "state.db", original / "state.db")
        assert profiles.profile_exists("retired")
        with SessionDB(original / "state.db") as db:
            assert len(db.get_messages_as_conversation("history")) == 1
    assert memory_store_sql("SELECT deleted_at FROM memory.agents WHERE agent_id = 'retired'") == [(None,)]

    _clock(monkeypatch, deleted + timedelta(days=31))
    _collect_text(30)
    assert memory_store_sql("SELECT count(*) FROM memory.messages WHERE agent_id = 'retired'") == [(1,)]


def test_agent_role_cannot_change_deletion_time(profile_home, _memory_store):
    _history("active")
    with psycopg.connect(_memory_store.app_dsn()) as conn:
        conn.execute("SELECT set_config('lina.agent_id', 'active', false)")
        with pytest.raises(psycopg.errors.InsufficientPrivilege):
            conn.execute("UPDATE memory.agents SET deleted_at = now() WHERE agent_id = 'active'")


def test_configured_store_requires_owner_before_deleting_profile(profile_home, monkeypatch, memory_store_sql):
    import hermes_state_store
    original = _history("retired")

    def no_owner():
        raise hermes_state_store.StoreUnavailableError("owner configuration missing")

    monkeypatch.setattr(hermes_state_store, "resolve_owner_dsn", no_owner)
    with pytest.raises(RuntimeError, match="owner configuration missing"):
        profiles.delete_profile("retired", yes=True)

    assert original.is_dir()
    assert memory_store_sql("SELECT count(*) FROM memory.messages WHERE agent_id = 'retired'") == [(1,)]


def test_failed_snapshot_keeps_deletion_time(profile_home, tmp_path, memory_store_sql, monkeypatch):
    from hermes_cli import backup_restore
    original = _history("retired")
    snapshot = tmp_path / "snapshot"
    shutil.copytree(original, snapshot)
    deleted = datetime(2030, 1, 1, tzinfo=UTC)
    _clock(monkeypatch, deleted)
    profiles.delete_profile("retired", yes=True)
    original.mkdir()
    monkeypatch.setattr(backup_restore, "_restore_db_pages", lambda *_args: False)

    assert not backup_restore._safe_restore_db(snapshot / "state.db", original / "state.db")

    assert memory_store_sql("SELECT deleted_at FROM memory.agents WHERE agent_id = 'retired'") == [(deleted,)]
    assert not profiles.profile_exists("retired")
    assert memory_store_sql("SELECT count(*) FROM memory.messages WHERE agent_id = 'retired'") == [(1,)]


def test_gc_reads_grace_setting(profile_home, memory_store_sql, monkeypatch):
    from hermes_cli.config import atomic_config_write
    _history("retired")
    atomic_config_write(profile_home / "config.yaml", {"sessions": {"message_text_grace_days": 40}})
    deleted = datetime(2030, 1, 1, tzinfo=UTC)
    _clock(monkeypatch, deleted)
    profiles.delete_profile("retired", yes=True)
    _clock(monkeypatch, deleted + timedelta(days=31))

    _collect_text(None)
    assert memory_store_sql("SELECT count(*) FROM memory.messages WHERE agent_id = 'retired'") == [(1,)]
    _collect_text(30)
    assert memory_store_sql("SELECT count(*) FROM memory.messages WHERE agent_id = 'retired'") == [(0,)]


def test_reusing_deleted_name_by_rename_keeps_live_history(profile_home, memory_store_sql, monkeypatch):
    _history("retired")
    deleted = datetime(2030, 1, 1, tzinfo=UTC)
    _clock(monkeypatch, deleted)
    profiles.delete_profile("retired", yes=True)
    _clock(monkeypatch, deleted + timedelta(days=31))
    _collect_text(30)
    _history("active")
    monkeypatch.setattr(profiles, "create_wrapper_script", lambda *_args: Path("unused"))
    monkeypatch.setattr(profiles, "remove_wrapper_script", lambda *_args: None)
    monkeypatch.setattr(profiles, "check_alias_collision", lambda *_args: None)

    profiles.rename_profile("active", "retired")
    _collect_text(30)

    assert memory_store_sql("SELECT count(*) FROM memory.messages WHERE agent_id = 'retired'") == [(1,)]
    assert memory_store_sql("SELECT deleted_at FROM memory.agents WHERE agent_id = 'retired'") == [(None,)]
