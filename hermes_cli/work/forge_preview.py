"""Opt-in previews for forges that serve Git's remote archive protocol."""

from __future__ import annotations

from contextlib import suppress
import os
import re
import selectors
import signal
import subprocess
import tarfile
import time
from typing import Final

from hermes_cli.work.forge_files import ForgeError, _REPO_ENV, _path
from hermes_platform.resolver import locate_command

MAX_PREVIEW_BYTES: Final = 1024 * 1024
_TAR_OVERHEAD: Final = 128 * 1024
_READ_SIZE: Final = 8192
_TIMEOUT_SECONDS = 10.0
_COMMIT: Final = re.compile(r"(?:[0-9a-f]{40}|[0-9a-f]{64})")


class _ArchiveStream:
    """Supply tar bytes under a single deadline while draining Git diagnostics."""

    def __init__(self, selector: selectors.BaseSelector, limit: int) -> None:
        self.selector = selector
        self.deadline = time.monotonic() + _TIMEOUT_SECONDS
        self.remaining = limit + _TAR_OVERHEAD
        self.stderr = bytearray()

    def read(self, size: int) -> bytes:
        while self.selector.get_map():
            remaining_time = self.deadline - time.monotonic()
            if remaining_time <= 0:
                raise ForgeError(504, "Git archive preview timed out.")
            for key, _ in self.selector.select(remaining_time):
                data = os.read(key.fd, min(size, self.remaining) if key.data == "archive" else _READ_SIZE)
                if key.data == "archive" and self.remaining == 0:
                    raise ForgeError(502, "Git archive preview exceeded its byte budget.")
                if not data:
                    self.selector.unregister(key.fd)
                elif key.data == "archive":
                    self.remaining -= len(data)
                    return data
                else:
                    self.stderr.extend(data[:_READ_SIZE - len(self.stderr)])
        return b""

    def invalid_archive(self) -> ForgeError:
        if b"did not match any files" in self.stderr:
            return ForgeError(404, "File not found in the Git archive.")
        if b"operation not supported by protocol" in self.stderr.lower():
            return ForgeError(501, "The Git transport does not support remote archives.")
        return ForgeError(502, "The Git remote did not provide a valid archive preview.")


def _read_prefix(stream: _ArchiveStream, path: str, limit: int) -> tuple[str, bytes, int]:
    try:
        with tarfile.open(fileobj=stream, mode="r|", bufsize=_READ_SIZE, encoding="utf-8", errors="strict") as archive:
            for member in archive:
                if member.name != path:
                    if member.isdir() and path.startswith(member.name + "/"):
                        continue
                    raise stream.invalid_archive()
                if member.type not in (tarfile.REGTYPE, tarfile.AREGTYPE):
                    raise ForgeError(404, "File not found in the Git archive.")
                commit = archive.pax_headers.get("comment", "")
                file = archive.extractfile(member)
                if not _COMMIT.fullmatch(commit) or file is None or member.size < 0:
                    raise stream.invalid_archive()
                with file:
                    size = min(limit, member.size)
                    # BufferedReader.read() can prefetch an entire filesystem block
                    # for a one-byte preview. read1() bounds the underlying request.
                    prefix = file.read1(size)
                if len(prefix) != size:
                    raise stream.invalid_archive()
                return commit, prefix, member.size
    except (tarfile.TarError, OSError, UnicodeError, ValueError) as exc:
        raise stream.invalid_archive() from exc
    raise ForgeError(404, "File not found in the Git archive. It may be marked export-ignore.")


def _stop_archive(process: subprocess.Popen[bytes]) -> None:
    # Stop the transport as well as Git so the server sees the connection close.
    with suppress(ProcessLookupError):
        os.killpg(process.pid, signal.SIGKILL)  # windows-footgun: ok — preview only starts Git on POSIX
    try:
        process.wait(timeout=1)
    except subprocess.TimeoutExpired as exc:
        raise ForgeError(502, "Git archive preview could not be stopped.") from exc


def preview(url: str, path: str, limit: int) -> tuple[str, bytes, int]:
    """Return HEAD's commit ID, an archive member's prefix, and its archived size.

    Git applies committed export attributes: export-ignore hides a file and
    export-subst can change its bytes and size. This API requires upload-archive
    support and performs no fallback fetch of the blob.
    """
    _path(path)
    if path.startswith(("-", ":")) or any(char in path for char in "*?[]"):
        raise ForgeError(400, "Expected a literal Git archive path.")
    if type(limit) is not int or not 0 <= limit <= MAX_PREVIEW_BYTES:
        raise ForgeError(400, f"Preview size must be between 0 and {MAX_PREVIEW_BYTES} bytes.")
    if os.name != "posix":
        raise ForgeError(501, "Remote Git archive preview is unavailable on this host.")

    git = locate_command("git").command
    if not git:
        raise ForgeError(502, "Git is unavailable.")
    env = {key: value for key, value in os.environ.items() if key not in _REPO_ENV}
    env.update(LC_ALL="C", GIT_TERMINAL_PROMPT="0")
    command = [*git, "archive", "--format=tar", f"--remote={url}", "HEAD", "--", path]
    try:
        # windows-footgun: ok — POSIX process groups allow the transport to be killed too
        process = subprocess.Popen(
            command, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            env=env, start_new_session=True,  # detached-spawn: ok — killed and reaped below
        )
    except OSError as exc:
        raise ForgeError(502, "Git archive could not be started.") from exc

    try:
        assert process.stdout is not None and process.stderr is not None
        with process.stdout, process.stderr, selectors.DefaultSelector() as selector:
            selector.register(process.stderr, selectors.EVENT_READ, "diagnostics")
            selector.register(process.stdout, selectors.EVENT_READ, "archive")
            return _read_prefix(_ArchiveStream(selector, limit), path, limit)
    finally:
        _stop_archive(process)
