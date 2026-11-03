"""Live source toggles through the real agent memory surfaces (lina#23)."""

import json

import pytest

from tests.plugins.memory.lina.harness import drain, make_agent, run_turn, write_home
from utils import atomic_roundtrip_yaml_update


@pytest.fixture(params=["default", "other"])
def switched_agent(tmp_path, monkeypatch, lina_env, memory_store_sql, request):
    from hermes_state import SessionDB

    root = write_home(tmp_path / "home")
    home = root if request.param == "default" else write_home(root / "profiles" / request.param)
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setenv("LINA_HOME", str(root))
    memory_store_sql(
        "INSERT INTO memory.messages(uid,source,scope,role,content,content_sha256) "
        "VALUES ('synthetic-external','messages','agents','user','orchid EXTERNAL_SENTINEL','digest')")
    db = SessionDB(home / "state.db")
    agent = make_agent("switch-session", db)
    run_turn(agent, "orchid agent conversation", "Recorded.")
    drain(agent)
    yield agent, root
    agent._memory_manager.shutdown_all()
    db.close()


def _call(agent, name, **args):
    return agent._memory_manager.handle_tool_call(name, args, session_id="switch-session")


def test_existing_agent_hides_external_reads_and_restores_identical_refs(switched_agent, memory_store_sql):
    agent, home = switched_agent
    manager = agent._memory_manager
    schemas = manager.get_all_tool_schemas()
    before = json.loads(_call(agent, "memory_search", query="orchid", sources=["agent", "messages"]))
    refs = [hit["ref"] for hit in before["results"]]
    assert "msg:synthetic-external" in refs
    rows = memory_store_sql("SELECT * FROM memory.messages WHERE source='messages'")
    assert "EXTERNAL_SENTINEL" in _call(agent, "memory_recall", query="orchid")

    # When the host switch changes during an existing session.
    atomic_roundtrip_yaml_update(home / "config.yaml", "memory.external_messages.enabled", False)

    # Then all reads hide external rows, but own rows and tool definitions remain.
    off = json.loads(_call(agent, "memory_search", query="orchid", sources=["agent", "messages"]))
    assert off["results"] and {hit["source"] for hit in off["results"]} == {"agent"}
    assert [item["source"] for item in off["skipped"]] == ["messages"]
    assert "error" in json.loads(_call(agent, "memory_open", ref="msg:synthetic-external"))
    assert "EXTERNAL_SENTINEL" not in _call(agent, "memory_recall", query="orchid")
    assert "EXTERNAL_SENTINEL" not in manager.prefetch_all("orchid", session_id="switch-session")
    assert manager.get_all_tool_schemas() == schemas
    assert memory_store_sql("SELECT * FROM memory.messages WHERE source='messages'") == rows

    atomic_roundtrip_yaml_update(home / "config.yaml", "memory.external_messages.enabled", True)
    again = json.loads(_call(agent, "memory_search", query="orchid", sources=["agent", "messages"]))
    assert [hit["ref"] for hit in again["results"]] == refs
    assert json.loads(_call(agent, "memory_open", ref="msg:synthetic-external"))["text"] == "orchid EXTERNAL_SENTINEL"
    assert "EXTERNAL_SENTINEL" in _call(agent, "memory_recall", query="orchid")


def test_new_turn_composition_obeys_live_host_switch(switched_agent):
    agent, home = switched_agent

    # Given external recall really reaches a composed model request while on.
    run_turn(agent, "orchid", "On response.")
    on_request = agent.client.chat.completions.create.call_args.kwargs
    assert "EXTERNAL_SENTINEL" in json.dumps(on_request["messages"])
    drain(agent)
    atomic_roundtrip_yaml_update(home / "config.yaml", "memory.external_messages.enabled", False)

    # When the same agent composes a fresh turn while off.
    run_turn(agent, "orchid", "Off response.")

    # Then new context contains no external recall; the tool surface stays fixed.
    off_request = agent.client.chat.completions.create.call_args.kwargs
    assert "EXTERNAL_SENTINEL" not in json.dumps(off_request["messages"])
    assert off_request.get("tools") == on_request.get("tools")
    drain(agent)
