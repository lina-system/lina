"""lina#21 and #359: tastes may change only their own cache boundary on the model wire."""

from __future__ import annotations

from collections.abc import Callable
from pathlib import Path
from typing import Final, Protocol

import pytest

from agent.memory_recall_tool import RECALL_TOOL_NAME
from agent.self_note_section import SELF_NOTE_FILENAME, SELF_NOTE_SECTION_ID
from agent.system_prompt import _restore_plugin_prompt_sections
from hermes_cli.plugins_dispatch import format_system_prompt_section
from tests.plugins.memory.lina import test_cache_order
from tests.plugins.memory.lina.test_cache_order import (
    RECALL,
    RECALL_TURN,
    TURNS,
    Clock,
    _agent,
    _configure,
    _converse,
    _wire,
    recording_model,
)

cache_home = test_cache_order.home
clock = test_cache_order.clock

CORE_LABEL: Final = "orchard frost cloth"
NEW_CORE_LABEL: Final = "raised seed trays"
ACTIVE_LABEL: Final = "recall the plan"
STANDALONE_LABEL: Final = "garden question 22"
STANDALONE_TURN: Final = 22
SELF_NOTE: Final = "SYNTHETIC SELF NOTE: use concrete garden examples."


class StoreSQL(Protocol):
    def __call__(self, sql: str, params: tuple[str, ...] = ()) -> list[tuple]: ...


def seed_taste(sql: StoreSQL, label: str, status: str) -> None:
    sql(
        """
        INSERT INTO memory.tastes
            (agent_id, target_kind, label, valence, strength, status, origin,
             first_seen, protected_passed, protected_at)
        VALUES ('default', 'method', %s, 'like', 0.7, %s, 'outcome',
                DATE '2026-09-01', true, TIMESTAMPTZ '2026-09-01 09:00+00')
        """,
        (label, status),
    )


@pytest.fixture
def taste_home(cache_home: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    from plugins.memory.lina import LinaMemoryProvider

    (cache_home / SELF_NOTE_FILENAME).write_text(SELF_NOTE, encoding="utf-8")
    cwd = cache_home.parent / "garden"
    cwd.mkdir()
    monkeypatch.chdir(cwd)
    monkeypatch.setattr(LinaMemoryProvider, "session_card_block", lambda self: "HONCHO-CARD unchanged")
    return cache_home


def self_note_and_frame(prompt: str) -> tuple[str, str]:
    section, = (section for section in _restore_plugin_prompt_sections(prompt)
                if section.id == SELF_NOTE_SECTION_ID)
    frame = format_system_prompt_section(section.id, section.content)
    return section.content, prompt[:prompt.index(frame)]


def record_taste_turns(
    home: Path, clock: Clock, *, before_turn: Callable[[int], None] = lambda n: None,
) -> list:
    with recording_model() as server:
        server.url = f"http://127.0.0.1:{server.server_port}/v1"
        _configure(home, server.url)
        agent = _agent(home, server, "20261103_090000_taste-cache", clock.now)
        try:
            _converse(agent, TURNS, before_turn)
        finally:
            agent.close()
        return list(server.requests)


def test_same_day_core_strength_keeps_prompt_bytes_but_new_core_changes_only_its_frame(
    taste_home: Path, clock: Clock, memory_store_sql: StoreSQL,
) -> None:
    seed_taste(memory_store_sql, CORE_LABEL, "core")
    with recording_model() as server:
        server.url = f"http://127.0.0.1:{server.server_port}/v1"
        _configure(taste_home, server.url)
        prompts = []
        for suffix in ("before", "strength", "promoted"):
            agent = _agent(taste_home, server, f"20261103_090000_{suffix}", clock.now)
            try:
                _converse(agent, 1)
            finally:
                agent.close()
            prompts.append(server.requests[-1].body["messages"][0]["content"])
            if suffix == "before":
                memory_store_sql("UPDATE memory.tastes SET strength = 0.9 WHERE label = %s", (CORE_LABEL,))
            if suffix == "strength":
                seed_taste(memory_store_sql, NEW_CORE_LABEL, "core")

    first, rescored, promoted = prompts
    assert first == rescored
    first_note, first_prefix = self_note_and_frame(first)
    promoted_note, promoted_prefix = self_note_and_frame(promoted)
    assert first_prefix == promoted_prefix
    assert f"like: {CORE_LABEL}" in first_note
    assert f"like: {NEW_CORE_LABEL}" not in first_note
    assert f"like: {NEW_CORE_LABEL}" in promoted_note
    assert promoted_note.index(f"like: {NEW_CORE_LABEL}") < promoted_note.index(SELF_NOTE)


def test_core_refresh_waits_for_compaction_and_selected_tastes_only_extend_the_recall_tail(
    taste_home: Path, clock: Clock, memory_store_sql: StoreSQL,
) -> None:
    seed_taste(memory_store_sql, CORE_LABEL, "core")
    seed_taste(memory_store_sql, NEW_CORE_LABEL, "active")
    seed_taste(memory_store_sql, ACTIVE_LABEL, "active")
    seed_taste(memory_store_sql, STANDALONE_LABEL, "active")

    def promote(turn: int) -> None:
        if turn == 3:
            memory_store_sql("UPDATE memory.tastes SET status = 'core' WHERE label = %s", (NEW_CORE_LABEL,))

    requests = record_taste_turns(taste_home, clock, before_turn=promote)

    assert len(requests) == TURNS
    first = requests[0]
    rebuilt = next(index for index, request in enumerate(requests) if request.compactions != first.compactions)
    assert STANDALONE_TURN < len(requests) and 3 < rebuilt
    for turn, request in enumerate(requests):
        system = request.body["messages"][0]["content"]
        section, prefix = self_note_and_frame(system)
        assert prefix == self_note_and_frame(first.body["messages"][0]["content"])[1]
        assert f"like: {CORE_LABEL}" in section
        assert (f"like: {NEW_CORE_LABEL}" in section) == (turn >= rebuilt)
        if turn and request.compactions == requests[turn - 1].compactions:
            previous = _wire(requests[turn - 1])
            assert _wire(request)[:len(previous)] == previous, f"turn {turn + 1} rewrote the prefix"

    for turn, label in ((RECALL_TURN, ACTIVE_LABEL), (STANDALONE_TURN, STANDALONE_LABEL)):
        messages = requests[turn].body["messages"]
        user, call, result = messages[-3:]
        assert user["role"] == "user" and call["role"] == "assistant" and result["role"] == "tool"
        tool_call, = call["tool_calls"]
        assert tool_call["function"]["name"] == RECALL_TOOL_NAME
        assert result["tool_call_id"] == tool_call["id"]
        assert f"like: {label}" in result["content"]
        assert (RECALL in result["content"]) == (turn == RECALL_TURN)
        for request in requests:
            for message in request.body["messages"]:
                if message["role"] != "tool":
                    assert f"like: {label}" not in str(message.get("content"))


def test_next_session_sends_the_evidence_checked_self_note_with_its_core_tastes(
    taste_home: Path, clock: Clock, memory_store_sql: StoreSQL,
) -> None:
    from hermes_cli.heart import main, rows
    from hermes_cli.heart.models import Entry

    stamp = "2025-09-01T09:00:00+00:00"
    entry = Entry(ts=stamp, event="Planted seedlings", feeling="calm", note="Sketches helped",
                  evidence=(), strength=2)
    with rows.open_store(taste_home) as store:
        uid = rows.row_uid(store.agent_id, entry.model_dump(mode="json"))
        rows.put(store, [rows.row(uid, entry, entry.note)])
    seed_taste(memory_store_sql, CORE_LABEL, "core")
    checked = f"I sketch seed trays. (2025-09-01, evidence {stamp})"
    candidate = taste_home / "candidate.md"
    candidate.write_text(SELF_NOTE + "\n" + checked + "\n", encoding="utf-8")
    with recording_model() as server:
        server.url = f"http://127.0.0.1:{server.server_port}/v1"
        _configure(taste_home, server.url)
        for suffix in ("old", "next"):
            agent = _agent(taste_home, server, f"20261103_090000_{suffix}", clock.now)
            try:
                _converse(agent, 1)
            finally:
                agent.close()
            if suffix == "old":
                main(["self-apply", "--file", str(candidate)])
        old, new = (self_note_and_frame(req.body["messages"][0]["content"]) for req in server.requests)
    assert old[1] == new[1] and checked not in old[0]
    assert f"like: {CORE_LABEL}" in new[0] and checked in new[0]
