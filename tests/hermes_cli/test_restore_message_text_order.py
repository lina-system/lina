"""lina#12: a restore validates and writes message text before it replaces anything live.

Every path that brings back a state.db with its text (quick snapshot, emergency image, ZIP import,
profile import, profile rename) must leave the live state.db and the profile directory as they were
when the text cannot be restored, and say why.
"""

import json
import shutil
import sqlite3
import tarfile
import zipfile
from argparse import Namespace
from contextlib import closing
from pathlib import Path
from unittest.mock import patch

import pytest

from hermes_cli import backup
from hermes_state import SessionDB
from hermes_state_store import TEXT_CACHE, MessageTextRestoreError, StoreUnavailableError

SIDECAR = "memory-messages.jsonl"


@pytest.fixture
def home(tmp_path, monkeypatch):
    home = tmp_path / ".hermes"
    home.mkdir()
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setattr(backup, "get_default_hermes_root", lambda: home)
    monkeypatch.setattr(backup, "_revive_gateway_after_import", lambda home: None)
    return home


def _append(profile: Path, sid: str, *lines: str) -> None:
    db = SessionDB(db_path=profile / "state.db")
    try:
        if db.get_session(sid) is None:
            db.create_session(sid, "cli")
        for line in lines:
            db.append_message(sid, "user", content=line)
    finally:
        db.close()


def _placements(profile: Path) -> list[tuple]:
    with closing(sqlite3.connect(profile / "state.db")) as conn:
        return conn.execute("SELECT id, session_id, text_uid FROM transcript_rows ORDER BY id").fetchall()


def _live_state(profile: Path) -> tuple:
    config = profile / "config.yaml"
    return _placements(profile), config.read_text(encoding="utf-8") if config.exists() else None


def _corrupt_checksum(sidecar: Path) -> None:
    rows = [json.loads(line) for line in sidecar.read_text(encoding="utf-8").splitlines()]
    rows[0]["content_sha256"] = "0" * 64
    sidecar.write_text("".join(json.dumps(row) + "\n" for row in rows), encoding="utf-8")


def _snapshot_then_change_live(home: Path) -> str:
    (home / "config.yaml").write_text("model: snapshot\n", encoding="utf-8")
    _append(home, "s", "kept in the snapshot")
    snapshot_id = backup.create_quick_snapshot(hermes_home=home)
    _append(home, "s", "written after the snapshot")
    (home / "config.yaml").write_text("model: live\n", encoding="utf-8")
    return snapshot_id


def test_quick_snapshot_with_a_corrupt_text_checksum_leaves_the_profile_as_it_was(home):
    snapshot_id = _snapshot_then_change_live(home)
    _corrupt_checksum(home / "state-snapshots" / snapshot_id / SIDECAR)
    before = _live_state(home)

    with pytest.raises(MessageTextRestoreError, match="content_sha256"):
        backup.restore_quick_snapshot(snapshot_id, hermes_home=home)

    assert _live_state(home) == before


def test_quick_snapshot_while_the_store_is_unreachable_leaves_the_profile_as_it_was(home, monkeypatch):
    import hermes_state_store

    snapshot_id = _snapshot_then_change_live(home)
    before = _live_state(home)
    TEXT_CACHE.clear()

    def unreachable():
        raise StoreUnavailableError("cannot reach the memory store: connection refused")

    with monkeypatch.context() as patched:
        patched.setattr(hermes_state_store, "resolve_dsn", unreachable)
        with pytest.raises(MessageTextRestoreError, match="cannot reach the memory store"):
            backup.restore_quick_snapshot(snapshot_id, hermes_home=home)

    assert _live_state(home) == before


def test_emergency_image_with_a_corrupt_text_checksum_leaves_state_db_as_it_was(home):
    from hermes_cli.backup_sqlite import preflight_state_db

    _append(home, "e", "kept in the emergency image")
    image = Path(preflight_state_db(home)["path"])
    _append(home, "e", "written after the image")
    _corrupt_checksum(image.with_name(f"{image.name}.{SIDECAR}"))
    before = _placements(home)

    with pytest.raises(MessageTextRestoreError, match="content_sha256"):
        backup._safe_restore_db(image, home / "state.db")

    assert _placements(home) == before


def test_zip_import_missing_a_referenced_row_restores_nothing(home, tmp_path, memory_store_sql):
    (home / "config.yaml").write_text("model: archived\n", encoding="utf-8")
    _append(home, "z", "first archived line", "second archived line")
    archive = tmp_path / "backup.zip"
    assert backup.run_backup(Namespace(output=str(archive)))
    dropped = json.loads(zipfile.ZipFile(archive).read(SIDECAR).decode().splitlines()[1])["uid"]
    damaged = tmp_path / "damaged.zip"
    with zipfile.ZipFile(archive) as src, zipfile.ZipFile(damaged, "w") as dst:
        for item in src.infolist():
            data = src.read(item)
            if item.filename == SIDECAR:
                data = data.decode().splitlines()[0].encode() + b"\n"
            dst.writestr(item, data)
    memory_store_sql("DELETE FROM memory.messages WHERE uid = %s", (dropped,))
    _append(home, "z", "written after the backup")
    (home / "config.yaml").write_text("model: live\n", encoding="utf-8")
    before = _live_state(home)
    TEXT_CACHE.clear()

    with pytest.raises(SystemExit) as refused:
        backup.run_import(Namespace(zipfile=str(damaged), force=True))

    assert refused.value.code == 1

    assert _live_state(home) == before


def test_profile_import_with_a_corrupt_text_checksum_creates_no_profile(home, tmp_path, monkeypatch):
    from hermes_cli import profiles

    monkeypatch.setattr(profiles, "_get_profiles_root", lambda: home / "profiles")
    source = home / "profiles" / "original"
    source.mkdir(parents=True)
    (source / "config.yaml").write_text("model: test\n", encoding="utf-8")
    _append(source, "p", "profile line")
    archive = Path(profiles.export_profile("original", str(tmp_path / "profile.tar.gz")))
    unpacked = tmp_path / "unpacked"
    with tarfile.open(archive) as tar:
        tar.extractall(unpacked, filter="data")
    _corrupt_checksum(unpacked / "original" / SIDECAR)
    damaged = tmp_path / "damaged.tar.gz"
    with tarfile.open(damaged, "w:gz") as tar:
        tar.add(unpacked / "original", arcname="original")
    shutil.rmtree(unpacked)

    with pytest.raises(MessageTextRestoreError, match="content_sha256"):
        profiles.import_profile(str(damaged), name="copy")

    assert not (home / "profiles" / "copy").exists()


def test_rename_onto_text_another_copy_holds_is_refused_before_anything_moves(home, memory_store_sql):
    from hermes_cli.profiles import create_profile, rename_profile

    old_dir = create_profile("harbor", no_alias=True)
    _append(old_dir, "r", "harbor line")
    (uid,) = [row[2] for row in _placements(old_dir)]
    tail = uid.removeprefix("agent:harbor:")
    memory_store_sql(
        "INSERT INTO memory.messages (uid, source, agent_id, content, native, content_sha256)"
        " VALUES (%s, 'agent', 'lighthouse', 'other text', '{\"payload_sha256\": \"x\"}'::jsonb, 'x')",
        (f"agent:lighthouse:{tail}",))
    before = memory_store_sql("SELECT uid, agent_id FROM memory.messages ORDER BY uid")

    with patch("hermes_cli.profiles.check_alias_collision", return_value="skip"), \
            pytest.raises(MessageTextRestoreError, match="lighthouse"):
        rename_profile("harbor", "lighthouse")

    assert old_dir.is_dir() and not (old_dir.parent / "lighthouse").exists()
    assert memory_store_sql("SELECT uid, agent_id FROM memory.messages ORDER BY uid") == before


def test_quick_snapshot_of_a_state_db_without_message_text_restores_with_no_store(home, monkeypatch, memory_store_sql):
    import hermes_state_store

    db = SessionDB(db_path=home / "state.db")
    try:
        db.create_session("empty", "cli")
    finally:
        db.close()
    snapshot_id = backup.create_quick_snapshot(hermes_home=home)
    with closing(sqlite3.connect(home / "state.db")) as conn:
        conn.execute("DELETE FROM sessions")
        conn.commit()

    def no_store():
        raise StoreUnavailableError("the memory store is not set up")

    monkeypatch.setattr(hermes_state_store, "resolve_dsn", no_store)
    assert backup.restore_quick_snapshot(snapshot_id, hermes_home=home)

    with closing(sqlite3.connect(home / "state.db")) as conn:
        assert conn.execute("SELECT id FROM sessions").fetchall() == [("empty",)]
