"""lina#387: a filtered nearest-neighbour query under the HNSW index returns as many rows as pass the filter.

The corpus puts the query owner's rows behind many nearer rows of another owner, while the owner still holds most
rows, so the planner takes the HNSW index as it does on a real store. With pgvector's defaults (ef_search 40, no
iterative scan) the index hands back the 40 nearest rows of the table, the owner filter drops them all, and the
query returns nothing; with the store's search settings it returns the owner's nearest five, as an exact scan does.
"""

from __future__ import annotations

import hashlib
import math
import random

import psycopg
import pytest

from hermes_cli.memory_eval_search import Question, new_vector
from hermes_state_store import HNSW_FILTERED_SEARCH, MessageStore

MODEL = "m"
CROWD = 600  # another owner's rows, all nearer the query than any of the owner's
OWNER = 900  # the owner's own rows: a majority, so the planner takes the HNSW index as on a real store
QUERY = [1.0] + [0.0] * 1023


def _vector(rng: random.Random, near: float) -> list[float]:
    """A unit vector at cosine *near* to the first axis."""
    noise = [rng.gauss(0.0, 1.0) for _ in range(1023)]
    norm = math.sqrt(sum(v * v for v in noise))
    side = math.sqrt(max(0.0, 1.0 - near * near))
    return [near] + [side * v / norm for v in noise]


def _literal(vector: list[float]) -> str:
    return "[" + ",".join(repr(v) for v in vector) + "]"


def _owner_uid(i: int) -> str:
    return f"agent:owner:{i:03d}"


@pytest.fixture
def crowded_store(_memory_store, memory_store_sql):
    """As on the #148 store, where one agent owns 75% of the vectors a question may read and its answer is not
    among the nearest rows of the whole table."""
    rng = random.Random(387)
    sql = memory_store_sql
    rows = []

    def row(uid: str, agent: str, near: float) -> None:
        content = f"text of {uid}"
        message_id = sql("INSERT INTO memory.messages (uid, source, agent_id, role, content, content_sha256) VALUES "
                         "(%s, 'agent', %s, 'user', %s, %s) RETURNING message_id",
                         (uid, agent, content, hashlib.sha256(content.encode()).hexdigest()))[0][0]
        chunk = sql("INSERT INTO vec.chunks (source, agent_id, message_ids, text) VALUES ('agent', %s, %s, %s) "
                    "RETURNING chunk_id", (agent, [message_id], content))[0][0]
        rows.append((chunk, agent, _literal(_vector(rng, near))))

    for i in range(CROWD):
        row(f"agent:crowd:{i}", "crowd", 0.9)
    for i in range(OWNER):
        row(_owner_uid(i), "owner", 0.6 - i * 0.0005)
    for chunk, agent, literal in rows:
        sql("INSERT INTO vec.embeddings (kind, item_id, agent_id, model, dim, embedding) VALUES "
            "('chunk', %s, %s, %s, 1024, %s::vector)", (chunk, agent, MODEL, literal))
    for table in ("vec.embeddings", "vec.chunks", "memory.messages"):
        sql(f"ANALYZE {table}")
    return _memory_store


_PLAIN = ("SELECT m.uid FROM vec.embeddings e JOIN vec.chunks c ON c.chunk_id = e.item_id "
          "JOIN memory.messages m ON m.message_id = c.message_ids[1] WHERE e.model = %s AND e.agent_id = 'owner' "
          "ORDER BY e.embedding <=> %s::vector LIMIT 5")


def test_under_pgvector_defaults_the_index_loses_the_owners_rows(crowded_store):
    """The failure the settings fix, so the tests below prove something."""
    with psycopg.connect(crowded_store.superuser_dsn(), autocommit=True) as conn:
        plan = "\n".join(r[0] for r in conn.execute("EXPLAIN " + _PLAIN, (MODEL, _literal(QUERY))).fetchall())
        rows = conn.execute(_PLAIN, (MODEL, _literal(QUERY))).fetchall()
        with conn.transaction():
            conn.execute("SET LOCAL enable_indexscan = off")
            exact = [r[0] for r in conn.execute(_PLAIN, (MODEL, _literal(QUERY))).fetchall()]
    assert "ix_embeddings_embedding_hnsw" in plan
    assert exact == [_owner_uid(i) for i in range(5)]
    assert [r[0] for r in rows] != exact


def test_eval_search_returns_the_owners_nearest_five_behind_a_crowd(crowded_store):
    question = Question(id="q", source="agent", agent="owner", question="?", expected=(_owner_uid(0),))
    with psycopg.connect(crowded_store.superuser_dsn(), autocommit=True) as conn:
        ranked = new_vector(conn, QUERY, MODEL, question)
        assert conn.execute("SHOW hnsw.iterative_scan").fetchone()[0] == "off"  # SET LOCAL ended with the query
    assert ranked == [_owner_uid(i) for i in range(5)]


def test_memory_search_returns_the_owners_nearest_five_behind_a_crowd(crowded_store):
    store = MessageStore("owner", dsn=crowded_store.app_dsn())
    try:
        rows = store.search_memory_vectors(QUERY, model=MODEL, sources=["agent"], limit=5)
        with store.write():  # inside a write transaction the settings take a savepoint, not the transaction
            in_write = store.search_memory_vectors(QUERY, model=MODEL, sources=["agent"], limit=5)
    finally:
        store.close()
    assert [row.uid for row in rows] == [_owner_uid(i) for i in range(5)]
    assert [row.uid for row in in_write] == [row.uid for row in rows]


def test_the_settings_are_transaction_local():
    assert all(setting.startswith("SET LOCAL hnsw.") for setting in HNSW_FILTERED_SEARCH)
