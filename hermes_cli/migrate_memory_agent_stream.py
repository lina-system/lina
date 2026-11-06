"""Disk-backed bookkeeping and bounded iteration for legacy agent imports."""

from __future__ import annotations

import hashlib
import json
import sqlite3
from dataclasses import dataclass
from itertools import islice, zip_longest
from pathlib import Path

from hermes_cli.migrate_memory_report import ManifestRow, SourceReport

BATCH_ROWS = 256
SMALL_REPORT_ROWS = 4096


def batches(rows, size=BATCH_ROWS):
    rows = iter(rows)
    while batch := list(islice(rows, size)):
        yield batch


def scratch() -> sqlite3.Connection:
    """An automatically removed on-disk SQLite database, never a payload-sized Python map."""
    conn = sqlite3.connect("")
    conn.execute("PRAGMA temp_store=FILE")
    conn.execute("PRAGMA cache_size=-4096")
    return conn


def same_copy(source: sqlite3.Connection, destination: sqlite3.Connection) -> None:
    """Compare full SQLite values and multiplicities, with disk-side sorting for unkeyed tables."""
    source.execute("PRAGMA temp_store=FILE")
    destination.execute("PRAGMA temp_store=FILE")
    for (name,) in source.execute("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'"):
        quoted = '"' + name.replace('"', '""') + '"'
        columns = source.execute(f"PRAGMA table_info({quoted})").fetchall()
        keys = sorted((r[5], r[0] + 1) for r in columns if r[5])
        order = ",".join(str(pos) for _, pos in keys) if keys else ",".join(str(i + 1) for i in range(len(columns)))
        left = source.execute(f"SELECT * FROM {quoted} ORDER BY {order}")
        right = destination.execute(f"SELECT * FROM {quoted} ORDER BY {order}")
        for a, b in zip_longest(left, right):
            if a is None or b is None or tuple(a) != tuple(b):
                raise ValueError(f"destination is not the source copy: {name}")


@dataclass(frozen=True)
class DiskReport:
    """Large reports expose counts and hash-only JSONL receipts instead of huge JSON arrays."""

    name: str
    source_rows: int
    destination_rows: int
    matched: int
    inserted: int
    missing: int
    unexpected: int
    manifests: dict

    @property
    def ok(self) -> bool:
        return (self.matched >= 0 and self.inserted >= 0
                and self.source_rows == self.matched + self.inserted
                and not self.missing and not self.unexpected)

    def as_dict(self) -> dict:
        return {"source": self.name, "source_rows": self.source_rows,
                "destination_rows": self.destination_rows, "matched": self.matched,
                "inserted": self.inserted, "skipped": [], "missing": self.missing,
                "unexpected": self.unexpected, "manifests": self.manifests, "ok": self.ok}


def finish_report(ledger: sqlite3.Connection, path: Path, name: str, inserted: int):
    """Compare multisets on disk; small callers retain the original SourceReport API."""
    source_count = ledger.execute("SELECT count(*) FROM source_manifest").fetchone()[0]
    dest_count = ledger.execute("SELECT count(*) FROM destination_manifest").fetchone()[0]
    if max(source_count, dest_count) <= SMALL_REPORT_ROWS:
        return SourceReport(name, tuple(ManifestRow(*r) for r in ledger.execute("SELECT * FROM source_manifest ORDER BY rowid")),
                            tuple(ManifestRow(*r) for r in ledger.execute("SELECT * FROM destination_manifest ORDER BY rowid")),
                            source_count - inserted, inserted)
    manifests = {}
    counts = {}
    for kind, query in (
        ("source", "SELECT * FROM source_manifest ORDER BY native_id, sha256"),
        ("destination", "SELECT * FROM destination_manifest ORDER BY native_id, sha256"),
        ("missing", "SELECT * FROM source_manifest EXCEPT SELECT * FROM destination_manifest"),
        ("unexpected", "SELECT * FROM destination_manifest EXCEPT SELECT * FROM source_manifest"),
    ):
        output = path.with_name(path.name + f".migration-{kind}.jsonl")
        digest = hashlib.sha256()
        count = 0
        with output.open("wb") as stream:
            for native_id, sha256 in ledger.execute(query):
                line = (json.dumps({"native_id": native_id, "sha256": sha256}, separators=(",", ":")) + "\n").encode()
                stream.write(line)
                digest.update(line)
                count += 1
        counts[kind] = count
        manifests[kind] = {"path": str(output), "rows": count, "sha256": digest.hexdigest()}
    return DiskReport(name, source_count, dest_count, source_count - inserted, inserted,
                      counts["missing"], counts["unexpected"], manifests)
