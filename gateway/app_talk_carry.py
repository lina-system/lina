"""What the owner and an agent said in the LINA app, carried into that agent's next messaging DM turn (lina#167).

The app talks with the agent that owns a to-do in the agent's own app conversation (an API session): the
gateway refuses an API turn into a Telegram conversation (``session_api_turn.admit_api_steps``,
``permission_denied``), so the messaging conversation cannot be the one the app writes. The dashboard leaves one
note per settled exchange in the agent's profile home; the agent's next DM turn on a messaging platform takes
every note and reads them ahead of the owner's new message, the way a model-switch note rides on the next user
message. A note is taken once: the reader claims it with an atomic rename before reading it.
"""

from __future__ import annotations

import json
import os
import time
import uuid
from pathlib import Path

CARRY_DIR = "app_talk"
REPLY_CHARS = 2000
# Surfaces that are not a conversation with a person on a messaging platform.
_NOT_MESSAGING = frozenset({"local", "api_server", "webhook", "msgraph_webhook"})


def write_carry(home: Path, note: dict) -> Path:
    """One note (``item``, ``title``, ``message``, ``reply``) for the next DM turn of the profile at *home*."""
    folder = home / CARRY_DIR
    folder.mkdir(parents=True, exist_ok=True)
    path = folder / f"{time.time_ns():020d}-{uuid.uuid4().hex}.json"
    tmp = path.with_suffix(".tmp")
    tmp.write_text(json.dumps(note, ensure_ascii=False), encoding="utf-8")
    os.replace(tmp, path)
    return path


def take_carry(home: Path) -> list[dict]:
    """Every waiting note, oldest first; each is claimed by rename, so two turns never both read one."""
    folder = home / CARRY_DIR
    notes = []
    for path in sorted(folder.glob("*.json")) if folder.is_dir() else ():
        claimed = path.with_suffix(".taken")
        try:
            os.replace(path, claimed)
        except FileNotFoundError:
            continue
        try:
            notes.append(json.loads(claimed.read_text(encoding="utf-8-sig")))
        except (OSError, ValueError):
            continue
        finally:
            claimed.unlink(missing_ok=True)
    return notes


def carry_text(notes: list[dict]) -> str:
    lines = []
    for note in notes:
        reply = str(note.get("reply") or "")
        if len(reply) > REPLY_CHARS:
            reply = reply[:REPLY_CHARS] + " …"
        lines.append(f'[In the LINA app the owner talked with you about to-do {note.get("item")} '
                     f'"{note.get("title")}".]\nOwner: {note.get("message")}\nYou replied: {reply}')
    return "\n\n".join(lines)


def carry_into_dm_turn(runner, source, message):
    """*message* with the waiting app notes ahead of it on a messaging DM turn; *message* unchanged elsewhere.
    The notes are read from the home of the profile the turn runs as (``_resolve_profile_home_for_source``)."""
    platform = getattr(getattr(source, "platform", None), "value", None)
    if not isinstance(message, str) or getattr(source, "chat_type", None) != "dm" or platform in _NOT_MESSAGING:
        return message
    notes = take_carry(runner._resolve_profile_home_for_source(source))
    return carry_text(notes) + "\n\n" + message if notes else message
