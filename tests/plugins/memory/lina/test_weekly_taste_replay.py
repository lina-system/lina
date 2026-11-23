"""A recovered weekly reply keeps the first public stream and the held original private."""

from types import SimpleNamespace

import pytest

from gateway.session_cli_turn import take_cli_result_fields
from tests.agent.test_taste_voice_store import _promote
from tests.hermes_cli import test_gateway_agent
from tests.hermes_cli.test_gateway_agent import frame
from tests.plugins.memory.lina import test_cache_order
from tests.plugins.memory.lina.test_turn_taste_routes import _route_agent, _run

home = test_cache_order.home
clock = test_cache_order.clock
make_agent = test_gateway_agent.make_agent


@pytest.mark.parametrize("held", [False, True], ids=["earlier-append", "held-rewrite"])
def test_recovered_weekly_reply_is_shown_once(home, clock, memory_store_sql, make_agent, monkeypatch, capsys, held):
    from cli import HermesCLI
    from hermes_cli import lifecycle

    _promote(memory_store_sql, "default", "orchard sketches", "like", clock.now)
    invoke_hook = lifecycle.invoke_hook

    def append_hook(name, **kwargs):
        if name == "transform_llm_output":
            return [kwargs["response_text"] + "\nPublic note.  "]
        return invoke_hook(name, **kwargs)

    if held:
        monkeypatch.setattr("agent.conversation_loop.held_stream_transforms", lambda: (lambda **_: "Public reply.",))
    else:
        monkeypatch.setattr(lifecycle, "invoke_hook", append_hook)
    streamed = []
    with _route_agent(home, "cli") as (agent, requests):
        agent.stream_delta_callback = streamed.append
        result, _ = _run(agent, requests, "Describe the weather.")
    assert "".join(part for part in streamed if part) == ("" if held else "reply 1")
    receipt = take_cli_result_fields(result)
    assert receipt["pre_transform_response"] == (None if held else "reply 1")
    expected_body = "Public reply." if held else "reply 1\nPublic note.  "
    assert receipt["final_response"].startswith(expected_body + "\n")

    cli = HermesCLI.__new__(HermesCLI)
    cli.final_response_markdown = "raw"
    cli.show_reasoning = cli.show_timestamps = cli._last_turn_interrupted = False
    cli._reset_stream_state()
    events = [] if held else [frame("message.delta", {"text": "reply "})]
    events.append(frame("session.replay_gap", {}, admission=None))
    viewer, _ = make_agent(events, result=receipt, stream_delta_callback=cli._stream_delta)
    recovered = viewer.run_conversation("Describe the weather.")
    cli._flush_stream()
    cli._chat_print_response_panel(SimpleNamespace(result=recovered, use_streaming_tts=False, box_opened=False),
                                   recovered["final_response"])
    visible = capsys.readouterr().out
    assert visible.count("orchard sketches") == 1, visible
    assert visible.count("reply 1") == int(not held)
    assert visible.count("Public reply." if held else "Public note.") == 1


def test_tui_completion_replaces_streamed_body_with_weekly_reply(home, clock, memory_store_sql):
    from contextlib import nullcontext

    import tui_gateway.server as srv
    from tests.tui_gateway.test_turn_payload_response_transformed import _turn

    _promote(memory_store_sql, "default", "orchard sketches", "like", clock.now)
    deltas = []
    with _route_agent(home, "tui") as (agent, requests):
        agent.stream_delta_callback = deltas.append
        result, _ = _run(agent, requests, "Describe the weather.")
        st = _turn(result)
        st.agent, st.history, st.compression_count = agent, [], 0
        session = {"pending_title": None, "session_key": agent.session_id, "agent": agent, "history_lock": nullcontext()}
        payload, _, _ = srv._complete_turn_payload(session, st, None, 80)
        complete = srv._event_frame("message.complete", agent.session_id, payload)["params"]["payload"]
    assert "".join(delta for delta in deltas if delta) == "reply 1"
    assert complete["response_transformed"] and complete["text"] == result["final_response"]
    assert complete["text"].startswith("reply 1\n") and complete["text"].count("orchard sketches") == 1
    assert "pre_transform_response" not in complete


def test_api_turn_leaves_weekly_reply_for_owner(home, clock, memory_store_sql):
    _promote(memory_store_sql, "default", "orchard sketches", "like", clock.now)
    with _route_agent(home, "api_server") as (agent, requests):
        result, _ = _run(agent, requests, "Describe the weather.")
    assert result["final_response"] == "reply 1"
    assert memory_store_sql("SELECT count(*) FROM memory.taste_voice_claims") == [(0,)]
