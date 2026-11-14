"""lina#19: `lina store reembed` fills vec.embeddings in batches and continues where it stopped.

These run on the real Postgres from tests/_fixtures/memory_store.py.
"""

from __future__ import annotations

import hashlib

import httpx
import psycopg
import pytest

from agent.embedding_client import EmbeddingClient, EmbeddingError, EmbeddingSettings
from hermes_cli.memory_eval_search import Question, new_vector
from hermes_cli.store_migrations import MIGRATIONS_DIR
from hermes_cli.store_reembed import ReembedError, expand_sources, reembed
from hermes_state_store import MessageStore
from tests.agent.test_embedding_client import magpie_transport
from tests.agent.test_embedding_client_retry import _Endpoint


class _Client:
    model = "qwen3-embedding-8b"

    def __init__(self, dimensions: int = 1024) -> None:
        self.dimensions = dimensions
        self.calls: list[list[str]] = []

    def embed(self, texts: list[str]) -> list[list[float]]:
        self.calls.append(list(texts))
        vectors = []
        for text in texts:
            seed = int(hashlib.sha256(text.encode()).hexdigest()[:8], 16)
            vector = [0.0] * self.dimensions
            vector[seed % self.dimensions] = 1.0
            vectors.append(vector)
        return vectors


def _message(sql, uid: str, source: str, role: str, content: str, agent: str = "default") -> int:
    return sql("INSERT INTO memory.messages (uid, source, agent_id, role, content, content_sha256) "
               "VALUES (%s, %s, %s, %s, %s, %s) RETURNING message_id",
               (uid, source, agent, role, content, hashlib.sha256(content.encode()).hexdigest()))[0][0]


def _connect(memory_store):
    return lambda: psycopg.connect(memory_store.superuser_dsn(), autocommit=True)


def _external(sql, uid: str, content: str) -> int:
    return sql("INSERT INTO memory.messages (uid, source, agent_id, scope, role, content, content_sha256) "
               "VALUES (%s, 'messages', NULL, 'agents', 'user', %s, %s) RETURNING message_id",
               (uid, content, hashlib.sha256(content.encode()).hexdigest()))[0][0]


def test_a_reembedded_external_message_is_found_by_the_vector_search_of_every_agent(_memory_store, memory_store_sql):
    message_id = _external(memory_store_sql, "messages:shared", "the boiler service is on tuesday")
    client = _Client()
    reembed(_connect(_memory_store), client, ["messages"], batch_size=10)
    query = client.embed(["the boiler service is on tuesday"])[0]

    for agent in ("alpha", "beta"):
        store = MessageStore(agent, dsn=_memory_store.app_dsn())
        try:
            assert store.has_memory_vectors(model=client.model, sources=["messages"])
            rows = store.search_memory_vectors(query, model=client.model, sources=["messages"], limit=5)
        finally:
            store.close()
        assert [row.uid for row in rows] == ["messages:shared"]
        question = Question(id="q", source="messages", agent=agent, question="?", expected=("messages:shared",))
        with psycopg.connect(_memory_store.superuser_dsn(), autocommit=True) as conn:
            assert new_vector(conn, query, client.model, question) == ["messages:shared"]

    reembed(_connect(_memory_store), client, ["messages"], batch_size=10, from_start=True)
    assert memory_store_sql("SELECT count(*) FROM vec.chunks WHERE message_ids = %s::bigint[]", ([message_id],)) == [(1,)]
    assert memory_store_sql("SELECT count(*) FROM vec.embeddings WHERE kind = 'chunk'") == [(1,)]
    assert memory_store_sql("SELECT count(*) FROM vec.chunks WHERE agent_id = 'None'") == [(0,)]


def test_an_agent_reads_but_cannot_change_shared_chunk_vectors(_memory_store, memory_store_sql):
    _external(memory_store_sql, "messages:ro", "the parcel is at the neighbour")
    reembed(_connect(_memory_store), _Client(), ["messages"], batch_size=10)
    with psycopg.connect(_memory_store.app_dsn(), autocommit=True) as conn:
        conn.execute("SELECT set_config('lina.agent_id', 'alpha', false)")
        assert conn.execute("SELECT current_user").fetchone() == ("lina_app",)
        assert conn.execute("SELECT count(*) FROM vec.chunks c JOIN vec.embeddings e "
                            "ON e.kind = 'chunk' AND e.item_id = c.chunk_id").fetchone() == (1,)
        assert conn.execute("UPDATE vec.chunks SET text = 'changed'").rowcount == 0
        assert conn.execute("DELETE FROM vec.embeddings").rowcount == 0
        with pytest.raises(psycopg.errors.InsufficientPrivilege):
            conn.execute("INSERT INTO vec.chunks (source, agent_id, message_ids, text) "
                         "VALUES ('messages', '*', '{1}', 'forged')")
    assert memory_store_sql("SELECT text FROM vec.chunks") == [("the parcel is at the neighbour",)]


def test_the_migration_gives_vectors_written_with_the_none_owner_the_shared_owner(_memory_store, memory_store_sql):
    message_id = _external(memory_store_sql, "messages:old", "an old external message")
    chunk = memory_store_sql("INSERT INTO vec.chunks (source, agent_id, message_ids, text) VALUES "
                             "('messages', 'None', %s::bigint[], 't') RETURNING chunk_id", ([message_id],))[0][0]
    memory_store_sql("INSERT INTO vec.embeddings (kind, item_id, agent_id, model, dim, embedding) VALUES "
                     "('chunk', %s, 'None', 'm', 1024, %s::vector)", (chunk, "[" + ",".join(["1"] + ["0"] * 1023) + "]"))
    migration = next(MIGRATIONS_DIR.glob("0009_*.sql")).read_text(encoding="utf-8-sig")
    memory_store_sql(migration)
    memory_store_sql(migration)
    assert memory_store_sql("SELECT agent_id FROM vec.chunks") == [("*",)]
    assert memory_store_sql("SELECT agent_id FROM vec.embeddings") == [("*",)]


def test_agent_turns_embed_in_batches_and_a_second_run_continues_after_the_last_batch(_memory_store, memory_store_sql):
    ids = [_message(memory_store_sql, f"agent:default:r{i}", "agent", "user" if i % 2 else "assistant",
                    f"turn number {i} about the backup schedule") for i in range(5)]
    tool_id = _message(memory_store_sql, "agent:default:tool", "agent", "tool", "tool output that is not embedded")
    _message(memory_store_sql, "messages:x1", "messages", "user", "an external message")
    client = _Client()
    lines: list[str] = []

    first = reembed(_connect(_memory_store), client, ["agent"], batch_size=2, max_batches=1, out=lines.append)
    assert first[0].embedded == 2 and first[0].last_item_id == ids[1]
    assert memory_store_sql("SELECT last_item_id FROM vec.reembed_progress WHERE source = 'agent'")[0][0] == ids[1]

    second = reembed(_connect(_memory_store), client, ["agent"], batch_size=2, out=lines.append)
    assert second[0].resumed_after == ids[1]
    assert second[0].embedded == 3
    assert [len(call) for call in client.calls] == [2, 2, 1]
    assert [text for call in client.calls for text in call] == [f"turn number {i} about the backup schedule" for i in range(5)]

    rows = memory_store_sql(
        "SELECT c.message_ids, c.text, e.agent_id, e.dim, e.model FROM vec.embeddings e "
        "JOIN vec.chunks c ON e.kind = 'chunk' AND c.chunk_id = e.item_id ORDER BY c.message_ids")
    assert [r[0] for r in rows] == [[i] for i in ids]
    assert {(r[2], r[3], r[4]) for r in rows} == {("default", 1024, "qwen3-embedding-8b")}
    assert all(r[0] != [tool_id] for r in rows)
    assert any(line.startswith("agent: 3 rows done, 0 left; 3 embedded") for line in lines)


def test_from_start_goes_through_every_row_again(_memory_store, memory_store_sql):
    _message(memory_store_sql, "messages:a", "messages", "user", "the plumber comes on saturday")
    _message(memory_store_sql, "messages:b", "messages", "user", "")
    client = _Client()
    reembed(_connect(_memory_store), client, ["messages"], batch_size=10)
    again = reembed(_connect(_memory_store), client, ["messages"], batch_size=10)
    assert again[0].embedded == 0
    fresh = reembed(_connect(_memory_store), client, ["messages"], batch_size=10, from_start=True)
    assert (fresh[0].embedded, fresh[0].skipped) == (1, 1)
    assert memory_store_sql("SELECT count(*) FROM vec.embeddings WHERE kind = 'chunk'")[0][0] == 1


def test_a_message_with_part_chunks_keeps_one_whole_message_chunk_across_runs(_memory_store, memory_store_sql):
    message_id = _message(memory_store_sql, "messages:parts", "messages", "user", "the long message LCM-X cut in two")
    for index, part in enumerate(("the long message", "LCM-X cut in two")):
        memory_store_sql("INSERT INTO vec.chunks (source, agent_id, message_ids, chunk_index, text) VALUES "
                         "('lcm-x', 'default', %s::bigint[], %s, %s)", ([message_id], index, part))
    client = _Client()
    reembed(_connect(_memory_store), client, ["messages"], batch_size=10)
    reembed(_connect(_memory_store), client, ["messages"], batch_size=10, from_start=True)
    rows = memory_store_sql("SELECT source, chunk_index, text FROM vec.chunks WHERE message_ids = %s::bigint[] "
                            "ORDER BY source, chunk_index NULLS FIRST", ([message_id],))
    assert rows == [("lcm-x", 0, "the long message"), ("lcm-x", 1, "LCM-X cut in two"),
                    ("messages", None, "the long message LCM-X cut in two")]
    assert memory_store_sql("SELECT count(*) FROM vec.embeddings WHERE kind = 'chunk'")[0][0] == 1


def test_honcho_conclusions_embed_as_documents_keyed_by_vec_id(_memory_store, memory_store_sql):
    memory_store_sql("CREATE TABLE ltm.documents (id text PRIMARY KEY, vec_id bigint GENERATED BY DEFAULT AS "
                     "IDENTITY UNIQUE, workspace_name text NOT NULL, content text, deleted_at timestamptz)")
    try:
        memory_store_sql("INSERT INTO ltm.documents (id, workspace_name, content, deleted_at) VALUES "
                         "('d1', 'hermes', 'the owner prefers green tea', NULL), "
                         "('d2', 'hermes', 'a deleted conclusion', now())")
        counts = reembed(_connect(_memory_store), _Client(), expand_sources("memory"), batch_size=5)
        assert counts[0].embedded == 1
        assert memory_store_sql(
            "SELECT e.kind, e.agent_id, d.id FROM vec.embeddings e JOIN ltm.documents d ON d.vec_id = e.item_id"
        ) == [("document", "hermes", "d1")]
    finally:
        memory_store_sql("DROP TABLE ltm.documents")


def test_a_store_without_honcho_tables_reports_it_and_embeds_nothing(_memory_store):
    counts = reembed(_connect(_memory_store), _Client(), ["memory"], batch_size=5)
    assert counts[0].embedded == 0
    assert "no Honcho tables" in counts[0].line()


def test_a_client_at_another_width_than_the_table_is_refused(_memory_store):
    with pytest.raises(ReembedError, match="1024-wide"):
        reembed(_connect(_memory_store), _Client(dimensions=768), ["agent"], batch_size=5)


def test_reembed_on_magpie_with_the_bare_model_stops_with_its_catalog_name_and_writes_nothing(
        _memory_store, memory_store_sql):
    _message(memory_store_sql, "m1", "agent", "user", "hello")
    settings = EmbeddingSettings(base_url="http://127.0.0.1:3425/v1", model="qwen3-embedding-8b")
    with EmbeddingClient(settings, transport=magpie_transport(["hub/nous/qwen/qwen3-embedding-8b"])) as client:
        with pytest.raises(EmbeddingError, match="embedding.model 'qwen3-embedding-8b'.*'hub/nous/qwen/qwen3-embedding-8b'"):
            reembed(_connect(_memory_store), client, ["agent"], batch_size=5)
    assert memory_store_sql("SELECT count(*) FROM vec.embeddings")[0][0] == 0
    assert memory_store_sql("SELECT count(*) FROM vec.reembed_progress")[0][0] == 0


def test_a_persistent_429_stops_with_the_progress_saved_and_the_rows_left(_memory_store, memory_store_sql):
    ids = [_message(memory_store_sql, f"m{n}", "agent", "user", f"turn {n}") for n in range(5)]
    endpoint, sleeps = _Endpoint((200, {}), (429, {}), width=1024), []
    settings = EmbeddingSettings(base_url="http://127.0.0.1:9/v1", max_retries=2)
    with EmbeddingClient(settings, transport=httpx.MockTransport(endpoint), sleep=sleeps.append) as client:
        with pytest.raises(ReembedError) as stopped:
            reembed(_connect(_memory_store), client, ["agent"], batch_size=2, out=lambda line: None)
    assert "rows left: agent 3" in str(stopped.value)
    assert "`lina store reembed --source agent` again to continue" in str(stopped.value)
    assert sleeps == [1.0, 2.0]
    assert memory_store_sql("SELECT last_item_id FROM vec.reembed_progress WHERE source = 'agent'") == [(ids[1],)]
    assert memory_store_sql("SELECT count(*) FROM vec.embeddings")[0][0] == 2


def test_the_end_line_gives_rows_done_and_rows_left(_memory_store, memory_store_sql):
    for n in range(3):
        _message(memory_store_sql, f"m{n}", "agent", "user", f"turn {n}")
    lines: list[str] = []
    reembed(_connect(_memory_store), _Client(), ["agent"], batch_size=2, max_batches=1, out=lines.append)
    assert lines[-1].startswith("agent: 2 rows done, 1 left;")
    lines.clear()
    reembed(_connect(_memory_store), _Client(), ["agent"], batch_size=2, out=lines.append)
    assert lines[-1].startswith("agent: 1 rows done, 0 left;")


def test_unknown_sources_are_refused():
    assert expand_sources("all") == ("agent", "messages", "memory")
    with pytest.raises(ReembedError, match="unknown source"):
        expand_sources("lcm")
