"""Synthetic copy-only migration inputs: isolation, WAL consistency and retries."""

import hashlib
import json
import sqlite3
from contextlib import closing
from dataclasses import FrozenInstanceError
from pathlib import Path

import pytest

from hermes_cli.migrate_memory_copy import SnapshotError, snapshot


def database(path: Path) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with closing(sqlite3.connect(path)) as conn, conn:
        conn.execute("CREATE TABLE records (value TEXT)")
        conn.execute("INSERT INTO records VALUES ('original')")


def hashes(root: Path) -> dict[str, str]:
    return {str(path.relative_to(root)): hashlib.sha256(path.read_bytes()).hexdigest()
            for path in root.rglob("*") if path.is_file()}


def test_named_profile_snapshot_has_precise_safe_inputs(tmp_path: Path) -> None:
    # Given a profile with both migration inputs and unrelated secret files.
    home = tmp_path / "source"
    profile = home / "profiles/alpha"
    database(profile / "state.db")
    database(profile / "lcm.db")
    database(profile / "work.db")
    for relative in ("SOUL.md", "persona.md", "memory/MEMORY.md", "memory/USER.md",
                     "memories/MEMORY.md", "personas/alternate.md", "skills/demo/SKILL.md"):
        path = profile / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(relative)
    (profile / "config.yaml").write_text(
        "api_key: secret\nmemory:\n  memory_enabled: true\n  memory_char_limit: 2200\n"
        "  api_key: secret\n  provider: secret\nunknown: {password: secret}\n")
    for relative in (".env", "auth.json", "unknown-field.json"):
        (profile / relative).write_text("secret")
    dump, spool = tmp_path / "honcho.json", tmp_path / "collector.jsonl"
    dump.write_text('{"messages": []}')
    spool.write_text('{"text": "message"}\n')
    before = hashes(home)

    # When inputs are copied, no destination profile is created by this layer.
    result = snapshot(home, "alpha", tmp_path / "work", honcho_dump=dump, spool=spool)

    # Then receipts account for every safe copy and expose only snapshot paths.
    assert result.profile_dir == tmp_path / "work/snapshot/profile"
    assert result.source_profile_dir == profile
    assert result.lcm_db == result.profile_dir / "lcm.db"
    assert result.work_db == result.profile_dir / "work.db"
    assert result.honcho_dump.read_bytes() == dump.read_bytes()
    assert result.spool.read_bytes() == spool.read_bytes()
    assert (result.profile_dir / "skills/demo/SKILL.md").read_text() == "skills/demo/SKILL.md"
    assert (result.profile_dir / "memory/MEMORY.md").read_text() == "memory/MEMORY.md"
    assert (result.profile_dir / "memories/MEMORY.md").read_text() == "memories/MEMORY.md"
    assert (result.profile_dir / "personas/alternate.md").read_text() == "personas/alternate.md"
    assert json.loads((result.profile_dir / "config.yaml").read_text()) == {
        "memory": {"memory_enabled": True, "memory_char_limit": 2200}}
    assert not any((result.profile_dir / name).exists() for name in (".env", "auth.json", "unknown-field.json"))
    assert not (home / "profiles/alpha-copy").exists()
    assert hashes(home) == before
    copied = {row.destination for row in result.receipt}
    assert copied == {path for path in result.root.rglob("*") if path.is_file()} - {result.root / "receipt.json"}
    for row in result.receipt:
        assert row.size == row.destination.stat().st_size
        assert row.sha256 == hashlib.sha256(row.destination.read_bytes()).hexdigest()
        assert row.destination.stat().st_mode & 0o222 == 0
    with pytest.raises(FrozenInstanceError):
        result.profile = "different"


def test_online_backup_includes_committed_wal_without_changing_original(tmp_path: Path) -> None:
    # Given committed WAL frames and an uncommitted next write.
    home = tmp_path / "source"
    database(home / "state.db")
    with closing(sqlite3.connect(home / "state.db")) as writer:
        writer.execute("PRAGMA journal_mode=WAL")
        writer.execute("PRAGMA wal_autocheckpoint=0")
        writer.execute("INSERT INTO records VALUES ('committed WAL')")
        writer.commit()
        writer.execute("INSERT INTO records VALUES ('uncommitted')")
        before = hashes(home)

        # When the online read-only backup runs while the writer remains open.
        result = snapshot(home, "default", tmp_path / "work")

        # Then only committed rows survive, without modifying source bytes.
        assert hashes(home) == before
        with closing(sqlite3.connect(result.state_db.as_uri() + "?mode=ro", uri=True)) as copied:
            assert copied.execute("SELECT value FROM records").fetchall() == [
                ("original",), ("committed WAL",)]
            assert copied.execute("PRAGMA integrity_check").fetchone() == ("ok",)
        assert result.lcm_db is None and result.work_db is None
        assert result.spool is None and result.honcho_dump is None


def test_retry_reuses_verified_copy_without_overwriting(tmp_path: Path) -> None:
    # Given an already published snapshot.
    home, work = tmp_path / "source", tmp_path / "work"
    database(home / "state.db")
    first = snapshot(home, "default", work)
    before = {path: (path.stat().st_ino, path.stat().st_mtime_ns)
              for path in first.root.rglob("*")}

    # When identical inputs are retried.
    second = snapshot(home, "default", work)

    # Then the exact immutable files and receipt are reused.
    assert second == first
    assert before == {path: (path.stat().st_ino, path.stat().st_mtime_ns) for path in first.root.rglob("*")}


@pytest.mark.parametrize("change", ["copy", "extra", "profile"])
def test_retry_refuses_changed_inputs_or_copy(tmp_path: Path, change: str) -> None:
    # Given a published snapshot and a changed source or destination.
    home, work = tmp_path / "source", tmp_path / "work"
    database(home / "state.db")
    (home / "SOUL.md").write_text("persona")
    first = snapshot(home, "default", work)
    if change == "copy":
        copied = first.profile_dir / "SOUL.md"
        copied.chmod(0o600)
        copied.write_text("changed")
    if change == "extra":
        (first.profile_dir / "unexpected").write_text("changed")
    selected = "default"
    if change == "profile":
        selected = "alpha"
        database(home / "profiles/alpha/state.db")
    before = hashes(first.root)

    # When a retry is attempted, then it refuses without repairing or overwriting.
    with pytest.raises(SnapshotError, match="refusing overwrite"):
        snapshot(home, selected, work)
    assert hashes(first.root) == before


def test_retry_uses_snapshot_after_live_sources_change_or_disappear(tmp_path: Path) -> None:
    # Given an immutable snapshot whose live database has advanced.
    home, work = tmp_path / "source", tmp_path / "work"
    database(home / "state.db")
    (home / "SOUL.md").write_text("original persona")
    first = snapshot(home, "default", work)
    with closing(sqlite3.connect(home / "state.db")) as writer, writer:
        writer.execute("INSERT INTO records VALUES ('new live row')")
    (home / "SOUL.md").unlink()
    before = hashes(first.root)

    # When retried, then the published copies are reused without reading live content.
    assert snapshot(home, "default", work) == first
    assert hashes(first.root) == before
    assert (first.profile_dir / "SOUL.md").read_text() == "original persona"
    with closing(sqlite3.connect(first.state_db)) as copied:
        assert copied.execute("SELECT value FROM records").fetchall() == [("original",)]


@pytest.mark.parametrize("kind", ["file", "directory", "wal", "shm", "work", "external"])
def test_symlink_inputs_are_rejected(tmp_path: Path, kind: str) -> None:
    # Given a symlink on one of the input or output paths.
    home, work = tmp_path / "source", tmp_path / "work"
    database(home / "state.db")
    target = tmp_path / "outside"
    target.write_text("do not read")
    links = {"file": home / "SOUL.md", "directory": home / "skills",
             "wal": home / "state.db-wal", "shm": home / "state.db-shm",
             "work": work, "external": tmp_path / "spool"}
    links[kind].symlink_to(target)

    # When snapshotting, then symlinks fail before publication.
    with pytest.raises(SnapshotError, match="symlinks"):
        snapshot(home, "default", work, spool=links[kind] if kind == "external" else None)
    assert not (work / "snapshot").exists()


@pytest.mark.parametrize("profile", ["../alpha", "/alpha", "alpha/beta", "..", ""])
def test_profile_traversal_is_rejected(tmp_path: Path, profile: str) -> None:
    database(tmp_path / "source/state.db")
    with pytest.raises(SnapshotError, match="invalid profile"):
        snapshot(tmp_path / "source", profile, tmp_path / "work")


def test_overlap_and_explicit_path_traversal_are_rejected(tmp_path: Path) -> None:
    home = tmp_path / "source"
    database(home / "state.db")
    with pytest.raises(SnapshotError, match="overlap"):
        snapshot(home, "default", home / "work")
    with pytest.raises(SnapshotError, match="overlap"):
        snapshot(home, "default", tmp_path)
    with pytest.raises(SnapshotError, match="traversal"):
        snapshot(home, "default", tmp_path / "work", spool=home / ".." / "outside")


def test_failed_backup_can_retry_and_explicit_work_db_is_copied(tmp_path: Path) -> None:
    # Given a malformed required database and a separate optional work database.
    home, work = tmp_path / "source", tmp_path / "work"
    home.mkdir()
    (home / "state.db").write_text("not SQLite")
    external_work = tmp_path / "work-source.db"
    database(external_work)
    with pytest.raises(sqlite3.DatabaseError):
        snapshot(home, "default", work, work_db=external_work)
    assert list(work.iterdir()) == []
    (home / "state.db").unlink()
    database(home / "state.db")

    # When the repaired input is retried, then no partial snapshot blocks it.
    result = snapshot(home, "default", work, work_db=external_work)
    with closing(sqlite3.connect(result.work_db)) as copied:
        assert copied.execute("SELECT value FROM records").fetchall() == [("original",)]


@pytest.mark.parametrize("profile,host", [("default", "hermes"), ("alpha", "hermes_alpha")])
def test_honcho_snapshot_keeps_only_selected_host_identity(tmp_path: Path, profile: str, host: str) -> None:
    home = tmp_path / "source"
    selected = home if profile == "default" else home / "profiles" / profile
    database(selected / "state.db")
    legacy = {"baseUrl": "https://OLD-ENDPOINT-DO-NOT-COPY.example", "apiKey": "SYNTHETIC-KEY",
              "observationMode": "directional", "hosts": {
                  host: {"workspace": "shared", "aiPeer": "agent-a", "peerName": "person"},
                  "hermes_other": {"workspace": "other", "aiPeer": "agent-b", "peerName": "other-person"}}}
    (selected / "honcho.json").write_text(json.dumps(legacy))

    copied = snapshot(home, profile, tmp_path / "work")

    identity = copied.root / "external/honcho-config.json"
    assert json.loads(identity.read_text()) == {"hosts": {host: {
        "workspace": "shared", "aiPeer": "agent-a", "peerName": "person", "observationMode": "directional"}}}
    assert not (copied.profile_dir / "honcho.json").exists()
    assert "OLD-ENDPOINT-DO-NOT-COPY" not in identity.read_text()
    assert "SYNTHETIC-KEY" not in identity.read_text()
