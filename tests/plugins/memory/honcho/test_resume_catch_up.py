"""A process's first turn waits for recall the same way whether it opens a session or resumes one.

In one long-lived process, turn N's injection comes from the refresh queued at the end of
turn N-1. ``hermes chat --resume <id> --query ...`` starts a new process at turn N, where that
refresh never ran and later turns do not wait, so the resumed turn injected nothing.

Driven through Hermes' real MemoryManager: it abandons a provider prefetch after a few seconds,
so first-turn waits that ran inside prefetch() could never be configured longer than that.
"""

import threading
import time
from unittest.mock import MagicMock, patch

import pytest
from agent import skill_commands
from agent.memory_manager import MemoryManager
from plugins.memory.honcho import HonchoMemoryProvider
from plugins.memory.honcho.client import HonchoClientConfig

PREVIOUS = "Add the ShapeIndex encoder."
CURRENT = "Now decode it back."
DIALECTIC_ANSWER = "The user wants every encoder round-trip tested."
LATENCY = 0.3
# What firstTurnBaseWait / firstTurnDialecticWait are set to wherever a test checks the budget.
FIRST_TURN_WAIT = 0.05


def _provider(context_queries: list[str], *, save_messages: bool = False, prewarm_latency: float = LATENCY,
              prewarm_answer: str = "Generic prewarm summary.",
              prewarm_gate: threading.Event | None = None, **cfg_overrides) -> HonchoMemoryProvider:
    """A provider over a Honcho that answers slowly and holds the earlier turn only if it saved it."""
    manager = MagicMock()
    manager.get_or_create.return_value.messages = (
        [{"role": "user", "content": PREVIOUS}, {"role": "assistant", "content": "Done."}] if save_messages else []
    )
    manager.pop_context_result.return_value = {}

    def _context(_key, query=None, **_kwargs):
        context_queries.append(query)
        # health: allow HX013 -- the delay IS the behaviour under test (a Honcho slower than the turn budget)
        time.sleep(LATENCY)
        return {"representation": f"conclusions recalled for: {query}"}

    def _dialectic(*_args, **_kwargs):
        if threading.current_thread().name == "honcho-prewarm-dialectic":
            if prewarm_gate is not None:
                # The test releases this gate at teardown: a prewarm still in flight, with no clock in it.
                prewarm_gate.wait()
            else:
                # health: allow HX013 -- the delay IS the behaviour under test (a prewarm slower than the resumed dialectic)
                time.sleep(prewarm_latency)
            return prewarm_answer
        # health: allow HX013 -- the delay IS the behaviour under test (a Honcho slower than the turn budget)
        time.sleep(LATENCY)
        return DIALECTIC_ANSWER

    manager.get_prefetch_context.side_effect = _context
    manager.dialectic_query.side_effect = _dialectic

    cfg = HonchoClientConfig(api_key="test-key", enabled=True, recall_mode="hybrid", dialectic_depth=1,
                             save_messages=save_messages, **cfg_overrides)
    provider = HonchoMemoryProvider()
    with patch("plugins.memory.honcho.client.HonchoClientConfig.from_global_config", return_value=cfg), \
         patch("plugins.memory.honcho.client.get_honcho_client", return_value=MagicMock()), \
         patch("plugins.memory.honcho.session.HonchoSessionManager", return_value=manager), \
         patch("hermes_constants.get_hermes_home", return_value=MagicMock()):
        provider.initialize(session_id="resumed-session")
    if provider._init_thread is not None:
        provider._init_thread.join(timeout=5.0)
    return provider


def _memory(provider: HonchoMemoryProvider) -> MemoryManager:
    # Shorter than every Honcho call above, so any wait left inside prefetch() is abandoned.
    memory = MemoryManager(external_prefetch_timeout=0.2)
    memory.add_provider(provider)
    return memory


class _RequestedWaits:
    """Every ``Thread.join(timeout=...)`` the code under test asks for, in the order it asks.

    The wait budgets are the behaviour under test. Timing the call with ``time.monotonic()``
    measures thread spawn and scheduler latency too, which is what made this flake.
    """

    def __init__(self) -> None:
        self.calls: list[tuple[str, float | None]] = []

    def __enter__(self) -> "_RequestedWaits":
        real_join = threading.Thread.join

        def _join(thread, timeout=None):
            self.calls.append((thread.name, timeout))
            return real_join(thread, timeout)

        self._patch = patch.object(threading.Thread, "join", _join)
        self._patch.start()
        return self

    def __exit__(self, *_exc) -> None:
        self._patch.stop()


@pytest.mark.parametrize(
    "save_messages, turn_start_kwargs",
    [(False, {"previous_message": PREVIOUS}), (True, {})],
    ids=["from-hermes-transcript", "from-honcho-history"],
)
def test_resumed_turn_injects_the_previous_turns_refresh(save_messages, turn_start_kwargs):
    context_queries: list[str] = []
    memory = _memory(_provider(context_queries, save_messages=save_messages))

    memory.on_turn_start(2, CURRENT, **turn_start_kwargs)
    injected = memory.prefetch_all(CURRENT)

    assert f"conclusions recalled for: {PREVIOUS}" in injected
    assert DIALECTIC_ANSWER in injected
    assert context_queries[0] == PREVIOUS

    # The catch-up stands in for one missed refresh; the next turn is an ordinary later turn.
    memory.on_turn_start(3, "Cover the edge cases.", previous_message=CURRENT)
    memory.prefetch_all("Cover the edge cases.")
    assert context_queries.count(PREVIOUS) == 1


def test_first_turn_waits_outlast_the_memory_managers_prefetch_timeout():
    memory = _memory(_provider([]))

    memory.on_turn_start(1, CURRENT)
    injected = memory.prefetch_all(CURRENT)

    assert f"conclusions recalled for: {CURRENT}" in injected
    # Turn 1 reuses the in-flight process-start prewarm rather than starting its own dialectic.
    assert "Generic prewarm summary." in injected


@pytest.mark.parametrize(
    "turn, turn_start_kwargs, dialectic_thread",
    [(1, {}, "honcho-prewarm-dialectic"), (2, {"previous_message": PREVIOUS}, "honcho-resume-dialectic")],
    ids=["new-session", "resumed"],
)
def test_first_turn_waits_are_bounded_by_the_first_turn_knobs(turn, turn_start_kwargs, dialectic_thread):
    # Held for the whole test, so the process-start prewarm is still in flight when the turn starts.
    prewarm = threading.Event()
    memory = _memory(_provider([], prewarm_gate=prewarm, first_turn_base_wait=FIRST_TURN_WAIT,
                               first_turn_dialectic_wait=FIRST_TURN_WAIT))

    try:
        with _RequestedWaits() as waits:
            memory.on_turn_start(turn, CURRENT, **turn_start_kwargs)
        injected = memory.prefetch_all(CURRENT)
    finally:
        prewarm.set()

    # The base layer is waited on first, then exactly one dialectic, each within its knob.
    assert [name for name, _ in waits.calls] == ["honcho-base-first", dialectic_thread]
    base_wait, dialectic_wait = [timeout for _, timeout in waits.calls]
    assert base_wait is not None and 0 <= base_wait <= FIRST_TURN_WAIT
    # The turn-1 dialectic wait uses its knob, not the 2s default it would fall back to.
    assert dialectic_wait == FIRST_TURN_WAIT
    assert "conclusions recalled for" not in injected


def test_late_prewarm_does_not_replace_the_resumed_dialectic():
    provider = _provider([], prewarm_latency=4 * LATENCY)
    memory = _memory(provider)

    memory.on_turn_start(2, CURRENT, previous_message=PREVIOUS)
    for thread in [t for t in threading.enumerate() if t.name == "honcho-prewarm-dialectic"]:
        thread.join(timeout=5.0)  # the prewarm finishes after the resumed dialectic

    injected = memory.prefetch_all(CURRENT)
    assert DIALECTIC_ANSWER in injected
    assert "Generic prewarm summary." not in injected


def _skill_message(instruction: str) -> str:
    """A slash-skill turn as Hermes expands it: activation note, skill body, then the user's instruction."""
    return (f'{skill_commands._SKILL_INVOCATION_PREFIX}"encoders" skill. {skill_commands._SINGLE_SKILL_MARKER}\n'
            f"# Encoders skill\nLong skill body the user never typed.\n"
            f"{skill_commands._SINGLE_SKILL_INSTRUCTION}{instruction}")


def test_first_turn_recalls_on_the_instruction_inside_a_skill_invocation():
    context_queries: list[str] = []
    memory = _memory(_provider(context_queries))

    memory.on_turn_start(1, _skill_message(CURRENT))

    assert context_queries == [CURRENT]


def test_trivial_first_turn_leaves_the_dialectic_wait_to_the_next_turn():
    # The prewarm comes back empty, so no dialectic has landed when the first real question arrives.
    provider = _provider([], prewarm_latency=0.0, prewarm_answer="")
    memory = _memory(provider)

    memory.on_turn_start(1, "hi")
    memory.prefetch_all("hi")
    provider.on_turn_start(2, CURRENT)

    # Called directly: the turn-2 dialectic wait still runs inside prefetch(), as it always has.
    assert DIALECTIC_ANSWER in provider.prefetch(CURRENT)


def test_resume_after_a_trivial_turn_recalls_on_the_current_message():
    context_queries: list[str] = []
    memory = _memory(_provider(context_queries))

    memory.on_turn_start(3, CURRENT, previous_message="thanks")

    assert context_queries == [CURRENT]
