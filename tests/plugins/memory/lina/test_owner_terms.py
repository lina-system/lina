"""lina#379: ``owner_terms`` through the real agent and lina-memory: a finished turn's owner words are found, the
agent's reply in the same turn never is. All text is synthetic."""

from __future__ import annotations

import json

import pytest

from tests.plugins.memory.lina.harness import drain, make_agent, run_turn, write_home

pytest.importorskip("kiwipiepy", reason="Korean analysis needs the 'korean' extra (kiwipiepy), not installed here")

SESSION = "terms-1"


@pytest.fixture
def home(tmp_path, monkeypatch, lina_env):
    home = write_home(tmp_path / ".hermes")
    monkeypatch.setenv("HERMES_HOME", str(home))
    return home


def test_the_tool_answers_with_the_owners_words_only(home, memory_store_sql):
    from hermes_state import SessionDB
    db = SessionDB(home / "state.db")
    agent = make_agent(SESSION, db)
    try:
        run_turn(agent, "taste slot은 취향 목록이라고 부르자", "알겠어요, taste slot을 취향칸으로 정리할게요.")
        run_turn(agent, "취향 목록에 커피 넣어줘", "취향 목록에 커피를 넣었어요.", db.get_messages_as_conversation(SESSION))
        drain(agent)
        provider = next(p for p in agent._memory_manager.providers if p.name == "lina-memory")
        assert "owner_terms" in {schema["name"] for schema in provider.get_tool_schemas()}
        english = json.loads(provider.handle_tool_call("owner_terms", {"concept": "taste slot"}))
        korean = json.loads(provider.handle_tool_call("owner_terms", {"concept": "취향 목록"}))
        never = json.loads(provider.handle_tool_call("owner_terms", {"concept": "취향칸"}))
        empty = json.loads(provider.handle_tool_call("owner_terms", {"concept": " "}))
        speakers = dict(memory_store_sql("SELECT uid, speaker FROM memory.messages WHERE source = 'agent'"))
    finally:
        agent.shutdown_memory_provider()
        db.close()

    assert [(t["term"], t["count"]) for t in english["terms"]] == [("취향 목록", 1)]
    assert english["use"] == korean["use"] == "취향 목록"
    assert korean["terms"][0]["count"] == 2
    assert {speakers[uid] for t in english["terms"] + korean["terms"] for uid in t["uids"]} == {"owner"}
    assert never["terms"] == [] and never["hint"] == "describe it in plain words; do not coin a name"
    assert "error" in empty
