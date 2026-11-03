"""lina#190: an explicit memory_recall in a turn whose automatic recall already ran still asks the backend.

The turn-start path is prefetch_sync, which advances the dialectic and context cadence. A second query
through that path comes back empty. The explicit call must not.
"""

from __future__ import annotations

from plugins.memory.honcho import HonchoMemoryProvider
from plugins.memory.honcho.client import HonchoClientConfig


class _Backend:
    def __init__(self) -> None:
        self.prompts: list[str] = []

    def get_prefetch_context(self, session, query, **kwargs):
        return {"representation": f"base:{query}"}

    def dialectic_query(self, session, prompt, **kwargs):
        self.prompts.append(prompt)
        if "garden notes" in prompt:
            return "HIT garden notes"
        if "compiler bug" in prompt:
            return "HIT compiler bug"
        return ""


def _provider() -> HonchoMemoryProvider:
    provider = HonchoMemoryProvider()
    cfg = HonchoClientConfig(timeout=1)
    cfg.recall_sync = True
    provider._config = cfg
    provider._manager = _Backend()
    provider._session_key = "session-a"
    provider._session_initialized = True
    for name in ("recall_sync", "recall_mode", "injection_frequency", "context_cadence",
                 "dialectic_cadence", "dialectic_depth", "dialectic_depth_levels"):
        setattr(provider, f"_{name}", getattr(cfg, name))
    return provider


def test_two_queries_in_one_turn_both_reach_the_backend():
    from agent.memory_manager import MemoryManager
    from plugins.memory.lina import LinaMemoryProvider

    provider = _provider()
    lina = LinaMemoryProvider()
    lina._honcho = provider
    lina._honcho_on = True
    manager = MemoryManager()
    manager.add_provider(lina)

    provider.on_turn_start(1, "where the compiler bug was left")
    assert "compiler bug" in provider.prefetch("where the compiler bug was left")
    cadence = (provider._last_dialectic_turn, provider._last_context_turn)
    assert provider._manager.prompts  # the turn-start recall asked the backend

    second = manager.handle_tool_call("memory_recall", {"query": "who keeps the garden notes"})

    assert len(provider._manager.prompts) == 2
    assert "garden notes" in provider._manager.prompts[1]
    assert "HIT garden notes" in second
    assert (provider._last_dialectic_turn, provider._last_context_turn) == cadence
