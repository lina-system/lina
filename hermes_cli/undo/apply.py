"""Preflight every object, restore local objects, and journal inverse entries."""

from __future__ import annotations

from pathlib import Path
import time
from typing import assert_never

from hermes_cli.undo import copies, journal, objects
from hermes_cli.undo.env import Env
from hermes_cli.undo.models import Entry, FileRef, LedgerRef, OutsideRef, Reference, SkillRef, UndoError, when
from hermes_cli.undo.pending import current_hash
from hermes_cli.undo.storage import Store


def preflight(store: Store, env: Env, refs: list[Reference]) -> None:
    for ref in refs:
        match ref:
            case FileRef():
                if current_hash(Path(ref.path)) != ref.after:
                    raise UndoError("file changed since this journal entry; resolve before applying")
                copies.restored_bytes(store, ref)
            case SkillRef():
                objects.check_skill(env, ref)
            case LedgerRef():
                objects.check_ledger(ref)
            case OutsideRef():
                pass
            case unreachable:
                assert_never(unreachable)


def apply(store: Store, env: Env, change_id: str) -> None:
    with store.locked(".apply.lock"):
        with store.locked():
            rows = journal.read(store)
            row = next((r for r in rows if r.change_id == change_id), None)
            if row is None:
                raise UndoError("unknown change_id")
            if any(r.undo_of == change_id for r in rows):
                raise UndoError("already applied; apply its inverse journal entry instead")
            if row.kind == "revert" and row.until and when(row.until) <= time.time():
                raise UndoError("revert window has expired")
            refs = row.objects if row.kind == "revert" else []
            preflight(store, env, refs)
            fresh = {r.path: copies.capture(store, Path(r.path)) for r in refs if isinstance(r, FileRef)}
        inverse = []
        complete = False
        try:
            for ref in refs:
                match ref:
                    case FileRef():
                        with store.locked():
                            if current_hash(Path(ref.path)) != ref.after:
                                raise UndoError("file changed during apply; resolve before continuing")
                            copies.restore(store, ref)
                        copy = fresh[ref.path]
                        inverse.append(ref.model_copy(update={"sha256": copy.sha256, "checkpoint": copy.checkpoint,
                                                               "after": ref.sha256, "shrunk": None}))
                    case SkillRef():
                        inverse.append(objects.restore_skill(env, ref))
                    case LedgerRef():
                        inverse.append(objects.restore_ledger(ref))
                    case OutsideRef():
                        pass
                    case unreachable:
                        assert_never(unreachable)
            complete = True
        finally:
            if inverse:
                at, inverse_id = journal.identity()
                journal.append(store, Entry(at=at, change_id=inverse_id, bot=row.bot,
                                            what="Undo " + change_id + ("" if complete else " (partial)"),
                                            objects=list(reversed(inverse)), undo_of=change_id,
                                            how="Apply this journal entry to undo the undo"))
                print(("Applied" if complete else "Partially applied") + "; inverse change_id=" + inverse_id)
        if row.kind != "revert" or any(isinstance(r, OutsideRef) for r in row.objects):
            print("Bot step (not executed): " + row.how)
