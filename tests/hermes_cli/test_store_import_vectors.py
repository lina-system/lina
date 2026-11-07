"""lina#365: `lina store reembed --source messages --from-index <db>` imports the collector's stored chunk vectors.

The collector index is a synthetic SQLite file of the shape docs/store.md defines; the store is the real Postgres
from tests/_fixtures/memory_store.py.
"""

from __future__ import annotations

import argparse
import hashlib
import sqlite3
import struct

import psycopg
import pytest

from agent.embedding_client import truncate_stored
from hermes_cli.memory_eval_search import Question, new_vector
from hermes_cli.store_import_vectors import import_index, model_key
from hermes_cli.subcommands.store import build_store_parser
from hermes_state_store import MessageStore

COLLECTOR_MODEL = "qwen/qwen3-embedding-8b"
MODEL = "qwen3-embedding-8b"


def _stored(hot: int) -> list[float]:
    vector = [0.0] * 1536
    vector[hot] = 3.0
    vector[1200] = 4.0
    return vector


def _index(path, chunks: list[tuple[int, str, list[float], list[str]]]):
    conn = sqlite3.connect(path)
    conn.executescript("CREATE TABLE chunks (chunk_id INTEGER PRIMARY KEY, model TEXT NOT NULL, dim INTEGER NOT NULL, "
                       "embedding BLOB NOT NULL); CREATE TABLE chunk_messages (chunk_id INTEGER NOT NULL, "
                       "position INTEGER NOT NULL, message_uid TEXT NOT NULL, PRIMARY KEY (chunk_id, position));")
    for chunk_id, model, vector, uids in chunks:
        conn.execute("INSERT INTO chunks VALUES (?, ?, ?, ?)",
                     (chunk_id, model, len(vector), struct.pack(f"<{len(vector)}f", *vector)))
        conn.executemany("INSERT INTO chunk_messages VALUES (?, ?, ?)",
                         [(chunk_id, position, uid) for position, uid in enumerate(uids)])
    conn.commit()
    conn.close()
    return path


def _external(sql, uid: str, content: str) -> int:
    return sql("INSERT INTO memory.messages (uid, source, agent_id, scope, role, content, content_sha256) "
               "VALUES (%s, 'messages', NULL, 'agents', 'user', %s, %s) RETURNING message_id",
               (f"messages:{uid}", content, hashlib.sha256(content.encode()).hexdigest()))[0][0]


def _import(memory_store, path, lines=None):
    with psycopg.connect(memory_store.superuser_dsn(), autocommit=True) as conn:
        return import_index(conn, path, model=MODEL, dimensions=1024, batch_size=2,
                            out=(lines.append if lines is not None else lambda _line: None))


def _vec_rows(sql):
    return sql("SELECT c.source, c.agent_id, c.message_ids, c.text, e.agent_id, e.model, e.dim FROM vec.chunks c "
               "JOIN vec.embeddings e ON e.kind = 'chunk' AND e.item_id = c.chunk_id ORDER BY c.chunk_id")


def test_model_names_compare_without_the_provider_prefix():
    assert model_key(COLLECTOR_MODEL) == model_key(MODEL) == MODEL
    assert model_key("text-embedding-3-small") != model_key(MODEL)


def test_a_same_model_chunk_becomes_one_chunk_row_and_one_cut_vector(_memory_store, memory_store_sql, tmp_path):
    ids = [_external(memory_store_sql, f"u{i}", f"message {i} about the boiler") for i in range(3)]
    index = _index(tmp_path / "index.db", [(7, COLLECTOR_MODEL, _stored(5), ["u2", "u0", "u1"])])

    counts = _import(_memory_store, index)

    assert (counts.imported, counts.present) == (1, 0)
    assert _vec_rows(memory_store_sql) == [
        ("messages", "*", [ids[2], ids[0], ids[1]],
         "message 2 about the boiler\nmessage 0 about the boiler\nmessage 1 about the boiler", "*", MODEL, 1024)]
    stored = memory_store_sql("SELECT embedding::text FROM vec.embeddings")[0][0]
    expected = truncate_stored(_stored(5), stored_model=MODEL, model=MODEL)
    assert [float(v) for v in stored.strip("[]").split(",")] == pytest.approx(expected)
    assert memory_store_sql("SELECT count(*) FROM vec.reembed_progress") == [(0,)]


def test_incomplete_empty_and_other_model_chunks_are_counted_and_left(_memory_store, memory_store_sql, tmp_path):
    _external(memory_store_sql, "a", "the parcel is at the neighbour")
    _external(memory_store_sql, "blank", "   ")
    forgotten = _external(memory_store_sql, "gone", "a forgotten message")
    memory_store_sql("UPDATE memory.messages SET forgotten_at = now() WHERE message_id = %s", (forgotten,))
    index = _index(tmp_path / "index.db", [
        (1, COLLECTOR_MODEL, _stored(1), ["a", "not-ingested"]),
        (2, COLLECTOR_MODEL, _stored(2), ["a", "gone"]),
        (3, COLLECTOR_MODEL, _stored(3), []),
        (4, COLLECTOR_MODEL, _stored(4), ["blank"]),
        (5, "text-embedding-3-small", _stored(5), ["a"]),
        (6, COLLECTOR_MODEL, [1.0] * 512, ["a"]),
    ])
    lines: list[str] = []

    counts = _import(_memory_store, index, lines)

    assert (counts.imported, counts.missing_messages, counts.empty, counts.other_model, counts.unusable) == (0, 2, 2, 1, 1)
    assert lines[-1] == ("messages index: 0 imported, 0 already in the store; left for `lina store reembed --source "
                         "messages`: 2 with messages not in memory.messages, 2 empty, 1 of another model, "
                         "1 unusable vectors")
    assert memory_store_sql("SELECT count(*) FROM vec.chunks") == [(0,)]
    assert memory_store_sql("SELECT count(*) FROM vec.embeddings") == [(0,)]


def test_a_rerun_inserts_nothing(_memory_store, memory_store_sql, tmp_path):
    for uid in ("x", "y", "z"):
        _external(memory_store_sql, uid, f"text of {uid}")
    index = _index(tmp_path / "index.db", [(1, COLLECTOR_MODEL, _stored(1), ["x", "y"]),
                                           (2, COLLECTOR_MODEL, _stored(2), ["z"]),
                                           (3, COLLECTOR_MODEL, _stored(3), ["x", "z"])])
    first = _import(_memory_store, index)
    before = _vec_rows(memory_store_sql)

    again = _import(_memory_store, index)

    assert (first.imported, again.imported, again.present) == (3, 0, 3)
    assert _vec_rows(memory_store_sql) == before
    assert memory_store_sql("SELECT count(*) FROM vec.chunks") == [(3,)]
    assert memory_store_sql("SELECT count(*) FROM vec.embeddings") == [(3,)]


def test_an_imported_chunk_is_found_by_the_vector_search_of_every_agent(_memory_store, memory_store_sql, tmp_path):
    for uid, text in (("s1", "the boiler service is on tuesday"), ("s2", "bring the spare key"),
                      ("o1", "an unrelated message")):
        _external(memory_store_sql, uid, text)
    index = _index(tmp_path / "index.db", [(1, COLLECTOR_MODEL, _stored(10), ["s1", "s2"]),
                                           (2, COLLECTOR_MODEL, _stored(20), ["o1"])])
    _import(_memory_store, index)
    query = truncate_stored(_stored(10), stored_model=MODEL, model=MODEL)

    for agent in ("alpha", "beta"):
        store = MessageStore(agent, dsn=_memory_store.app_dsn())
        try:
            rows = store.search_memory_vectors(query, model=MODEL, sources=["messages"], limit=2)
        finally:
            store.close()
        assert [row.uid for row in rows] == ["messages:s1", "messages:s2"]
        question = Question(id="q", source="messages", agent=agent, question="?", expected=("messages:s1",))
        with psycopg.connect(_memory_store.superuser_dsn(), autocommit=True) as conn:
            assert new_vector(conn, query, MODEL, question, k=1) == ["messages:s1"]


def _cli(*argv: str) -> int:
    parser = argparse.ArgumentParser()
    build_store_parser(parser.add_subparsers())
    args = parser.parse_args(["store", "reembed", *argv])
    with pytest.raises(SystemExit) as exit_info:
        args.func(args)
    return exit_info.value.code


def test_the_cli_imports_from_an_index_and_refuses_other_sources(_memory_store, memory_store_sql, tmp_path, capsys):
    _external(memory_store_sql, "c1", "the meter reading is due")
    index = _index(tmp_path / "index.db", [(1, COLLECTOR_MODEL, _stored(1), ["c1"])])

    assert _cli("--source", "messages", "--from-index", str(index)) == 0
    assert "messages index: 1 imported" in capsys.readouterr().out
    assert _cli("--from-index", str(index)) == 1
    assert "use it with --source messages" in capsys.readouterr().out
    assert _cli("--source", "messages", "--from-index", str(tmp_path / "missing.db")) == 1
    assert "no collector index" in capsys.readouterr().out
    assert memory_store_sql("SELECT count(*) FROM vec.embeddings") == [(1,)]
