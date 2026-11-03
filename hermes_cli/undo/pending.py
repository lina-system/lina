"""Pre-write batches, reference typing, shrink warnings and crash-safe close."""

from __future__ import annotations

import fnmatch
from pathlib import Path
import re
import sys
import time

from hermes_cli.undo import copies, journal
from hermes_cli.undo.env import Env
from hermes_cli.undo.models import Entry, FileRef, Shrunk, UndoError, when
from hermes_cli.undo.storage import Store, atomic, digest, no_links, private_dir, read_bytes

HOW = "Apply this journal entry to restore the saved file copies"


def current_hash(path: Path) -> str | None:
    data = read_bytes(path)
    return digest(data) if data is not None else None


def pending_path(store: Store, change_id: str) -> Path:
    if not re.fullmatch(r"[0-9a-f]{32}", change_id):
        raise UndoError("invalid pending change_id")
    path = store.root / "pending" / (change_id + ".json")
    no_links(path)
    return path


def file_type(env: Env, path: Path):
    for home in env.profiles.values():
        if path in (home / "memories/MEMORY.md", home / "memories/USER.md"):
            return "memory"
        if path == home / "SOUL.md":
            return "rule"
    if any(fnmatch.fnmatchcase(str(path), str(Path(p).expanduser().absolute())) for p in env.settings.rules):
        return "rule"
    return "file"


def begin(store: Store, env: Env, args) -> str:
    paths = sorted({Path(path).expanduser().absolute() for path in args.path})
    at, change_id = journal.identity()
    # Parse metadata and the entire path list before any checkpoint or pending write.
    row = Entry(at=at, bot=args.bot, change_id=change_id, what=args.what, how=HOW,
                objects=[FileRef(type=file_type(env, p), path=str(p), sha256=None, after=None) for p in paths])
    with store.locked():
        private_dir(store.root / "pending")
        refs = []
        for ref in row.objects:
            copy = copies.capture(store, Path(ref.path))
            refs.append(ref.model_copy(update={"sha256": copy.sha256, "after": copy.sha256,
                                               "checkpoint": copy.checkpoint}))
        row = row.model_copy(update={"objects": refs})
        atomic(pending_path(store, change_id), row.model_dump_json().encode())
    return change_id


def read_pending(store: Store, change_id: str) -> Entry:
    data = read_bytes(pending_path(store, change_id))
    if data is None:
        raise UndoError("unknown pending change_id")
    row = Entry.model_validate_json(data)
    if (row.change_id != change_id or row.kind != "revert" or row.until or row.how != HOW
            or row.undo_of or any(not isinstance(r, FileRef) or r.sha256 != r.after for r in row.objects)):
        raise UndoError("invalid pending file references")
    return row


def changed_ref(store: Store, ref: FileRef) -> FileRef | None:
    data = read_bytes(Path(ref.path))
    after = digest(data) if data is not None else None
    if after == ref.sha256:
        return None
    before = copies.restored_bytes(store, ref)
    size = len(data) if data is not None else None
    lost = None
    if before and (size is None or size == 0 or (len(before) >= 200 and size * 10 < len(before))):
        lost = Shrunk(before=len(before), after=size)
    return ref.model_copy(update={"after": after, "shrunk": lost})


def completed(row: Entry, saved: Entry) -> bool:
    before = {(r.type, r.path, r.sha256, r.checkpoint) for r in saved.objects if isinstance(r, FileRef)}
    return (not row.undo_of and all(getattr(row, k) == getattr(saved, k) for k in ("bot", "what", "kind", "until", "how"))
            and all(isinstance(r, FileRef) and (r.type, r.path, r.sha256, r.checkpoint) in before for r in row.objects))


def end(store: Store, change_id: str) -> str:
    with store.locked():
        saved = read_pending(store, change_id)
        path = pending_path(store, change_id)
        existing = next((r for r in journal.read(store) if r.change_id == change_id), None)
        if existing is not None:
            if not completed(existing, saved):
                raise UndoError("change_id already exists with different references")
            path.unlink()
            return change_id
        refs = [changed_ref(store, r) for r in saved.objects if isinstance(r, FileRef)]
        refs = [r for r in refs if r is not None]
        if refs:
            at, _ = journal.identity()
            journal.append_locked(store, saved.model_copy(update={"objects": refs, "at": at}))
            for ref in refs:
                if ref.shrunk:
                    detail = ("missing" if ref.shrunk.after is None else
                              f"shrank {ref.shrunk.before} -> {ref.shrunk.after} bytes")
                    print(f"undo: {ref.path} {detail}; check the write (restore: lina undo apply {change_id})",
                          file=sys.stderr)
        path.unlink()
        return change_id if refs else "No changed files; pending change closed"


def unfinished(store: Store, bot: str, rows: list[Entry]) -> list[str]:
    folder = store.root / "pending"
    no_links(folder)
    recorded = {r.change_id for r in rows}
    result = []
    for path in sorted(folder.glob("*.json")):
        row = read_pending(store, path.stem)
        if row.bot == bot and row.change_id not in recorded and time.time() - when(row.at) > 86400:
            import json

            result.append(json.dumps({"at": row.at, "bot": row.bot, "change_id": row.change_id,
                                      "what": row.what, "status": "unfinished"}))
    return result
