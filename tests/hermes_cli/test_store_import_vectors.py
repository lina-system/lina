"""lina#365: `lina store reembed --source messages --from-index <db>` imports the collector's stored chunk vectors.

The collector index is a synthetic SQLite file of the collector's shape, its sqlite-vec vec0 table written as the
0.1 shadow tables directly (no sqlite-vec); the store is the real Postgres from tests/_fixtures/memory_store.py.
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
from hermes_cli.store_reembed import reembed
from hermes_cli.subcommands.store import build_store_parser
from hermes_state_store import MessageStore

COLLECTOR_MODEL = "qwen/qwen3-embedding-8b"
MODEL = "qwen3-embedding-8b"
DIM = 1536


def _stored(hot: int) -> list[float]:
    vector = [0.0] * DIM
    vector[hot] = 0.6
    vector[1200] = 1.0
    return vector


def _index(path, chunks: dict[int, list[str]], vectors: dict[int, list[float]], *, block: int = 4,
           deleted: frozenset[int] = frozenset(), model: str = COLLECTOR_MODEL, version: str = "v0.1.9"):
    """A collector index: chunks, chunk_messages (uids in ordinal order) and chunk_vectors as vec0's shadow
    tables, *block* vectors per block, the vectors of *deleted* chunks with their validity bit cleared."""
    conn = sqlite3.connect(path)
    conn.executescript("""
        CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT);
        CREATE TABLE chunks (id INTEGER PRIMARY KEY, source TEXT, room_id TEXT, start_ts TEXT, end_ts TEXT, text TEXT);
        CREATE TABLE chunk_messages (chunk_id INTEGER REFERENCES chunks(id), uid TEXT, ordinal INTEGER,
                                     PRIMARY KEY (chunk_id, uid));
        CREATE TABLE chunk_vectors_info (key TEXT PRIMARY KEY, value ANY);
        CREATE TABLE chunk_vectors_chunks (chunk_id INTEGER PRIMARY KEY AUTOINCREMENT, size INTEGER NOT NULL,
                                           validity BLOB NOT NULL, rowids BLOB NOT NULL);
        CREATE TABLE chunk_vectors_rowids (rowid INTEGER PRIMARY KEY AUTOINCREMENT, id, chunk_id INTEGER,
                                           chunk_offset INTEGER);
        CREATE TABLE chunk_vectors_vector_chunks00 (rowid PRIMARY KEY, vectors BLOB NOT NULL);
    """)
    conn.executemany("INSERT INTO meta VALUES (?, ?)", [("vector_model", model), ("vector_dimension", str(DIM)),
                                                        ("vector_fingerprint", "synthetic")])
    conn.execute("INSERT INTO chunk_vectors_info VALUES ('CREATE_VERSION', ?)", (version,))
    for chunk_id, uids in chunks.items():
        conn.execute("INSERT INTO chunks VALUES (?, 'synthetic', 'room', '', '', 'collector text')", (chunk_id,))
        # Stored in reverse so only the ordinal gives the chunk's order.
        conn.executemany("INSERT INTO chunk_messages VALUES (?, ?, ?)",
                         [(chunk_id, uid, ordinal) for ordinal, uid in reversed(list(enumerate(uids)))])
    ids = sorted(vectors)
    for number, start in enumerate(range(0, len(ids), block), start=1):
        members = ids[start:start + block]
        validity = bytearray((block + 7) // 8)
        values: list[float] = []
        for offset, chunk_id in enumerate(members):
            if chunk_id not in deleted:
                validity[offset // 8] |= 1 << (offset % 8)
            values.extend(vectors[chunk_id])
            conn.execute("INSERT INTO chunk_vectors_rowids (rowid, chunk_id, chunk_offset) VALUES (?, ?, ?)",
                         (chunk_id, number, offset))
        values.extend([0.0] * DIM * (block - len(members)))
        conn.execute("INSERT INTO chunk_vectors_chunks VALUES (?, ?, ?, ?)",
                     (number, block, bytes(validity), struct.pack(f"<{block}q", *members, *[0] * (block - len(members)))))
        conn.execute("INSERT INTO chunk_vectors_vector_chunks00 VALUES (?, ?)",
                     (number, struct.pack(f"<{len(values)}f", *values)))
    conn.commit()
    conn.close()
    return path


def _external(sql, uid: str, content: str) -> int:
    return sql("INSERT INTO memory.messages (uid, source, agent_id, scope, role, content, content_sha256) "
               "VALUES (%s, 'messages', NULL, 'agents', 'user', %s, %s) RETURNING message_id",
               (f"messages:{uid}", content, hashlib.sha256(content.encode()).hexdigest()))[0][0]


def _connect(memory_store):
    return lambda: psycopg.connect(memory_store.superuser_dsn(), autocommit=True)


def _import(memory_store, path, lines=None):
    with _connect(memory_store)() as conn:
        return import_index(conn, path, model=MODEL, dimensions=1024, batch_size=2,
                            out=(lines.append if lines is not None else lambda _line: None))


def _vec_rows(sql):
    return sql("SELECT c.source, c.agent_id, c.message_ids, c.text, e.agent_id, e.model, e.dim FROM vec.chunks c "
               "JOIN vec.embeddings e ON e.kind = 'chunk' AND e.item_id = c.chunk_id ORDER BY c.chunk_id")


def _vector_of(sql, message_ids: list[int]) -> list[float]:
    text = sql("SELECT e.embedding::text FROM vec.embeddings e JOIN vec.chunks c ON e.item_id = c.chunk_id "
               "WHERE e.kind = 'chunk' AND c.message_ids = %s::bigint[]", (message_ids,))[0][0]
    return [float(v) for v in text.strip("[]").split(",")]


def _cut(vector: list[float]) -> list[float]:
    return truncate_stored(vector, stored_model=MODEL, model=MODEL)


def test_model_names_compare_without_the_provider_prefix():
    assert model_key(COLLECTOR_MODEL) == model_key(MODEL) == MODEL
    assert model_key("text-embedding-3-small") != model_key(MODEL)


def test_same_model_chunks_become_one_chunk_row_and_one_cut_vector_each(_memory_store, memory_store_sql, tmp_path):
    ids = {u: _external(memory_store_sql, u, f"message {u} about the boiler") for u in ("a", "b", "c", "d", "e", "f")}
    chunks = {1: ["c", "a", "b"], 2: ["d"], 3: ["e"], 4: ["f"], 5: ["a", "f"]}
    vectors = {n: _stored(n) for n in chunks}
    index = _index(tmp_path / "index.db", chunks, vectors, block=4, deleted=frozenset({3}))

    counts = _import(_memory_store, index)

    assert (counts.imported, counts.unusable, counts.present) == (4, 1, 0)
    rows = _vec_rows(memory_store_sql)
    assert [r[2] for r in rows] == [[ids["c"], ids["a"], ids["b"]], [ids["d"]], [ids["f"]], [ids["a"], ids["f"]]]
    assert rows[0][3] == "message c about the boiler\nmessage a about the boiler\nmessage b about the boiler"
    assert {(r[0], r[1], r[4], r[5], r[6]) for r in rows} == {("messages", "*", "*", MODEL, 1024)}
    # Chunk 2 is the second cell of block 1, chunk 5 the first of block 2.
    assert _vector_of(memory_store_sql, [ids["d"]]) == pytest.approx(_cut(_stored(2)))
    assert _vector_of(memory_store_sql, [ids["a"], ids["f"]]) == pytest.approx(_cut(_stored(5)))
    assert memory_store_sql("SELECT count(*) FROM vec.reembed_progress") == [(0,)]


def test_incomplete_empty_and_unusable_chunks_are_counted_and_left(_memory_store, memory_store_sql, tmp_path):
    _external(memory_store_sql, "a", "the parcel is at the neighbour")
    _external(memory_store_sql, "blank", "   ")
    forgotten = _external(memory_store_sql, "gone", "a forgotten message")
    memory_store_sql("UPDATE memory.messages SET forgotten_at = now() WHERE message_id = %s", (forgotten,))
    chunks = {1: ["a", "not-ingested"], 2: ["a", "gone"], 3: [], 4: ["blank"], 5: ["a"], 6: ["a", "blank"]}
    index = _index(tmp_path / "index.db", chunks, {1: _stored(1), 2: _stored(2), 4: _stored(4), 6: [0.0] * DIM})
    lines: list[str] = []

    counts = _import(_memory_store, index, lines)

    assert (counts.imported, counts.missing_messages, counts.empty, counts.other_model, counts.unusable) == (0, 2, 2, 0, 2)
    assert lines[-1] == ("messages index: 0 imported, 0 already in the store; left for `lina store reembed --source "
                         "messages`: 2 with messages not in memory.messages, 2 empty, 0 of another model, "
                         "2 unusable vectors")
    assert memory_store_sql("SELECT count(*) FROM vec.chunks") == [(0,)]


def test_an_index_of_another_model_is_counted_whole_and_imports_nothing(_memory_store, memory_store_sql, tmp_path):
    _external(memory_store_sql, "a", "the meter reading is due")
    index = _index(tmp_path / "index.db", {1: ["a"], 2: ["a"]}, {1: _stored(1), 2: _stored(2)},
                   model="openai/text-embedding-3-large")

    counts = _import(_memory_store, index)

    assert (counts.imported, counts.other_model) == (0, 2)
    assert memory_store_sql("SELECT count(*) FROM vec.embeddings") == [(0,)]


def test_a_rerun_inserts_nothing(_memory_store, memory_store_sql, tmp_path):
    for uid in ("x", "y", "z"):
        _external(memory_store_sql, uid, f"text of {uid}")
    chunks = {1: ["x", "y"], 2: ["z"], 3: ["x", "z"]}
    index = _index(tmp_path / "index.db", chunks, {n: _stored(n) for n in chunks})
    first = _import(_memory_store, index)
    before = _vec_rows(memory_store_sql)

    again = _import(_memory_store, index)

    assert (first.imported, again.imported, again.present) == (3, 0, 3)
    assert _vec_rows(memory_store_sql) == before
    assert memory_store_sql("SELECT count(*) FROM vec.embeddings") == [(3,)]


def test_an_imported_chunk_is_found_by_the_vector_search_of_every_agent(_memory_store, memory_store_sql, tmp_path):
    for uid, text in (("s1", "the boiler service is on tuesday"), ("s2", "bring the spare key"),
                      ("o1", "an unrelated message")):
        _external(memory_store_sql, uid, text)
    index = _index(tmp_path / "index.db", {1: ["s1", "s2"], 2: ["o1"]}, {1: _stored(10), 2: _stored(20)})
    _import(_memory_store, index)
    query = _cut(_stored(10))

    for agent in ("alpha", "beta"):
        store = MessageStore(agent, dsn=_memory_store.app_dsn())
        try:
            rows = store.search_memory_vectors(query, model=MODEL, sources=["messages"], limit=2)
        finally:
            store.close()
        assert [row.uid for row in rows] == ["messages:s1", "messages:s2"]
        question = Question(id="q", source="messages", agent=agent, question="?", expected=("messages:s1",))
        with _connect(_memory_store)() as conn:
            assert new_vector(conn, query, MODEL, question, k=1) == ["messages:s1"]


class _Client:
    dimensions = 1024

    def __init__(self, model: str = MODEL) -> None:
        self.model = model
        self.texts: list[str] = []

    def embed(self, texts: list[str]) -> list[list[float]]:
        self.texts.extend(texts)
        return [[1.0] + [0.0] * 1023 for _ in texts]


def test_reembed_skips_the_messages_an_imported_chunk_covers(_memory_store, memory_store_sql, tmp_path):
    for uid in ("m1", "m2", "m3"):
        _external(memory_store_sql, uid, f"text of {uid}")
    _import(_memory_store, _index(tmp_path / "index.db", {1: ["m1", "m2"]}, {1: _stored(1)}))
    client = _Client()
    lines: list[str] = []

    counts = reembed(_connect(_memory_store), client, ["messages"], batch_size=10, out=lines.append)

    assert client.texts == ["text of m3"]
    assert (counts[0].embedded, counts[0].covered, counts[0].skipped) == (1, 2, 0)
    assert "1 embedded, 0 skipped (no text)" in lines[-1] and "2 covered by imported collector chunks" in lines[-1]
    other = _Client("another-model")
    reembed(_connect(_memory_store), other, ["messages"], batch_size=10)
    assert other.texts == ["text of m1", "text of m2", "text of m3"]


def _cli(*argv: str) -> int:
    parser = argparse.ArgumentParser()
    build_store_parser(parser.add_subparsers())
    args = parser.parse_args(["store", "reembed", *argv])
    with pytest.raises(SystemExit) as exit_info:
        args.func(args)
    return exit_info.value.code


def test_the_cli_imports_from_an_index_and_refuses_what_it_cannot_read(_memory_store, memory_store_sql, tmp_path, capsys):
    _external(memory_store_sql, "c1", "the meter reading is due")
    index = _index(tmp_path / "index.db", {1: ["c1"]}, {1: _stored(1)})
    newer = _index(tmp_path / "newer.db", {1: ["c1"]}, {1: _stored(1)}, version="v0.2.0")
    sqlite3.connect(tmp_path / "other.db").execute("CREATE TABLE chunks (id INTEGER PRIMARY KEY)").connection.close()

    assert _cli("--source", "messages", "--from-index", str(index)) == 0
    assert "messages index: 1 imported" in capsys.readouterr().out
    assert _cli("--from-index", str(index)) == 1
    assert "use it with --source messages" in capsys.readouterr().out
    assert _cli("--source", "messages", "--from-index", str(newer)) == 1
    assert "sqlite-vec v0.2.0" in capsys.readouterr().out
    assert _cli("--source", "messages", "--from-index", str(tmp_path / "other.db")) == 1
    assert "is not a collector index" in capsys.readouterr().out
    assert _cli("--source", "messages", "--from-index", str(tmp_path / "missing.db")) == 1
    assert "no collector index" in capsys.readouterr().out
    assert memory_store_sql("SELECT count(*) FROM vec.embeddings") == [(1,)]
