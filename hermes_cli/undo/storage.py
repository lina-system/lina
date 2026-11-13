"""Private metadata writes and cooperative process serialization."""

from __future__ import annotations

from collections.abc import Iterator
from contextlib import contextmanager
from datetime import datetime
import fcntl  # windows-footgun: ok -- undo is a POSIX hub command, like work
import hashlib
import os
from pathlib import Path
import tempfile
import time

from hermes_cli.lina_deps import lina_root
from hermes_cli.undo.models import UndoError


def timestamp() -> str:
    return datetime.fromtimestamp(time.time()).astimezone().isoformat(timespec="seconds")


def digest(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def no_links(path: Path) -> None:
    if any(p.is_symlink() for p in (path, *path.parents)):
        raise UndoError("symbolic links are not supported for undo paths")


def private_dir(path: Path) -> None:
    no_links(path)
    path.mkdir(parents=True, exist_ok=True, mode=0o700)
    path.chmod(0o700)


def read_bytes(path: Path) -> bytes | None:
    no_links(path)
    try:
        return path.read_bytes()
    except FileNotFoundError:
        return None


def atomic(path: Path, data: bytes, *, mode: int = 0o600) -> None:
    no_links(path)
    fd, name = tempfile.mkstemp(prefix=".undo-", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as stream:
            os.fchmod(stream.fileno(), mode)
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(name, path)
    finally:
        Path(name).unlink(missing_ok=True)


class Store:
    def __init__(self) -> None:
        self.base = lina_root()
        self.root = self.base / "undo"

    @contextmanager
    def locked(self, name: str = ".lock") -> Iterator[Store]:
        private_dir(self.root)
        path = self.root / name
        no_links(path)
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as stream:
            os.fchmod(stream.fileno(), 0o600)
            fcntl.flock(stream.fileno(), fcntl.LOCK_EX)
            try:
                yield self
            finally:
                fcntl.flock(stream.fileno(), fcntl.LOCK_UN)
