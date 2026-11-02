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


def test_search_filters_and_oldest_sort_see_past_the_candidate_page(tmp_path, monkeypatch):
    import hermes_state_search

    monkeypatch.setattr(hermes_state_search, "_CANDIDATE_LIMIT", 2)
    db = SessionDB(tmp_path / "state.db")
    try:
        db.create_session(session_id="quiet", source="telegram")
        for i in range(2):
            db.append_message("quiet", "user", f"harbor ledger {i}", timestamp=1_600_000_000 + i)
        db.create_session(session_id="busy", source="cli")
        for i in range(5):
            db.append_message("busy", "user", f"harbor ledger busy {i}", timestamp=1_700_000_000 + i)

        quiet = db.search_messages("ledger", source_filter=["telegram"], limit=10)
        assert sorted(r["session_id"] for r in quiet) == ["quiet", "quiet"]
        oldest = db.search_messages("ledger", sort="oldest", limit=1)
        assert [r["session_id"] for r in oldest] == ["quiet"]
        assert len(db.search_messages("ledger", limit=50)) == 7
    finally:
        db.close()


def test_a_cached_message_written_again_after_another_process_removed_its_row_is_stored(tmp_path, memory_store_sql):
    import hermes_state_store

    db = SessionDB(tmp_path / "state.db")
    try:
        db.create_session(session_id="cache", source="cli")
        row = {"session_id": "cache", "role": "user", "content": "the pier inventory", "timestamp": 1_700_000_000.0,
               "message_uid": "cache-msg-1"}
        db._execute_write(lambda conn: insert_rows(conn, db.message_store, [row]))
        uid = _text_uid(db, "cache", "user")
        # Another process collects the row; this process's cache still holds it.
        memory_store_sql("DELETE FROM memory.messages WHERE uid = %s", (uid,))
        db._execute_write(lambda conn: insert_rows(conn, db.message_store, [row]))

        assert memory_store_sql("SELECT count(*) FROM memory.messages WHERE uid = %s", (uid,)) == [(1,)]
        hermes_state_store.TEXT_CACHE.clear()
        assert [m["content"] for m in db.get_messages_as_conversation("cache")] == ["the pier inventory"] * 2
    finally:
        db.close()


def test_text_shared_by_a_branch_is_collected_once_no_session_uses_it_and_its_grace_ran_out(
        tmp_path, memory_store_sql):
    from hermes_state_store import collect_session_db_text

    db = SessionDB(tmp_path / "state.db")
    try:
        for sid in ("parent", "branch"):
            db.create_session(session_id=sid, source="cli")
            rows = [{"session_id": sid, "role": "user", "content": f"shared line {i}", "timestamp": 1_700_000_000.0 + i,
                     "message_uid": f"shared-{i}"} for i in range(3)]
            db._execute_write(lambda conn, rows=rows: insert_rows(conn, db.message_store, rows))
        shared = memory_store_sql("SELECT count(*) FROM memory.messages")
        assert shared == [(3,)]
        age = "UPDATE memory.messages SET observed_at = now() - interval '40 days'"

        db.delete_session("parent")
        memory_store_sql(age)
        assert collect_session_db_text(db, older_than_days=30) == 0  # the branch still uses every row
        assert [m["content"] for m in db.get_messages_as_conversation("branch")] == [
            "shared line 0", "shared line 1", "shared line 2"]

        db.delete_session("branch")
        memory_store_sql("UPDATE memory.messages SET observed_at = now() - interval '5 days'")
        assert collect_session_db_text(db, older_than_days=30) == 0  # within the grace period: kept
        assert memory_store_sql("SELECT count(*) FROM memory.messages") == [(3,)]
        memory_store_sql(age)
        assert collect_session_db_text(db, older_than_days=30) == 3
        assert memory_store_sql("SELECT count(*) FROM memory.messages") == [(0,)]
    finally:
        db.close()
