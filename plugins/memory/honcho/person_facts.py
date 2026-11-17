"""Honcho's side of person pages (lina#445): every conclusion Honcho holds about one peer, with its id and time.

Honcho keeps conversation memory and goes on deriving conclusions; the person's facts live on their brain page
(``hermes_cli/brain/people.py``), which takes these conclusions as ``agent`` timeline entries keyed by their id.
Listing only reads: nothing here creates, changes or deletes anything in Honcho.
"""

from __future__ import annotations

from collections.abc import Callable, Iterable
from typing import Any

PAGE_SIZE = 100


def _record(conclusion: Any) -> dict[str, Any]:
    created = getattr(conclusion, "created_at", None)
    return {"id": str(conclusion.id), "content": getattr(conclusion, "content", "") or "",
            "created_at": created.isoformat() if hasattr(created, "isoformat") else created}


def conclusions_about(
    observers: Iterable[Any], peer_id: str, *,
    read_record: Callable[[Any, Any], dict[str, Any]] | None = None,
) -> list[dict[str, Any]]:
    """Read each id once; person-page imports supply a citation reader and accept only owner-grounded conclusions."""
    found: dict[str, dict[str, Any]] = {}
    for observer in observers:
        view = observer.conclusions_of(peer_id)
        page = 1
        while True:
            items = list(view.list(page=page, size=PAGE_SIZE).items)
            for item in items:
                record = read_record(item, view) if read_record else _record(item)
                if read_record and (record["source_speakers"] != ["owner"] or record["source_grades"] != ["owner"]):
                    continue
                found.setdefault(str(item.id), record)
            if len(items) < PAGE_SIZE:
                break
            page += 1
    return list(found.values())


class HonchoNotConfigured(RuntimeError):
    pass


def home_lister() -> tuple[str, Callable[[str], list[dict[str, Any]]]]:
    """This home's Honcho workspace and a ``list_conclusions(peer_id)`` for the one-time import: what the peer
    itself and this agent's own peer (``aiPeer``) concluded about the peer."""
    from . import _cfg_usable
    from .client import HonchoClientConfig, get_honcho_client
    from .conclusion_sources import conclusion_record
    from .session_peers import assistant_peer_id_for, sanitize_peer_id
    config = HonchoClientConfig.from_global_config()
    if not _cfg_usable(config):
        raise HonchoNotConfigured("Honcho is not configured for this home (no enabled honcho.json with a key or URL)")
    client = get_honcho_client(config)

    def list_conclusions(peer_id: str) -> list[dict[str, Any]]:
        assistant = assistant_peer_id_for(config)
        observers = dict.fromkeys((sanitize_peer_id(peer_id), assistant))
        return conclusions_about(
            [client.peer(observer) for observer in observers], peer_id,
            read_record=lambda item, scope: conclusion_record(
                item, scope, lambda session_id, message_id: client.session(session_id).get_message(message_id),
                assistant_peer_id=assistant))
    return config.workspace_id, list_conclusions
