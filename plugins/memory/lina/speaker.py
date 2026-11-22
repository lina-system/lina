"""Who said each row of a finished turn (lina#324): the speaker kind ``memory.messages`` keeps for it.

A row's kind comes from where it arrived, never from its text: a user row's from the route the policy table gives it
(``policy.yaml``'s ``speaker``), an assistant or tool row's from its role's route. A person on a person channel is the
owner, except in a shared room (the gateway labelled the sender, ``gateway/inbound_notes.py``), where a person is
the owner only through a handle the owner confirmed as theirs (``identity.OWNER_HANDLE``, lina#313) and otherwise
someone outside. The refs: ``agent:<profile>`` for the agent, ``tool:<name>`` for a tool result, the sender's
``<platform>:<user id>`` for a person (``local:owner`` on local surfaces), as ``links.row_refs`` spells handles.
"""

from __future__ import annotations

from collections.abc import Callable, Mapping, Sequence
from typing import Any, Optional

from hermes_state_store import GATEWAY_NOTES_KEY

from .policy import Policy, signals_for_turn, turn_start_index

RowSpeaker = tuple[str, str, Optional[str]]  # (Hermes message uid, kind, ref)


def notes_of(row: Mapping[str, Any]) -> Optional[dict[str, Any]]:
    """The gateway's notes on a user row (``{"owner_text": ..., "parts": [...]}``), when they are parts."""
    metadata = row.get("display_metadata")
    notes = metadata.get(GATEWAY_NOTES_KEY) if isinstance(metadata, Mapping) else None
    return notes if isinstance(notes, dict) else None


def is_shared_room(row: Mapping[str, Any]) -> bool:
    notes = notes_of(row) or {}
    return any(part.get("kind") == "sender_label" for part in notes.get("parts") or ())


def turn_speakers(policy: Policy, messages: Sequence[Any], turn_start: Any, *, agent_context: str, platform: str,
                  author: Mapping[str, Any], agent_ref: str, person_ref: Optional[str],
                  owner_handle: Callable[[], bool]) -> list[RowSpeaker]:
    """The kind and ref of each row of the turn starting at *turn_start*. *person_ref* names the turn's author;
    *owner_handle()* answers whether the author's handle is confirmed as the owner's (asked only in a shared room)."""
    rows = list(messages)
    start = turn_start_index(rows, turn_start)
    if start is None:
        return []
    found: list[RowSpeaker] = []
    for index in range(start, len(rows)):
        row = rows[index]
        uid = row.get("message_uid") if isinstance(row, Mapping) else None
        if not isinstance(uid, str) or not uid:
            continue
        role = row.get("role")
        if role == "user":
            kind = policy.rule(policy.route_for(signals_for_turn(
                rows, agent_context=agent_context, platform=platform, turn_author=dict(author) or None,
                turn_start=index))).speaker
            if kind == "owner" and is_shared_room(row) and not owner_handle():
                kind = "outside"
        elif (rule := policy.rule_for_role(str(role))) is not None:
            kind = rule.speaker
        else:
            continue
        if kind == "agent":
            ref = agent_ref
        elif kind in ("owner", "outside") and role == "user":
            ref = person_ref
        elif kind == "outside" and role == "tool":
            ref = f"tool:{row.get('tool_name') or row.get('name') or ''}"
        else:
            ref = None
        found.append((uid, kind, ref))
    return found
