"""Evidence-checked self-note edits and private per-apply undo snapshots."""

from __future__ import annotations

import datetime as dt
import difflib
import json
import re
from dataclasses import dataclass
from pathlib import Path

from pydantic import ValidationError

from hermes_cli.active_sessions import _FileLock
from hermes_cli.heart import journal, storage
from hermes_cli.heart.evidence import MACHINE_PREFIXES
from hermes_cli.heart.locale import protected_rule, text
from hermes_cli.heart.models import Change
from hermes_cli.heart.taste_context import normalized
from utils import atomic_write_bytes

SUFFIX = re.compile(r"\(\s*(\d{4}-\d{2}-\d{2})\s*,\s*(?:근거|evidence)\s+([^)]+)\)\s*$")
DATED = re.compile(r"\(\s*(\d{4}-\d{2}-\d{2})\s*,[^)]*\)\s*$")
BACKUP = re.compile(r"^(\d{4}-\d{2}-\d{2}T\d{6}[+-]\d{4})(?:-(\d+))?$")


@dataclass(frozen=True, slots=True)
class Refusal(Exception):
    rule: str
    detail: str


def removal_candidates(value: str) -> list[str]:
    lines = value.splitlines()
    dated = sorted((match[1], index) for index, line in enumerate(lines) if (match := DATED.search(line)))
    dropped: set[int] = set()
    selected = []
    for _, index in dated:
        remaining = "\n".join(line for i, line in enumerate(lines) if i not in dropped)
        if len(remaining + ("\n" if value.endswith("\n") else "")) < 1300:
            break
        dropped.add(index)
        selected.append(lines[index])
    return selected


def changed_lines(old: str, new: str) -> tuple[list[str], list[str], int]:
    before, after = old.splitlines(), new.splitlines()
    added, removed, count = [], [], 0
    for tag, a, b, c, d in difflib.SequenceMatcher(a=before, b=after, autojunk=False).get_opcodes():
        if tag == "equal":
            continue
        removed.extend(before[a:b])
        added.extend(after[c:d])
        count += max(b - a, d - c)
    return added, removed, count


def check(old: str, new: str) -> tuple[list[str], list[str], list[str]]:
    if len(new) > 1500:
        raise Refusal("length", text("length", count=len(new), overflow=len(new) - 1500))
    for line in new.splitlines():
        if any(prefix in line for prefix in MACHINE_PREFIXES):
            raise Refusal("machine-prefix", text("machine-prefix"))
        if len(line) > 200:
            raise Refusal("line-length", text("line-length", count=len(line)))
    added, removed, count = changed_lines(old, new)
    if count > 2:
        raise Refusal("line-count", text("line-count", count=count))
    for line in added + removed:
        # The citation keyword is not the assertion being edited.
        rule = protected_rule(SUFFIX.sub("", line))
        if rule is not None:
            raise Refusal(rule, text(rule))
    known = {entry.ts: storage.parse_ts(entry.ts) for entry in journal.entries()}
    citations = []
    for line in added:
        match = SUFFIX.search(line)
        if match is None:
            raise Refusal("evidence-suffix", text("evidence-suffix"))
        try:
            dt.date.fromisoformat(match[1])
        except ValueError as error:
            raise Refusal("evidence-suffix", text("evidence-suffix")) from error
        tokens = [token.strip() for token in match[2].split(",") if token.strip()]
        if not tokens:
            raise Refusal("evidence-suffix", text("evidence-suffix"))
        for token in tokens:
            parsed = known.get(token)
            if parsed is None or parsed.timestamp() > storage.now():
                raise Refusal("evidence-ts", text("evidence-ts", value=token))
            if token not in citations:
                citations.append(token)
    return added, removed, citations


def next_backup() -> Path:
    base = dt.datetime.fromtimestamp(storage.now()).astimezone().strftime("%Y-%m-%dT%H%M%S%z")
    directory = storage.private_dir(storage.home() / "heart/self-backups")
    name, index = base, 1
    while (directory / f"{name}.md").exists():
        index += 1
        name = f"{base}-{index}"
    return directory / f"{name}.md"


def _note_lock(home: Path) -> _FileLock:
    return _FileLock(storage.private_dir(home / "heart") / "self-note.lock")


def apply(args) -> None:
    with _note_lock(storage.home()):
        _apply(args)


def _apply(args) -> None:
    soul = storage.home() / "SOUL.self.md"
    changes = storage.home() / "heart/changes.jsonl"
    new = Path(args.file).read_text(encoding="utf-8-sig")
    old = storage.load_text(soul)
    timestamp = storage.stamp()
    try:
        added, removed, citations = check(old, new)
    except Refusal as refusal:
        storage.append(changes, Change(ts=timestamp, refused=refusal.rule))
        print(text("refusal", rule=refusal.rule, detail=refusal.detail))
        raise SystemExit(1) from None
    if not added and not removed:
        storage.append(changes, Change(ts=timestamp, changed=0))
        print(text("unchanged"))
        return
    backup = next_backup()
    storage.write_private(backup, old)
    storage.write_private(soul, new)
    storage.append(changes, Change(ts=timestamp, added=tuple(added), removed=tuple(removed), evidence=tuple(citations)))
    print(text("applied", added=len(added), removed=len(removed), backup=backup.name))


def backup_key(path: Path) -> tuple[float, int]:
    match = BACKUP.fullmatch(path.stem)
    parsed = storage.parse_ts(match[1]) if match else None
    return (parsed.timestamp() if parsed else 0, int(match[2] or 1) if match else 1)


def undo(args) -> None:
    with _note_lock(storage.home()):
        _undo()


def _undo() -> None:
    backups = sorted((storage.home() / "heart/self-backups").glob("*.md"), key=backup_key)
    if not backups:
        print(text("no_backup"))
        raise SystemExit(1)
    backup = backups[-1]
    storage.write_private(storage.home() / "SOUL.self.md", backup.read_text(encoding="utf-8-sig"))
    storage.append(storage.home() / "heart/changes.jsonl", Change(ts=storage.stamp(), undo=backup.name))
    print(text("undo", backup=backup.name))


def scrub_taste(home: Path, target: re.Pattern[str]) -> None:
    """Scrub matching lines from self-note copies and the self-edit audit.

    Each replacement is durable on its own. If a write fails, the caller rolls
    back its database transaction so a retry can find the taste and finish.
    """
    with _note_lock(home):
        backups = sorted((home / "heart/self-backups").glob("*.md"))
        changes = home / "heart/changes.jsonl"
        for path in (*backups, changes, home / "SOUL.self.md"):
            if not path.resolve().is_relative_to(home.resolve()):
                raise storage.HeartError(text("forget_target_required"))
            try:
                original = path.read_bytes()
            except FileNotFoundError:
                continue
            bom = b"\xef\xbb\xbf" if original.startswith(b"\xef\xbb\xbf") else b""
            source = original.decode("utf-8-sig")
            lines = source.splitlines(keepends=True)
            if path == changes:
                scrubbed = "".join(_scrub_change(line, target) for line in lines)
            else:
                scrubbed = "".join(line for line in lines if not target.search(normalized(line)))
            if scrubbed != source:
                atomic_write_bytes(path, bom + scrubbed.encode("utf-8"), mode=0o600, fsync_dir=True)


def _scrub_change(line: str, target: re.Pattern[str]) -> str:
    try:
        change = Change.model_validate_json(line)
    except ValidationError:
        return line
    updated = {
        key: [value for value in values if not target.search(normalized(value))]
        for key, values in (("added", change.added), ("removed", change.removed))
        if any(target.search(normalized(value)) for value in values)
    }
    if not updated:
        return line
    ending = line[len(line.rstrip("\r\n")):]
    return json.dumps({**json.loads(line), **updated}, ensure_ascii=False) + ending
