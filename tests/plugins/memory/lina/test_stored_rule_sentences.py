"""Storage matrix for the registered standing-rule tool (lina#775).

The owner turn is arranged in the store and its UID bound on the provider.
The separate conversation test exercises how a live turn sets that UID.
"""

import json
from collections.abc import Callable
from hashlib import sha256
from pathlib import Path

import pytest

from plugins.memory.lina import LinaMemoryProvider
from tests.plugins.memory.lina.test_rule_sentences import AMENDMENTS, CASES, SentenceCase, assert_korean_line


@pytest.fixture
def provider(tmp_path: Path) -> LinaMemoryProvider:
    provider = LinaMemoryProvider()
    provider._home = str(tmp_path / "profiles" / "alpha")
    provider._session_id = "matrix-session"
    return provider


@pytest.fixture
def store_turn(provider: LinaMemoryProvider, memory_store_sql, monkeypatch) -> Callable[[SentenceCase], str]:
    current = None
    monkeypatch.setattr(LinaMemoryProvider, "_rule_source_uid", property(lambda self: current), raising=False)

    def insert(case: SentenceCase) -> str:
        nonlocal current
        uid = f"agent:alpha:matrix-{case.label}"
        # A session exists in every case. A deictic place needs its own identifier.
        conversation = case.place.removeprefix("channel:") if case.place else None
        memory_store_sql(
            """INSERT INTO memory.messages
               (uid, source, agent_id, session_id, role, speaker, content, content_sha256, conversation_id)
               VALUES (%s, 'agent', 'alpha', %s, 'user', 'owner', %s, %s, %s)""",
            (uid, provider._session_id, case.text, sha256(case.text.encode()).hexdigest(), conversation),
        )
        current = uid
        return uid

    return insert


@pytest.mark.parametrize("case", CASES, ids=lambda case: case.label)
def test_record_stores_labelled_owner_scope(
    case: SentenceCase, provider: LinaMemoryProvider, store_turn: Callable[[SentenceCase], str], memory_store_sql,
) -> None:
    assert "standing_rule" in {schema["name"] for schema in provider.get_tool_schemas()}
    uid = store_turn(case)
    response = json.loads(provider.handle_tool_call("standing_rule", {"action": "record"}))

    # Read as the fixture's superuser so RLS cannot hide incorrect targets or writes.
    rows = memory_store_sql(
        """SELECT scope, agent_id, place_ref, statement, source_uid, status
           FROM memory.standing_rules"""
    )
    if case.scope is None:
        assert rows == []
    else:
        assert rows == [(case.scope, case.target, case.place_ref, case.statement, uid, "active")]
    assert memory_store_sql(
        "SELECT content, native ->> 'fact_correction' FROM memory.messages WHERE uid = %s", (uid,)
    ) == [(case.text, case.fact)]

    if case.refused:
        assert "error" not in response
        assert_korean_line(response["refusal"])
        if case.refusal_topic:
            assert case.refusal_topic in response["refusal"]
    elif case.scope is None:
        assert response["fact_uid"] == uid
    else:
        assert response["scope"] == case.scope
        assert_korean_line(response["reply"])
    if case.unclear:
        assert_korean_line(response["question"])
        assert response["question"].count("?") == 1
    else:
        assert "question" not in response


@pytest.mark.parametrize("case", AMENDMENTS, ids=lambda case: case.label)
def test_change_keeps_existing_place_and_target(
    case: SentenceCase, provider: LinaMemoryProvider, store_turn: Callable[[SentenceCase], str], memory_store_sql,
) -> None:
    prior_uid = store_turn(SentenceCase("prior", "In the review channel, you should use two lines."))
    recorded = json.loads(provider.handle_tool_call("standing_rule", {"action": "record"}))
    uid = store_turn(case)

    changed = json.loads(provider.handle_tool_call("standing_rule", {
        "action": "change", "rule_id": recorded["rule_id"],
    }))

    assert (changed["version"], changed["scope"]) == (2, "place")
    assert_korean_line(changed["reply"])
    assert "question" not in changed
    assert memory_store_sql(
        """SELECT version, status, scope, agent_id, place_ref, statement, source_uid
           FROM memory.standing_rules ORDER BY version"""
    ) == [
        (1, "replaced", "place", "agent:alpha", "channel:review", "use two lines", prior_uid),
        (2, "active", "place", "agent:alpha", "channel:review", case.statement, uid),
    ]
    assert memory_store_sql(
        "SELECT native ->> 'fact_correction' FROM memory.messages WHERE uid = %s", (uid,)
    ) == [(None,)]
