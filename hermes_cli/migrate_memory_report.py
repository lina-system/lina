"""Loss-detecting, multiplicity-preserving accounting for copied memory sources."""

from __future__ import annotations

from collections import Counter
from dataclasses import asdict, dataclass


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
