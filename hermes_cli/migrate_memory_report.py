"""Loss-detecting, multiplicity-preserving accounting for copied memory sources."""

from __future__ import annotations

import re
from collections import Counter
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Sequence

# The previous machine's work-status commands: its kanban wrapper (also `hermes -p <profile> kanban`) and queue.
_OLD_WORK_COMMAND = re.compile(r"\bhermes\b(?:\s+-p\s+\S+)?\s+kanban\b|\bkanban\s+--board\b|\bsession-queue\b")
_DATABASES = frozenset({"state.db", "lcm.db", "work.db"})


@dataclass(frozen=True, order=True)
class ManifestRow:
    native_id: str
    sha256: str


@dataclass(frozen=True)
class SkippedRow:
    native_id: str
    kind: str
    reason: str


@dataclass(frozen=True)
class SourceReport:
    """Counts refer to source placements, even when several share one stored text."""

    name: str
    source: tuple[ManifestRow, ...]
    destination: tuple[ManifestRow, ...]
    matched: int
    inserted: int
    skipped: tuple[SkippedRow, ...] = ()

    @property
    def ok(self) -> bool:
        return (self.matched >= 0 and self.inserted >= 0
                and len(self.source) == self.matched + self.inserted + len(self.skipped)
                and all(row.reason and row.kind for row in self.skipped)
                and Counter(self.source) == Counter(self.destination))

    def as_dict(self) -> dict:
        source, destination = Counter(self.source), Counter(self.destination)
        return {
            "source": self.name, "source_rows": len(self.source),
            "matched": self.matched, "inserted": self.inserted,
            "skipped": [asdict(row) for row in self.skipped],
            "source_manifest": [asdict(row) for row in sorted(self.source)],
            "destination_manifest": [asdict(row) for row in sorted(self.destination)],
            "missing": [asdict(row) for row in sorted((source - destination).elements())],
            "unexpected": [asdict(row) for row in sorted((destination - source).elements())],
            "ok": self.ok,
        }


def _census_patterns(hosts: Sequence[str], source_home: Path) -> dict[str, re.Pattern | None]:
    named = "|".join(re.escape(host) for host in hosts)
    host = rf"(?<![\w.-])(?:{named})(?![\w-])"
    paths = [str(source_home)]
    if source_home.is_relative_to(Path.home()) and source_home != Path.home():
        paths.append("~/" + source_home.relative_to(Path.home()).as_posix())
    return {"old_work_command": _OLD_WORK_COMMAND,
            "ssh_to_old_host": re.compile(rf"\bssh\b[^\n]*?{host}", re.IGNORECASE) if hosts else None,
            "old_host_name": re.compile(host, re.IGNORECASE) if hosts else None,
            "old_path": re.compile("|".join(re.escape(path) for path in paths))}


def copy_census(profile_dir: Path, *, old_hosts: Sequence[str], source_home: Path, top: int = 5) -> dict:
    """What the copied files still say about the previous machine, counted and never rewritten (lina#719): per
    class the files with a hit, the hits and the files with most hits. The copy's ``SOUL.overlay.md``
    supersedes their status lines; the rest are often still true of machines that exist."""
    patterns = _census_patterns(old_hosts, source_home)
    hits: dict[str, Counter] = {name: Counter() for name in patterns}
    for path in sorted(profile_dir.rglob("*")):
        if not path.is_file() or path.name in _DATABASES or path.relative_to(profile_dir) == Path("config.yaml"):
            continue
        text = path.read_bytes().decode("utf-8", errors="replace")
        relative = path.relative_to(profile_dir).as_posix()
        for name, pattern in patterns.items():
            if pattern is not None and (count := sum(1 for _ in pattern.finditer(text))):
                hits[name][relative] = count
    return {name: {"files": len(counts), "hits": sum(counts.values()),
                   "top": [{"file": file, "hits": count}
                           for file, count in sorted(counts.items(), key=lambda item: (-item[1], item[0]))[:top]]}
            for name, counts in hits.items()}


def duplicate_report(agent: str) -> dict:
    """Query immutable identities; raw Hermes IDs may legitimately have several text versions."""
    from hermes_state_store import _owner_connection

    with _owner_connection() as conn:
        uid = conn.execute(
            "SELECT uid, count(*) FROM memory.messages WHERE agent_id=%s OR source='messages' "
            "GROUP BY uid HAVING count(*) > 1", (agent,)).fetchall()
        native = conn.execute(
            "SELECT source, coalesce(native->>'message_uid', split_part(uid, '#', 1)), "
            "coalesce(native->>'payload_sha256', content_sha256), count(*) "
            "FROM memory.messages WHERE agent_id=%s OR source='messages' "
            "GROUP BY 1,2,3 HAVING count(*) > 1", (agent,)).fetchall()
        raw = conn.execute(
            "SELECT source, coalesce(native->>'message_uid', split_part(uid, '#', 1)), count(*) "
            "FROM memory.messages WHERE agent_id=%s OR source='messages' "
            "GROUP BY 1,2 HAVING count(*) > 1", (agent,)).fetchall()
    return {"uid": uid, "source_native_version": native, "raw_native_versions": raw,
            "native_id_definition": "source native identity plus immutable payload version",
            "ok": not uid and not native}
