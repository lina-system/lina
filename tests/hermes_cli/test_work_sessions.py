"""Optional queue session results exposed through JSON (lina#36)."""

import json
import sqlite3
from contextlib import closing

from tests.hermes_cli._work_env import NOW, WorkEnv


def test_session_json_reads_current_assistant_when_profile_database_exists(tmp_path, monkeypatch):
    # Given
    work = WorkEnv(tmp_path, monkeypatch)
    from hermes_cli.profiles import get_profile_dir
    profile = get_profile_dir("alpha")
    profile.mkdir(parents=True, exist_ok=True)
    (profile / "config.yaml").write_text("{}", encoding="utf-8")
    with closing(sqlite3.connect(profile / "state.db")) as db:
        db.execute("CREATE TABLE sessions (id TEXT, title TEXT)")
        db.execute("CREATE TABLE messages (session_id TEXT, role TEXT, content TEXT, timestamp REAL)")
        db.execute("INSERT INTO sessions VALUES (?, ?)", ("current", "topic"))
        db.execute("INSERT INTO messages VALUES (?, ?, ?, ?)", ("current", "assistant", "Current result", NOW))
        db.commit()
    (work.queue / "running").mkdir(parents=True)
    (work.queue / "running/current.json").write_text(json.dumps({
        "id": "current", "profile": "alpha", "name": "topic", "state": "running", "started": NOW - 60,
    }), encoding="utf-8")
    # When
    sessions = work.json()["sessions"]
    # Then
    assert sessions[0]["now_text"] == "Current result"


def test_pending_query_is_hidden_when_file_is_outside_home(tmp_path, monkeypatch):
    # Given
    work = WorkEnv(tmp_path, monkeypatch)
    query = tmp_path / "private.txt"
    query.write_text("Private query", encoding="utf-8")
    (work.queue / "pending").mkdir(parents=True)
    (work.queue / "pending/current.json").write_text(json.dumps({
        "id": "current", "profile": "alpha", "name": "topic", "state": "pending", "query_file": str(query),
    }), encoding="utf-8")
    # When
    sessions = work.json()["sessions"]
    # Then
    assert len(sessions) == 1
    assert "query" not in sessions[0]


def test_finished_output_preserves_first_result_when_log_is_long(tmp_path, monkeypatch):
    # Given
    work = WorkEnv(tmp_path, monkeypatch)
    work.fixture()
    log = work.home / "output.log"
    log.write_text("=== 2026-01-01 12:00:00 synthetic run\nFirst result\n" + "More detail " * 900,
                   encoding="utf-8")
    (work.queue / "done").mkdir()
    (work.queue / "done/old.json").write_text(json.dumps({
        "id": "old", "profile": "alpha", "name": "fixture-topic", "state": "exited",
        "started": NOW - 9000, "ended": NOW - 8000, "rc": 0, "output_log": str(log),
    }), encoding="utf-8")
    # When
    sessions = work.json()["sessions"]
    # Then
    current = next(row for row in sessions if row["bucket"] == "running")
    assert current["last_result"] == "First result"
