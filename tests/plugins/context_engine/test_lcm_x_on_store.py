"""LCM-X on the memory store (lina#16): its stores run on schema ``ctx`` and read message text from
``memory.messages``, through ``plugins/context_engine/lcm_x_store.py``. Real Postgres (tests/_fixtures/memory_store.py)."""

from __future__ import annotations

import importlib
import importlib.util
import sqlite3
import sys
import time
from pathlib import Path

import pytest

import hermes_state_store
from plugins.context_engine import lcm_x_store

PLUGIN = Path(__file__).resolve().parents[3] / "plugins" / "context_engine" / "lcm-x"
PACKAGE = "lina_lcm_x_on_store"


def _lcm(module: str):
    if PACKAGE not in sys.modules:
        spec = importlib.util.spec_from_file_location(PACKAGE, PLUGIN / "__init__.py",
                                                      submodule_search_locations=[str(PLUGIN)])
        package = importlib.util.module_from_spec(spec)
        package.__path__ = [str(PLUGIN)]
        sys.modules[PACKAGE] = package
    return importlib.import_module(f"{PACKAGE}.{module}")


@pytest.fixture
def routed(tmp_path):
    """A profile home whose lcm.db is routed to the store; unrouted after the test."""
    paths = []

    def make(profile: str) -> Path:
        db = tmp_path / "profiles" / profile / "lcm.db"
        db.parent.mkdir(parents=True)
        assert lcm_x_store.route(db) == profile
        paths.append(db)
        return db
    yield make
    for db in paths:
        lcm_x_store.unroute(db)


def _stores(db: Path):
    return _lcm("store").MessageStore(db), _lcm("dag").SummaryDAG(db)


def test_korean_two_character_term_recalls_messages_and_summaries(routed):
    db = routed("ko")
    store, dag = _stores(db)
    try:
        ids = store.append_batch("s1", [{"role": "user", "content": "어제 회의록을 정리했다"},
                                        {"role": "assistant", "content": "deploy notes are ready"}])
        node = _lcm("dag").SummaryNode(session_id="s1", summary="회의록 요약: 출시 일정", source_ids=ids,
                                       token_count=5, created_at=time.time())
        node_id = dag.add_node(node)

        assert [row["store_id"] for row in store.search("회의", limit=5)] == [ids[0]]
        assert [row["store_id"] for row in store.search("deploy", limit=5)] == [ids[1]]
        assert [n.node_id for n in dag.search("회의", limit=5)] == [node_id]
        assert not db.exists()
    finally:
        store.close()
        dag.close()


def test_a_hermes_turn_is_not_stored_twice(routed, memory_store_sql):
    db = routed("p1")
    text = hermes_state_store.MessageStore("p1")
    values = ("회의는 3시에", None) + (None,) * (len(hermes_state_store.TEXT_FIELDS) - 2)
    with text.write():
        uid = text.put_text(message_uid="m-1", values=values, role="user", tool_name=None, tool_call_id=None,
                            timestamp=time.time(), session_id="hermes-s", seq_in_session=1, meta={})
    text.close()
    store, dag = _stores(db)
    try:
        [store_id] = store.append_batch("s1", [{"role": "user", "content": "회의는 3시에", "message_uid": "m-1"}])
        assert store.get(store_id)["content"] == "회의는 3시에"
        rows = memory_store_sql("SELECT uid FROM memory.messages WHERE agent_id = 'p1'")
        assert rows == [(uid,)]
        assert memory_store_sql("SELECT text_uid FROM ctx.lcm_messages WHERE store_id = %s", (store_id,)) == [(uid,)]

        [other] = store.append_batch("s1", [{"role": "assistant", "content": "알겠습니다", "message_uid": "m-2"}])
        assert store.get(other)["content"] == "알겠습니다"
        assert len(memory_store_sql("SELECT uid FROM memory.messages WHERE agent_id = 'p1'")) == 2
    finally:
        store.close()
        dag.close()


def test_summary_vectors_live_in_vec_embeddings(routed, memory_store_sql):
    from array import array
    db = routed("vec")
    store, dag = _stores(db)
    conn = lcm_x_store.backend_for(db)()
    try:
        node_id = dag.add_node(_lcm("dag").SummaryNode(session_id="s1", summary="회의 요약", created_at=time.time()))
        conn.execute("INSERT INTO lcm_embedding_profile(identity_hash, provider, model_name, dim) VALUES (?, ?, ?, ?)",
                     ("h1", "openai", "qwen3-embedding-8b", 1024))
        blob = array("f", [0.5] * 1024).tobytes()
        conn.execute("INSERT INTO lcm_embedding_vectors(embedded_id, identity_hash, vec) VALUES(?, ?, ?)",
                     (str(node_id), "h1", blob))
        conn.commit()
        assert memory_store_sql("SELECT kind, item_id, agent_id, model FROM vec.embeddings") == [
            ("summary", node_id, "vec", "qwen3-embedding-8b")]
        assert conn.execute("SELECT vec FROM lcm_embedding_vectors WHERE embedded_id = ?",
                            (str(node_id),)).fetchone()[0] == blob
        for width in (8, 1536):
            with pytest.raises(sqlite3.IntegrityError, match="1024 wide"):
                conn.execute("INSERT INTO lcm_embedding_vectors(embedded_id, identity_hash, vec) VALUES(?, ?, ?)",
                             (str(node_id), "h1", array("f", [0.5] * width).tobytes()))
            conn.rollback()
        conn.execute("DELETE FROM lcm_embedding_vectors WHERE embedded_id = ? AND identity_hash = ?",
                     (str(node_id), "h1"))
        conn.commit()
        assert memory_store_sql("SELECT count(*) FROM vec.embeddings") == [(0,)]
    finally:
        conn.close()
        store.close()
        dag.close()


def test_chunk_vectors_live_in_vec_chunks_and_embeddings(routed, memory_store_sql):
    from array import array
    db = routed("chunks")
    store, dag = _stores(db)
    conn = lcm_x_store.backend_for(db)()
    try:
        [store_id] = store.append_batch("s1", [{"role": "user", "content": "첫 문장. 두 번째 문장."}])
        conn.execute("INSERT INTO lcm_embedding_profile(identity_hash, provider, model_name, dim) VALUES (?, ?, ?, ?)",
                     ("h1", "openai", "qwen3-embedding-8b", 1024))
        blob = array("f", [0.5] * 1024).tobytes()
        conn.execute("INSERT INTO lcm_chunk_vectors(chunk_id, identity_hash, vec) VALUES(?, ?, ?)",
                     (f"{store_id}:1", "h1", blob))
        conn.execute("INSERT INTO lcm_chunk_meta(chunk_id, identity_hash, store_id, chunk_index, char_start, char_end, "
                     "token_estimate, embedded_at, archived) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)",
                     (f"{store_id}:1", "h1", store_id, 1, 6, 14, 3, "now"))
        conn.commit()
        [(message_id,)] = memory_store_sql("SELECT message_id FROM memory.messages WHERE agent_id = 'chunks'")
        assert memory_store_sql("SELECT source, message_ids, chunk_index, text FROM vec.chunks") == [
            ("lcm-x", [message_id], 1, "두 번째 문장.")]
        assert memory_store_sql("SELECT kind, model FROM vec.embeddings") == [("chunk", "qwen3-embedding-8b")]
        assert conn.execute("SELECT chunk_id, vec FROM lcm_chunk_vectors").fetchall() == [(f"{store_id}:1", blob)]
        conn.execute("DELETE FROM lcm_chunk_vectors WHERE chunk_id = ? AND identity_hash = ?", (f"{store_id}:1", "h1"))
        conn.commit()
        assert memory_store_sql("SELECT count(*) FROM vec.embeddings") == [(0,)]
    finally:
        conn.close()
        store.close()
        dag.close()


@pytest.mark.parametrize("backend", ["auto", "postgres"])
def test_store_backend_routes_a_fresh_home_and_keeps_an_existing_lcm_db(tmp_path, backend):
    session = _lcm("sqlite_session")
    fresh = tmp_path / "profiles" / "fresh" / "lcm.db"
    old = tmp_path / "profiles" / "old" / "lcm.db"
    old.parent.mkdir(parents=True)
    old.write_bytes(b"SQLite format 3\x00")
    try:
        assert session.route_to_host_store(fresh, "sqlite") is False and lcm_x_store.backend_for(fresh) is None
        assert session.route_to_host_store(fresh, backend) is True and lcm_x_store.backend_for(fresh) is not None
        assert session.route_to_host_store(old, backend) is False and lcm_x_store.backend_for(old) is None
        assert old.read_bytes() == b"SQLite format 3\x00"
    finally:
        lcm_x_store.unroute(fresh)


def test_each_profile_sees_only_its_own_rows(routed):
    first, second = routed("alpha"), routed("beta")
    store_a, dag_a = _stores(first)
    store_b, dag_b = _stores(second)
    try:
        [mine] = store_a.append_batch("s1", [{"role": "user", "content": "alpha only"}])
        assert store_b.get(mine) is None
        assert store_b.search("alpha", limit=5) == []
        assert store_a.get(mine)["content"] == "alpha only"
    finally:
        for closable in (store_a, dag_a, store_b, dag_b):
            closable.close()
