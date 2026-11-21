"""The committed inbound user row available to an in-process memory tool during its call."""

from __future__ import annotations

from contextlib import contextmanager
from contextvars import ContextVar
from dataclasses import dataclass
from functools import partial
from typing import Any, Callable

from agent.message_metadata import message_uid_or_none
from agent.persisted_turn_receipt import committed_row_id


@dataclass(frozen=True)
class PersistedUserTurn:
    session_id: str
    text_uid: Callable[[], str | None]
    row: dict[str, Any]
    author: dict[str, Any]


@dataclass
class _Turn:
    manager: Any
    source: PersistedUserTurn | None = None


@dataclass
class _Dispatch:
    provider: Any


_turn: ContextVar[_Turn | None] = ContextVar("memory_tool_turn", default=None)
_dispatch: ContextVar[_Dispatch | None] = ContextVar("dispatched_memory_provider", default=None)


@contextmanager
def memory_tool_turn(manager):
    turn = _Turn(manager)
    token = _turn.set(turn)
    try:
        yield
    finally:
        # A timed-out tool worker may still hold a copy of this context.
        turn.source = None
        _turn.reset(token)


def bind_persisted_user_turn(agent, row, author) -> None:
    turn = _turn.get()
    if (turn is None or turn.manager is None or turn.manager is not agent._memory_manager
            or getattr(agent, "_persist_disabled", False)):
        return
    row_id = committed_row_id(row)
    uid = message_uid_or_none(row)
    if row_id is None or not uid or row.get("role") != "user" or not isinstance(row.get("content"), str):
        return
    lookup = getattr(getattr(agent, "_session_db", None), "user_row_text_uid", None)
    if lookup is None:
        return
    # The note kinds are routing signals. Their bodies, including the raw owner
    # text, must not travel through memory tool dispatch.
    metadata = row.get("display_metadata") or {}
    if not isinstance(metadata, dict):
        return
    notes = metadata.get("gateway_notes") or {}
    if not isinstance(notes, dict):
        return
    routing = {key: row[key] for key in ("role", "message_uid", "display_kind", "_compressed_summary") if key in row}
    routing["display_metadata"] = {
        "gateway_notes": {"parts": [{"kind": p.get("kind")} for p in notes.get("parts") or () if isinstance(p, dict)]},
        "compaction_inflight_task_restatement": bool(metadata.get("compaction_inflight_task_restatement")),
    }
    turn.source = PersistedUserTurn(agent.session_id, partial(lookup, agent.session_id, row_id, uid),
                                    routing, dict(author or {}))


@contextmanager
def memory_tool_dispatch(manager, provider):
    turn = _turn.get()
    dispatch = _Dispatch(provider if turn and turn.manager is manager else None)
    token = _dispatch.set(dispatch)
    try:
        yield
    finally:
        dispatch.provider = None
        _dispatch.reset(token)


def current_tool_turn(provider) -> PersistedUserTurn | None:
    """Only the dispatched provider can obtain the current turn's committed source."""
    turn = _turn.get()
    dispatch = _dispatch.get()
    return turn.source if turn and dispatch and dispatch.provider is provider else None
