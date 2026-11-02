"""lina#12: a message is written once to memory.messages and every reader reads that one row.

These run on the real Postgres from tests/_fixtures/memory_store.py.
"""

from __future__ import annotations

import json

import pytest

from gateway.config import GatewayConfig
from gateway.session import SessionStore
from hermes_state import SessionDB
from hermes_state_store import MessageStore, insert_rows
from tools.session_search_tool import session_search


def _text_uid(db: SessionDB, session_id: str, role: str) -> str:
    row = db._conn.execute(
        "SELECT text_uid FROM transcript_rows WHERE session_id = ? AND role = ? ORDER BY id DESC LIMIT 1",
        (session_id, role)).fetchone()
    return row[0]


def test_a_gateway_turn_is_one_row_that_search_transcript_and_engines_read(tmp_path, monkeypatch, memory_store_sql):
    import hermes_state

    monkeypatch.setenv("HERMES_HOME", str(tmp_path))
    monkeypatch.setattr(hermes_state, "DEFAULT_DB_PATH", tmp_path / "state.db")
    store = SessionStore(sessions_dir=tmp_path, config=GatewayConfig())
    try:
        sid = "gw-one-row"
        store._db.create_session(session_id=sid, source="telegram")
        store.append_to_transcript(sid, {"role": "user", "content": "Where did we park the zeppelin manifest?",
                                         "timestamp": 1_700_000_000.0})
        store.append_to_transcript(sid, {"role": "assistant", "content": "In the hangar ledger.",
                                         "timestamp": 1_700_000_001.0})
        db = store._db
        uid = _text_uid(db, sid, "user")

        found = json.loads(session_search(query="zeppelin", db=db))
        assert [r["session_id"] for r in found["results"]] == [sid]

        transcript = db.get_messages_as_conversation(sid)
        assert [m["content"] for m in transcript] == [
            "Where did we park the zeppelin manifest?", "In the hangar ledger."]

        engine_rows = db.message_store.session_messages(sid)
        first = engine_rows[0]
        assert (first["uid"], first["role"], first["content"]) == (uid, "user", "Where did we park the zeppelin manifest?")

        assert memory_store_sql("SELECT count(*) FROM memory.messages WHERE uid = %s", (uid,)) == [(1,)]
    finally:
        store.close_all_db_handles()


def test_a_two_character_korean_term_matches_inside_a_longer_word(tmp_path):
    db = SessionDB(tmp_path / "state.db")
    try:
        db.create_session(session_id="ko", source="cli")
        db.append_message("ko", "user", "오늘 회의록을 정리해 줘")
        db.append_message("ko", "assistant", "정리했습니다")

        hits = db.search_messages("회의")
        assert [(h["session_id"], h["role"]) for h in hits] == [("ko", "user")]
        assert ">>>회의<<<" in hits[0]["snippet"]

        tool_hits = json.loads(session_search(query="회의", db=db))
        assert [r["session_id"] for r in tool_hits["results"]] == ["ko"]
    finally:
        db.close()


def test_state_db_keeps_no_message_text(tmp_path):
    db = SessionDB(tmp_path / "state.db")
    marker = "ultramarine-walrus-7731"
    try:
        db.create_session(session_id="s", source="cli")
        db.append_message("s", "user", f"remember {marker}")
        db.append_message("s", "assistant", "ok", tool_calls=[{"id": "c1", "type": "function", "function": {
            "name": "memory", "arguments": json.dumps({"note": marker})}}], reasoning=f"thinking {marker}")
        assert db.get_messages("s")[0]["content"] == f"remember {marker}"
        db._conn.execute("PRAGMA wal_checkpoint(TRUNCATE)")
    finally:
        db.close()
    for path in tmp_path.glob("state.db*"):
        assert marker.encode() not in path.read_bytes(), path.name


def test_one_profile_cannot_read_another_profiles_rows(tmp_path):
    db = SessionDB(tmp_path / "state.db")
    try:
        db.create_session(session_id="s", source="cli")
        db.append_message("s", "user", "private to this profile")
        uid = _text_uid(db, "s", "user")
        assert db.message_store.texts([uid])
        other = MessageStore("some-other-profile")
        try:
            assert other.texts([uid]) == {}
            assert other.count() == 0
        finally:
            other.close()
    finally:
        db.close()


def test_a_compacted_copy_of_a_message_shares_its_row(tmp_path, memory_store_sql):
    db = SessionDB(tmp_path / "state.db")
    try:
        db.create_session(session_id="s", source="cli")
        db.append_message("s", "user", "first question")
        db.append_message("s", "assistant", "first answer")
        live = db.get_messages_as_conversation("s")
        db.archive_and_compact("s", [{"role": "user", "content": "[summary]"}, *live], tail_count=2)
        rows = db._conn.execute(
            "SELECT text_uid, active, compacted FROM transcript_rows WHERE session_id = 's' ORDER BY id").fetchall()
        assert len(rows) == 5
        assert memory_store_sql("SELECT count(*) FROM memory.messages") == [(3,)]
    finally:
        db.close()


def test_text_of_a_rolled_back_write_is_written_again_by_its_retry(tmp_path, memory_store_sql):
    db = SessionDB(tmp_path / "state.db")
    try:
        db.create_session(session_id="rb", source="cli")
        row = {"session_id": "rb", "role": "user", "content": "the vanished ledger", "timestamp": 1_700_000_000.0,
               "message_uid": "rb-msg-1"}

        def fails_before_commit(conn):
            insert_rows(conn, db.message_store, [row])
            raise RuntimeError("the state.db write fails after the text went in")

        with pytest.raises(RuntimeError):
            db._execute_write(fails_before_commit)
        db._execute_write(lambda conn: insert_rows(conn, db.message_store, [row]))

        assert memory_store_sql("SELECT count(*) FROM memory.messages WHERE uid = %s",
                                (_text_uid(db, "rb", "user"),)) == [(1,)]
        assert [m["content"] for m in db.get_messages_as_conversation("rb")] == ["the vanished ledger"]
    finally:
        db.close()

