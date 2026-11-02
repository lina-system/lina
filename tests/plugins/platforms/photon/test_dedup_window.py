"""The shared deduplicator honors Photon's window."""
from gateway.platforms import helpers
from plugins.platforms.photon import adapter

def test_photon_dedup_window(monkeypatch):
    now = [1000.0]
    monkeypatch.setattr(helpers.time, "time", lambda: now[0])
    dedup = helpers.MessageDeduplicator(max_size=adapter._DEDUP_MAX_SIZE, ttl_seconds=adapter._DEDUP_WINDOW_SECONDS)
    assert dedup.is_duplicate("m") is False
    assert dedup.is_duplicate("m") is True
    now[0] += adapter._DEDUP_WINDOW_SECONDS
    assert dedup.is_duplicate("m") is False
