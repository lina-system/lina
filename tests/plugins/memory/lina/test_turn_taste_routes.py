"""lina#359: owner-only taste context on the actual AIAgent model request."""

from __future__ import annotations

from contextlib import contextmanager
from pathlib import Path
from types import SimpleNamespace

import pytest

from agent.memory_recall_tool import RECALL_TOOL_NAME
from gateway import inbound_notes
from plugins.memory.lina.identity import OWNER_HANDLE, IdentityRegistry
from tests.plugins.memory.lina import test_cache_order
from tests.plugins.memory.lina.test_cache_order import (
    RECALL,
    _configure,
    recording_model,
)

home = test_cache_order.home

ACTIVE = "cedar beam"
CORE = "frost canopy"
ASK = f"Please draft a detailed plan using a {ACTIVE}."
OWNER_ID = "test-owner"


@pytest.fixture
def tastes(memory_store_sql):
    memory_store_sql(
        """
        INSERT INTO memory.tastes
            (agent_id, target_kind, label, valence, strength, status, origin, first_seen,
             protected_passed, protected_at)
        VALUES ('default', 'method', %s, 'like', 0.7, 'active', 'outcome',
                DATE '2026-11-01', true, TIMESTAMPTZ '2026-11-01 09:00+00'),
               ('default', 'method', %s, 'like', 0.7, 'core', 'outcome',
                DATE '2026-11-01', true, TIMESTAMPTZ '2026-11-01 09:00+00'),
               ('other-profile', 'method', %s, 'dislike', 0.8, 'active', 'outcome',
                DATE '2026-11-01', true, TIMESTAMPTZ '2026-11-01 09:00+00')
        """,
        (ACTIVE, CORE, ACTIVE),
    )
    registry = IdentityRegistry()
    try:
        registry.link(
            ("gateway_user", f"telegram:{OWNER_ID}"), registry.person_of(OWNER_HANDLE),
            status="confirmed", evidence="synthetic test confirmation",
        )
    finally:
        registry.close()


@contextmanager
def _route_agent(
    home: Path, platform: str, *, append_recall: bool = False,
    skip_memory: bool = False, gate_memory_tool: bool = False,
):
    from hermes_state import SessionDB
    from run_agent import AIAgent

    with recording_model() as server:
        server.url = f"http://127.0.0.1:{server.server_port}/v1"
        _configure(home, server.url, "  auto_recall_append: true\n" if append_recall else "")
        db = SessionDB(db_path=home / "state.db")
        gateway_identity = (
            {"user_id": OWNER_ID, "gateway_session_key": "agent:main:telegram:dm:test-owner"}
            if platform == "telegram" else {}
        )
        agent = AIAgent(
            api_key="loopback", base_url=server.url, provider="custom", model="m",
            quiet_mode=True, skip_context_files=True, skip_background_review=True,
            session_id="taste-routes", platform=platform, session_db=db, skip_memory=skip_memory,
            enabled_toolsets=["skills", "memory", "todo"],
            disabled_toolsets=["memory"] if gate_memory_tool else None, **gateway_identity,
        )
        server.agent = agent
        try:
            yield agent, server.requests
        finally:
            agent.close()
            db.close()


def _run(agent, requests, ask: str = ASK, **kwargs):
    before = len(requests)
    result = agent.run_conversation(ask, **kwargs)
    assert result["completed"] and len(requests) == before + 1, result
    return result, requests[-1].body["messages"]


def _assert_taste_tail(messages, *, ordinary_recall: bool) -> None:
    user, assistant, tool = messages[-3:]
    assert [user["role"], assistant["role"], tool["role"]] == ["user", "assistant", "tool"]
    call, = assistant["tool_calls"]
    assert call["function"]["name"] == RECALL_TOOL_NAME
    assert tool["tool_call_id"] == call["id"]
    assert f"like: {ACTIVE}" in tool["content"]
    assert f"dislike: {ACTIVE}" not in tool["content"]
    assert (RECALL in tool["content"]) is ordinary_recall
    assert not any(f"like: {ACTIVE}" in str(row.get("content")) for row in messages[:-1])


@pytest.mark.parametrize("platform,shared", [("cli", False), ("telegram", False), ("telegram", True)])
def test_admitted_owner_gets_only_own_matching_tastes_in_the_final_recall_result(home, tastes, platform, shared):
    author = {"id": OWNER_ID, "name": "Synthetic sender"} if platform == "telegram" else None
    kwargs = {"turn_author": author}
    ask = ASK
    if shared:
        event = SimpleNamespace()
        notes = inbound_notes.start(event, ASK)
        ask = notes.step("sender_label", ASK, f"[Synthetic sender]\n\n{ASK}")
        kwargs["persist_user_message"] = inbound_notes.persist_text(event, ask)
        kwargs["persist_user_display_metadata"] = inbound_notes.display_metadata(event)
    with _route_agent(home, platform) as (agent, requests):
        _, messages = _run(agent, requests, ask, **kwargs)
    _assert_taste_tail(messages, ordinary_recall=False)


@pytest.mark.parametrize(
    "platform,author,display_kind,origin",
    [
        pytest.param("telegram", {"id": "other", "name": "Synthetic sender"}, None, None, id="nonowner-dm"),
        pytest.param("telegram", {"name": "Synthetic sender"}, None, None, id="unknown-dm"),
        pytest.param("telegram", None, None, None, id="missing-dm-sender"),
        pytest.param("cli", {"id": "bot:test-relay", "is_bot": True}, None, None, id="cli-relay"),
        pytest.param("cli", {"is_bot": True}, None, None, id="unidentified-cli-relay"),
        pytest.param("api_server", {"id": OWNER_ID}, None, None, id="api-attribution"),
        pytest.param("cron", None, None, None, id="cron"),
        pytest.param("subagent", None, None, None, id="worker"),
        pytest.param("cli", None, "async_delegation_complete", None, id="worker-notice"),
        pytest.param("cli", None, "process_complete", None, id="background-notice"),
        pytest.param("cli", None, None, "background_review", id="background-fork"),
    ],
)
def test_excluded_routes_receive_no_taste_context(home, tastes, platform, author, display_kind, origin):
    with _route_agent(home, platform) as (agent, requests):
        if origin is not None:
            agent._turn_origin = origin
        _, messages = _run(agent, requests, turn_author=author, persist_user_display_kind=display_kind)
    assert not any(row.get("role") == "tool" for row in messages)
    assert not any(f"like: {CORE}" in str(row.get("content")) for row in messages)


@pytest.mark.parametrize("append_recall", [False, True])
def test_picks_preserve_the_configured_transport_for_ordinary_recall(home, tastes, append_recall):
    with _route_agent(home, "cli", append_recall=append_recall) as (agent, requests):
        _, messages = _run(agent, requests, f"Please recall the plan for the {ACTIVE}.")
    _assert_taste_tail(messages, ordinary_recall=not append_recall)
    assert (RECALL in messages[-3]["content"]) is append_recall


@pytest.mark.parametrize("skip_memory,ask", [(False, "Please explain a different plan."), (True, ASK)])
def test_turn_without_pick_or_memory_manager_emits_no_recall_round(home, tastes, skip_memory, ask):
    with _route_agent(home, "cli", skip_memory=skip_memory) as (agent, requests):
        _, messages = _run(agent, requests, ask)
    assert messages[-1]["role"] == "user"


@pytest.mark.parametrize("gate_memory_tool", [False, True])
def test_unavailable_tastes_leave_ordinary_recall_on_its_existing_path(home, tastes, monkeypatch, gate_memory_tool):
    from hermes_cli.heart import taste_context
    from hermes_state_store import StoreUnavailableError

    def unavailable(home: Path, query: str) -> tuple[str, ...]:
        raise StoreUnavailableError("synthetic unavailable taste store")

    if not gate_memory_tool:
        monkeypatch.setattr(taste_context, "pick_lines", unavailable)
    with _route_agent(home, "cli", gate_memory_tool=gate_memory_tool) as (agent, requests):
        _, messages = _run(agent, requests, f"Please recall the plan for the {ACTIVE}.")
    tail = messages[-1]
    assert tail["role"] == ("user" if gate_memory_tool else "tool")
    assert RECALL in tail["content"]
    assert f"like: {ACTIVE}" not in tail["content"]


@pytest.mark.parametrize("ordinary_recall", [False, True])
def test_nonowner_replay_hides_generated_tastes_without_rewriting_owner_history(home, tastes, ordinary_recall):
    owner = {"id": OWNER_ID, "name": "Synthetic sender"}
    visitor = {"id": "other", "name": "Synthetic sender"}
    ask = f"Please recall the plan for the {ACTIVE}." if ordinary_recall else ASK
    with _route_agent(home, "telegram") as (agent, requests):
        first, owner_wire = _run(agent, requests, ask, turn_author=owner)
        second, visitor_wire = _run(
            agent, requests, turn_author=visitor, conversation_history=first["messages"],
        )
        _, returned_owner_wire = _run(
            agent, requests, "Please help with another plan.",
            turn_author=owner, conversation_history=second["messages"],
        )
    _assert_taste_tail(owner_wire, ordinary_recall=ordinary_recall)
    replayed = [row["content"] for row in visitor_wire if row.get("role") == "tool"]
    if ordinary_recall:
        recall, = replayed
        assert RECALL in recall and f"like: {ACTIVE}" not in recall
    else:
        assert replayed == []
    assert not any(f"like: {CORE}" in str(row.get("content")) for row in visitor_wire)
    assert returned_owner_wire[:len(owner_wire)] == owner_wire
