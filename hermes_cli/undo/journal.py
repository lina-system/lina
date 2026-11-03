"""Append-only, reference-only change history."""

from __future__ import annotations

import os
import uuid

from hermes_cli.undo.models import Entry, UndoError
from hermes_cli.undo.storage import Store, no_links, timestamp


def read(store: Store) -> list[Entry]:
    path = store.root / "journal.jsonl"
    no_links(path)
    try:
        with path.open(encoding="utf-8") as stream:
            rows = []
            for line in stream:
                if not line.endswith("\n"):
                    raise UndoError("incomplete journal tail; repair before writing")
                rows.append(Entry.model_validate_json(line))
            return rows
    except FileNotFoundError:
        return []


def append_locked(store: Store, row: Entry) -> Entry:
    if any(old.change_id == row.change_id for old in read(store)):
        raise UndoError("change_id already exists")
    path = store.root / "journal.jsonl"
    no_links(path)
    fd = os.open(path, os.O_WRONLY | os.O_APPEND | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as stream:
        os.fchmod(stream.fileno(), 0o600)
        stream.write(row.model_dump_json() + "\n")
        stream.flush()
        os.fsync(stream.fileno())
    return row


def append(store: Store, row: Entry) -> Entry:
    with store.locked():
        return append_locked(store, row)


def identity() -> tuple[str, str]:
    return timestamp(), uuid.uuid4().hex
