"""lina#25: the synthetic cache load (``cache_load.py``) with ``memory.auto_recall_append: true`` (300 tokens)."""

from __future__ import annotations

from hermes_cli import cache_measure
from tests.plugins.memory.lina.cache_load import TURNS, run_load


def test_synthetic_load_with_recall_appended_feeds_the_cache_report(lina_env, tmp_path, monkeypatch):
    load = run_load(tmp_path, monkeypatch, append=True)

    assert len(load.rows) == len(load.fingerprints) == TURNS
    assert sorted(set(load.compactions)) == [0, 1, 2]
    assert load.last_timestamp.startswith("2026-11-04")
    assert cache_measure.rates(load.rows).metered == TURNS
