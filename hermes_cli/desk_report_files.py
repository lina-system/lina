"""Read Desk markdown beneath the allowlist, with the owner's report-deny globs."""

from __future__ import annotations

import errno
import fnmatch
import os
import stat
from contextlib import ExitStack
from dataclasses import dataclass
from pathlib import Path

from agent.personal_work import MARKER_FILE

ALLOWED = ("1_Goals", "2_Areas")


class Refused(ValueError):
    """A source that must not be published."""


@dataclass(frozen=True)
class Source:
    path: str
    identity: str
    body: str
    title: str


def identity(info: os.stat_result) -> str:
    return f"{info.st_dev}:{info.st_ino}"


def _personal(directory: int) -> None:
    try:
        os.stat(MARKER_FILE, dir_fd=directory, follow_symlinks=False)
    except FileNotFoundError:
        return
    raise Refused("personal folder")


def read_source(root: Path, name: str, deny: list[str]) -> Source:
    target = Path(name.removeprefix("desk:")).expanduser()
    try:
        path = (root / target).relative_to(root)
    except ValueError as exc:
        raise Refused("outside the allowed Desk folders") from exc
    if not path.parts or path.parts[0] not in ALLOWED or ".." in path.parts:
        raise Refused("outside the allowed Desk folders")
    if path.suffix.lower() != ".md":
        raise Refused("only markdown (.md) files are allowed")
    if any(part.casefold() in {"private", "personal"} for part in path.parts[:-1]):
        raise Refused("personal folder")
    if any(fnmatch.fnmatchcase(p.as_posix(), glob) for p in (path, *path.parents) for glob in deny):
        raise Refused("matched the report deny list")

    # Resolve each component against the directory already opened. A rename or
    # symlink swap between validation and read cannot redirect the final open.
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
            except OSError as exc:
                if exc.errno in {errno.ELOOP, errno.ENOTDIR}:
                    raise Refused("link or non-directory in the report path") from exc
                raise
            stack.callback(os.close, fd)
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
            raise Refused("only regular files without hard links are allowed")
        with open(fd, encoding="utf-8", closefd=False) as stream:
            body = stream.read().strip()
    title = next((line[2:].strip() for line in body.splitlines() if line.startswith("# ") and line[2:].strip()),
                 path.stem)
    return Source(path.as_posix(), identity(info), body, title)
