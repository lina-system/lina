"""Basis timestamps and profile-local quote verification (lina#36)."""

import sqlite3
import datetime
from contextlib import closing

import pytest

from tests.hermes_cli._work_env import NOW, WorkEnv


@pytest.mark.parametrize("source", ["10-27 01:0x chat", "10-27", "2026-02-30 01:12", "10-27 24:00", ""])
def test_vague_timestamp_stays_unknown(tmp_path, monkeypatch, source):
    # Given
    WorkEnv(tmp_path, monkeypatch)
    from hermes_cli.work.sources import said_ts
    # When
    timestamp = said_ts(source)
    # Then
    assert timestamp == 0


def test_basis_verifies_against_assigned_profile_when_user_quote_matches(tmp_path, monkeypatch):
    # Given
    work = WorkEnv(tmp_path, monkeypatch)
    from hermes_cli.profiles import get_profile_dir
    profile = get_profile_dir("alpha")
    profile.mkdir(parents=True, exist_ok=True)
    (profile / "config.yaml").write_text("{}", encoding="utf-8")
    with closing(sqlite3.connect(profile / "state.db")) as db:
        db.execute("CREATE TABLE sessions (id TEXT PRIMARY KEY, source TEXT)")
        db.execute("CREATE TABLE messages (session_id TEXT, role TEXT, content TEXT, timestamp REAL)")
        db.execute("INSERT INTO sessions VALUES ('message', 'chat')")
        db.execute("INSERT INTO messages VALUES (?, ?, ?, ?)", ("message", "user", "자료를 정리해 줘", NOW - 60))
        db.commit()
    before = (profile / "state.db").read_bytes()
    # When
    work.ok("add", "메모", "--by", "alpha", "--title", "자료 정리", "--basis", "자료를 정리해 줘")
    # Then
    row = next(row for row in work.json()["items"] if row["id"] == "M1")
    assert (row["basis"][0]["verified"], row["basis"][0]["at"], row["basis"][0]["src"]) == (
        "found", NOW - 60, "chat")
    assert (profile / "state.db").read_bytes() == before


def test_future_basis_uses_created_time_without_mutating_record(tmp_path, monkeypatch):
    # Given
    WorkEnv(tmp_path, monkeypatch)
    from hermes_cli.work.sources import basis_of, first_said
    row = dict(created=NOW - 100, ts=NOW - 50, basis=[dict(text="Synthetic request", at=NOW - 10)])
    # When
    basis = basis_of(row)
    # Then
    assert basis[0]["at"] == 0
    assert first_said(row) == NOW - 100
    assert row["basis"][0]["at"] == NOW - 10


@pytest.mark.parametrize("claimed,expected_at,expected_status", [
    (0, NOW - 120, "found"),
    (NOW - 60, NOW - 60, "found"),
    (NOW - 3600, NOW - 120, "time_mismatch"),
])
def test_quote_match_selects_earliest_or_closest_user_message(tmp_path, monkeypatch, claimed, expected_at, expected_status):
    # Given
    WorkEnv(tmp_path, monkeypatch)
    from hermes_cli.profiles import get_profile_dir
    from hermes_cli.work.sources import verify_basis
    profile = get_profile_dir("alpha")
    profile.mkdir(parents=True, exist_ok=True)
    (profile / "config.yaml").write_text("{}", encoding="utf-8")
    with closing(sqlite3.connect(profile / "state.db")) as db:
        db.execute("CREATE TABLE sessions (id TEXT PRIMARY KEY, source TEXT)")
        db.execute("CREATE TABLE messages (session_id TEXT, role TEXT, content TEXT, timestamp REAL)")
        db.execute("INSERT INTO sessions VALUES ('message', 'chat')")
        db.executemany("INSERT INTO messages VALUES (?, ?, ?, ?)", [
            ("message", "user", "Synthetic, blue—kite!", NOW - 120),
            ("message", "user", "Synthetic blue kite", NOW - 60),
            ("message", "assistant", "Synthetic blue kite", NOW - 10),
            ("message", "user", "Synthetic blue kite", NOW + 10),
        ])
        db.commit()
    said = datetime.datetime.fromtimestamp(claimed, datetime.UTC).strftime("%Y-%m-%d %H:%M:%S") if claimed else ""
    # When
    result = verify_basis(dict(text="Synthetic blue kite", said=said), "alpha")
    # Then
    assert (result["at"], result["verified"], result["src"]) == (expected_at, expected_status, "chat")


@pytest.mark.parametrize("role,offset", [("assistant", -60), ("user", 60), ("user", -14 * 86400 - 1)])
def test_quote_rejects_messages_outside_user_history(tmp_path, monkeypatch, role, offset):
    # Given
    WorkEnv(tmp_path, monkeypatch)
    from hermes_cli.profiles import get_profile_dir
    from hermes_cli.work.sources import verify_basis
    profile = get_profile_dir("alpha")
    profile.mkdir(parents=True, exist_ok=True)
    (profile / "config.yaml").write_text("{}", encoding="utf-8")
    with closing(sqlite3.connect(profile / "state.db")) as db:
        db.execute("CREATE TABLE sessions (id TEXT PRIMARY KEY, source TEXT)")
        db.execute("CREATE TABLE messages (session_id TEXT, role TEXT, content TEXT, timestamp REAL)")
        db.execute("INSERT INTO sessions VALUES ('message', 'chat')")
        db.execute("INSERT INTO messages VALUES (?, ?, ?, ?)", ("message", role, "Synthetic request", NOW + offset))
        db.commit()
    # When
    result = verify_basis(dict(text="Synthetic request", at=0), "alpha")
    # Then
    assert result["verified"] == "not_found"
