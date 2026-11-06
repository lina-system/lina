"""Migrate a copied spool with the collector reader and the live source's cursor."""

from __future__ import annotations

import hashlib
from collections import Counter
from pathlib import Path

from hermes_cli.external_messages import _BATCH_LINES, _SpoolReader, SpoolError
from hermes_cli.migrate_memory_report import ManifestRow, SourceReport
from hermes_state_store import ExternalBatch, _owner_connection, ingest_external_batch


def _covered_prefix(original: Path, copied_offset: int, copied_digest: str,
                    live_offset: int, live_digest: str) -> None:
    """Prove the snapshot prefix against the cursor-covered live bytes, without ingesting them."""
    import fcntl

    if live_offset == copied_offset:
        if live_digest != copied_digest:
            raise SpoolError("copied spool prefix differs from the committed cursor")
        return
    try:
        with original.open("rb") as stream:
            fcntl.flock(stream.fileno(), fcntl.LOCK_SH)
            digest = hashlib.sha256()
            consumed = 0
            for boundary, expected in ((copied_offset, copied_digest), (live_offset, live_digest)):
                while consumed < boundary:
                    chunk = stream.read(min(boundary - consumed, 1024 * 1024))
                    if not chunk:
                        raise SpoolError("live spool prefix was truncated before the committed cursor")
                    digest.update(chunk)
                    consumed += len(chunk)
                if digest.hexdigest() != expected:
                    raise SpoolError("copied or live spool consumed prefix was rewritten")
    except FileNotFoundError:
        raise SpoolError("live spool is unavailable to verify the advanced cursor prefix") from None


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
    inserted = 0
    with copy.open("rb") as stream:
        reader = _SpoolReader(stream)

        def read_batch(live_offset: int, live_digest: str) -> ExternalBatch:
            if live_offset >= offset:
                _covered_prefix(original, offset, digest, live_offset, live_digest)
                return ExternalBatch((), 0, live_offset, live_digest)
            return reader.read_batch(live_offset, live_digest)

        while True:
            batch, added = ingest_external_batch(str(original.resolve()), read_batch)
            inserted += added
            if batch.lines < _BATCH_LINES:
                break
    occurrences = Counter(row.native_id for row in expected)
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
        # A live edit may add later versions of a copied UID. Verify only copied versions,
        # while retaining corrupted copied text so the manifest still detects it.
        destination.extend([row] * occurrences[row.native_id])
    return SourceReport("external", tuple(expected), tuple(destination),
                        matched=len(expected) - inserted, inserted=inserted)
