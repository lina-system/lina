"""lina#20: a turn's record starts at the row the runtime fixed when the turn began (``AIAgent._persist_user_message_idx``,
passed as ``turn_start``), never at the last user row of the tail. A mid-turn ``/steer`` and a stop-gate nudge append
user rows inside the turn: the steer stays part of the same turn and record, and a nudge never decides the route.

The store-backed rows give each row its ``message_uid``, so both tests take ``memory_store_sql``."""

from __future__ import annotations

from types import SimpleNamespace
from unittest.mock import MagicMock, patch

import pytest

from tests.plugins.memory.lina.harness import _response, drain, make_agent, write_home


@pytest.fixture
def home(tmp_path, monkeypatch, lina_env):
    home = write_home(tmp_path / ".hermes")
    monkeypatch.setenv("HERMES_HOME", str(home))
    return home


def _recording_agent(session_id: str, home, scripted):
    """An agent whose model answers with ``scripted`` (callables, one per request) and whose lina-memory records
    every turn record it fills the conversation from."""
    from hermes_state import SessionDB

    agent = make_agent(session_id, SessionDB(home / "state.db"))
    lina = next(p for p in agent._memory_manager.providers if p.name == "lina-memory")
    records, fill = [], lina._fill_conversation

    def recording_fill(record):
        records.append(record)
        return fill(record)

    lina._fill_conversation = recording_fill
    replies = iter(scripted)
    agent.client = MagicMock()
    agent.client.chat.completions.create.side_effect = lambda *_a, **_k: next(replies)()
    agent._cached_system_prompt = "You are helpful."
    return agent, lina, records


def test_a_steer_in_the_middle_of_a_tool_turn_stays_in_the_persons_one_record(home, lina_env, memory_store_sql):
    question = "person question with a tool round #st"
    call = SimpleNamespace(id="call_st", type="function",
                           function=SimpleNamespace(name="lookup_note", arguments='{"q": "st"}'))
    agent = None

    def tool_round_while_the_person_steers():
        agent.steer("also compare it with last year's plan #steer")
        return _response(None, [call])

    agent, lina, records = _recording_agent(
        "steer-1", home, [tool_round_while_the_person_steers, lambda: _response("reply after the steer #st")])
    result = agent.run_conversation(question)
    drain(agent)

    messages = result["messages"]
    asked = next(i for i, m in enumerate(messages) if m.get("role") == "user" and m.get("content") == question)
    steer = next(m for m in messages if m.get("display_kind") == "steer")
    assert "#steer" in steer["content"]
    turn = messages[asked:]
    assert [m.get("role") for m in turn] == ["user", "assistant", "tool", "user", "assistant"]

    assert len(records) == 1
    record = records[0]
    assert record.route == "person_channel" and record.words_of == "person"
    store_uid = lina._store_uid
    assert record.user_uid == store_uid(messages[asked]["message_uid"])
    assert record.message_uids == tuple(store_uid(m["message_uid"]) for m in turn)
    assert record.reply_uid == store_uid(turn[-1]["message_uid"])
    assert [(m["content"], m["metadata"]["lina_uid"]) for m in lina_env.written] == [
        (question, record.user_uid), ("reply after the steer #st", record.reply_uid)]
    conversations = dict(memory_store_sql(
        "SELECT split_part(uid, '#', 1), conversation_id FROM memory.messages WHERE agent_id IS NOT NULL"))
    assert {uid: conversations.get(uid) for uid in record.message_uids} == {
        uid: record.conversation_id for uid in record.message_uids}


def test_a_stop_gate_note_after_an_internal_notification_keeps_the_notifications_route(
        home, lina_env, memory_store_sql):
    notice = "[IMPORTANT: Background process 7 matched watch pattern \"ERROR\" #ng"
    note = "Language server: 1 new error in garden.py after your edit. #lsp"
    notes = iter([note])
    agent, lina, records = _recording_agent(
        "nudge-1", home, [lambda: _response("interim answer #ng"), lambda: _response("final answer #ng")])
    with patch("agent.turn_stop_gates._late_lsp_verdicts", lambda _agent: next(notes, None)):
        result = agent.run_conversation(notice, persist_user_display_kind="internal_notification")
    drain(agent)

    messages = result["messages"]
    assert [(m.get("role"), m.get("content")) for m in messages] == [
        ("user", notice), ("assistant", "interim answer #ng"), ("user", note), ("assistant", "final answer #ng")]
    assert [r.route for r in records] == ["gateway_notice"]
    assert records[0].user_uid == lina._store_uid(messages[0]["message_uid"])
    assert records[0].message_uids == tuple(lina._store_uid(m["message_uid"]) for m in messages)
    assert lina_env.written == []
