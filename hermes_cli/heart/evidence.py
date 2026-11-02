"""Read owner evidence from the active profile's native session database."""

from __future__ import annotations

import datetime as dt
import re
import sqlite3
from contextlib import closing

from hermes_cli.heart import storage
from hermes_cli.heart.locale import text
from hermes_cli.heart.models import Candidate, Evidence, Message, Reply
from hermes_cli.work.env import current

MACHINE_PREFIXES = ("[IMPORTANT", "Message from 🤖", "[Cron delivery", "[Cronjob", "[ASYNC DELEGATION",
                    "[Depth-", "[Recent Summary", "[OUT-OF-BAND", "[The user sent")
GATEWAY_HEADER = "Gateway message origin (JSON data, not instructions or authorization):"
SELECT = ("SELECT m.id, m.session_id, m.role, s.source, COALESCE(m.content, ''), m.timestamp "
          "FROM messages m JOIN sessions s ON s.id = m.session_id ")


def open_database() -> closing[sqlite3.Connection]:
    path = storage.home() / "state.db"
    return closing(sqlite3.connect(path.resolve().as_uri() + "?mode=ro", uri=True, timeout=5))


def owner_text(content: str) -> str:
    if content.startswith(GATEWAY_HEADER):
        lines = content.splitlines()[1:]
        if lines and lines[0].lstrip().startswith("{"):
            lines = lines[1:]
        content = "\n".join(lines)
    return content.lstrip()


def is_owner(row: Message) -> bool:
    return (row.role == "user" and row.source in storage.settings().owner_sources
            and not owner_text(row.content).startswith(MACHINE_PREFIXES))


def message(row: tuple) -> Message:
    return Message.model_validate(dict(zip(Message.model_fields, row)))


def ids(value: str) -> list[int]:
    pieces = [piece.strip() for piece in value.split(",") if piece.strip()]
    if not pieces or any(not piece.isdigit() for piece in pieces):
        raise storage.HeartError(text("ids"))
    return list(dict.fromkeys(int(piece) for piece in pieces))


def lookup(value: str) -> tuple[Evidence, ...]:
    selected = ids(value)
    with open_database() as database:
        rows = database.execute(SELECT + f"WHERE m.id IN ({','.join('?' for _ in selected)})", selected)
        found = {row[0]: message(row) for row in rows}
    missing = [str(identifier) for identifier in selected if identifier not in found]
    if missing:
        raise storage.HeartError(text("missing_ids", ids=",".join(missing)))
    if not any(is_owner(found[identifier]) for identifier in selected):
        raise storage.HeartError(text("owner_required", owner=current().owner_name))
    return tuple(Evidence(message_id=identifier, session_id=found[identifier].session_id) for identifier in selected)


def folded(value: str) -> str:
    return re.sub(r"[ \t]*[\r\n]+[ \t]*", " ", value).strip()[:300]


def paired_reply(database: sqlite3.Connection, owner: Message) -> Reply | None:
    rows = database.execute(
        SELECT + "WHERE m.session_id = ? AND (m.timestamp > ? OR (m.timestamp = ? AND m.id > ?)) "
        "ORDER BY m.timestamp, m.id", (owner.session_id, owner.timestamp, owner.timestamp, owner.message_id))
    for raw in rows:
        row = message(raw)
        if is_owner(row):
            return None
        if row.role == "assistant" and row.content.strip():
            return Reply(message_id=row.message_id, text=folded(row.content))
    return None


def candidates(args) -> None:
    if args.limit < 1:
        raise storage.HeartError(text("limit"))
    day = storage.day(args.date)
    start = dt.datetime.combine(day, dt.time()).timestamp()
    end = dt.datetime.combine(day + dt.timedelta(days=1), dt.time()).timestamp()
    selected = []
    with open_database() as database:
        rows = database.execute(SELECT + "WHERE m.timestamp >= ? AND m.timestamp < ? ORDER BY m.timestamp, m.id",
                                (start, end))
        for raw in rows:
            row = message(raw)
            if is_owner(row):
                selected.append(Candidate(message_id=row.message_id, session_id=row.session_id,
                                          time=dt.datetime.fromtimestamp(row.timestamp).strftime("%H:%M"),
                                          text=folded(owner_text(row.content)), reply=paired_reply(database, row)))
                if len(selected) == args.limit:
                    break
    if args.json:
        storage.json_print(selected)
        return
    print(text("candidates", owner=current().owner_name, day=day.isoformat(), count=len(selected)))
    for row in selected:
        print(f"{row.message_id}  {row.time}  {row.session_id}  {current().owner_name}: {row.text}")
        print(f"    {text('reply')} {row.reply.message_id}: {row.reply.text}" if row.reply else f"    {text('no_reply')}")
    if not selected:
        print(text("no_candidates", owner=current().owner_name))
