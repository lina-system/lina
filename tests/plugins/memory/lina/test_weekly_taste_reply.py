"""The weekly batch changes one completed reply without changing its model request."""

from hashlib import sha256
from types import SimpleNamespace

import pytest

from tests.plugins.memory.lina import test_cache_order
from tests.plugins.memory.lina.test_turn_taste_routes import _route_agent, _run

home = test_cache_order.home
clock = test_cache_order.clock


def test_streamed_weekly_footer_prints_after_the_body_once(home, clock, memory_store_sql, capsys):
    from cli import HermesCLI
    from tests.agent.test_taste_voice_store import _promote

    _promote(memory_store_sql, "default", "orchard sketches", "like", clock.now)
    cli = HermesCLI.__new__(HermesCLI)
    cli.final_response_markdown = "raw"
    cli.show_reasoning = cli.show_timestamps = cli._last_turn_interrupted = False
    cli._reset_stream_state()
    with _route_agent(home, "cli") as (agent, requests):
        agent.stream_delta_callback = cli._stream_delta
        result, _ = _run(agent, requests, "Describe the weather.")
    cli._flush_stream()
    body = capsys.readouterr().out
    assert body.count("reply 1") == 1 and "orchard sketches" not in body

    response = result["final_response"]
    turn = SimpleNamespace(result=result, use_streaming_tts=False, box_opened=False)
    cli._chat_print_response_panel(turn, response)
    tail = capsys.readouterr().out
    print("Streamed CLI output:", repr(body), "Post-stream output:", repr(tail))
    assert "orchard sketches" in response and tail.strip() == response.split("\n", 1)[1]


def test_weekly_promotion_ends_the_next_reply_without_changing_the_prefix(home, clock, memory_store_sql):
    label = "orchard sketches"
    question = "Please describe the weather."
    with _route_agent(home, "cli") as (agent, requests):
        first, before = _run(agent, requests, question)
        memory_store_sql(
            """
            WITH promoted AS (
                INSERT INTO memory.tastes
                    (agent_id, target_kind, label, valence, strength, status, origin,
                     first_seen, protected_passed, protected_at)
                VALUES ('default', 'method', %s, 'like', 0.8, 'core', 'outcome',
                        DATE '2026-09-01', true, TIMESTAMPTZ '2026-11-03 08:00+00')
                RETURNING agent_id, id
            )
            INSERT INTO memory.taste_events
                (agent_id, taste_id, reason, changed_by, changed_at,
                 from_status, to_status, from_valence, to_valence)
            SELECT agent_id, id, 'status_changed', 'review', TIMESTAMPTZ '2026-11-03 00:00+00',
                   'active', 'core', 'like', 'like' FROM promoted
            """,
            (label,),
        )
        second, due = _run(agent, requests, question, conversation_history=first["messages"])
        third, later = _run(agent, requests, question, conversation_history=second["messages"])

    prompts = [next(row["content"] for row in rows if row["role"] == "system")
               for rows in (before, due, later)]
    digests = [sha256(prompt.encode()).hexdigest() for prompt in prompts]
    print("system SHA-256 (before, due, later)", *digests)
    print("final replies", *(repr(result["final_response"]) for result in (first, second, third)))
    assert len(set(digests)) == 1
    assert due[:len(before)] == before and later[:len(due)] == due
    assert due[-1]["role"] == "user"
    assert label not in str(due)
    assert first["final_response"] == "reply 1"
    assert third["final_response"] == "reply 3"
    lines = second["final_response"].splitlines()
    assert lines[0] == "reply 2" and label in lines[-1]
    assert second["final_response"].count(label) == 1


def _task_taste(sql):
    sql("""INSERT INTO memory.tastes (agent_id, target_kind, label, valence, strength, status, origin)
           VALUES ('default', 'task_type', 'orchard sketches', 'dislike', 0.8, 'active', 'outcome')""")


def test_disagreement_and_dislike_reach_recall_once_for_their_budgets(home, memory_store_sql):
    _task_taste(memory_store_sql)
    ask = "I like orchard sketches. Please draw orchard sketches."
    with _route_agent(home, "cli") as (agent, requests):
        first, permitted = _run(agent, requests, ask, task_id="sketch-1")
        second, repeat = _run(agent, requests, ask, task_id="sketch-1", conversation_history=first["messages"])
        _, other = _run(agent, requests, ask, task_id="sketch-2", conversation_history=second["messages"])
    granted = permitted[-1]["content"]
    call, = permitted[-2]["tool_calls"]
    assert call["function"]["name"] == "memory_recall" and call["id"] == permitted[-1]["tool_call_id"]
    assert "사용자" in granted and "검증" in granted
    assert repeat[:len(permitted)] == permitted
    assert "사용자" not in repeat[-1]["content"] and "검증" not in repeat[-1]["content"]
    assert "검증" in other[-1]["content"] and "사용자의 선호" not in other[-1]["content"]
    assert memory_store_sql("SELECT scope, count(*) FROM memory.taste_voice_claims WHERE spent GROUP BY scope ORDER BY scope") == [
        ("task", 2), ("week", 1),
    ]
    assert memory_store_sql("SELECT count(*) FROM memory.taste_voice_measurements WHERE disliked") == [(3,)]


@pytest.mark.parametrize(
    "platform,origin,author,ask",
    [
        ("cron", None, None, "orchard sketches"),
        ("subagent", None, None, "orchard sketches"),
        ("cli", None, {"id": "bot:relay", "is_bot": True}, "orchard sketches"),
        ("cli", "background_review", None, "orchard sketches"),
        ("cli", None, None, '[IMPORTANT: The user has invoked the "report-pages" skill. '
         'The full skill content is loaded below.]\n\n<skill>orchard sketches</skill>'),
    ],
)
def test_excluded_turn_leaves_both_budgets_and_measurements_empty(
    home, memory_store_sql, platform, origin, author, ask,
):
    _task_taste(memory_store_sql)
    with _route_agent(home, platform) as (agent, requests):
        agent._turn_origin = origin
        response, _ = _run(agent, requests, ask, turn_author=author)
    assert response["final_response"] == "reply 1"
    assert memory_store_sql("SELECT count(*) FROM memory.taste_voice_claims") == [(0,)]
    assert memory_store_sql("SELECT count(*) FROM memory.taste_voice_measurements") == [(0,)]


@pytest.mark.parametrize("outcome", ["failed", "interrupted"])
def test_unfinished_reply_releases_the_reservations_seen_by_the_model(
    home, memory_store_sql, monkeypatch, outcome,
):
    _task_taste(memory_store_sql)
    ask = "I like orchard sketches. Please draw orchard sketches."
    recorded = []
    respond = test_cache_order.RecordingModel.do_POST

    def stop_at_model(request):
        recorded.append(memory_store_sql("SELECT scope, spent FROM memory.taste_voice_claims ORDER BY scope"))
        if outcome == "interrupted":
            request.server.agent.interrupt(hard_cancel=True)
            return respond(request)
        request.rfile.read(int(request.headers["Content-Length"]))
        request.send_error(400, "Synthetic invalid request")

    with _route_agent(home, "cli") as (agent, requests):
        with monkeypatch.context() as scoped:
            scoped.setattr(test_cache_order.RecordingModel, "do_POST", stop_at_model)
            result = agent.run_conversation(ask, task_id="sketch")
        assert recorded and all(pending == [("task", False), ("week", False)] for pending in recorded)
        assert result[outcome] and not result["completed"]
        assert memory_store_sql("SELECT count(*) FROM memory.taste_voice_claims") == [(0,)]
        assert memory_store_sql("SELECT count(*) FROM memory.taste_voice_measurements") == [(0,)]
        agent.clear_interrupt()
        _run(agent, requests, ask, task_id="sketch", conversation_history=result["messages"])
    assert memory_store_sql("SELECT scope, spent FROM memory.taste_voice_claims ORDER BY scope") == [
        ("task", True), ("week", True),
    ]


def test_selected_method_dislike_records_both_flags_without_spending_a_task_line(home, memory_store_sql):
    from agent.taste_voice_store import measure

    measure(home, "earlier", "text_reply", 120, ("scripts/run_tests.sh",), disliked=False)
    memory_store_sql("""INSERT INTO memory.tastes (agent_id, target_kind, label, valence, strength, status, origin)
                        VALUES ('default', 'method', 'orchard sketches', 'dislike', 0.8, 'active', 'outcome')""")
    with _route_agent(home, "cli") as (agent, requests):
        result, _ = _run(agent, requests, "Please use orchard sketches.")
    assert memory_store_sql("SELECT turn_id, length, flags FROM memory.taste_voice_measurements WHERE disliked") == [
        (result["turn_id"], len(result["final_response"]), {"short_output": 1, "skipped_verification": 1}),
    ]
    assert memory_store_sql("SELECT count(*) FROM memory.taste_voice_claims") == [(0,)]


def test_report_tool_in_current_turn_releases_weekly_footer_for_next_reply(home, clock, memory_store_sql):
    from tests.agent.test_taste_voice_store import _promote

    _promote(memory_store_sql, "default", "orchard sketches", "like", clock.now)
    # The tool call names a synthetic executable in this test's home.
    command = home / "report-link"
    command.write_text("#!/bin/sh\nexit 0\n", encoding="utf-8")
    command.chmod(0o700)

    def report():
        yield "", "terminal", {"command": f"'{command}' synthetic.md"}

    with _route_agent(home, "cli") as (agent, requests):
        sent, request = _run(agent, requests, "Finish the synthetic document.", prelude=report())
        assert any(call["function"]["name"] == "terminal" for row in request for call in row.get("tool_calls", []))
        assert sent["final_response"] == "reply 1"
        assert memory_store_sql("SELECT count(*) FROM memory.taste_voice_claims") == [(0,)]
        assert memory_store_sql("SELECT count(*) FROM memory.taste_voice_measurements") == [(0,)]
        ordinary, _ = _run(agent, requests, "Describe the weather.", conversation_history=sent["messages"])
    assert "orchard sketches" in ordinary["final_response"].splitlines()[-1]
