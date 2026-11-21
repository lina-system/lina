"""Private, crash-safe state for one manual source, independent of the spool."""

from __future__ import annotations

import json
import math
import os
import stat
from contextlib import contextmanager
from dataclasses import asdict, dataclass, field
from datetime import datetime
from pathlib import Path
from typing import Any

from utils import atomic_json_write

from .defaults import CALL_LIMITS

ERROR_KINDS = frozenset({
    "auth", "inactive_account", "rate_limited", "provider_error", "invalid_response",
    "invalid_cursor", "invalid_config", "tool_denied", "collection_error",
})
OUTCOMES = frozenset({
    "new", "started", "ok", "run_limit", "hour_limit", "day_limit", "message_limit",
    "resumed", "stopped", "error",
})
_PROGRESS_FIELDS = frozenset({
    "since", "until", "issued_at", "page_token", "offset", "thread_ts", "thread_page_token",
    "thread_offset", "history_page_token", "history_offset", "channel_page_token", "channel_offset",
    "query_hash", "page_hash", "thread_page_hash",
})


class StateError(ValueError):
    """A state or path cannot be used safely."""


def _sync_directory(directory: Path) -> None:
    fd = os.open(directory, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def _create_state_directory(directory: Path) -> None:
    if not directory.is_dir():
        _create_state_directory(directory.parent)
        directory.mkdir(mode=0o700, exist_ok=True)
        _sync_directory(directory.parent)


def timestamp(value: str) -> datetime:
    parsed = datetime.fromisoformat(value)
    if parsed.utcoffset() is None:
        raise ValueError("timezone required")
    return parsed


def _validate_progress(progress: dict) -> None:
    # A provider page can contain message text and addresses. Persist only scalar read positions.
    if not isinstance(progress, dict) or progress.keys() - _PROGRESS_FIELDS:
        raise StateError("invalid progress fields")
    if timestamp(progress["until"]) < timestamp(progress["since"]):
        raise StateError("invalid pending window")
    for key, value in progress.items():
        if key.endswith("offset"):
            if type(value) is not int or value < 0:
                raise StateError("invalid page offset")
        elif key == "issued_at" and type(value) in (int, float) and math.isfinite(value):
            continue
        elif value is not None and (not isinstance(value, str) or len(value) > 8192
                                    or (value and not value.isprintable())):
            raise StateError("invalid page position")


@dataclass
class SourceState:
    calls: list[float] = field(default_factory=list)
    last_attempt: float | None = None
    last_success: float | None = None
    last_probe_success: float | None = None
    failures: int = 0
    stopped: bool = False
    last_error: str | None = None
    error_time: float | None = None
    outcome: str = "new"
    rooms: dict[str, dict[str, Any]] = field(default_factory=dict)

    def usage(self, now: float) -> tuple[int, int]:
        return sum(t > now - 3600 for t in self.calls), sum(t > now - 86400 for t in self.calls)

    def validate(self) -> None:
        if type(self.stopped) is not bool or type(self.failures) is not int or self.failures < 0:
            raise StateError("invalid state")
        if not isinstance(self.calls, list) or not isinstance(self.rooms, dict):
            raise StateError("invalid state")
        if self.outcome not in OUTCOMES or (self.last_error is not None and self.last_error not in ERROR_KINDS):
            raise StateError("invalid state")
        times = [self.last_attempt, self.last_success, self.last_probe_success, self.error_time]
        for t in [*self.calls, *(t for t in times if t is not None)]:
            if type(t) not in (int, float) or not math.isfinite(t):
                raise StateError("invalid state")
        for room in self.rooms.values():
            if not isinstance(room, dict) or not room or room.keys() - {"cursor", "since", "pending"}:
                raise StateError("invalid room")
            if "cursor" in room or "since" in room:
                if timestamp(room["cursor"]) < timestamp(room["since"]):
                    raise StateError("invalid coverage")
            if "pending" in room:
                _validate_progress(room["pending"])

    @classmethod
    def load(cls, path: Path) -> SourceState:
        try:
            info = path.lstat()
        except FileNotFoundError:
            return cls()
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
            raise StateError("unsafe state file")
        try:
            data = json.loads(path.read_bytes())
            if not isinstance(data, dict) or set(data) != cls.__dataclass_fields__.keys():
                raise StateError("invalid state")
            state = cls(**data)
            state.validate()
        except (TypeError, ValueError, KeyError, OverflowError):
            raise StateError("invalid state") from None
        return state

    def save(self, path: Path) -> None:
        atomic_json_write(path, asdict(self), mode=0o600)
        # A precharged call can start only after the rename itself is durable.
        _sync_directory(path.parent)


@dataclass(frozen=True)
class RoomCheckpoint:
    state: SourceState
    path: Path | None

    def pending(self, room: str, progress: dict | None) -> None:
        """Persist a resumable position without advertising coverage for an unfinished window."""
        if self.path is None:
            return
        if progress is None:
            fields = self.state.rooms.get(room)
            if fields is None:
                return
            fields.pop("pending", None)
            if not fields:
                self.state.rooms.pop(room)
        else:
            _validate_progress(progress)
            self.state.rooms.setdefault(room, {})["pending"] = progress.copy()
        self.state.save(self.path)

    def __call__(self, room: str, cursor: str, since: str) -> None:
        if self.path is None:
            return
        start = timestamp(since)
        if timestamp(cursor) < start:
            raise StateError("invalid coverage")
        previous = self.state.rooms.get(room, {})
        if "cursor" in previous and timestamp(previous["since"]) <= start <= timestamp(previous["cursor"]):
            since = previous["since"]
        self.state.rooms[room] = {"cursor": cursor, "since": since}
        self.state.save(self.path)


def state_directory(override: Path | None) -> Path:
    from hermes_constants import get_default_hermes_root

    return override.expanduser() if override is not None else get_default_hermes_root() / "state" / "sources"


def check_spool(path: Path, directory: Path) -> None:
    target = path.resolve()
    reserved = {directory.resolve() / f"{kind}.{suffix}" for kind in CALL_LIMITS for suffix in ("json", "lock")}
    if target in reserved or target == directory.resolve() or path.is_symlink():
        raise StateError("unsafe spool")
    if path.exists():
        info = path.stat()
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
            raise StateError("unsafe spool")


@contextmanager
def locked_state(directory: Path, kind: str):
    import fcntl

    if kind not in CALL_LIMITS or directory.is_symlink():
        raise StateError("unsafe state directory")
    _create_state_directory(directory)
    if directory.stat().st_mode & 0o022:
        raise StateError("state directory is writable by others")
    path = directory / f"{kind}.json"
    fd = os.open(directory / f"{kind}.lock", os.O_RDONLY | os.O_CREAT | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600)
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
            raise StateError("unsafe lock file")
        fcntl.flock(fd, fcntl.LOCK_EX)
        yield path, SourceState.load(path)
    finally:
        os.close(fd)
