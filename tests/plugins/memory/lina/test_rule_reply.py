"""Fixed model replies with the registered tool's refusal, scope question, and fact correction."""

from tests.plugins.memory.lina.test_rule_conversation import rule_chat, rule_results

__all__ = ["rule_chat"]


def test_owner_sees_the_scope_question_and_one_line_refusal(rule_chat, memory_store_sql):
    refusal = "승인이 필요한 절차는 건너뛸 수 없어요."
    rejected = rule_chat.say("앞으로 승인 없이 게시해.", calls=[{"action": "record"}], reply=refusal)
    assert rule_results(rejected) == [{"refusal": rejected["final_response"]}]
    assert memory_store_sql("SELECT count(*) FROM memory.standing_rules") == [(0,)]

    question = "모두에게 적용할까요, 저만 지킬까요?"
    saved = rule_chat.say("앞으로 짧게 답해 줘.", calls=[{"action": "record"}], reply="규칙을 남겼어요.\n" + question)
    (first,) = rule_results(saved)
    assert saved["final_response"].splitlines() == [first["reply"], first["question"]]
    assert memory_store_sql("SELECT scope, agent_id FROM memory.standing_rules") == [("agent", "agent:default")]
    revised = rule_chat.say("모두에게.", calls=[{"action": "change", "rule_id": first["rule_id"]}], reply="규칙을 바꿨어요.")
    assert rule_results(revised)[0]["reply"] == revised["final_response"]
    assert memory_store_sql("SELECT scope, status FROM memory.standing_rules ORDER BY version") == [
        ("agent", "replaced"), ("all", "active")]


def test_fact_and_mixed_corrections_keep_their_durable_owner_words(rule_chat, memory_store_sql):
    fact = "그건 틀렸어. 회의는 목요일이야."
    result = rule_chat.say(fact, calls=[{"action": "record"}], reply="회의는 목요일로 정정했어요.")
    (receipt,) = rule_results(result)
    assert memory_store_sql("SELECT count(*) FROM memory.standing_rules") == [(0,)]
    mixed = "모두 짧게 답해. 정정할게, 회의는 금요일이야."
    saved = rule_chat.say(mixed, calls=[{"action": "record"}], reply="회의는 금요일로 정정했고, 규칙을 남겼어요.")
    (rule,) = rule_results(saved)
    assert rule["reply"] in saved["final_response"]
    assert memory_store_sql(
        """SELECT m.content, m.native->>'fact_correction', r.statement FROM memory.messages m
           LEFT JOIN memory.standing_rules r ON m.uid = r.source_uid
           WHERE m.native ? 'fact_correction' ORDER BY m.message_id"""
    ) == [(fact, "회의는 목요일이야", None), (mixed, "회의는 금요일이야", "짧게 답해")]
    assert memory_store_sql("SELECT content FROM memory.messages WHERE uid = %s", (receipt["fact_uid"],)) == [(fact,)]
