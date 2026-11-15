"""Carry a Hermes agent's heart into a LINA home: its files unchanged, its trial journal as ``heart`` rows."""

from __future__ import annotations

import datetime as dt
import json
import os
from contextlib import closing
from dataclasses import asdict, dataclass, field
from pathlib import Path
from typing import Literal

from pydantic import ValidationError

from hermes_cli.heart import rows, storage
from hermes_cli.heart.journal import conclusion
from hermes_cli.heart.locale import text
from hermes_cli.heart.models import Entry, Pref, Record
from hermes_state_store import MessageStore

FILES = ("SOUL.self.md", "heart/preferences.json", "heart/changes.jsonl", "heart/temperament-seed.json")
DIRECTORIES = ("heart/quiz", "heart/self-backups")
JOURNAL = "heart/journal.jsonl"


class TrialLine(Record):
    """A journal line as the trial wrote it: evidence cites LCM ``store_id``s."""
    ts: str
    event: str
    feeling: str
    note: str
    strength: int
    via: Literal["turn", "review"] = "turn"
    pref: Pref | None = None
    evidence: tuple[dict, ...] = ()


@dataclass
class Carried:
    copied: list[str] = field(default_factory=list)
    same: list[str] = field(default_factory=list)
    skipped: list[tuple[str, str]] = field(default_factory=list)
    lines: int = 0
    written: int = 0
    present: int = 0
    unmapped: list[tuple[int, list[str]]] = field(default_factory=list)
    unreadable: list[tuple[int, str]] = field(default_factory=list)

    @property
    def ok(self) -> bool:
        """Every journal line became a row (an unmapped citation is listed, its entry kept)."""
        return not self.unreadable

    def as_dict(self) -> dict:
        return {**asdict(self), "ok": self.ok, "counted": self.lines == self.written + self.present + len(self.unreadable)}


def _inputs(source: Path, report: Carried) -> list[tuple[str, Path]]:
    found = [(relative, source / relative) for relative in FILES if (source / relative).exists()]
    for relative in DIRECTORIES:
        directory = source / relative
        if directory.is_symlink():
            report.skipped.append((relative, "symlink"))
        elif directory.is_dir():
            found.extend((path.relative_to(source).as_posix(), path) for path in sorted(directory.rglob("*"))
                         if path.is_symlink() or path.is_file())
    kept = []
    for relative, path in found:
        if path.is_symlink() or not path.is_file():
            report.skipped.append((relative, "symlink" if path.is_symlink() else "not a regular file"))
        else:
            kept.append((relative, path))
    return kept


def _copy(relative: str, path: Path, home: Path, report: Carried, dry_run: bool) -> None:
    target = home / relative
    if target.is_file() and not target.is_symlink() and target.read_bytes() == path.read_bytes():
        report.same.append(relative)
        return
    if target.exists() or target.is_symlink():
        newer = target.lstat().st_mtime >= path.stat().st_mtime
        report.skipped.append((relative, "the file here is newer" if newer else "the file here differs"))
        return
    report.copied.append(relative)
    if dry_run:
        return
    for parent in reversed(Path(relative).parents[:-1]):
        storage.private_dir(home / parent)
    data, stat = path.read_bytes(), path.stat()
    descriptor = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "wb") as stream:
        stream.write(data)
    os.utime(target, ns=(stat.st_atime_ns, stat.st_mtime_ns))


def _trial_lines(path: Path, report: Carried) -> list[tuple[int, dict, TrialLine]]:
    parsed = []
    for number, line in enumerate(path.read_text(encoding="utf-8-sig").splitlines(), 1):
        if not line.strip():
            continue
        report.lines += 1
        try:
            raw = json.loads(line)
            trial = TrialLine.model_validate(raw)
            dt.datetime.fromisoformat(trial.ts)
        except (ValueError, ValidationError) as error:
            report.unreadable.append((number, str(error).splitlines()[0][:200]))
            continue
        parsed.append((number, raw, trial))
    return parsed


def _journal(path: Path, store: MessageStore, report: Carried, dry_run: bool) -> None:
    parsed = _trial_lines(path, report)
    cited = {item["store_id"] for _, _, trial in parsed for item in trial.evidence
             if isinstance(item.get("store_id"), int)}
    mapped = rows.lcm_uids(store, cited) if cited else {}
    written = []
    for number, raw, trial in parsed:
        uids, unmapped = [], []
        for item in trial.evidence:
            uid = mapped.get(item.get("store_id"))
            if uid is None:
                unmapped.append(item)
            elif uid not in uids:
                uids.append(uid)
        if unmapped:
            report.unmapped.append((number, [str(item.get("store_id", json.dumps(item))) for item in unmapped]))
        entry = Entry(**trial.model_dump(exclude={"evidence"}), evidence=tuple(uids))
        written.append(rows.row(rows.row_uid(store.agent_id, raw), entry, conclusion(entry),
                                {"unmapped_evidence": unmapped} if unmapped else None))
    if dry_run:
        report.present = len(rows.present(store, (row["uid"] for row in written)))
        report.written = len(written) - report.present
    else:
        report.written = rows.put(store, written)
        report.present = len(written) - report.written


def carry(source: Path, home: Path, agent: str, *, dry_run: bool = False) -> Carried:
    """Copy *source*'s self note and trial files into *home* (private, never over a differing file: it is skipped
    and listed) and write its trial journal as *agent*'s ``heart`` rows, idempotent by uid. Each cited LCM
    ``store_id`` becomes the uid of the moved row (``ctx.lcm_messages``); one with no moved row is listed and its
    entry kept."""
    report = Carried()
    for relative, path in _inputs(source, report):
        _copy(relative, path, home, report, dry_run)
    journal = source / JOURNAL
    if journal.is_symlink():
        report.skipped.append((JOURNAL, "symlink"))
    elif journal.is_file():
        with closing(MessageStore(agent)) as store:
            _journal(journal, store, report, dry_run)
    return report


def command(args) -> None:
    home = storage.home()
    source = Path(args.source).expanduser()
    if not source.is_dir():
        raise storage.HeartError(f"--from: not a directory: {source}")
    if source.resolve() == home.resolve():
        raise storage.HeartError("--from names this home")
    report = carry(source, home, rows.agent(home), dry_run=args.dry_run)
    print(text("import_files", copied=len(report.copied), same=len(report.same), skipped=len(report.skipped)))
    for relative, reason in report.skipped:
        print(text("import_skipped", path=relative, reason=reason))
    print(text("import_journal", lines=report.lines, written=report.written, present=report.present,
               unreadable=len(report.unreadable)))
    for number, ids in report.unmapped:
        print(text("import_unmapped", line=number, ids=",".join(ids)))
    for number, reason in report.unreadable:
        print(text("import_unreadable", line=number, reason=reason))
    if args.dry_run:
        print(text("import_dry"))
    if not report.ok:
        raise SystemExit(1)
