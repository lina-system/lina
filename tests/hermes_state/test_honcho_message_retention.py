"""lina#13: Honcho holds shared text independently of state.db transcripts."""

import hashlib
from pathlib import Path

import pytest
import psycopg

from hermes_state import SessionDB
from hermes_state_store import MessageStore, move_agent_texts


@pytest.fixture
def honcho_meta(memory_store_sql):
    # The retention contract needs the same shared-ID FK as Honcho's migration.
    memory_store_sql("CREATE TABLE ltm.message_meta (message_id bigint PRIMARY KEY "
                     "REFERENCES memory.messages(message_id) ON DELETE CASCADE)")
    memory_store_sql("ALTER TABLE ltm.message_meta OWNER TO honcho")
    try:
        yield memory_store_sql
    finally:
        memory_store_sql("DROP TABLE ltm.message_meta")


def _aged_text(sql, uid):
    return sql(
        "INSERT INTO memory.messages (uid, source, agent_id, content, content_sha256, observed_at) "
        "VALUES (%s, 'agent', 'harbor', 'synthetic held text', %s, now() - interval '40 days') "
        "RETURNING message_id", (uid, hashlib.sha256(b"synthetic held text").hexdigest()))[0][0]


@pytest.mark.parametrize("uid", ["agent:harbor:shared", "honcho:harbor:public"])
def test_collection_preserves_text_held_only_by_honcho(honcho_meta, uid):
    message_id = _aged_text(honcho_meta, uid)
    honcho_meta("INSERT INTO ltm.message_meta VALUES (%s)", (message_id,))
    store = MessageStore("harbor")
    try:
        deleted = store.collect_unreferenced(lambda _uids: set(), older_than_days=30)
        assert deleted == 0
        assert honcho_meta("SELECT m.content FROM memory.messages m JOIN ltm.message_meta h "
                           "USING (message_id)") == [("synthetic held text",)]
    finally:
        store.close()


def test_collection_deletes_text_without_honcho_holder(honcho_meta):
    _aged_text(honcho_meta, "agent:harbor:unheld")
    store = MessageStore("harbor")
    try:
        assert store.collect_unreferenced(lambda _uids: set(), older_than_days=30) == 1
        assert honcho_meta("SELECT count(*) FROM memory.messages") == [(0,)]
    finally:
        store.close()


def test_holder_check_does_not_grant_metadata_reads(honcho_meta, _memory_store):
    message_id = _aged_text(honcho_meta, "agent:harbor:least-privilege")
    honcho_meta("INSERT INTO ltm.message_meta VALUES (%s)", (message_id,))
    with psycopg.connect(_memory_store.app_dsn(), autocommit=True) as conn:
        assert conn.execute("SELECT memory.honcho_holds_message(%s)", (message_id,)).fetchone() == (True,)
        with pytest.raises(psycopg.errors.InsufficientPrivilege):
            conn.execute("SELECT message_id FROM ltm.message_meta")


@pytest.mark.parametrize("schema_exists", [True, False])
def test_collection_before_honcho_table_exists(memory_store_sql, schema_exists):
    _aged_text(memory_store_sql, "agent:harbor:before-honcho")
    if not schema_exists:
        memory_store_sql("DROP SCHEMA ltm")
    store = MessageStore("harbor")
    try:
        assert store.collect_unreferenced(lambda _uids: set(), older_than_days=30) == 1
        assert memory_store_sql("SELECT count(*) FROM memory.messages") == [(0,)]
    finally:
        store.close()
        if not schema_exists:
            memory_store_sql("CREATE SCHEMA ltm AUTHORIZATION honcho")
            memory_store_sql("GRANT USAGE ON SCHEMA ltm TO lina_app")


def test_profile_text_move_preserves_honcho_read(tmp_path: Path, honcho_meta, _memory_store):
    path = tmp_path / "state.db"
    db = SessionDB(path)
    try:
        db.create_session("history", "cli")
        db.append_message("history", "user", "synthetic renamed text")
        old_agent = db.message_store.agent_id
        message_id = honcho_meta("SELECT message_id FROM memory.messages")[0][0]
        honcho_meta("INSERT INTO ltm.message_meta VALUES (%s)", (message_id,))
    finally:
        db.close()

    assert move_agent_texts(path, old_agent, "lighthouse") == 1

    with psycopg.connect(_memory_store.superuser_dsn()) as conn:
        conn.execute("SET LOCAL ROLE honcho")
        assert conn.execute("SELECT m.message_id, m.agent_id, m.content FROM ltm.message_meta h "
                            "JOIN memory.messages m USING (message_id)").fetchall() == [
                                (message_id, "lighthouse", "synthetic renamed text")]
    db = SessionDB(path)
    try:
        assert db._conn is not None
        assert db._conn.execute("SELECT text_uid FROM transcript_rows").fetchone()[0].startswith(
            "agent:lighthouse:")
    finally:
        db.close()


def test_collection_checks_honcho_hold_at_delete_not_candidate_scan(honcho_meta):
    message_id = _aged_text(honcho_meta, "agent:harbor:late-holder")

    def held_after_scan(_uids):
        honcho_meta("INSERT INTO ltm.message_meta VALUES (%s)", (message_id,))
        return set()

    store = MessageStore("harbor")
    try:
        assert store.collect_unreferenced(held_after_scan, older_than_days=30) == 0
        assert honcho_meta("SELECT count(*) FROM ltm.message_meta") == [(1,)]
    finally:
        store.close()
