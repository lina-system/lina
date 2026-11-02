"""Backups preserve transcript text after the external store collects it."""

from argparse import Namespace

import pytest

from hermes_cli import backup
from hermes_state import SessionDB
from hermes_state_store import TEXT_CACHE


@pytest.fixture
def home(tmp_path, monkeypatch):
    home = tmp_path / ".hermes"
    home.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setattr(backup, "get_default_hermes_root", lambda: home)
    monkeypatch.setattr(backup, "_revive_gateway_after_import", lambda home: None)
    return home


def _seed(home, sid):
    db = SessionDB(db_path=home / "state.db")
    try:
        db.create_session(sid, "cli")
        db.append_message(sid, "user", content="Remember this after collection.")
    finally:
        db.close()


def _content(home, sid):
    db = SessionDB(db_path=home / "state.db")
    try:
        return [message["content"] for message in db.get_messages_as_conversation(sid)]
    finally:
        db.close()


def test_quick_snapshot_restores_collected_message_text(home, memory_store_sql):
    # Given a snapshot whose session and external text have since been deleted.
    sid = "snapshot-collected"
    _seed(home, sid)
    snapshot = backup.create_quick_snapshot(hermes_home=home)
    assert snapshot
    db = SessionDB(db_path=home / "state.db")
    try:
        db.delete_session(sid)
    finally:
        db.close()
    memory_store_sql("DELETE FROM memory.messages WHERE session_id = %s", (sid,))
    TEXT_CACHE.clear()

    # When the snapshot is restored after collection.
    assert backup.restore_quick_snapshot(snapshot, hermes_home=home)

    # Then its transcript is readable from the restored store.
    assert _content(home, sid) == ["Remember this after collection."]


def test_full_backup_import_restores_empty_message_store(home, tmp_path, memory_store_sql):
    # Given a full backup and an empty external store.
    sid = "archive-collected"
    _seed(home, sid)
    archive = tmp_path / "backup.zip"
    assert backup.run_backup(Namespace(output=str(archive)))
    memory_store_sql("TRUNCATE memory.messages")
    TEXT_CACHE.clear()

    # When the CLI import path restores the archive.
    assert backup.run_import(Namespace(zipfile=str(archive), force=True)) is None

    # Then the transcript bodies are available again.
    assert _content(home, sid) == ["Remember this after collection."]


def test_named_profile_archive_restores_under_imported_agent(home, tmp_path, monkeypatch, memory_store_sql):
    from hermes_cli import profiles

    # Given a named profile exported before the external store was emptied.
    monkeypatch.setattr(profiles, "_get_profiles_root", lambda: home / "profiles")
    source = home / "profiles" / "original"
    source.mkdir(parents=True)
    (source / "config.yaml").write_text("model: test\n", encoding="utf-8")
    _seed(source, "profile-collected")
    archive = profiles.export_profile("original", str(tmp_path / "profile.tar.gz"))
    memory_store_sql("TRUNCATE memory.messages")
    TEXT_CACHE.clear()

    # When imported with a different profile identity.
    restored = profiles.import_profile(str(archive), name="restored")

    # Then the target agent can read the original transcript.
    assert _content(restored, "profile-collected") == ["Remember this after collection."]


def test_full_backup_restores_named_profile_text(home, tmp_path, monkeypatch, memory_store_sql):
    from hermes_cli import profiles

    # Given a root backup containing a named profile's state database.
    source = home / "profiles" / "nested"
    source.mkdir(parents=True)
    _seed(source, "nested-collected")
    monkeypatch.setattr(profiles, "create_wrapper_script", lambda name: None)
    archive = tmp_path / "nested.zip"
    assert backup.run_backup(Namespace(output=str(archive)))
    memory_store_sql("TRUNCATE memory.messages")
    TEXT_CACHE.clear()

    # When imported into an empty text store.
    assert backup.run_import(Namespace(zipfile=str(archive), force=True)) is None

    # Then the named agent, rather than default, owns the recovered text.
    assert _content(source, "nested-collected") == ["Remember this after collection."]


def test_emergency_preflight_restores_collected_text(home, memory_store_sql):
    from pathlib import Path
    from hermes_cli.backup_sqlite import preflight_state_db

    # Given a modern emergency image followed by external text collection.
    _seed(home, "emergency-collected")
    image = Path(preflight_state_db(home)["path"])
    memory_store_sql("TRUNCATE memory.messages")
    TEXT_CACHE.clear()

    # When the emergency image is restored through the shared primitive.
    assert backup._safe_restore_db(image, home / "state.db")

    # Then its external bodies are available.
    assert _content(home, "emergency-collected") == ["Remember this after collection."]
