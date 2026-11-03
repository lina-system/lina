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


def _uids(memory_store_sql, agent):
    return [row[0] for row in memory_store_sql(
        "SELECT uid FROM memory.messages WHERE agent_id = %s ORDER BY uid", (agent,))]


def test_profile_imported_under_another_name_reads_its_text_beside_the_original(
        home, tmp_path, monkeypatch, memory_store_sql):
    from hermes_cli import profiles

    monkeypatch.setattr(profiles, "_get_profiles_root", lambda: home / "profiles")
    source = home / "profiles" / "original"
    source.mkdir(parents=True)
    (source / "config.yaml").write_text("model: test\n", encoding="utf-8")
    _seed(source, "renamed-session")
    original_uids = _uids(memory_store_sql, "original")
    archive = profiles.export_profile("original", str(tmp_path / "profile.tar.gz"))
    TEXT_CACHE.clear()

    restored = profiles.import_profile(str(archive), name="renamed")

    TEXT_CACHE.clear()
    assert _content(restored, "renamed-session") == ["Remember this after collection."]
    assert _content(source, "renamed-session") == ["Remember this after collection."]
    assert _uids(memory_store_sql, "original") == original_uids
    renamed = _uids(memory_store_sql, "renamed")
    assert renamed and all(uid.startswith("agent:renamed:") for uid in renamed)


def test_full_backup_of_two_profiles_imported_into_another_profile_home(home, tmp_path, monkeypatch, memory_store_sql):
    from hermes_cli import profiles

    _seed(home, "root-session")
    nested = home / "profiles" / "alpha"
    nested.mkdir(parents=True)
    _seed(nested, "alpha-session")
    monkeypatch.setattr(profiles, "create_wrapper_script", lambda name: None)
    archive = tmp_path / "two.zip"
    assert backup.run_backup(Namespace(output=str(archive)))
    default_uids = _uids(memory_store_sql, "default")

    # Imported into a named profile's home while the originals are still in the store.
    target = tmp_path / "other" / "profiles" / "beta"
    target.mkdir(parents=True)
    monkeypatch.setenv("HERMES_HOME", str(target))
    monkeypatch.setattr(backup, "get_default_hermes_root", lambda: target)
    TEXT_CACHE.clear()
    assert backup.run_import(Namespace(zipfile=str(archive), force=True)) is None

    TEXT_CACHE.clear()
    assert _content(target, "root-session") == ["Remember this after collection."]
    assert _content(target / "profiles" / "alpha", "alpha-session") == ["Remember this after collection."]
    assert _uids(memory_store_sql, "default") == default_uids
    beta = _uids(memory_store_sql, "beta")
    assert beta and all(uid.startswith("agent:beta:") for uid in beta)


def test_restore_whose_text_is_missing_a_referenced_row_fails(home, memory_store_sql):
    from hermes_state_store import MessageTextRestoreError

    db = SessionDB(db_path=home / "state.db")
    try:
        db.create_session("gap", "cli")
        db.append_message("gap", "user", content="first kept line")
        db.append_message("gap", "assistant", content="second line the sidecar lost")
    finally:
        db.close()
    snapshot_id = backup.create_quick_snapshot(hermes_home=home)
    sidecar = home / "state-snapshots" / snapshot_id / "memory-messages.jsonl"
    lines = sidecar.read_text(encoding="utf-8").splitlines()
    assert len(lines) == 2
    sidecar.write_text(lines[0] + "\n", encoding="utf-8")
    memory_store_sql("TRUNCATE memory.messages")
    TEXT_CACHE.clear()

    with pytest.raises(MessageTextRestoreError, match="1 message text"):
        backup.restore_quick_snapshot(snapshot_id, hermes_home=home)
