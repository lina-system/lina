"""lina#21: in card_in_prompt mode the person card is fetched when the system prompt is built and per-turn
recall carries only dialectic results; a slow card leaves the prompt without it instead of holding the turn."""

import logging
import threading
import time
from pathlib import Path
from unittest.mock import Mock

import pytest

from hermes_constants import reset_hermes_home_override, set_hermes_home_override
from plugins.memory.honcho import HonchoMemoryProvider
from plugins.memory.honcho.client import HonchoClientConfig

CARD = "Alice grows tomatoes"


class CardManager:
    def __init__(self, gate: threading.Event | None = None):
        self.gate = gate
        self.context_queries = []
        self.queued = []

    def get_prefetch_context(self, session, query, **kwargs):
        self.context_queries.append(query)
        if self.gate is not None:
            self.gate.wait(10)
        return {"card": CARD, "representation": "likes gardening"}

    def set_context_result(self, session, context):
        pass

    def pop_context_result(self, session):
        return None

    def prefetch_context(self, session, query):
        self.queued.append(query)

    def dialectic_query(self, session, prompt, **kwargs):
        return "dialectic: sow in spring"


def make_provider(manager, *, card_in_prompt=True, recall_sync=False):
    provider = HonchoMemoryProvider(card_in_prompt=card_in_prompt)
    cfg = HonchoClientConfig(timeout=1)
    cfg.recall_sync = recall_sync
    provider._config = cfg
    provider._manager = manager
    provider._session_key = "session-a"
    provider._session_initialized = True
    for name in ("recall_sync", "recall_mode", "injection_frequency", "context_cadence",
                 "dialectic_cadence", "dialectic_depth", "dialectic_depth_levels"):
        setattr(provider, f"_{name}", getattr(cfg, name))
    return provider


def test_card_comes_from_the_prompt_build_and_never_from_per_turn_recall():
    manager = CardManager()
    provider = make_provider(manager)
    block = provider.session_card_block()
    assert CARD in block and "likes gardening" in block
    for turn, query in enumerate(("plan the garden beds", "when do tomatoes go out", "what about basil"), 1):
        provider.on_turn_start(turn, query)
        assert CARD not in provider.prefetch(query)
        provider.queue_prefetch(query)
    assert manager.context_queries == [None]
    assert manager.queued == []


def test_without_card_in_prompt_the_card_stays_in_turn_recall():
    manager = CardManager()
    provider = make_provider(manager, card_in_prompt=False)
    assert provider.session_card_block() == ""
    provider.on_turn_start(1, "plan the garden beds")
    assert CARD in provider.prefetch("plan the garden beds")


def test_synchronous_recall_skips_the_card_too():
    manager = CardManager()
    provider = make_provider(manager, recall_sync=True)
    provider.on_turn_start(1, "plan the garden beds")
    assert CARD not in provider.prefetch("plan the garden beds")
    assert manager.context_queries == []


def test_a_slow_card_builds_the_prompt_without_it_within_the_base_wait(caplog):
    gate = threading.Event()
    provider = make_provider(CardManager(gate))
    provider._FIRST_TURN_BASE_TIMEOUT = 0.2
    started = time.monotonic()
    try:
        with caplog.at_level(logging.INFO, logger="plugins.memory.honcho"):
            assert provider.session_card_block() == ""
        assert time.monotonic() - started < 3.0
        assert any("person card not ready" in r.getMessage() for r in caplog.records)
    finally:
        gate.set()


@pytest.mark.parametrize(
    "has_profile_tastes,pin,owner_page,expected",
    [
        pytest.param(True, None, None, {"summary", "representation", "card"}, id="tastes"),
        pytest.param(False, None, None, {"summary", "representation", "card", "ai_representation", "ai_card"},
                     id="no-tastes"),
        pytest.param(True, ["aiCard", "peerCard"], None, {"card"}, id="tastes-with-host-pin"),
        pytest.param(False, ["aiCard", "peerCard"], None, {"ai_card", "card"}, id="no-tastes-with-host-pin"),
        pytest.param(None, ["aiCard", "peerCard"], None, {"ai_card", "card"}, id="taste-store-unavailable"),
        pytest.param(True, [], None, set(), id="empty-host-pin"),
        pytest.param(True, None, "Owner brain card", {"summary", "owner_page"}, id="tastes-with-owner-page"),
    ],
)
def test_lina_session_card_respects_tastes_of_the_bound_profile_and_honcho_pins(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    has_profile_tastes: bool | None,
    pin: list[str] | None,
    owner_page: str | None,
    expected: set[str],
) -> None:
    from hermes_cli.heart import taste_context
    from hermes_state_store import StoreUnavailableError
    from plugins.memory.honcho import client, session
    from plugins.memory.lina import LinaMemoryProvider

    launch_home = tmp_path / "launch"
    profile_home = tmp_path / "profile"
    launch_home.mkdir()
    profile_home.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(launch_home))
    taste_home = profile_home if has_profile_tastes else launch_home

    def has_tastes(home: Path) -> bool:
        if has_profile_tastes is None:
            raise StoreUnavailableError("fixture taste store unavailable")
        return home == taste_home

    monkeypatch.setattr(taste_context, "has_tastes", has_tastes)

    raw = {} if pin is None else {
        "injection": {"sessionStart": ["peerRepresentation"]},
        "hosts": {"hermes": {"injection": {"sessionStart": pin}}},
    }
    cfg = HonchoClientConfig(
        api_key="test-key", enabled=True, host="hermes", raw=raw, recall_sync=True, save_messages=False, timeout=2,
    )
    sections = {
        "summary": "We planned a garden bed.",
        "representation": "Prefers seedlings.",
        "card": CARD,
        "ai_representation": "Explains the planting schedule.",
        "ai_card": "Answers gardening questions.",
    }
    manager = Mock(spec=session.HonchoSessionManager)
    manager.get_prefetch_context.return_value = sections
    monkeypatch.setattr(HonchoClientConfig, "from_global_config", lambda: cfg)
    monkeypatch.setattr(client, "get_honcho_client", Mock())
    monkeypatch.setattr(session, "HonchoSessionManager", Mock(return_value=manager))
    monkeypatch.setattr(LinaMemoryProvider, "_page_card", lambda self: owner_page)
    provider = LinaMemoryProvider()
    # The manager passes its owning home explicitly even when another profile
    # owns the caller's current context.
    token = set_hermes_home_override(launch_home)
    try:
        provider.initialize("taste-session", hermes_home=str(profile_home), cwd=str(tmp_path))
        block = provider.session_card_block()
    finally:
        try:
            provider.shutdown()
        finally:
            reset_hermes_home_override(token)

    if owner_page:
        sections = {**sections, "owner_page": owner_page}
    assert {key for key, text in sections.items() if text in block} == expected
