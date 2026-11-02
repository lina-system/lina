"""Short-lived curator and work subprocess boundaries and their receipts."""

from __future__ import annotations

import os
from pathlib import Path
import subprocess
import sys

from pydantic import Field

from hermes_cli.undo.env import Env
from hermes_cli.undo.models import LedgerRef, Model, SkillRef, UndoError
from hermes_cli.undo.pending import current_hash
from hermes_cli.undo.storage import read_bytes


class SkillFile(Model):
    path: str
    sha256: str


class Evidence(Model):
    # Curator's evidence is extensible; only rollback_target participates here.
    model_config = {"extra": "ignore", "frozen": True}
    rollback_target: str | None = None


class SkillEntry(Model):
    model_config = {"extra": "ignore", "frozen": True}
    id: str
    action: str
    before: list[SkillFile] = Field(default_factory=list)
    after: list[SkillFile] = Field(default_factory=list)
    evidence: Evidence = Field(default_factory=Evidence)


def skill_rows(env: Env, ref: SkillRef) -> list[SkillEntry]:
    if ref.profile not in env.profiles:
        raise UndoError("unknown skill profile")
    data = read_bytes(env.profiles[ref.profile] / "skills/.curator_ledger.jsonl")
    if data is None:
        raise UndoError("missing skill ledger")
    return [SkillEntry.model_validate_json(line) for line in data.splitlines() if line.strip()]


def skill_paths(row: SkillEntry) -> set[str]:
    return {f.path for f in row.before + row.after}


def check_skill(env: Env, ref: SkillRef) -> None:
    rows = skill_rows(env, ref)
    index = next((i for i, r in enumerate(rows) if r.id == ref.entry_id), None)
    if index is None:
        raise UndoError("skill entry no longer exists")
    touched = skill_paths(rows[index])
    if any(touched & skill_paths(r) for r in rows[index + 1:]):
        raise UndoError("skill has later changes; owning bot must resolve them first")
    expected = {f.path: f.sha256 for f in rows[index].after}
    for name in touched:
        path = Path(name)
        if not path.is_absolute():
            path = env.profiles[ref.profile] / "skills" / path
        if current_hash(path) != expected.get(name):
            raise UndoError("skill changed since its ledger entry")


def run(argv: list[str], env: dict[str, str]) -> None:
    result = subprocess.run(argv, env=env, stdin=subprocess.DEVNULL,
                            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=120, check=False)
    if result.returncode:
        raise UndoError("local undo command failed; inspect the owning tool's state")


def restore_skill(env: Env, ref: SkillRef) -> SkillRef:
    check_skill(env, ref)
    ids = {r.id for r in skill_rows(env, ref)}
    child_env = dict(os.environ, HERMES_HOME=str(env.profiles[ref.profile]))
    run([sys.executable, "-m", "hermes_cli.main", "curator", "rollback", ref.entry_id, "--yes"], child_env)
    candidates = [r for r in skill_rows(env, ref) if r.id not in ids and r.action == "rollback"
                  and r.evidence.rollback_target == ref.entry_id]
    if len(candidates) != 1:
        raise UndoError("curator returned without one rollback receipt; inspect the skill ledger")
    return SkillRef(type="skill", profile=ref.profile, entry_id=candidates[0].id)


def ledger_revision(ref: LedgerRef) -> int:
    from hermes_cli.work import env, ledger

    env.bind(env.load())
    try:
        rows = [r for r in ledger.rows(ref.id) if not r.get("_meta")]
        if not rows:
            raise UndoError("unknown work item")
        return int(rows[-1]["rev"])
    finally:
        ledger.close()


def check_ledger(ref: LedgerRef) -> None:
    if ledger_revision(ref) != ref.after:
        raise UndoError("work item changed since this journal entry")


def restore_ledger(ref: LedgerRef) -> LedgerRef:
    check_ledger(ref)
    run([sys.executable, "-m", "hermes_cli.main", "work", "undo", ref.id, "--to", str(ref.rev),
         "--expect", str(ref.after), "--why", "Undo recorded change"], dict(os.environ))
    if ledger_revision(ref) != ref.after + 1:
        raise UndoError("work returned without one undo revision; inspect the ledger")
    return LedgerRef(type="ledger", id=ref.id, rev=ref.after, after=ref.after + 1)
