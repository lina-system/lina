"""One backend dialectic per (session, query) in flight, and the level sent is the configured one.

A dialectic is an agent loop on the Honcho server that can outlive the client's HTTP timeout; a
second fire beside a running one starts a second loop that the server runs to the end.
"""

import threading
import time
from unittest.mock import MagicMock, patch

from plugins.memory.honcho import HonchoMemoryProvider
from plugins.memory.honcho.client import HonchoClientConfig


def _make_provider(**cfg_extra):
    cfg = HonchoClientConfig(**{"api_key": "test-key", "enabled": True, "recall_mode": "hybrid", **cfg_extra})
    provider = HonchoMemoryProvider()
    manager = MagicMock()
    manager.get_or_create.return_value = MagicMock(messages=[])
    manager.get_prefetch_context.return_value = None
    manager.dialectic_query.return_value = ""
    with patch("plugins.memory.honcho.client.HonchoClientConfig.from_global_config", return_value=cfg), \
         patch("plugins.memory.honcho.client.get_honcho_client", return_value=MagicMock()), \
         patch("plugins.memory.honcho.session.HonchoSessionManager", return_value=manager), \
         patch("hermes_constants.get_hermes_home", return_value=MagicMock()):
        provider.initialize(session_id="test-inflight")
    for thread in (provider._init_thread, provider._prefetch_thread):
        if thread:
            thread.join(timeout=3.0)
    provider._prefetch_thread = None
    provider._last_dialectic_turn = -999
    manager.dialectic_query.reset_mock()
    return provider, manager


def _block_dialectic(manager):
    """Make dialectic_query block until released; returns (started, release)."""
    started, release = threading.Event(), threading.Event()

    def _chat(*_args, **_kwargs):
        started.set()
        release.wait(timeout=10.0)
        return "synthesis"
    manager.dialectic_query.side_effect = _chat
    return started, release


def test_same_session_and_query_reuses_the_in_flight_dialectic():
    provider, manager = _make_provider()
    started, release = _block_dialectic(manager)
    try:
        first = provider._spawn_dialectic("what is the user working on", thread_name="t1", fired_at=1, log_label="a")
        assert started.wait(timeout=5.0)
        second = provider._spawn_dialectic("what is the user working on", thread_name="t2", fired_at=2, log_label="b")
    finally:
        release.set()
    for thread in {first, second}:
        thread.join(timeout=5.0)
    assert second is first
    assert manager.dialectic_query.call_count == 1


def test_running_dialectic_is_not_refired_after_the_turn(monkeypatch):
    """No timeout in honcho.json: the HTTP client then uses its 30 s default, and a dialectic still
    running 20 s later must not be fired again by the next turn's refresh."""
    provider, manager = _make_provider(timeout=None)
    started, release = _block_dialectic(manager)
    try:
        provider._turn_count = 1
        provider.queue_prefetch("what is the user working on this week")
        assert started.wait(timeout=5.0)
        real_monotonic = time.monotonic
        monkeypatch.setattr(time, "monotonic", lambda: real_monotonic() + 20.0)
        provider._turn_count = 2
        provider.queue_prefetch("and which of those projects is due first")
        monkeypatch.undo()
    finally:
        release.set()
    provider._prefetch_thread.join(timeout=5.0)
    assert manager.dialectic_query.call_count == 1


def test_configured_level_above_the_cap_is_sent_as_configured():
    provider, manager = _make_provider(dialectic_reasoning_level="max", reasoning_level_cap="high")
    manager.dialectic_query.return_value = "synthesis"
    provider._run_dialectic_depth("short question")
    assert manager.dialectic_query.call_args.kwargs["reasoning_level"] == "max"
