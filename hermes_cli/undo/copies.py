"""Exact file versions in upstream checkpoints, not a second blob store.

A stable one-file scope avoids checkpoint excludes, user git attributes, broad
directory capture, and recursive capture of the checkpoint store itself. The
scope is empty between operations. Null hashes record absence, which upstream's
single-file checkout cannot restore. Pruning remains entirely upstream-owned.
"""

from __future__ import annotations

from contextlib import contextmanager
from collections.abc import Iterator
from dataclasses import dataclass
from pathlib import Path
import os
import stat

from hermes_constants import reset_hermes_home_override, set_hermes_home_override
from hermes_cli.undo.models import FileRef, UndoError
from hermes_cli.undo.storage import Store, atomic, digest, no_links, private_dir, read_bytes


@dataclass(frozen=True, slots=True)
class Copy:
    sha256: str | None
    checkpoint: str | None
    size: int


@contextmanager
def manager(store: Store) -> Iterator:
    from tools.checkpoint_manager import CheckpointManager

    token = set_hermes_home_override(store.base)
    private_dir(store.base / "checkpoints")
    previous_mask = os.umask(0o077)
    try:
        yield CheckpointManager(enabled=True, max_file_size_mb=0)
    finally:
        os.umask(previous_mask)
        reset_hermes_home_override(token)


def scope(store: Store, path: Path) -> Path:
    folder = store.root / "scopes"
    private_dir(folder)
    folder = folder / digest(str(path).encode())
    private_dir(folder)
    no_links(folder / "payload")
    return folder


def capture(store: Store, path: Path) -> Copy:
    if path == store.root or store.root in path.parents or path == store.base / "checkpoints" or store.base / "checkpoints" in path.parents:
        raise UndoError("cannot snapshot undo or checkpoint storage")
    data = read_bytes(path)
    if data is None:
        return Copy(None, None, 0)
    folder = scope(store, path)
    atomic(folder / "payload", data)
    try:
        with manager(store) as checkpoints:
            checkpoints.ensure_checkpoint(str(folder), "undo pre-write copy")
            rows = checkpoints.list_checkpoints(str(folder))
        if not rows:
            raise UndoError("checkpoint capture failed")
        copy = Copy(digest(data), rows[0]["hash"], len(data))
        # A failed/no-change capture must never certify a different payload.
        reference = FileRef(type="file", path=str(path), sha256=copy.sha256,
                            after=copy.sha256, checkpoint=copy.checkpoint)
        restored_bytes(store, reference)
        return copy
    finally:
        (folder / "payload").unlink(missing_ok=True)


def restored_bytes(store: Store, ref: FileRef) -> bytes | None:
    if ref.sha256 is None:
        return None
    folder = scope(store, Path(ref.path))
    try:
        with manager(store) as checkpoints:
            result = checkpoints.restore(str(folder), ref.checkpoint, "payload")
        if not result["success"]:
            raise UndoError("checkpoint unavailable; inspect lina checkpoints")
        data = read_bytes(folder / "payload")
        if data is None or digest(data) != ref.sha256:
            raise UndoError("missing or corrupt restore copy")
        return data
    finally:
        (folder / "payload").unlink(missing_ok=True)


def restore(store: Store, ref: FileRef) -> None:
    path = Path(ref.path)
    data = restored_bytes(store, ref)
    no_links(path)
    if data is None:
        path.unlink(missing_ok=True)
    else:
        mode = stat.S_IMODE(path.stat().st_mode) if path.exists() else 0o600
        private_dir(path.parent) if not path.parent.exists() else no_links(path.parent)
        atomic(path, data, mode=mode)
