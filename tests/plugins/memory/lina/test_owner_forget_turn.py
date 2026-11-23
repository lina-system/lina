"""Exercise owner forget commands through the admitted turn and its loopback provider."""

import pytest

from hermes_cli.heart.locale import text
from tests.plugins.memory.lina import test_cache_order
from tests.plugins.memory.lina.test_taste_cache_order import seed_taste
from tests.plugins.memory.lina.test_turn_taste_routes import _route_agent

home = test_cache_order.home


@pytest.mark.parametrize("command", ["그건 잊어", "워터컬러 취향 잊어줘"], ids=["deictic", "explicit"])
def test_owner_turn_retires_the_taste_without_inference(home, memory_store_sql, command):
    (taste_id,), = memory_store_sql(
        "INSERT INTO memory.tastes (agent_id,target_kind,label,valence,strength,status,origin) "
        "VALUES ('default','method','워터컬러','like',0.7,'active','outcome') RETURNING id"
    )
    with _route_agent(home, "cli") as (agent, requests):
        first = agent.run_conversation("워터컬러 취향으로 그림을 그려요.")
        assert first["completed"] and agent._owner_taste_allowed
        count = len(requests)
        result = agent.run_conversation(command, conversation_history=first["messages"])
        status = memory_store_sql("SELECT status, label, forgotten_by FROM memory.tastes WHERE id=%s", (taste_id,))
        assert (len(requests) - count, status) == (0, [("forgotten", None, "owner")]), result["final_response"]
        assert result["completed"] and result["turn_exit_reason"] == "owner_taste_forget_local_reply"
        assert result["final_response"] == text("forget_done")
        stored = agent._session_db.get_messages_as_conversation(agent.session_id)
        assert [(row["role"], row["content"]) for row in stored[-2:]] == [
            ("user", command), ("assistant", text("forget_done")),
        ]
        follow_up = "please recall the plan for 워터컬러"
        agent.run_conversation(follow_up, conversation_history=stored)
        assert len(requests) == count + 1
        last = requests[-1].body["messages"][-1]
        assert last["role"] == "tool" and test_cache_order.RECALL in last["content"]
        assert "워터컬러" not in last["content"]


@pytest.mark.parametrize("route", ["nonowner", "api", "delegated"])
def test_excluded_turn_keeps_live_taste(home, memory_store_sql, route):
    from gateway.session_api_turn import api_execution

    seed_taste(memory_store_sql, "워터컬러", "active")
    with _route_agent(home, "telegram" if route == "nonowner" else "cli") as (agent, requests):
        if route == "delegated":
            agent._delegate_depth = 1
        token = api_execution.set({"settings": {}}) if route == "api" else None
        try:
            author = {"id": "visitor"} if route == "nonowner" else None
            result = agent.run_conversation("워터컬러 취향 잊어줘", turn_author=author)
        finally:
            if token is not None:
                api_execution.reset(token)
        assert result["completed"] and len(requests) == 1 and not agent._owner_taste_allowed
        assert memory_store_sql("SELECT status FROM memory.tastes WHERE label='워터컬러'") == [("active",)]


def test_frozen_prompt_after_direct_forget(home, memory_store_sql):
    from hermes_cli.heart.forget import forget_from_conversation

    seed_taste(memory_store_sql, "워터컬러", "core")
    with _route_agent(home, "cli") as (agent, requests):
        first = agent.run_conversation("안녕")
        before = requests[-1].body["messages"][0]["content"]
        assert "워터컬러" in before
        retired = forget_from_conversation("워터컬러 취향 잊어줘", owner_verified=True, history=[], target_home=home)
        assert retired and retired.forgotten
        result = agent.run_conversation("워터컬러 기법은?", conversation_history=first["messages"])
        messages = requests[-1].body["messages"]
        assert result["completed"] and before.encode() == messages[0]["content"].encode()
        assert not any("워터컬러" in row["content"] for row in messages if row["role"] == "tool")
