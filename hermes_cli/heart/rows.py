"""Journal entries as ``memory.messages`` rows of source ``heart`` (docs/store.md "Heart journal rows")."""

from __future__ import annotations

import datetime as dt
import hashlib
import json
from collections.abc import Iterable
from contextlib import closing
from pathlib import Path

from hermes_cli.heart.models import Entry
from hermes_state_store import MessageStore, agent_id_for, payload_digest

SOURCE = "heart"
_ENTRY_FIELDS = ("ts", "event", "feeling", "note", "strength", "via", "pref", "evidence")


def agent(home: Path) -> str:
    """The agent of a home: the same id its SessionDB writes under, from the path alone."""
    return agent_id_for(home / "state.db")


def open_store(home: Path) -> closing[MessageStore]:
    return closing(MessageStore(agent(home)))


def row_uid(agent_id: str, record: dict) -> str:
    """``heart:<profile>:<16 hex>`` of the entry's digest: the same entry always gets the same uid."""
    canonical = json.dumps(record, sort_keys=True, ensure_ascii=False, separators=(",", ":"))
    return f"{SOURCE}:{agent_id}:{hashlib.sha256(canonical.encode('utf-8')).hexdigest()[:16]}"


def row(uid: str, entry: Entry, content: str, extra: dict | None = None) -> dict:
    """The entry as a row ``MessageStore.import_texts`` writes: fields in ``native``, the conclusion in ``content``."""
    native = {**entry.model_dump(mode="json", include=set(_ENTRY_FIELDS), exclude_none=True), **(extra or {})}
    native["payload_sha256"] = payload_digest((content, None, None, None, None, None, None, None))
    return {"uid": uid, "source": SOURCE, "session_id": None, "conversation_id": None, "seq_in_session": None,
            "role": None, "content": content, "tool_name": None, "tool_call_id": None, "tool_calls": None,
            "scope": "agent", "native": native, "content_sha256": hashlib.sha256(content.encode("utf-8")).hexdigest(),
            "ts": dt.datetime.fromisoformat(entry.ts).timestamp(), "payload_sha256": native["payload_sha256"]}


def put(store: MessageStore, rows: list[dict]) -> int:
    """Write *rows*, keeping a row whose uid is already there; returns the rows written."""
    return store.import_texts(rows)


def present(store: MessageStore, uids: Iterable[str]) -> set[str]:
    return {found["uid"] for found in store.export_texts(uids)}


def entries(store: MessageStore) -> list[Entry]:
    """This agent's journal, oldest first; row-level security keeps it to the agent's own rows."""
    with store._cursor() as cur:
        cur.execute("SELECT native FROM memory.messages WHERE source = %s AND forgotten_at IS NULL"
                    " ORDER BY ts, message_id", (SOURCE,))
        found = cur.fetchall()
    return [Entry.model_validate({k: v for k, v in native.items() if k in _ENTRY_FIELDS}) for (native,) in found]


def texts(store: MessageStore, uids: Iterable[str]) -> dict[str, str]:
    """The plain text of the rows of *uids* this agent may cite: its own, not forgotten."""
    from hermes_state_source_visibility import hidden_message

    wanted = list(dict.fromkeys(uids))
    with store._cursor() as cur:
        cur.execute("SELECT uid, coalesce(content, '') FROM memory.messages m WHERE uid = ANY(%s)"
                    f" AND forgotten_at IS NULL AND NOT {hidden_message('m')}", (wanted,))
        return dict(cur.fetchall())


def lcm_uids(store: MessageStore, store_ids: Iterable[int]) -> dict[int, str]:
    """The ``memory.messages`` uid of each moved LCM message (``ctx.lcm_messages.store_id`` -> ``text_uid``)."""
    with store._cursor() as cur:
        cur.execute("SELECT store_id, text_uid FROM ctx.lcm_messages WHERE store_id = ANY(%s)", (list(store_ids),))
        return {int(store_id): uid for store_id, uid in cur.fetchall()}
