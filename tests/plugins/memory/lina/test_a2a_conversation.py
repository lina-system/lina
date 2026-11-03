"""lina#190: a bot turn's Honcho session is the a2a session sync_turn writes, and it is a key of that turn's conversation.

The human session stays a key of the same conversation.
"""

from __future__ import annotations

from plugins.memory.honcho.session_peers import sanitize_peer_id
from tests.plugins.memory.lina.harness import drain, make_agent, run_turn, write_home

BOT = {"id": "bot:scout", "name": "Scout", "is_bot": True}


def test_a_bot_turn_registers_its_a2a_session_on_the_conversation(tmp_path, monkeypatch, lina_env, memory_store_sql):
    from hermes_state import SessionDB

    home = write_home(tmp_path / ".hermes")
    monkeypatch.setenv("HERMES_HOME", str(home))
    agent = make_agent("a2a-conv", SessionDB(home / "state.db"))
    run_turn(agent, "Scout relays the build status #a2a", "reply #a2a", turn_author=BOT)
    drain(agent)

    (message,) = [row for row in lina_env.written if row["content"] == "Scout relays the build status #a2a"]
    conversation = message["metadata"]["lina_conversation"]
    lina = next(provider for provider in agent._memory_manager.providers if provider.name == "lina-memory")
    honcho = lina._honcho
    base = honcho.session_key
    # get_or_create stores the sanitized a2a key; that is the session the turn is written to.
    a2a = sanitize_peer_id(honcho._a2a_session_key(BOT, base))
    assert message["session"] == a2a
    workspace = honcho._config.workspace_id
    rows = dict(memory_store_sql(
        "SELECT key, identity_id FROM memory.identity_keys WHERE kind = 'conversation' AND source = 'honcho_session'"))
    assert rows[f"{workspace}:{a2a}"] == conversation
    assert rows[f"{workspace}:{base}"] == conversation
