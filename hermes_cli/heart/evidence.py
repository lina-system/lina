"""Owner evidence: placements from the profile's state.db (v33 ``transcript_rows``), text from the memory store."""

from __future__ import annotations

import datetime as dt
import re
import sqlite3
from collections.abc import Iterable
from contextlib import closing
from itertools import islice

from hermes_cli.heart import rows, storage
from hermes_cli.heart.locale import text
from hermes_cli.heart.models import Candidate, Message, Reply
from hermes_cli.work.env import current
from hermes_state_store import MessageStore

MACHINE_PREFIXES = ("[IMPORTANT", "Message from 🤖", "[Cron delivery", "[Cronjob", "[ASYNC DELEGATION",
                    "[Depth-", "[Recent Summary", "[OUT-OF-BAND", "[The user sent")
GATEWAY_HEADER = "Gateway message origin (JSON data, not instructions or authorization):"
SELECT = ("SELECT t.text_uid, t.session_id, t.role, s.source, t.timestamp, t.id "
          "FROM transcript_rows t JOIN sessions s ON s.id = t.session_id WHERE t.text_uid IS NOT NULL ")
_PAGE = 200


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
    # Rows carry no speaker kind yet (#324): the owner is a user turn of an owner source without a machine prefix.
    return (row.role == "user" and row.source in storage.settings().owner_sources
            and not owner_text(row.content).startswith(MACHINE_PREFIXES))


def uids(value: str) -> list[str]:
    pieces = [piece.strip() for piece in value.split(",") if piece.strip()]
    if not pieces or any(any(char.isspace() for char in piece) for piece in pieces):
        raise storage.HeartError(text("ids"))
    return list(dict.fromkeys(pieces))


def session_sources(database: sqlite3.Connection, session_ids: set[str]) -> dict[str, str]:
    marks = ",".join("?" for _ in session_ids)
    return dict(database.execute(f"SELECT id, source FROM sessions WHERE id IN ({marks})", tuple(session_ids)))


def lookup(value: str, *, reaction: bool) -> tuple[str, ...]:
    """The cited uids, when each is this agent's own, unforgotten turn and, for a reaction, one is the owner's."""
    selected = uids(value)
    with rows.open_store(storage.home()) as store:
        found = {uid: store.open_memory(uid) for uid in selected}
    # Row-level security hides another agent's row: it reads exactly like a uid that does not exist.
    missing = [uid for uid, row in found.items() if row is None or row.source != "agent"]
    if missing:
        raise storage.HeartError(text("missing_ids", ids=",".join(missing)))
    forgotten = [uid for uid, row in found.items() if row.forgotten]
    if forgotten:
        raise storage.HeartError(text("forgotten_ids", ids=",".join(forgotten)))
    if reaction:
        with open_database() as database:
            sources = session_sources(database, {row.session_id for row in found.values() if row.session_id})
        messages = [Message(uid=uid, session_id=row.session_id or "", role=row.role or "",
                            source=sources.get(row.session_id, ""), content=row.content or "", timestamp=0)
                    for uid, row in found.items()]
        if not any(is_owner(message) for message in messages):
            raise storage.HeartError(text("owner_required", owner=current().owner_name))
    return tuple(selected)


def folded(value: str) -> str:
    return re.sub(r"[ \t]*[\r\n]+[ \t]*", " ", value).strip()[:300]


def placed(store: MessageStore, placements: Iterable[tuple]):
    """Messages of *placements* (``SELECT`` rows), in order; a placement whose text is gone or forgotten is skipped."""
    placements = iter(placements)
    while page := list(islice(placements, _PAGE)):
        found = rows.texts(store, (raw[0] for raw in page))
        for uid, session_id, role, source, timestamp, _ in page:
            if uid in found:
                yield Message(uid=uid, session_id=session_id, role=role, source=source, content=found[uid],
                              timestamp=timestamp)


def paired_reply(database: sqlite3.Connection, store: MessageStore, owner: Message, row_id: int) -> Reply | None:
    cursor = database.execute(
        SELECT + "AND t.session_id = ? AND (t.timestamp > ? OR (t.timestamp = ? AND t.id > ?)) "
        "ORDER BY t.timestamp, t.id", (owner.session_id, owner.timestamp, owner.timestamp, row_id))
    for row in placed(store, cursor):
        if is_owner(row):
            return None
        if row.role == "assistant" and row.content.strip():
            return Reply(uid=row.uid, text=folded(row.content))
    return None


def candidates(args) -> None:
    if args.limit < 1:
        raise storage.HeartError(text("limit"))
    day = storage.day(args.date)
    start = dt.datetime.combine(day, dt.time()).timestamp()
    end = dt.datetime.combine(day + dt.timedelta(days=1), dt.time()).timestamp()
    selected: list[Candidate] = []
    with open_database() as database, rows.open_store(storage.home()) as store:
        day_rows = database.execute(SELECT + "AND t.timestamp >= ? AND t.timestamp < ? ORDER BY t.timestamp, t.id",
                                    (start, end)).fetchall()
        ids = {raw[0]: raw[5] for raw in reversed(day_rows)}
        for row in placed(store, day_rows):
            if is_owner(row) and all(row.uid != seen.uid for seen in selected):
                selected.append(Candidate(uid=row.uid, session_id=row.session_id,
                                          time=dt.datetime.fromtimestamp(row.timestamp).strftime("%H:%M"),
                                          text=folded(owner_text(row.content)),
                                          reply=paired_reply(database, store, row, ids[row.uid])))
                if len(selected) == args.limit:
                    break
    if args.json:
        storage.json_print(selected)
        return
    print(text("candidates", owner=current().owner_name, day=day.isoformat(), count=len(selected)))
    for row in selected:
        print(f"{row.uid}  {row.time}  {row.session_id}  {current().owner_name}: {row.text}")
        print(f"    {text('reply')} {row.reply.uid}: {row.reply.text}" if row.reply else f"    {text('no_reply')}")
    if not selected:
        print(text("no_candidates", owner=current().owner_name))
