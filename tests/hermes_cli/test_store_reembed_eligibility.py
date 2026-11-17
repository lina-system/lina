"""Reembedding excludes injected turns and preserves skill instructions (#737)."""

from hermes_cli.store_reembed import reembed, rows_left
from tests.hermes_cli.test_store_reembed import _Client, _connect, _message


def test_reembed_and_rows_left_exclude_injected_turns(_memory_store, memory_store_sql):
    _message(memory_store_sql, "agent:summary", "agent", "user",
             "[Depth-3 Summary (d3, node 12)]\nsynthetic summary")
    _message(memory_store_sql, "agent:request", "agent", "user", "inspect the synthetic release")
    client = _Client()
    with _connect(_memory_store)() as conn:
        assert rows_left(conn, "agent", client.model) == 1

    counts = reembed(_connect(_memory_store), client, ["agent"], batch_size=1)

    assert [text for batch in client.calls for text in batch] == ["inspect the synthetic release"]
    assert (counts[0].embedded, counts[0].left) == (1, 0)


def test_reembed_uses_only_skill_instruction(_memory_store, memory_store_sql):
    from tests.hermes_state.test_memory_search_eligibility import skill_turn

    _message(memory_store_sql, "agent:skill", "agent", "user", skill_turn("inspect the synthetic release"))
    _message(memory_store_sql, "agent:bare", "agent", "user", skill_turn())
    client = _Client()
    with _connect(_memory_store)() as conn:
        assert rows_left(conn, "agent", client.model) == 1

    reembed(_connect(_memory_store), client, ["agent"], batch_size=1)

    assert [text for batch in client.calls for text in batch] == ["inspect the synthetic release"]
