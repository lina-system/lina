"""Migrate a copied spool with the collector reader and the live source's cursor."""

from __future__ import annotations

import hashlib
from collections import Counter
from pathlib import Path

from hermes_cli.external_messages import _BATCH_LINES, _SpoolReader, ingest
from hermes_cli.migrate_memory_report import ManifestRow, SourceReport
from hermes_state_store import _owner_connection


def migrate_external(copy: Path, original: Path) -> SourceReport:
    """Verify sanitized payload versions, retaining the original cursor key for live ingestion."""
    expected = []
    bases = set()
    with copy.open("rb") as stream:
        reader = _SpoolReader(stream)
        offset, digest = 0, hashlib.sha256(b"").hexdigest()
        while True:
            batch = reader.read_batch(offset, digest)
            for message in batch.messages:
                bases.add(message.uid)
                expected.append(ManifestRow(
                    f"{message.uid}#{message.digest}", hashlib.sha256(message.content.encode()).hexdigest()))
            offset, digest = batch.offset, batch.prefix_sha256
            if batch.lines < _BATCH_LINES:
                break
    result = ingest(copy, cursor_key=str(original.resolve()))
    occurrences = Counter(expected)
    with _owner_connection() as conn:
        rows = conn.execute(
            "SELECT split_part(uid, '#', 1), native->>'payload_sha256', content, content_sha256 "
            "FROM memory.messages WHERE source='messages' AND split_part(uid, '#', 1) = ANY(%s)",
            (sorted(bases),),
        ).fetchall()
    destination = []
    for base, payload, content, digest in rows:
        # The manifest hashes actual destination text, not the stored digest claim.
        actual = hashlib.sha256((content or "").encode()).hexdigest()
        row = ManifestRow(f"{base}#{payload}", actual if actual == digest else "invalid:" + actual)
        destination.extend([row] * max(1, occurrences[row]))
    return SourceReport("external", tuple(expected), tuple(destination),
                        matched=len(expected) - result.inserted, inserted=result.inserted)
