"""The context-engine loader's turn handover (lina#20): a memory provider claims a session and the engine's own
``post_llm_call`` ingest leaves that session's turns to it; any other session keeps the engine's ingest."""

from plugins import context_engine as loader


class _Taker:
    def __init__(self):
        self.turns = []

    def take(self, history, *, session_id, turn_start=None):
        self.turns.append((session_id, list(history)))


def test_a_claimed_session_goes_to_the_claimer_and_others_to_the_engine():
    engine_calls = []
    hook = loader._handed_over_by_memory_layer(lambda **kw: engine_calls.append(kw["session_id"]))
    taker = _Taker()
    loader.claim_turn_handover("s-claimed", taker.take)
    try:
        hook(session_id="s-claimed", conversation_history=[{"role": "user", "content": "hi"}])
        hook(session_id="s-other", conversation_history=[{"role": "user", "content": "hi"}])
    finally:
        loader.release_turn_handover("s-claimed", taker.take)
    assert taker.turns == [("s-claimed", [{"role": "user", "content": "hi"}])]
    assert engine_calls == ["s-other"]


def test_release_hands_the_session_back_to_the_engine_and_a_newer_claim_survives_an_older_release():
    first, second = _Taker(), _Taker()
    loader.claim_turn_handover("s", first.take)
    loader.claim_turn_handover("s", second.take)
    loader.release_turn_handover("s", first.take)  # an older agent's release must not drop the newer claim
    assert loader.turn_handover_for("s") == second.take
    loader.release_turn_handover("s", second.take)
    assert loader.turn_handover_for("s") is None


def test_a_dropped_claimer_holds_no_claim():
    taker = _Taker()
    loader.claim_turn_handover("s-gone", taker.take)
    del taker
    assert loader.turn_handover_for("s-gone") is None


def test_an_unclaimed_turn_reaches_a_narrow_legacy_hook_through_the_dispatcher():
    """The dispatcher hands a ``**kwargs`` callback the whole payload; the wrapper must still give a narrow hook only
    the names it declares, as the dispatcher does for an unwrapped one."""
    from hermes_cli.plugins_dispatch import PluginDispatchMixin

    seen = []

    def narrow(conversation_history):
        seen.append(conversation_history)
        return "ok"

    payload = {"conversation_history": [{"role": "user", "content": "hi"}], "session_id": "s-narrow",
               "platform": "cli", "model": "m"}
    assert PluginDispatchMixin._invoke_hook_callback(narrow, payload) == "ok"
    wrapped = loader._handed_over_by_memory_layer(narrow)
    assert PluginDispatchMixin._invoke_hook_callback(wrapped, payload) == "ok"
    assert seen == [payload["conversation_history"]] * 2
