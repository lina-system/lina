"""Conversation tool behavior against the disposable memory store."""

import json

import pytest

from plugins.memory.lina import LinaMemoryProvider


@pytest.fixture
def conversation(tmp_path, memory_store_sql, monkeypatch):
    provider = LinaMemoryProvider()
    provider._home = str(tmp_path / "profiles" / "alpha")
    provider._session_id = "room"
    assert "standing_rule" in {tool["name"] for tool in provider.get_tool_schemas()}
    current = None
    monkeypatch.setattr(LinaMemoryProvider, "_rule_source_uid", property(lambda self: current), raising=False)

    def say(uid, words, action="record", **kwargs):
        nonlocal current
        memory_store_sql(
            """INSERT INTO memory.messages
               (uid, source, agent_id, session_id, role, speaker, content, content_sha256, conversation_id)
               VALUES (%s, 'agent', 'alpha', 'room', 'user', 'owner', %s, 'fixture', 'sample')""",
            (uid, words),
        )
        current = uid
        return json.loads(provider.handle_tool_call("standing_rule", {"action": action, **kwargs}))

    return provider, say


def test_version_history_and_current_turn_provenance(conversation, memory_store_sql):
    provider, say = conversation
    first = say("first", "Everyone, always use short reports.")
    rid = first["rule_id"]
    assert first["version"] == 1
    second = say("second", "Everyone, always use two lines.", "change", rule_id=rid)
    assert second["version"] == 2
    for forged in ({"source_uid": "first"}, {"source_uid": "second"}, {"speaker": "owner"}):
        assert "error" in json.loads(provider.handle_tool_call(
            "standing_rule", {"action": "end", "rule_id": rid, **forged}))
    assert say("third", "Forget that rule.", "end", rule_id=rid)["version"] == 3
    assert say("list", "What rules do I have?", "list")["rules"] == []
    history = say("history", "Show the history.", "list", history=True)["rules"]
    assert [(row["version"], row["status"]) for row in history] == [
        (1, "replaced"), (2, "replaced"), (3, "ended")]
    assert history[0]["replaced_at"] and history[1]["replaced_at"]
    assert memory_store_sql(
        "SELECT source_uid FROM memory.standing_rules WHERE rule_id = %s ORDER BY version", (rid,)
    ) == [("first",), ("second",), ("third",)]


def test_question_once_and_answer_changes_scope(conversation, memory_store_sql):
    provider, say = conversation
    first = say("unclear", "Always use short reports.")
    assert first["scope"] == "agent" and first["question"].count("?") == 1
    retry = json.loads(provider.handle_tool_call("standing_rule", {"action": "record"}))
    assert retry["version"] == 1 and "question" not in retry
    wider = say("answer", "Everyone.", "change", rule_id=first["rule_id"])
    assert (wider["scope"], wider["version"]) == ("all", 2)
    assert memory_store_sql(
        "SELECT scope, agent_id, source_uid, status FROM memory.standing_rules "
        "WHERE rule_id = %s ORDER BY version", (first["rule_id"],)
    ) == [("agent", "agent:alpha", "unclear", "replaced"), ("all", "*", "answer", "active")]


def test_recorder_can_change_a_named_agents_rule_without_listing_it(conversation, memory_store_sql):
    _, say = conversation
    rule = say("create", "Tell the beta agent to keep reports short.")["rule_id"]
    assert say("list", "What rules do I have?", "list")["rules"] == []
    assert say("change", "Always use two lines.", "change", rule_id=rule)["version"] == 2
    assert say("end", "End that rule.", "end", rule_id=rule)["version"] == 3
    assert memory_store_sql(
        "SELECT agent_id, status, source_uid FROM memory.standing_rules WHERE rule_id = %s ORDER BY version",
        (rule,),
    ) == [("agent:beta", "replaced", "create"), ("agent:beta", "replaced", "change"),
          ("agent:beta", "ended", "end")]
