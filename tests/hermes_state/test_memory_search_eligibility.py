"""Search eligibility and skill instruction projection regressions for #737."""

import pytest

from hermes_state_store import MessageStore
from tests.hermes_cli.test_store_reembed import _message


def skill_turn(instruction: str = "") -> str:
    header = ('[IMPORTANT: The user has invoked the "demo" skill, indicating they want '
              'you to follow its instructions. The full skill content is loaded below.]\n\n'
              'scaffoldonly secret body\n\n')
    return header + (
        "The user has provided the following instruction alongside the skill invocation: " + instruction
        if instruction else "")


def _vector(sql, message_id: int, near: float) -> None:
    chunk = sql("INSERT INTO vec.chunks (source, agent_id, message_ids, text) "
                "VALUES ('agent', 'default', %s, 'synthetic') RETURNING chunk_id", ([message_id],))[0][0]
    vector = "[" + ",".join(map(str, [near, 1.0 - near] + [0.0] * 1022)) + "]"
    sql("INSERT INTO vec.embeddings (kind, item_id, agent_id, model, dim, embedding) "
        "VALUES ('chunk', %s, 'default', 'synthetic', 1024, %s::vector)", (chunk, vector))


@pytest.mark.parametrize("limit", [1, 2, 5])
def test_vectors_fill_eligible_limit_in_existing_rank(_memory_store, memory_store_sql, limit):
    for index, content in enumerate((
        "[Depth-3 Summary (d3, node 12)]\nsynthetic summary",
        "[Runtime note: synthetic runtime]",
        "eligible first", "eligible second",
    )):
        message_id = _message(memory_store_sql, f"agent:rank-{index}", "agent", "user", content)
        _vector(memory_store_sql, message_id, 1.0 - index * 0.15)
    store = MessageStore("default", dsn=_memory_store.app_dsn())
    try:
        rows = store.search_memory_vectors([1.0] + [0.0] * 1023, model="synthetic",
                                           sources=["agent"], limit=limit)
    finally:
        store.close()
    assert [row.content for row in rows] == ["eligible first", "eligible second"][:limit]


def test_skill_instruction_is_searchable_without_body_and_opens_as_instruction(_memory_store, memory_store_sql):
    _message(memory_store_sql, "agent:skill", "agent", "user", skill_turn("inspect synthetic release"))
    _message(memory_store_sql, "agent:bare", "agent", "user", skill_turn())
    store = MessageStore("default", dsn=_memory_store.app_dsn())
    try:
        rows = store.search_memory("synthetic release", sources=["agent"], limit=1)
        body_hits = store.search_memory("scaffoldonly", sources=["agent"])
        opened = store.open_memory("agent:skill")
    finally:
        store.close()
    assert [(row.uid, row.content) for row in rows] == [("agent:skill", "inspect synthetic release")]
    assert body_hits == []
    assert opened.content == "inspect synthetic release"


def test_old_skill_vectors_show_instruction_and_skip_bare_invocation(_memory_store, memory_store_sql):
    for index, instruction in enumerate(("", "inspect synthetic release")):
        message_id = _message(memory_store_sql, f"agent:skill-{index}", "agent", "user", skill_turn(instruction))
        _vector(memory_store_sql, message_id, 1.0 - index * 0.2)
    store = MessageStore("default", dsn=_memory_store.app_dsn())
    try:
        rows = store.search_memory_vectors([1.0] + [0.0] * 1023, model="synthetic",
                                           sources=["agent"], limit=1)
    finally:
        store.close()
    assert [row.content for row in rows] == ["inspect synthetic release"]
