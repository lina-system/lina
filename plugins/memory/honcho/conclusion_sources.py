"""Read existing conclusion citations without changing Honcho's stored representation."""

from __future__ import annotations

from collections.abc import Callable
from typing import Any


def message_speaker(message: Any, assistant_peer_id: str) -> str:
    metadata = getattr(message, "metadata", None) or {}
    return metadata.get("lina_speaker") or (
        "agent" if getattr(message, "peer_id", None) == assistant_peer_id else "unknown")


def _premise_missing(error: Exception) -> bool:
    """True for the Honcho SDK's 404 on a premise lookup; every other failure is a real service error."""
    return getattr(error, "status", None) == 404


def conclusion_record(
    conclusion: Any, scope: Any, get_message: Callable[[str, str], Any], *, assistant_peer_id: str,
) -> dict[str, Any]:
    """Follow explicit message citations and derived premises to their existing speaker kinds and grades.

    Uncited legacy conclusions, circular ancestry and premises that no longer resolve (deleted, or never stored:
    ``source_ids`` has no foreign key) remain unknown; none can establish an owner fact. Other read failures propagate.
    Shared premises and messages are read once within this traversal.
    """
    speakers: list[str] = []
    grades: list[str | None] = []
    root_id = str(conclusion.id)
    pending = [(root_id, frozenset())]
    visited: set[str] = set()
    messages: set[tuple[str, str]] = set()
    while pending:
        ident, ancestors = pending.pop()
        if ident in ancestors:
            speakers.append("unknown")
            grades.append(None)
            continue
        if ident in visited:
            continue
        visited.add(ident)
        try:
            current = conclusion if ident == root_id else scope.get(ident)
        except Exception as error:
            if not _premise_missing(error):
                raise
            speakers.append("unknown")
            grades.append(None)
            continue
        message_ids = getattr(current, "source_message_ids", None) or []
        source_ids = getattr(current, "source_ids", None) or []
        session_id = getattr(current, "session_id", None)
        if not (message_ids or source_ids) or (message_ids and not session_id):
            speakers.append("unknown")
            grades.append(None)
        for message_id in message_ids if session_id else ():
            key = (session_id, message_id)
            if key in messages:
                continue
            messages.add(key)
            message = get_message(*key)
            metadata = getattr(message, "metadata", None) or {}
            speakers.append(message_speaker(message, assistant_peer_id))
            grades.append(metadata.get("lina_grade"))
        pending.extend((source_id, ancestors | {ident}) for source_id in reversed(source_ids))
    created = getattr(conclusion, "created_at", None)
    return {
        "id": str(conclusion.id), "content": getattr(conclusion, "content", "") or "",
        "observer_id": getattr(conclusion, "observer_id", "") or "",
        "observed_id": getattr(conclusion, "observed_id", "") or "",
        "session_id": getattr(conclusion, "session_id", None),
        "created_at": created.isoformat() if hasattr(created, "isoformat") else created,
        "source_message_ids": list(getattr(conclusion, "source_message_ids", None) or ()),
        "source_ids": list(getattr(conclusion, "source_ids", None) or ()),
        "source_speakers": list(dict.fromkeys(speakers)),
        "source_grades": list(dict.fromkeys(grades)),
    }
