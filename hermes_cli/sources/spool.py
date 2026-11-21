"""The v1 collector identity and durable append boundary."""

from __future__ import annotations

import hashlib
import json
import os
import stat
import uuid
from datetime import datetime, timezone
from pathlib import Path


def message_uid(source: str, room: str, native_id: str) -> str:
    identity = json.dumps([source, room, native_id], separators=(",", ":"), ensure_ascii=False)
    return hashlib.sha256(identity.encode()).hexdigest()


def append_message(path: Path, message: dict) -> None:
    """Serialize producers with the ingest reader and preserve every committed byte."""
    import fcntl

    event = {"version": 1, "event_id": uuid.uuid4().hex,
             "ingested_at": datetime.now(timezone.utc).isoformat(), "messages": [message]}
    line = (json.dumps(event, ensure_ascii=False, separators=(",", ":")) + "\n").encode()
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    directory = os.open(path.parent, os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        if stat.S_IMODE(os.fstat(directory).st_mode) != 0o700:
            raise ValueError("spool directory must have mode 0700")
        flags = os.O_RDWR | os.O_APPEND | os.O_CREAT | os.O_NOFOLLOW | os.O_NONBLOCK
        fd = os.open(path.name, flags, 0o600, dir_fd=directory)
        with os.fdopen(fd, "a+b") as stream:
            info = os.fstat(fd)
            if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o600:
                raise ValueError("spool must be a regular file with mode 0600")
            fcntl.flock(fd, fcntl.LOCK_EX)
            if stream.seek(0, os.SEEK_END):
                stream.seek(-1, os.SEEK_END)
                if stream.read(1) != b"\n":
                    raise ValueError("spool has an incomplete event")
            stream.write(line)
            stream.flush()
            os.fsync(fd)
            os.fsync(directory)
    finally:
        os.close(directory)
