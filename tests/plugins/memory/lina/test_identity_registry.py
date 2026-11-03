"""lina#20: the identity registry (migration 0006) maps people, agents and conversations to stable ids.

A home that already has Honcho peer names and an LCM conversation keeps them: after lina-memory takes the memory
slot its turns go to the same Honcho peer and session and the same LCM conversation, the registry records those
names as source keys of one person and one conversation, and the ids come out the same when the registry is
built again from nothing.
"""

from __future__ import annotations

import json

from hermes_cli.store_migrations import MIGRATIONS_DIR
from tests.plugins.memory.lina.harness import HONCHO_CONFIG, drain, lcm_rows, make_agent, run_turn, write_home

PINNED = {**HONCHO_CONFIG, "pinPeerName": True}


def _identities(sql) -> dict:
    return {(kind, source, key): identity for kind, source, key, identity in sql(
        "SELECT kind, source, key, identity_id FROM memory.identity_keys")}


def test_the_identity_migration_runs_again_without_change(memory_store_sql):
    body = (MIGRATIONS_DIR / "0006_identity.sql").read_text(encoding="utf-8")
    before = memory_store_sql("SELECT table_name FROM information_schema.tables WHERE table_schema = 'memory' "
                              "ORDER BY table_name")

    memory_store_sql(body)
    memory_store_sql(body)

    assert ("identities",) in before and ("identity_keys",) in before
    assert memory_store_sql("SELECT table_name FROM information_schema.tables WHERE table_schema = 'memory' "
                            "ORDER BY table_name") == before


def test_existing_honcho_peers_and_lcm_conversation_resolve_to_the_same_ids(tmp_path, monkeypatch, lina_env,
                                                                            memory_store_sql):
    from hermes_state import SessionDB

    home = write_home(tmp_path / ".hermes", memory_provider="honcho", honcho=PINNED)
    monkeypatch.setenv("HERMES_HOME", str(home))
    db = SessionDB(home / "state.db")
    before = make_agent("before-20", db)  # a home from before lina#20: Honcho in the slot, LCM-X's own hook on
    run_turn(before, "first message from before the registry #b1", "answer #b1")
    drain(before)
    before.shutdown_memory_provider()
    old_peers = {(m["session"], m["peer"]) for m in lina_env.written}
    (old_conversation,) = {conversation for _, conversation, _, _ in lcm_rows(home)}
    assert memory_store_sql("SELECT count(*) FROM memory.identity_keys") == [(0,)]

    write_home(home, honcho=PINNED)  # lina-memory takes the slot
    lina_env.written.clear()
    after = make_agent("after-20", db)
    run_turn(after, "first message after the registry #a1", "answer #a1")
    drain(after)
    after.shutdown_memory_provider()

    assert {(m["session"], m["peer"]) for m in lina_env.written} == old_peers
    assert {conversation for _, conversation, _, _ in lcm_rows(home)} == {old_conversation}
    registry = _identities(memory_store_sql)
    (person,) = {m["metadata"]["lina_person"] for m in lina_env.written}
    (conversation,) = {m["metadata"]["lina_conversation"] for m in lina_env.written}
    session, peer = next((s, p) for s, p in old_peers if p == "alice")
    assert registry[("person", "honcho_peer", f"lina-test:{peer}")] == person
    assert registry[("conversation", "honcho_session", f"lina-test:{session}")] == conversation
    assert registry[("conversation", "lcm_conversation", f"default:{old_conversation}")] == conversation

    memory_store_sql("TRUNCATE memory.identity_keys, memory.identities")
    lina_env.written.clear()
    again = make_agent("after-20b", db)
    run_turn(again, "a message once the registry is rebuilt #a2", "answer #a2")
    drain(again)
    again.shutdown_memory_provider()

    assert {m["metadata"]["lina_person"] for m in lina_env.written} == {person}
    assert {m["metadata"]["lina_conversation"] for m in lina_env.written} == {conversation}
    assert _identities(memory_store_sql) == registry
    assert json.loads((home / "honcho.json").read_text())["peerName"] == "alice"
