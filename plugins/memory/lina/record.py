"""The one record lina-memory builds per finished turn and hands to every engine (lina#20)."""

from __future__ import annotations

import time
from dataclasses import dataclass
from typing import Any, Mapping, Optional, Sequence

from .policy import Rule, turn_start_index


@dataclass(frozen=True)
class TurnRecord:
    route: str
    words_of: str
    lcm: bool
    honcho: bool
    person_id: Optional[str]
    agent_id: str
    conversation_id: str
    session_id: str
    platform: str
    author_id: Optional[str]
    # memory.messages uids (agent:<profile>:<message uid>) of the turn's rows, user row first.
    message_uids: tuple[str, ...]
    user_uid: Optional[str]
    reply_uid: Optional[str]
    started_at: float
    ended_at: float


@dataclass(frozen=True)
class TurnRows:
    """The turn's rows in the agent's transcript: from its user row to the end."""
    hermes_uids: tuple[str, ...]
    user_hermes_uid: Optional[str]
    reply_hermes_uid: Optional[str]
    started_at: float
    ended_at: float


def _uid(message: Mapping[str, Any]) -> Optional[str]:
    uid = message.get("message_uid")
    return uid if isinstance(uid, str) and uid else None


def _timestamp(message: Mapping[str, Any]) -> Optional[float]:
    value = message.get("timestamp")
    return float(value) if isinstance(value, (int, float)) and not isinstance(value, bool) else None


def turn_rows(messages: Sequence[Any], turn_start: Any = None) -> TurnRows:
    index = turn_start_index(messages, turn_start)
    rows = [m for m in messages[index:] if isinstance(m, Mapping)] if index is not None else []
    replies = [m for m in rows if m.get("role") == "assistant" and m.get("content") and not m.get("tool_calls")]
    stamps = [t for t in (_timestamp(m) for m in rows) if t is not None]
    now = time.time()
    return TurnRows(
        hermes_uids=tuple(uid for uid in (_uid(m) for m in rows) if uid),
        user_hermes_uid=_uid(rows[0]) if rows else None,
        reply_hermes_uid=_uid(replies[-1]) if replies else None,
        started_at=min(stamps) if stamps else now, ended_at=max(stamps) if stamps else now,
    )


def build_record(rule: Rule, rows: TurnRows, *, store_uid, person_id: Optional[str], agent_id: str,
                 conversation_id: str, session_id: str, platform: str, author_id: Optional[str]) -> TurnRecord:
    """``store_uid`` maps a Hermes message uid to its memory.messages uid (``MessageStore.base_uid``)."""
    return TurnRecord(
        route=rule.route, words_of=rule.words_of, lcm=rule.lcm, honcho=rule.honcho, person_id=person_id,
        agent_id=agent_id, conversation_id=conversation_id, session_id=session_id, platform=platform,
        author_id=author_id, message_uids=tuple(store_uid(uid) for uid in rows.hermes_uids),
        user_uid=store_uid(rows.user_hermes_uid) if rows.user_hermes_uid else None,
        reply_uid=store_uid(rows.reply_hermes_uid) if rows.reply_hermes_uid else None,
        started_at=rows.started_at, ended_at=rows.ended_at,
    )
