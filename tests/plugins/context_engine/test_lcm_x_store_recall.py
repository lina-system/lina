"""lcm_recall for an agent whose LCM-X lives on the memory store (lina#704). The store keeps one embedding model (the
home's ``embedding:``, written by ``lina store reembed``), so LCM-X's default local model stays off there and says
nothing at agent init; the full-text arm answers on a store migrated with another provider's embedding profile; and
a ranked full-text query tokenizes only the rows it ranks plus a bounded sample, not the whole table. Real Postgres
(tests/_fixtures/memory_store.py)."""

from __future__ import annotations

import json
import logging
import sys
from unittest.mock import patch

import pytest

from plugins.context_engine import lcm_x_store
from tests.plugins.context_engine.test_lcm_x_on_store import _lcm

FASTEMBED_WARNING = "LCM semantic embeddings are ENABLED but the configured provider is unavailable"


@pytest.fixture
def store_home(tmp_path, monkeypatch):
    """A home whose LCM-X goes on the store, with LCM_EMBEDDINGS_ENABLED unset (LCM-X's default turns it on)."""
    home = tmp_path / "home"
    home.mkdir()
    (home / "config.yaml").write_text("context:\n  engine: lcm-x\nlcm:\n  store_backend: postgres\n")
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.delenv("LCM_EMBEDDINGS_ENABLED", raising=False)
    monkeypatch.setitem(sys.modules, "tiktoken", None)
    yield home
    lcm_x_store.unroute(home / "lcm.db")


def _engine(home):
    config = _lcm("config").LCMConfig.from_env()
    assert config.embeddings_enabled and not config.embeddings_enabled_explicit
    return _lcm("engine").LCMEngine(config=config, hermes_home=str(home))


def test_agent_init_on_the_store_does_not_turn_on_or_warn_about_the_local_model(store_home, caplog):
    from hermes_cli.plugins import _reset_plugin_managers_for_tests
    from run_agent import AIAgent

    _reset_plugin_managers_for_tests()
    try:
        with (
            caplog.at_level(logging.WARNING),
            patch("model_tools.get_tool_definitions", return_value=[]),
            patch("model_tools.check_toolset_requirements", return_value={}),
            patch("agent.process_bootstrap.OpenAI"),
            # Whether FastEmbed is installed varies by host; the contract is that the store home never warns even when
            # the local provider is unavailable, so force that.
            patch.object(_lcm("embedding_provider"), "probe_provider_availability",
                         return_value={"available": False, "detail": "fastembed missing", "provider": "fastembed",
                                       "model": "BAAI/bge-small-en-v1.5"}),
        ):
            agent = AIAgent(api_key="test-key-1234567890", base_url="https://openrouter.ai/api/v1", quiet_mode=True,
                            skip_context_files=True, skip_memory=True, session_id="s1")
        engine = agent.context_compressor
        assert lcm_x_store.store_identity(store_home / "lcm.db") is not None
        assert engine._config.embeddings_enabled is False
        assert [r.getMessage() for r in caplog.records if FASTEMBED_WARNING in r.getMessage()] == []
        doctor = engine.__class__.__module__.rsplit(".", 1)[0]
        check = sys.modules[f"{doctor}.tools"]._embedding_provider_health_check(engine)
        assert check["status"] == "pass" and "lina store reembed" in check["detail"]
        engine.shutdown()
    finally:
        _reset_plugin_managers_for_tests()


def test_recall_on_a_store_holding_another_provider_s_profile_answers_from_full_text(store_home, memory_store_sql):
    """``lina migrate memory`` copies the source's active embedding profile (here Voyage's) and drops its vectors."""
    engine = _engine(store_home)
    try:
        agent = lcm_x_store.store_identity(store_home / "lcm.db")["agent_id"]
        memory_store_sql("INSERT INTO ctx.lcm_embedding_profile (agent_id, identity_hash, provider, model_name, dim, "
                         "task) VALUES (%s, 'copied-voyage', 'voyage', 'voyage-3', 1024, 'summary')", (agent,))
        engine.on_session_start("s1", conversation_id="c1")
        engine.ingest([{"role": "user", "content": "the cobalt release moved to friday"},
                       {"role": "assistant", "content": "noted, friday it is"}])
        result = json.loads(_lcm("tools").lcm_recall({"query": "cobalt release"}, engine=engine))
        assert result["provenance"]["coverage"]["fts"] == "ok"
        assert result["hits"] and not result.get("timeout")
        assert "profile belongs to" not in result.get("degraded_reason", "")
    finally:
        engine.shutdown()


def _tokenized_rows(plan: dict) -> int:
    """Rows a plan node computed a tsvector for: what it returned and what its filter dropped."""
    text = json.dumps({k: v for k, v in plan.items() if k in ("Output", "Filter", "Recheck Cond")})
    own = 0
    if "to_tsvector" in text:
        loops = plan.get("Actual Loops", 1)
        own = loops * (plan.get("Actual Rows", 0) + plan.get("Rows Removed by Filter", 0)
                       + plan.get("Rows Removed by Index Recheck", 0))
    return max([own] + [_tokenized_rows(child) for child in plan.get("Plans", [])])


def test_a_ranked_full_text_query_tokenizes_its_matches_and_a_bounded_sample(store_home):
    rows = 1500
    db = store_home / "lcm.db"
    assert lcm_x_store.route(db)
    store = _lcm("store").MessageStore(db)
    conn = lcm_x_store.backend_for(db)()
    try:
        store.append_batch("s1", [{"role": "user", "content": f"filler row {n} about nothing much"} for n in range(rows)]
                           + [{"role": "user", "content": "the cobalt incident report"}] * 3)
        assert [r["content"] for r in store.search("cobalt", limit=5, sort="relevance", stemmed=True)] \
            == ["the cobalt incident report"] * 3
        template = lcm_x_store.translate(
            "SELECT messages_fts_stem.rowid, bm25(messages_fts_stem) AS rank FROM messages_fts_stem "
            "WHERE messages_fts_stem MATCH ? ORDER BY rank LIMIT 50", conn._catalog)
        sql, params = lcm_x_store.render(template, ["cobalt"])
        with conn._pg.transaction():
            [(plan,)] = conn._pg.execute("EXPLAIN (ANALYZE, VERBOSE, FORMAT JSON) " + sql, params).fetchall()
            [(index_scans,)] = conn._pg.execute(
                "SELECT pg_stat_get_xact_numscans('memory.messages_content_lcm_porter'::regclass)").fetchall()
        assert _tokenized_rows(plan[0]["Plan"]) < rows
        assert index_scans > 0
    finally:
        conn.close()
        store.close()
