"""Deferred LCM summaries reach the existing memory/all reembed surface on real PG."""

from __future__ import annotations

import pytest

from hermes_cli.store_reembed import expand_sources, reembed
from plugins.context_engine.lcm_x_store import PgConnection
from tests.hermes_cli.test_store_reembed import _Client, _connect


def _summary(sql, agent: str, node: int, text: str) -> None:
    sql("INSERT INTO ctx.summary_nodes (agent_id, node_id, session_id, summary, created_at, "
        "source_token_count) VALUES (%s, %s, 'copied-session', %s, 1, 17)", (agent, node, text))


@pytest.mark.parametrize("source", ["memory", "all"])
def test_deferred_summaries_use_global_keys_and_isolated_profiles(_memory_store, memory_store_sql, source):
    sql = memory_store_sql
    # Both homes retain the same native node ID and old, incompatible profile.
    for agent in ("alpha", "beta"):
        _summary(sql, agent, 1, f"{agent} summary")
        sql("INSERT INTO ctx.lcm_embedding_profile (agent_id, identity_hash, provider, model_name, dim) "
            "VALUES (%s, 'copied-profile', 'old-provider', 'old-model', 768)", (agent,))
    client = _Client()
    counts = reembed(_connect(_memory_store), client, expand_sources(source), batch_size=2)
    assert counts[-1].embedded == 2
    assert sorted(text for batch in client.calls for text in batch) == ["alpha summary", "beta summary"]
    rows = sql(
        "SELECT k.agent_id, k.embedded_id, k.item_id, e.agent_id, e.model, e.dim, "
        "p.provider, p.task, m.source_token_count FROM ctx.lcm_summary_vector_keys k "
        "JOIN vec.embeddings e ON e.kind = 'summary' AND e.item_id = k.item_id "
        "JOIN ctx.lcm_embedding_profile p ON p.agent_id = k.agent_id AND p.identity_hash = k.identity_hash "
        "JOIN ctx.lcm_embedding_meta m ON m.agent_id = k.agent_id AND m.embedded_id = k.embedded_id "
        "AND m.identity_hash = k.identity_hash ORDER BY k.agent_id")
    assert len(rows) == 2 and rows[0][2] != rows[1][2]
    assert [(r[0], r[1], *r[3:]) for r in rows] == [
        (agent, "1", agent, client.model, 1024, "openai-compatible", "summary", 17)
        for agent in ("alpha", "beta")]
    for agent in ("alpha", "beta"):
        with PgConnection(":memory:", agent, dsn=_memory_store.app_dsn()) as routed:
            assert routed.execute("SELECT embedded_id FROM lcm_embedding_vectors").fetchall() == [("1",)]
            assert routed.execute("SELECT summary FROM summary_nodes").fetchall() == [(f"{agent} summary",)]
        routed.close()
    assert sql("SELECT agent_id, dim FROM ctx.lcm_embedding_profile WHERE identity_hash = 'copied-profile' "
               "ORDER BY agent_id") == [("alpha", 768), ("beta", 768)]
    assert reembed(_connect(_memory_store), client, ["memory"], batch_size=2)[0].embedded == 0
    fresh = reembed(_connect(_memory_store), client, ["memory"], batch_size=2, from_start=True)
    assert fresh[0].embedded == 2
    assert sql("SELECT item_id FROM ctx.lcm_summary_vector_keys ORDER BY item_id") == sorted((r[2],) for r in rows)
    print("LCM DB evidence:", rows)


def test_summary_batches_resume_and_failed_embedding_does_not_advance(_memory_store, memory_store_sql):
    sql = memory_store_sql
    for node, text in ((1, "first summary"), (2, "   "), (3, "last summary")):
        _summary(sql, "alpha", node, text)
    client = _Client()
    lines: list[str] = []
    first = reembed(_connect(_memory_store), client, ["memory"], batch_size=1, max_batches=1, out=lines.append)
    assert first[0].embedded == 1
    progress = sql("SELECT source, last_item_id FROM vec.reembed_progress ORDER BY source")
    assert len(progress) == 1 and progress[0][1] == 1

    def fail(_texts):
        raise RuntimeError("fake embedding failure")

    client.embed = fail
    with pytest.raises(RuntimeError, match="fake embedding failure"):
        reembed(_connect(_memory_store), client, ["memory"], batch_size=2)
    assert sql("SELECT source, last_item_id FROM vec.reembed_progress ORDER BY source") == progress
    client = _Client()
    rest = reembed(_connect(_memory_store), client, ["memory"], batch_size=2, out=lines.append)
    assert (rest[0].embedded, rest[0].skipped) == (1, 1)
    assert client.calls == [["last summary"]]
    assert sql("SELECT last_item_id FROM vec.reembed_progress") == [(3,)]
    assert any("LCM" in line and "alpha" in line for line in lines)
