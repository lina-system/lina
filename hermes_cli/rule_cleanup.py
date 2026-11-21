"""Remove committed standing rules from an offline copied home after a Lina snapshot."""

from __future__ import annotations

from collections import defaultdict
from dataclasses import dataclass
from pathlib import Path
from typing import TYPE_CHECKING

from hermes_cli.undo import journal, pending, snapshot
from hermes_cli.undo.models import FileRef, UndoError
from hermes_cli.undo.storage import Store, atomic, digest, read_bytes

if TYPE_CHECKING:
    from hermes_cli.rule_census import Candidate


@dataclass(frozen=True, slots=True)
class _Edit:
    path: Path
    before: bytes
    after: bytes
    mode: int

    def require_original(self, store: Store) -> None:
        if snapshot.local_file_mode(store, self.path) != self.mode or read_bytes(self.path) != self.before:
            raise UndoError("a selected source changed during cleanup")


def _apply_edits(store: Store, edits: dict[Path, _Edit]) -> str:
    captured = snapshot.stage_locked(store, list(edits))
    change_id = captured.change_id
    pending_path = pending.pending_path(store, change_id)
    refs: list[FileRef] = []
    written: list[_Edit] = []
    try:
        for ref in captured.objects:
            if not isinstance(ref, FileRef):
                raise UndoError("snapshot did not capture every selected file")
            edit = edits[Path(ref.path)]
            if ref.sha256 != digest(edit.before):
                raise UndoError("a selected source changed before snapshot capture")
            edit.require_original(store)
            refs.append(ref.model_copy(update={"after": digest(edit.after)}))
        for edit in edits.values():
            edit.require_original(store)
            atomic(edit.path, edit.after, mode=edit.mode)
            written.append(edit)
    except (OSError, UndoError):
        try:
            for edit in reversed(written):
                snapshot.local_file_mode(store, edit.path)
                if read_bytes(edit.path) != edit.after:
                    raise UndoError("a source changed before cleanup could roll back")
                atomic(edit.path, edit.before, mode=edit.mode)
            pending_path.unlink()
        except (OSError, UndoError):
            raise UndoError(f"cleanup interrupted; inspect pending undo snapshot {change_id}") from None
        raise

    try:
        journal.append_locked(store, captured.model_copy(update={"objects": refs}))
        pending_path.unlink()
    except (OSError, UndoError, ValueError):
        # A durable pending batch can still be closed with `lina undo end`.
        raise UndoError(f"cleanup could not close undo snapshot {change_id}") from None
    return change_id


def cleanup(copy: Path, selected: list[Candidate]) -> str | None:
    """Return one undo change ID for committed candidates, or None for an empty selection."""
    if not selected:
        return None

    from hermes_cli.rule_census import CensusError, copy_root, remove_candidates

    try:
        store = Store(copy_root(copy))
        grouped: dict[Path, list[Candidate]] = defaultdict(list)
        for candidate in selected:
            grouped[candidate.path].append(candidate)
        with store.locked():
            edits = {}
            for path, candidates in grouped.items():
                mode = snapshot.local_file_mode(store, path)
                before = read_bytes(path)
                if before is None:
                    raise UndoError("a selected source disappeared before cleanup")
                after = remove_candidates(before, candidates)
                if after == before:
                    raise UndoError("snapshot would contain an unchanged selected source")
                edits[path] = _Edit(path, before, after, mode)
            return _apply_edits(store, edits)
    except CensusError as error:
        raise UndoError(str(error)) from None
    except OSError:
        raise UndoError("cleanup could not read or write copied-home files") from None
