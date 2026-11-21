"""Read and update files on the default branch of a registered Git remote."""

from __future__ import annotations

from collections.abc import Iterator
from contextlib import contextmanager
from dataclasses import dataclass
import os
from pathlib import Path
import re
import subprocess
from tempfile import TemporaryDirectory
from typing import Final, TypedDict

from hermes_platform.resolver import locate_command


class ForgeError(RuntimeError):
    """A Git service error with a status and public message safe for callers."""

    def __init__(self, status: int, message: str) -> None:
        super().__init__(message)
        self.status = status


class _Listing(TypedDict):
    name: str
    type: str


class _Tree(TypedDict):
    commit: str | None
    entries: list[_Listing]


class _Commit(TypedDict):
    commit: str


class _HistoryCommit(_Commit):
    author: str
    date: str
    subject: str


class _History(TypedDict):
    commits: list[_HistoryCommit]


_MODES: Final = {
    b"040000": "folder", b"100644": "file", b"100755": "file",
    b"120000": "symlink", b"160000": "submodule",
}
_OID: Final = re.compile(r"(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})")
_REPO_ENV: Final = frozenset({
    "GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE",
    "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE",
    "GIT_QUARANTINE_PATH", "GIT_GRAFT_FILE", "GIT_SHALLOW_FILE",
})


@dataclass(frozen=True, slots=True)
class _Entry:
    mode: bytes
    kind: bytes
    oid: str

    @property
    def entry_type(self) -> str:
        return _MODES.get(self.mode, "special")

    def record(self, name: bytes) -> bytes:
        return b" ".join((self.mode, self.kind, self.oid.encode("ascii"))) + b"\t" + name + b"\0"


@dataclass(frozen=True, slots=True)
class _Git:
    executable: str
    directory: Path

    def run(
        self, *args: str, data: bytes | None = None, allow_failure: bool = False,
        lazy: bool = False, extra_env: dict[str, str] | None = None,
    ) -> subprocess.CompletedProcess[bytes]:
        env = {key: value for key, value in os.environ.items() if key not in _REPO_ENV}
        env.update(LC_ALL="C", GIT_TERMINAL_PROMPT="0", GIT_NO_REPLACE_OBJECTS="1",
                   GIT_NO_LAZY_FETCH="0" if lazy else "1")
        if extra_env:
            env.update(extra_env)
        command = [
            self.executable, "--literal-pathspecs", "-c", "protocol.version=2",
            "-c", "gc.auto=0", "-c", "maintenance.auto=false", *args,
        ]
        try:
            result = subprocess.run(command, cwd=self.directory, env=env, input=data,
                                    stdin=subprocess.DEVNULL if data is None else None,
                                    capture_output=True, timeout=60, check=False)
        except subprocess.TimeoutExpired:
            raise ForgeError(504, "Git operation timed out.") from None
        except OSError:
            raise ForgeError(502, "Git could not be started.") from None
        if result.returncode and not allow_failure:
            raise ForgeError(502, "Git operation failed.")
        return result

    def oid(self, *args: str, data: bytes | None = None) -> str:
        return self.run(*args, data=data).stdout.strip().decode("ascii")


def _server_packets(trace: bytes) -> Iterator[bytes]:
    for line in trace.splitlines():
        identity, _, packet = line.partition(b"packet:")[2].strip().partition(b" ")
        if identity.endswith(b"<"):
            yield packet


@contextmanager
def _clone(url: str) -> Iterator[tuple[_Git, str, str | None]]:
    """Check filter support before any pack transfer and clone just the default branch."""
    command = locate_command("git").command
    if not command:
        raise ForgeError(502, "Git is unavailable.")
    # Git resolves plain local paths relative to the caller, even though it runs in scratch space.
    if url and ":" not in url:
        url = os.path.abspath(url)
    try:
        scratch = TemporaryDirectory(prefix="work-forge-", dir=os.environ.get("TMPDIR"))
    except OSError:
        raise ForgeError(502, "Git scratch space is unavailable.") from None
    with scratch as directory:
        setup = _Git(command[0], Path(directory))
        probe = setup.run("ls-remote", "--symref", "--", url, extra_env={"GIT_TRACE_PACKET": "1"})
        packets = set(_server_packets(probe.stderr))
        if not any(packet.startswith(b"fetch=") and b"filter" in packet.split()
                   for packet in packets):
            raise ForgeError(502, "The Git remote does not support filtered fetches.")
        clone = setup.run("clone", "--no-local", "--bare", "--no-checkout", "--filter=blob:none",
                          "--single-branch", "--no-tags", "--template=", "--", url, "repo.git")
        if b"filtering not recognized" in clone.stderr:
            raise ForgeError(502, "The Git remote does not support filtered fetches.")
        git = _Git(command[0], setup.directory / "repo.git")
        branch = os.fsdecode(git.run("symbolic-ref", "-q", "HEAD", allow_failure=True).stdout.rstrip(b"\n"))
        if not branch.startswith("refs/heads/"):
            raise ForgeError(502, "The Git remote has no default branch.")
        head = git.run("rev-parse", "--verify", "--quiet", "HEAD^{commit}", allow_failure=True)
        if head.returncode and (probe.stdout or b"unborn HEAD symref-target:" + os.fsencode(branch) not in packets):
            raise ForgeError(502, "The Git remote has no default branch commit.")
        yield git, branch, head.stdout.strip().decode("ascii") or None


def _path(path: str, *, root: bool = False) -> tuple[bytes, ...]:
    if root and path == "":
        return ()
    parts = path.split("/")
    if (any(part.lower() in {"", ".", "..", ".git"} for part in parts)
            or "\\" in path or re.match(r"^[a-zA-Z]:", path)
            or any(ord(char) < 32 or ord(char) == 127 for char in path)):
        raise ForgeError(400, "Invalid repository path.")
    try:
        return tuple(part.encode("utf-8") for part in parts)
    except UnicodeEncodeError:
        raise ForgeError(400, "Invalid repository path.") from None


def _revision(git: _Git, head: str | None, rev: str | None) -> str:
    if rev is not None and not _OID.fullmatch(rev):
        raise ForgeError(400, "Expected a full commit ID.")
    if head is None:
        raise ForgeError(404, "Commit not found on the default branch.")
    if rev is None or rev.lower() == head:
        return head
    oid = rev.lower()
    resolved = git.run("rev-parse", "--verify", "--quiet", f"{oid}^{{commit}}", allow_failure=True)
    if resolved.stdout.strip() != oid.encode("ascii"):
        raise ForgeError(404, "Commit not found on the default branch.")
    ancestor = git.run("merge-base", "--is-ancestor", oid, head, allow_failure=True)
    if ancestor.returncode:
        raise ForgeError(404, "Commit not found on the default branch.")
    return oid


def _entries(git: _Git, oid: str | None) -> dict[bytes, _Entry]:
    if oid is None:
        return {}
    entries: dict[bytes, _Entry] = {}
    for record in git.run("ls-tree", "-z", oid).stdout.split(b"\0")[:-1]:
        metadata, name = record.split(b"\t", 1)
        mode, kind, child = metadata.split(b" ")
        entries[name] = _Entry(mode, kind, child.decode("ascii"))
    return entries


def _directory(git: _Git, commit: str, parts: tuple[bytes, ...]) -> str:
    oid = commit
    for name in parts:
        entry = _entries(git, oid).get(name)
        if entry is None or entry.entry_type != "folder":
            raise ForgeError(404, "Repository path not found.")
        oid = entry.oid
    return oid


def _file_entry(git: _Git, commit: str, parts: tuple[bytes, ...]) -> _Entry:
    directory = _directory(git, commit, parts[:-1])
    entry = _entries(git, directory).get(parts[-1])
    if entry is None or entry.entry_type != "file":
        raise ForgeError(404, "Repository path not found.")
    return entry


def tree(url: str, path: str = "", rev: str | None = None) -> _Tree:
    """List immediate children of a directory at a default-branch commit."""
    parts = _path(path, root=True)
    with _clone(url) as (git, _, head):
        if head is None and rev is None and not parts:
            return {"commit": None, "entries": []}
        commit = _revision(git, head, rev)
        directory = _directory(git, commit, parts)
        entries: list[_Listing] = [
            {"name": name.decode("utf-8", errors="replace"), "type": entry.entry_type}
            for name, entry in _entries(git, directory).items()
        ]
        return {"commit": commit, "entries": entries}


def blob(url: str, path: str, rev: str | None = None) -> tuple[str, bytes]:
    """Return the commit and complete bytes of a regular file."""
    parts = _path(path)
    with _clone(url) as (git, _, head):
        commit = _revision(git, head, rev)
        entry = _file_entry(git, commit, parts)
        return commit, git.run("cat-file", "blob", entry.oid, lazy=True).stdout


def history(url: str, path: str) -> _History:
    """Return newest-first default-branch history for the literal path."""
    _path(path)
    with _clone(url) as (git, _, head):
        if head is None:
            return {"commits": []}
        fields = git.run(
            "log", "-z", "--encoding=UTF-8", "--format=%H%x00%an%x00%aI%x00%s", head, "--", path,
        ).stdout.decode("utf-8", errors="replace").split("\0")[:-1]
        commits: list[_HistoryCommit] = [
            {"commit": commit, "author": author, "date": date, "subject": subject}
            for commit, author, date, subject in zip(
                fields[::4], fields[1::4], fields[2::4], fields[3::4], strict=True,
            )
        ]
        return {"commits": commits}


def diff(url: str, path: str, old: str, new: str) -> str:
    """Return the unified diff for a literal path between reachable commits."""
    _path(path)
    with _clone(url) as (git, _, head):
        before, after = _revision(git, head, old), _revision(git, head, new)
        return git.run(
            "diff", "--patch", "--no-renames", "--no-ext-diff", "--no-textconv", "--no-color",
            "--src-prefix=a/", "--dst-prefix=b/", "--submodule=short", before, after, "--", path,
            lazy=True,
        ).stdout.decode("utf-8", errors="replace")


def _replace_file(
    git: _Git, head: str | None, parts: tuple[bytes, ...], oid: str, mode: bytes | None,
) -> str:
    """Rebuild only trees along the path, retaining the object IDs of untouched entries."""
    parents: list[tuple[dict[bytes, _Entry], bytes]] = []
    entries = _entries(git, head)
    for name in parts[:-1]:
        child = entries.get(name)
        if child is not None and child.entry_type != "folder":
            raise ForgeError(409, "A parent of the repository path is not a folder.")
        parents.append((entries, name))
        entries = _entries(git, child.oid if child else None)
    name = parts[-1]
    previous = entries.get(name)
    if previous is not None and previous.entry_type != "file":
        raise ForgeError(409, "The repository path is not a regular file.")
    file_mode = mode or (previous.mode if previous else b"100644")
    parents.append((entries, name))
    replacement = _Entry(file_mode, b"blob", oid)
    for entries, name in reversed(parents):
        entries[name] = replacement
        records = b"".join(entry.record(child_name) for child_name, entry in entries.items())
        replacement = _Entry(b"040000", b"tree", git.oid("mktree", "-z", "--missing", data=records))
    return replacement.oid


def _publish(git: _Git, branch: str, head: str | None, root: str, author: str, message: str) -> _Commit:
    if not author.strip() or any(char in author for char in "\0\r\n<>"):
        raise ForgeError(400, "Invalid commit author.")
    ident = {
        "GIT_AUTHOR_NAME": author, "GIT_COMMITTER_NAME": author,
        "GIT_AUTHOR_EMAIL": "work@localhost", "GIT_COMMITTER_EMAIL": "work@localhost",
    }
    parent = ["-p", head] if head else []
    commit = git.run("commit-tree", root, *parent, "--no-gpg-sign", "-m", message, extra_env=ident)
    oid = commit.stdout.strip().decode("ascii")
    # An exact lease on the destination also checks that an unborn branch is still absent.
    pushed = git.run("push", "--porcelain", "--no-verify", "--no-thin", "--recurse-submodules=no",
                     f"--force-with-lease={branch}:{head or ''}", "origin", f"{oid}:{branch}",
                     allow_failure=True)
    if (any(line.startswith(b"=\t") for line in pushed.stdout.splitlines())
            or b"(stale info)" in pushed.stdout):
        raise ForgeError(409, "The default branch changed. Reload and try again.")
    if pushed.returncode:
        # Receive-pack can detect a second writer after advertising the old ref.
        # Read the ref on rejection because servers describe that race differently.
        remote = git.run("ls-remote", "--refs", "--", "origin", branch, allow_failure=True)
        if remote.returncode == 0:
            actual = next((line.partition(b"\t")[0].decode("ascii") for line in remote.stdout.splitlines()
                           if line.partition(b"\t")[2] == os.fsencode(branch)), None)
            _check_base(head, actual)
        raise ForgeError(502, "Git could not update the default branch.")
    return {"commit": oid}


def _check_base(base: str | None, head: str | None) -> None:
    if base != head:
        raise ForgeError(409, "The default branch changed. Reload and try again.")


def save(url: str, path: str, base: str | None, content: bytes, author: str) -> _Commit:
    """Write bytes and advance the default branch if it still matches the base."""
    parts = _path(path)
    with _clone(url) as (git, branch, head):
        _check_base(base, head)
        oid = git.oid("hash-object", "-w", "--stdin", data=content)
        root = _replace_file(git, head, parts, oid, None)
        return _publish(git, branch, head, root, author, f"Save {path}")


def restore(url: str, path: str, base: str | None, source: str, author: str) -> _Commit:
    """Create a new commit with the path's bytes and mode from a reachable commit."""
    parts = _path(path)
    with _clone(url) as (git, branch, head):
        _check_base(base, head)
        original = _revision(git, head, source)
        entry = _file_entry(git, original, parts)
        root = _replace_file(git, head, parts, entry.oid, entry.mode)
        # Pack generation can need this blob even when it already exists in remote history.
        git.run("cat-file", "-e", entry.oid, lazy=True)
        return _publish(git, branch, head, root, author, f"Restore {path}")
