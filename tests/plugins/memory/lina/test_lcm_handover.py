"""lina#20: who hands a finished turn to LCM-X is decided per agent, never per home. LCM-X's ``post_llm_call`` hook
stays registered; for an agent carrying lina-memory the hook hands the turn to lina-memory, and every other agent
(a ``skip_memory`` child, a background review, the curator) keeps LCM-X's own ingest. Either way a turn is one
ingest, and a failing local engine never costs the Honcho delivery."""

from __future__ import annotations

import logging
from types import SimpleNamespace
from unittest.mock import patch

import pytest

from tests.plugins.memory.lina.harness import _response, drain, lcm_rows, make_agent, run_turn, write_home


@pytest.fixture
def home(tmp_path, monkeypatch, lina_env):
    home = write_home(tmp_path / ".hermes")
    monkeypatch.setenv("HERMES_HOME", str(home))
    return home


def _counting_ingest(agent):
    engine_cls = type(agent.context_compressor)
    calls, original = [], engine_cls.ingest

    def counting(self, messages):
        calls.append(self._session_id)
        return original(self, messages)
    return patch.object(engine_cls, "ingest", counting), calls


def test_a_skip_memory_child_is_ingested_once_by_lcm_x_beside_a_lina_memory_agent(home, lina_env):
    from hermes_state import SessionDB

    db = SessionDB(home / "state.db")
    parent = make_agent("parent-1", db)
    child = make_agent("child-1", db, skip_memory=True)
    assert child._memory_manager is None  # the child never builds lina-memory
    counting, calls = _counting_ingest(parent)
    with counting:
        run_turn(parent, "person question for the parent #pa", "parent reply #pa")
        run_turn(child, "delegated research task #ch", "child findings #ch")
        drain(parent)

    assert sorted(calls) == ["child-1", "parent-1"]  # one ingest per agent's turn
    by_session = {}
    for session_id, _conversation, role, content in lcm_rows(home):
        by_session.setdefault(session_id, []).append((role, content))
    assert by_session["child-1"] == [("user", "delegated research task #ch"), ("assistant", "child findings #ch")]
    assert ("user", "person question for the parent #pa") in by_session["parent-1"]


def test_an_interrupted_turn_of_a_lina_memory_agent_is_ingested_once(home, lina_env):
    from hermes_state import SessionDB

    agent = make_agent("stopped-1", SessionDB(home / "state.db"))
    history = run_turn(agent, "first question #s1", "first reply #s1")["messages"]
    counting, calls = _counting_ingest(agent)

    def stop_during_tool_round(*_args, **_kwargs):
        agent.interrupt("user pressed stop")
        call = SimpleNamespace(id="call_s2", type="function",
                               function=SimpleNamespace(name="lookup_note", arguments='{"q": "s2"}'))
        return _response(None, [call])

    agent.client.chat.completions.create.side_effect = stop_during_tool_round
    with counting:
        stopped = agent.run_conversation("second question #s2", conversation_history=history)
        # LCM-X keeps a stop that leaves an open user tail for the next completed turn (engine.on_turn_complete).
        run_turn(agent, "third question #s3", "third reply #s3", stopped["messages"])
        drain(agent)

    assert stopped.get("interrupted") is True
    assert calls == ["stopped-1"]  # the stopped turn and the next one: one ingest, no second copy
    # The host merges the stopped turn's open user row into the next one; LCM-X stores that text once.
    users = [content for _s, _c, role, content in lcm_rows(home) if role == "user"]
    assert sum("#s2" in content for content in users) == 1
    assert sum("#s3" in content for content in users) == 1


def test_an_lcm_failure_still_delivers_the_turn_to_honcho(home, lina_env, caplog):
    from hermes_state import SessionDB

    agent = make_agent("broken-engine", SessionDB(home / "state.db"))

    def broken(self, messages):
        raise RuntimeError("lcm.db is read-only")

    with patch.object(type(agent.context_compressor), "ingest", broken), caplog.at_level(logging.WARNING):
        history = run_turn(agent, "person question while LCM fails #lf", "reply #lf")["messages"]
        drain(agent)
    agent.shutdown_memory_provider(history)

    assert [m["content"] for m in lina_env.written] == ["person question while LCM fails #lf", "reply #lf"]
    failure = [r for r in caplog.records if "could not hand a turn" in r.getMessage()]
    assert len(failure) == 1 and failure[0].exc_info is not None
