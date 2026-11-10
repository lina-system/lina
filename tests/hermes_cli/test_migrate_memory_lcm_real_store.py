"""Shapes real LCM-X stores carry that a native LCM copy must take (lina#383), on synthetic data."""

from contextlib import closing
import json
import sqlite3

import pytest

from tests.hermes_cli.test_migrate_memory import _command, _run

QUARANTINE = """
    CREATE TABLE lcm_fork_quarantined_messages (store_id INTEGER PRIMARY KEY, original_session_id TEXT NOT NULL,
        original_conversation_id TEXT, node_ids TEXT NOT NULL, reason TEXT NOT NULL, frontier_before INTEGER,
        frontier_after INTEGER, quarantined_at REAL NOT NULL, restored_at REAL);
    CREATE TABLE lcm_chunk_meta (chunk_id TEXT, identity_hash TEXT, store_id INTEGER, chunk_index INTEGER,
        char_start INTEGER, char_end INTEGER, token_estimate INTEGER, embedded_at TEXT, archived INTEGER DEFAULT 0,
        PRIMARY KEY(chunk_id, identity_hash));
    CREATE TABLE lcm_chunk_vectors (chunk_id TEXT, identity_hash TEXT, vec BLOB NOT NULL);
"""
BINDINGS = """
    CREATE TABLE host_uid_bindings (store_id INTEGER NOT NULL, uid TEXT NOT NULL, lineage_key TEXT NOT NULL,
        kind TEXT NOT NULL, binding_version INTEGER NOT NULL, proof_kind TEXT, created_at REAL, first_check TEXT,
        disagree_seen INTEGER NOT NULL DEFAULT 0);
"""


def _source(tmp_path, script: str, *statements: tuple) -> None:
    with closing(sqlite3.connect(tmp_path / "hermes/profiles/alpha/lcm.db")) as db:
        db.executescript(script)
        for statement, values in statements:
            db.execute(statement, values)
        db.commit()


def _binding(store_id: int, uid: str, kind: str) -> tuple:
    return ("INSERT INTO host_uid_bindings(store_id,uid,lineage_key,kind,binding_version,proof_kind,created_at) "
            "VALUES (?,?,'lineage-a',?,1,'synthetic',1767323050)", (store_id, uid, kind))


def test_engine_bindings_and_quarantined_rows_migrate_and_replay(tmp_path, _memory_store, memory_store_sql):
    # Given: an engine uid binding (store_id 0) and two quarantined rows, one cited by a summary node.
    command = _command(tmp_path, _memory_store)
    held = "session-a::fork-quarantine"
    _source(tmp_path, BINDINGS + QUARANTINE,
            ("INSERT INTO messages VALUES (?, ?, 'agent', 'conversation-a::fork-quarantine', 'user', ?, ?)",
             (4, held, "uncited summary head copy", 1767323049)),
            ("INSERT INTO messages VALUES (?, ?, 'agent', 'conversation-a::fork-quarantine', 'user', ?, ?)",
             (5, held, "cited summary head copy", 1767323049)),
            ("INSERT INTO summary_nodes VALUES (3, 'session-a', 0, 'cites quarantine', 5, 10, '[5]', 'messages', "
             "1767323050, 1767323049, 1767323049, '')", ()),
            *[("INSERT INTO lcm_fork_quarantined_messages VALUES (?, 'session-a', 'conversation-a', '1', "
               "'synthetic', NULL, NULL, 1767323051, NULL)", (store_id,)) for store_id in (4, 5)],
            ("INSERT INTO lcm_chunk_meta(chunk_id,identity_hash,store_id,chunk_index) VALUES ('chunk-4','h',4,0)", ()),
            ("INSERT INTO lcm_chunk_vectors VALUES ('chunk-4','h',?)", (b"\0" * 16,)),
            _binding(0, "engine-uid", "engine"), _binding(1, "host-uid-1", "canonical"),
            _binding(4, "host-uid-4", "canonical"))
    # When: importing through the CLI, twice.
    done = _run(command, tmp_path)
    assert done.returncode == 0, done.stdout + done.stderr
    report = json.loads(done.stdout)["lcm"]
    again = _run(command, tmp_path)
    assert again.returncode == 0, again.stdout + again.stderr
    # Then: the engine binding is copied, the uncited row and what only it owns are counted out, the cited one stays.
    assert report["ok"] and report["engine_uid_bindings"] == 1
    assert report["fork_quarantine"] == {"records": 2, "left_out_messages": 1, "kept_cited_messages": 1}
    left = {"reason": "fork_quarantine_uncited"}
    assert report["counts"]["messages"] == {"source": 5, "matched": 0, "inserted": 4, "skipped": 1, **left}
    assert report["counts"]["host_uid_bindings"] == {"source": 3, "matched": 0, "inserted": 2, "skipped": 1, **left}
    assert report["counts"]["lcm_chunk_meta"] == {"source": 1, "matched": 0, "inserted": 0, "skipped": 1, **left}
    assert report["counts"]["lcm_chunk_vectors"]["skipped"] == 1
    assert report["counts"]["lcm_fork_quarantined_messages"]["skipped"] == 2
    assert memory_store_sql("SELECT store_id,session_id FROM ctx.lcm_messages ORDER BY store_id") == [
        (1, "session-a"), (2, "session-a"), (3, "session-a"), (5, held)]
    assert memory_store_sql("SELECT store_id,uid,kind FROM ctx.host_uid_bindings ORDER BY store_id") == [
        (0, "engine-uid", "engine"), (1, "host-uid-1", "canonical")]
    assert json.loads(again.stdout)["lcm"]["inserted"] == 0


@pytest.mark.parametrize("kind", ["canonical", "engine"])
def test_binding_to_a_missing_message_still_fails(tmp_path, _memory_store, memory_store_sql, kind):
    # Given: a binding whose non-zero store_id names no message.
    command = _command(tmp_path, _memory_store)
    _source(tmp_path, BINDINGS, _binding(0, "engine-uid", "engine"), _binding(99, "lost-uid", kind))
    # When / Then: refused before any LCM row is written.
    result = _run(command, tmp_path)
    assert result.returncode == 1
    assert "unresolved LCM reference: host_uid_bindings.store_id/99" in json.loads(result.stdout)["error"]
    assert memory_store_sql("SELECT count(*) FROM ctx.lcm_messages") == [(0,)]


def _tool_rows(tmp_path, lcm_name):
    with closing(sqlite3.connect(tmp_path / "hermes/profiles/alpha/state.db")) as db:
        db.execute("ALTER TABLE messages ADD COLUMN tool_name TEXT")
        db.execute("UPDATE messages SET tool_name='synthetic_tool' WHERE role='tool'")
        db.commit()
    _source(tmp_path, "ALTER TABLE messages ADD COLUMN tool_name TEXT;",
            ("INSERT INTO messages VALUES (4,'session-a','agent','conversation-a','tool','full tool response',"
             "1767323046,?)", (lcm_name,)))


def test_null_lcm_tool_name_takes_the_matched_host_name(tmp_path, _memory_store, memory_store_sql):
    # Given: an LCM tool row without the name its host text keeps.
    command = _command(tmp_path, _memory_store)
    _tool_rows(tmp_path, None)
    # When: importing twice.
    done = _run(command, tmp_path)
    assert done.returncode == 0, done.stdout + done.stderr
    again = _run(command, tmp_path)
    assert again.returncode == 0, again.stdout + again.stderr
    # Then: the copy reuses the host text with its name, counted, and the replay accepts it.
    report = json.loads(done.stdout)["lcm"]
    assert report["matched_text"] == 2 and report["tool_name_from_host"] == 1
    assert memory_store_sql("SELECT tool_name FROM ctx.messages WHERE store_id=4") == [("synthetic_tool",)]
    assert json.loads(again.stdout)["lcm"]["tool_name_from_host"] == 1


def test_different_lcm_tool_name_still_fails(tmp_path, _memory_store, memory_store_sql):
    command = _command(tmp_path, _memory_store)
    _tool_rows(tmp_path, "other_tool")
    result = _run(command, tmp_path)
    assert result.returncode == 1
    assert "changed imported LCM row: messages/4" in json.loads(result.stdout)["error"]
    assert memory_store_sql("SELECT count(*) FROM ctx.lcm_messages") == [(0,)]
