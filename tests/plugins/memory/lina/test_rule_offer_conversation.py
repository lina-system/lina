"""An ordinary reply displays an offer, then the owner's next turn decides its fate."""

from types import SimpleNamespace

import pytest
from psycopg.types.json import Jsonb

from gateway import inbound_notes
from tests.plugins.memory.lina.test_rule_conversation import rule_chat, rule_results

__all__ = ["rule_chat"]


@pytest.mark.parametrize("answer", ["네.", None])
def test_shared_question_in_an_ordinary_reply(rule_chat, memory_store_sql, answer):
    def record(words, reply):
        return rule_results(rule_chat.say(words, calls=[{"action": "record"}], reply=reply))[0]

    peer = record("Tell the beta agent to use two lines.", "베타의 규칙을 남겼어요.")
    question = "「use two lines」을 모두의 규칙으로 정할까요?"
    local = record("You should use two lines.", "다음 보고부터 두 줄로 쓸게요.\n" + question)
    assert local["question"] == question
    assert memory_store_sql(
        "SELECT role, content FROM memory.messages WHERE role = 'assistant' AND content LIKE %s",
        ("%\n" + question,),
    ) == [("assistant", "다음 보고부터 두 줄로 쓸게요.\n" + question)]
    assert memory_store_sql("SELECT scope, status FROM memory.standing_rules") == [("agent", "active")] * 2

    if answer is None:
        rule_chat.say("오늘 일정을 알려 줘.", reply="오늘은 일정이 없어요.")
        assert memory_store_sql("SELECT count(*) FROM memory.rule_offers") == [(1,)]
        assert memory_store_sql("SELECT scope, status FROM memory.standing_rules") == [("agent", "active")] * 2
        return
    result = rule_chat.say(answer, calls=[{"action": "answer_offer", "offer_id": local["offer_id"]}],
                           reply="모두의 규칙으로 정했어요.")
    (accepted,) = rule_results(result)
    uid = rule_chat.text_uid(result["messages"][result["current_turn_user_idx"]]["_row_id"])
    assert accepted["status"] == "recorded"
    assert memory_store_sql("SELECT source_uid, statement FROM memory.standing_rules WHERE scope = 'all'") == [
        (uid, "use two lines")]
    assert set(memory_store_sql(
        "SELECT rule_id::text, shared_rule_id::text FROM memory.standing_rules WHERE status = 'replaced'"
    )) == {(item["rule_id"], accepted["rule_id"]) for item in (peer, local)}


@pytest.mark.parametrize("quoted", [False, True], ids=["owner", "reply-quote"])
def test_pattern_question_uses_the_next_owner_sentence_as_its_source(rule_chat, memory_store_sql, quoted):
    first = rule_chat.say("두 줄짜리 보고가 읽기 쉬웠어.", reply="알겠어요.")
    basis = rule_chat.text_uid(first["messages"][first["current_turn_user_idx"]]["_row_id"])
    memory_store_sql(
        """INSERT INTO memory.messages (uid, source, agent_id, native, content, content_sha256)
           VALUES ('journal', 'heart', 'default', %s, 'Use two lines', 'fixture')""",
        (Jsonb({"note": "Use two lines", "evidence": [basis]}),),
    )
    question = "「Use two lines」을 제 규칙으로 정할까요?"
    offered = rule_chat.say("어떤 보고 형식이 좋을까?", calls=[{"action": "pattern_offer"}],
                            reply="두 줄 형식을 써 볼 수 있어요.\n" + question)
    (offer,) = rule_results(offered)
    assert offer["question"] == question
    assert memory_store_sql("SELECT count(*) FROM memory.standing_rules") == [(0,)]
    words = "네."
    turn_kwargs = {}
    if quoted:
        event = SimpleNamespace()
        notes = inbound_notes.start(event, words)
        words = notes.step("reply_quote", words, '이전 답변: "네."\n\n네.', quoted_speaker="agent", quoted_ref="41")
        turn_kwargs = {"persist_user_message": inbound_notes.persist_text(event, words),
                       "persist_user_display_metadata": inbound_notes.display_metadata(event)}
    result = rule_chat.say(words, calls=[{"action": "answer_offer", "offer_id": offer["offer_id"]}],
                           reply="확인했어요.", **turn_kwargs)
    if quoted:
        assert rule_results(result) == [{"status": "ignored"}]
        assert memory_store_sql("SELECT count(*) FROM memory.standing_rules") == [(0,)]
        return
    assert rule_results(result)[0]["status"] == "recorded"
    uid = rule_chat.text_uid(result["messages"][result["current_turn_user_idx"]]["_row_id"])
    assert memory_store_sql("SELECT source_uid, scope, agent_id, cites FROM memory.standing_rules") == [
        (uid, "agent", "agent:default", ["journal", basis])]
