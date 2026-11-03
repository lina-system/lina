"""lina#20: a turn reaches Honcho from a worker, after the next turn may already have started: lina-memory's own
worker, or the memory manager's mem-sync worker when Honcho holds the slot alone. Who wrote the turn is fixed when
the turn finishes; a person's turn followed directly by a bot relay stays the person's words, however late the
worker runs."""

from __future__ import annotations

import threading

import pytest

from tests.plugins.memory.lina.harness import drain, make_agent, run_turn, write_home

BOT = {"id": "bot:scout", "name": "Scout", "is_bot": True}


@pytest.fixture
def home(tmp_path, monkeypatch, lina_env):
    home = write_home(tmp_path / ".hermes")
    monkeypatch.setenv("HERMES_HOME", str(home))
    return home


def test_a_person_turn_delivered_after_the_next_relay_started_stays_the_persons(home, lina_env):
    from hermes_state import SessionDB

    agent = make_agent("late-worker", SessionDB(home / "state.db"))
    lina = next(p for p in agent._memory_manager.providers if p.name == "lina-memory")
    held = threading.Event()
    lina._submit(lambda: held.wait(60))  # the worker is busy until the relay turn has begun
    try:
        history = run_turn(agent, "person question about the garden #p1", "reply #p1")["messages"]
        history = run_turn(agent, "Scout relays the build status #r1", "reply #r1", history, turn_author=BOT)["messages"]
    finally:
        held.set()
    drain(agent)
    agent.shutdown_memory_provider(history)

    by_text = {m["content"]: m for m in lina_env.written}
    person = by_text["person question about the garden #p1"]
    assert person["peer"] == "1001" and person["metadata"]["lina_route"] == "person_channel"
    assert person["metadata"]["lina_person"] is not None
    relay = by_text["Scout relays the build status #r1"]
    assert relay["peer"] != "1001" and relay["metadata"]["lina_person"] is None
    assert relay["session"] != person["session"]  # the bot's own a2a session, never the person's


def test_honcho_alone_keeps_a_person_turn_the_persons_when_the_memory_worker_runs_late(tmp_path, monkeypatch, lina_env):
    from hermes_state import SessionDB

    home = write_home(tmp_path / ".hermes", memory_provider="honcho")
    monkeypatch.setenv("HERMES_HOME", str(home))
    agent = make_agent("late-mem-sync", SessionDB(home / "state.db"))
    assert [p.name for p in agent._memory_manager.providers if p.name != "builtin"] == ["honcho"]
    held = threading.Event()
    agent._memory_manager._submit_background(lambda: held.wait(60))  # mem-sync is busy until the relay began
    try:
        history = run_turn(agent, "person question about the garden #p2", "reply #p2")["messages"]
        history = run_turn(agent, "Scout relays the build status #r2", "reply #r2", history, turn_author=BOT)["messages"]
    finally:
        held.set()
    drain(agent)
    agent.shutdown_memory_provider(history)

    by_text = {m["content"]: m for m in lina_env.written}
    person = by_text["person question about the garden #p2"]
    relay = by_text["Scout relays the build status #r2"]
    assert person["peer"] == "1001"
    assert relay["peer"] != "1001" and relay["session"] != person["session"]
