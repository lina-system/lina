"""Read Desk markdown beneath the allowlist, with the owner's report-deny globs."""

from __future__ import annotations

import errno
import fnmatch
import os
import stat
from collections.abc import Iterator, Sequence
from contextlib import ExitStack, contextmanager
from dataclasses import dataclass
from pathlib import Path
from typing import TYPE_CHECKING

from agent.personal_work import MARKER_FILE

if TYPE_CHECKING:
    from hermes_cli.desk_report_images import Image

ALLOWED = ("1_Goals", "2_Areas")
MARKDOWN_LIMIT = 2 * 1024**2
PERSONAL_FOLDERS = ("사람", "인물", "people", "person", "카톡", "카카오", "kakao", "katalk", "메일",
                    "mail", "murmur", "private", "개인")


class Refused(ValueError):
    """A source that must not be published."""


@dataclass(frozen=True)
class Source:
    path: str
    identity: str
    body: str
    title: str
    images: tuple[Image, ...] = ()
    diagnostics: tuple[str, ...] = ()


def identity(info: os.stat_result) -> str:
    return f"{info.st_dev}:{info.st_ino}"


def _personal(directory: int) -> None:
    try:
        os.stat(MARKER_FILE, dir_fd=directory, follow_symlinks=False)
    except FileNotFoundError:
        return
    raise Refused("personal folder")


def _check_desk_path(path: Path, deny: Sequence[str]) -> None:
    if not path.parts or path.parts[0] not in ALLOWED or ".." in path.parts or "\\" in str(path):
        raise Refused("outside the allowed Desk folders")
    if any(part.casefold().startswith(PERSONAL_FOLDERS) for part in path.parts[:-1]):
        raise Refused("personal folder")
    if any(fnmatch.fnmatchcase(p.as_posix(), glob) for p in (path, *path.parents) for glob in deny):
        raise Refused("matched the report deny list")


def check_path(name: str, deny: Sequence[str]) -> Path:
    path = Path(name)
    _check_desk_path(path, deny)
    if path.suffix.lower() != ".md":
        raise Refused("only markdown (.md) files are allowed")
    return path


@contextmanager
def open_regular(root: Path, path: Path, *, missing_ok: bool = False) -> Iterator[int | None]:
    """Walk from a trusted root descriptor, checking markers before each descent."""
    if not path.parts or path.is_absolute() or ".." in path.parts:
        raise Refused("expected a relative path beneath the root")
    with ExitStack() as stack:
        fd = os.open(root, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        stack.callback(os.close, fd)
        for i, part in enumerate(path.parts):
            _personal(fd)
            flags = os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK
            if i < len(path.parts) - 1:
                flags |= os.O_DIRECTORY
            try:
                fd = os.open(part, flags, dir_fd=fd)
            except FileNotFoundError:
                if missing_ok:
                    yield None
                    return
                raise
            except OSError as exc:
                if exc.errno in {errno.ELOOP, errno.ENOTDIR}:
                    raise Refused("link or non-directory in the report path") from exc
                raise
            stack.callback(os.close, fd)
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
            raise Refused("only regular files without hard links are allowed")
        yield fd


def _current_file_identity(root: Path, path: Path) -> str | None:
    with open_regular(root, path, missing_ok=True) as fd:
        return None if fd is None else identity(os.fstat(fd))


def current_identity(root: Path, name: str, deny: Sequence[str]) -> str | None:
    return _current_file_identity(root, check_path(name, deny))


def bounded_read(fd: int, limit: int) -> bytes:
    if os.fstat(fd).st_size <= limit:
        with open(fd, "rb", closefd=False) as stream:
            data = stream.read(limit + 1)
        if len(data) <= limit:
            return data
    raise Refused(f"file exceeds {limit // 1024**2} MiB limit")


def read_source(root: Path, name: str, deny: Sequence[str]) -> Source:
    from hermes_cli.desk_report_images import live_images

    target = Path(name.removeprefix("desk:")).expanduser()
    try:
        relative = (root / target).relative_to(root)
    except ValueError as exc:
        raise Refused("outside the allowed Desk folders") from exc
    path = check_path(relative.as_posix(), deny)
    with open_regular(root, path) as fd:
        assert fd is not None
        file_id = identity(os.fstat(fd))
        body = bounded_read(fd, MARKDOWN_LIMIT).decode("utf-8").strip()
        body = body.replace("\r\n", "\n").replace("\r", "\n")
    title = next((line[2:].strip() for line in body.splitlines() if line.startswith("# ") and line[2:].strip()),
                 path.stem)
    images, diagnostics = live_images(root, path, body, deny)
    return Source(path.as_posix(), file_id, body, title, images, diagnostics)
