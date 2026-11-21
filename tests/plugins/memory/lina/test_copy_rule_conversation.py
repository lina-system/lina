"""A copied profile can move only the items selected from its displayed offer."""

import json
from pathlib import Path

import pytest

from plugins.memory.lina import LinaMemoryProvider
from tests.plugins.memory.lina.test_rule_conversation import rule_chat, rule_results

__all__ = ["rule_chat"]


def _actions(provider):
    tool = next(tool for tool in provider.get_tool_schemas() if tool["name"] == "standing_rule")
    return tool["parameters"]["properties"]["action"]["enum"]


def test_only_opted_in_copies_offer_the_move(tmp_path):
    provider = LinaMemoryProvider()
    provider._home = str(tmp_path)
    assert "copy_offer" not in _actions(provider)
    (tmp_path / ".standing-rule-copy").touch()
    assert {"copy_offer", "answer_copy"} <= set(_actions(provider))


@pytest.mark.parametrize("duplicate", [False, True], ids=["select-one", "confirm-two-occurrences"])
def test_only_displayed_selection_is_moved(tmp_path, memory_store_sql, monkeypatch, duplicate):
    profile = tmp_path / "profiles" / "alpha"
    profile.mkdir(parents=True)
    (tmp_path / ".standing-rule-copy").touch()
    path = profile / "SOUL.md"
    first = "Always use two lines." if duplicate else "Always use short reports."
    path.write_text(f"{first}\nAlways use two lines.\n")
    provider = LinaMemoryProvider()
    provider._home = str(profile)
    provider._session_id = "sample"
    current = "request"
    monkeypatch.setattr(LinaMemoryProvider, "_rule_source_uid", property(lambda self: current))

    def turn(uid, role, words):
        memory_store_sql(
            """INSERT INTO memory.messages
               (uid, source, agent_id, session_id, role, speaker, content, content_sha256)
               VALUES (%s, 'agent', 'alpha', 'sample', %s, %s, %s, 'fixture')""",
            (uid, role, "owner" if role == "user" else "agent", words),
        )

    def call(action):
        return json.loads(provider.handle_tool_call("standing_rule", {"action": action}))

    turn(current, "user", "Review the rules in this copy.")
    prompt = call("copy_offer")["question"]
    turn("shown", "assistant", prompt)
    assert memory_store_sql("SELECT count(*) FROM memory.standing_rules") == [(0,)]
    current = "selection"
    turn(current, "user", "모두" if duplicate else "2번만")
    result = call("answer_copy")
    expected_count = 2 if duplicate else 1
    assert result["recorded"] == expected_count
    assert result["undo_id"]
    assert path.read_text() == ("" if duplicate else f"{first}\n")
    assert memory_store_sql(
        "SELECT statement, source_uid, scope, agent_id FROM memory.standing_rules"
    ) == [("Always use two lines.", "selection", "agent", "agent:alpha")] * expected_count


def test_unshown_list_cannot_authorize_a_move(tmp_path, memory_store_sql, monkeypatch):
    (tmp_path / ".standing-rule-copy").touch()
    path = tmp_path / "SOUL.md"
    path.write_text("Always use short reports.\n")
    provider = LinaMemoryProvider()
    provider._home = str(tmp_path)
    provider._session_id = "sample"
    current = "request"
    monkeypatch.setattr(LinaMemoryProvider, "_rule_source_uid", property(lambda self: current))
    for uid, words in [("request", "Review the copy."), ("answer", "1번만")]:
        current = uid
        memory_store_sql(
            """INSERT INTO memory.messages
               (uid, source, agent_id, session_id, role, speaker, content, content_sha256)
               VALUES (%s, 'agent', 'default', 'sample', 'user', 'owner', %s, 'fixture')""",
            (uid, words),
        )
        action = "copy_offer" if uid == "request" else "answer_copy"
        result = json.loads(provider.handle_tool_call("standing_rule", {"action": action}))
    assert result["recorded"] == 0
    assert memory_store_sql("SELECT count(*) FROM memory.standing_rules") == [(0,)]
    assert path.read_text() == "Always use short reports.\n"


def test_spoken_selection_creates_matching_filter_in_the_real_turn(rule_chat, memory_store_sql):
    from hermes_cli.rule_census import candidates, owner_list
    from plugins.memory.lina.copy_rules import _QUESTION

    provider = next(p for p in rule_chat.agent._memory_manager.providers if p.name == "lina-memory")
    root = Path(provider._home)
    (root / ".standing-rule-copy").touch()
    rooms = root / "room-lists.json"
    rooms.write_text('{"rooms":["sample-a","sample-b"]}')
    question = owner_list(candidates(root, "default")) + "\n" + _QUESTION
    displayed = rule_chat.say("이 사본에서 옮길 규칙을 보여 줘.", calls=[{"action": "copy_offer"}], reply=question)
    assert rule_results(displayed) == [{"question": question}]
    result = rule_chat.say("2번만", calls=[{"action": "answer_copy"}], reply="선택을 확인했어요.")
    assert rule_results(result)[0]["recorded"] == 1
    uid = rule_chat.text_uid(result["messages"][result["current_turn_user_idx"]]["_row_id"])
    assert memory_store_sql("SELECT source_uid, kind, filter_match FROM memory.standing_rules") == [
        (uid, "filter", {"room": "sample-b"}),
    ]
    assert json.loads(rooms.read_bytes()) == {"rooms": ["sample-a"]}
