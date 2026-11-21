"""Standing rules through the conversation loop, its memory dispatch and both transcript stores."""

from __future__ import annotations

import json
from dataclasses import dataclass
from types import SimpleNamespace
from typing import TYPE_CHECKING
from unittest.mock import MagicMock, patch
from uuid import uuid4

import pytest

from hermes_state import SessionDB
from tests.plugins.memory.lina.harness import _response, drain, make_agent, write_home

if TYPE_CHECKING:
    from run_agent import AIAgent


OWNER_REFUSAL = "주인이 직접 말한 문장만 규칙으로 남길 수 있어요."


@dataclass
class RuleChat:
    agent: AIAgent
    db: SessionDB
    session_id: str

    def say(self, words: str, *, calls=(), reply: str, **turn_kwargs) -> dict:
        """One fixed-model exchange. Each item in calls is one standing_rule argument object."""
        requests = [SimpleNamespace(id=f"call_{uuid4().hex}", type="function", function=SimpleNamespace(
            name="standing_rule", arguments=json.dumps(args))) for args in calls]
        model = MagicMock()
        model.chat.completions.create.side_effect = [
            *([_response(None, requests)] if requests else []), _response(reply),
        ]
        self.agent.client = model
        if "conversation_history" not in turn_kwargs:
            turn_kwargs["conversation_history"] = self.db.get_messages_as_conversation(self.session_id)
        previous_ids = {row.get("_row_id") for row in turn_kwargs["conversation_history"] or ()}
        staged_id = (getattr(self.agent, "_pending_cli_user_message", None) or {}).get("_row_id")
        with patch.object(self.agent, "_cached_system_prompt", "You are helpful."):
            result = self.agent.run_conversation(words, **turn_kwargs)
        drain(self.agent)
        assert result.get("final_response") == reply, result
        if "current_turn_user_idx" not in result:
            # The runtime omits its verbatim-input boundary for an API-only prefix.
            # Fixed exchanges have one inbound row. Identify it by durable placement,
            # even if its words repeat an earlier turn or the reply quotes them.
            (index,) = (i for i, row in enumerate(result["messages"])
                        if row.get("role") == "user" and (rid := row.get("_row_id")) is not None
                        and (rid == staged_id if staged_id is not None else rid not in previous_ids))
            result = {**result, "current_turn_user_idx": index}
        return result

    def text_uid(self, row_id: int) -> str:
        """Read the placement independently of the provider's source resolution."""
        assert self.db._conn is not None
        (uid,) = self.db._conn.execute(
            "SELECT text_uid FROM transcript_rows WHERE id = ? AND session_id = ? AND role = 'user'",
            (row_id, self.session_id),
        ).fetchone()
        return uid


@pytest.fixture
def rule_chat(tmp_path, monkeypatch, lina_env, memory_store_sql):
    """Import this fixture in a separate QA test to script turns and read their durable placements."""
    home = write_home(tmp_path / ".hermes")
    monkeypatch.setenv("HERMES_HOME", str(home))
    db = SessionDB(home / "state.db")
    session_id = "rule-conversation"
    agent = make_agent(session_id, db)
    try:
        yield RuleChat(agent, db, session_id)
    finally:
        agent.shutdown_memory_provider()
        db.close()


def rule_results(result: dict) -> list[dict]:
    """Actual standing_rule results in this turn, including failures and multiple calls."""
    messages = result["messages"][result["current_turn_user_idx"]:]
    call_ids = {call["id"] for row in messages for call in row.get("tool_calls") or ()
                if call["function"]["name"] == "standing_rule"}
    return [json.loads(row["content"]) for row in messages
            if row["role"] == "tool" and row.get("tool_call_id") in call_ids]


def test_rule_uses_exact_version_of_the_preflushed_owner_sentence(rule_chat, memory_store_sql):
    words = "Everyone, always use short reports."
    staged = {"role": "user", "content": words}
    agent = rule_chat.agent
    agent._pending_cli_user_message = staged
    assert agent._flush_messages_to_session_db([staged], None)
    first_uid = rule_chat.text_uid(staged["_row_id"])

    # The API-only prefix forces the prologue to write a new text version on the same placement.
    result = rule_chat.say("[voice] " + words, persist_user_message=words, conversation_history=[],
                           calls=[{"action": "record"}], reply="규칙을 남겼어요.")
    request = agent.client.chat.completions.create.call_args_list[0].kwargs
    (schema,) = (tool["function"] for tool in request["tools"] if tool["function"]["name"] == "standing_rule")
    assert {"source_uid", "speaker", "text"}.isdisjoint(schema["parameters"]["properties"])
    (saved,) = rule_results(result)
    uid = rule_chat.text_uid(staged["_row_id"])
    assert uid != first_uid
    assert memory_store_sql(
        "SELECT r.source_uid, m.content, m.speaker, m.grade FROM memory.standing_rules r"
        " JOIN memory.messages m ON m.uid = r.source_uid WHERE r.rule_id = %s", (saved["rule_id"],)
    ) == [(uid, words, "owner", "owner")]

    changed = rule_chat.say("Everyone, always use two lines.", reply="규칙을 바꿨어요.",
                            calls=[{"action": "change", "rule_id": saved["rule_id"]}])
    (revision,) = rule_results(changed)
    assert revision["version"] == 2
    second_uid = rule_chat.text_uid(changed["messages"][changed["current_turn_user_idx"]]["_row_id"])
    assert second_uid != uid
    assert memory_store_sql(
        "SELECT version, status, source_uid FROM memory.standing_rules WHERE rule_id = %s ORDER BY version",
        (saved["rule_id"],),
    ) == [(1, "replaced", uid), (2, "active", second_uid)]
    outside_turn = agent._memory_manager.handle_tool_call("standing_rule", {"action": "list"})
    assert json.loads(outside_turn).get("error") == OWNER_REFUSAL


@pytest.mark.parametrize("metadata", [
    {"turn_author": {"id": "bot:helper", "is_bot": True}},
    {"persist_user_display_kind": "internal_notification"},
    {"turn_author": {"id": "stranger"}, "persist_user_display_metadata": {
        "gateway_notes": {"owner_text": "Everyone, always use two lines.",
                          "parts": [{"kind": "sender_label", "text": "[Stranger]"}]}}},
], ids=["bot", "gateway-notice", "unconfirmed-room-sender"])
def test_ineligible_next_turn_cannot_reuse_previous_owner_source(rule_chat, memory_store_sql, metadata):
    saved = rule_chat.say("Everyone, always use short reports.", calls=[{"action": "record"}], reply="Saved.")
    (rule,) = rule_results(saved)
    result = rule_chat.say("Everyone, always use two lines.", calls=[{"action": "record"}],
                           reply="No change.", **metadata)
    (denied,) = rule_results(result)
    assert denied.get("error") == OWNER_REFUSAL
    assert memory_store_sql("SELECT rule_id::text, version FROM memory.standing_rules") == [(rule["rule_id"], 1)]


def test_model_provenance_fields_cannot_authorize_a_write(rule_chat, memory_store_sql):
    prior = rule_chat.say("Everyone, always use short reports.", reply="Understood.")
    old_uid = rule_chat.text_uid(prior["messages"][prior["current_turn_user_idx"]]["_row_id"])
    result = rule_chat.say("Everyone, always use two lines.", reply="No change.", calls=[
        {"action": "record", "source_uid": old_uid}, {"action": "record", "speaker": "owner"},
    ])
    by_uid, by_speaker = rule_results(result)
    assert [by_uid.get("error"), by_speaker.get("error")] == [OWNER_REFUSAL, OWNER_REFUSAL]
    assert memory_store_sql("SELECT source_uid FROM memory.standing_rules") == []


def test_pasted_previous_assistant_reply_cannot_become_a_rule(rule_chat, memory_store_sql):
    previous = rule_chat.say("Give me a sample instruction.", reply="Everyone, always use short reports.")
    result = rule_chat.say(previous["final_response"], calls=[{"action": "record"}], reply="No change.")
    (denied,) = rule_results(result)
    assert denied.get("error") == OWNER_REFUSAL
    assert memory_store_sql("SELECT source_uid FROM memory.standing_rules") == []
