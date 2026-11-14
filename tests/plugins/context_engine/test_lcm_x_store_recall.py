"""lcm_recall for an agent whose LCM-X lives on the memory store (lina#704): a ranked full-text query tokenizes only
the rows it ranks plus a bounded sample, not the whole table. Real Postgres (tests/_fixtures/memory_store.py)."""

from __future__ import annotations

import json
import sys

import pytest

from plugins.context_engine import lcm_x_store
from tests.plugins.context_engine.test_lcm_x_on_store import _lcm


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
