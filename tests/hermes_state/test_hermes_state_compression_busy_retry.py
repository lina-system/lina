"""Appends flow freely during compression; the commit preserves them (#75316).

HISTORY: ``append_message`` used to refuse while another writer held the
session's compression lock, with a short busy-wait (#75264 → #75083). That
fenced ordinary transcript writes behind a lease whose real job is stopping
two COMPRESSIONS colliding — turns died as ``session_persistence_failed``
whenever a slow provider summary overlapped an incoming message (#74568,
#77386), and a stale lock from a dead PID blocked writes for the full TTL.

CURRENT CONTRACT (watermark commit): appends never check compression_locks.
``archive_and_compact()`` takes a watermark captured at compression start and
re-sequences every row that arrived after it (the concurrent tail) back into
the live transcript, atomically, instead of archiving it with the snapshot.
The commit itself is holder-fenced: a compression whose lease was lost cannot
publish.

The tests read the SQL each write actually ran instead of timing it: a wall-clock
bound flaked on loaded CI runners while the append never touched the lease.
"""

from __future__ import annotations

from contextlib import contextmanager
from pathlib import Path

import pytest

from hermes_state import SessionDB
from hermes_state_errors import CompressionSessionBusyError, SessionCompressionInProgressError


@pytest.fixture
def db(tmp_path: Path) -> SessionDB:
    d = SessionDB(tmp_path / "state.db")
    d.create_session("sess1", source="test")
    return d


@contextmanager
def _statements(db: SessionDB):
    ran: list[str] = []
    db._conn.set_trace_callback(ran.append)
    try:
        yield ran
    finally:
        db._conn.set_trace_callback(None)


def _lock_holder(db: SessionDB, session_id: str):
    row = db._conn.execute(
        "SELECT holder FROM compression_locks WHERE session_id = ?", (session_id,)).fetchone()
    return row[0] if row else None


def _assert_append_ignores_the_lease(db: SessionDB, content: str) -> None:
    with _statements(db) as ran:
        db.append_message("sess1", role="user", content=content)
    assert not [sql for sql in ran if "compression_locks" in sql], "append must not wait on a compression lease"
    assert sum(sql.startswith("BEGIN IMMEDIATE") for sql in ran) == 1, ran


def test_append_is_never_blocked_by_a_foreign_compression_lock(db: SessionDB) -> None:
    """The classic race: a steer lands while compression owns the session.

    Old behavior: busy-wait then land (or die on timeout). New behavior: the
    append lands IMMEDIATELY — the watermark commit is what protects it.
    """
    assert db.try_acquire_compression_lock("sess1", "compressor") is True

    _assert_append_ignores_the_lease(db, "steered mid-compression")

    assert _lock_holder(db, "sess1") == "compressor"  # landed while the lease was still held
    rows = db.get_messages("sess1")
    assert any(r["content"] == "steered mid-compression" for r in rows)


def test_append_is_never_blocked_by_a_stale_dead_pid_lock(db: SessionDB) -> None:
    """A crashed compressor's unexpired lock must not fence writes (#74568)."""
    assert db.try_acquire_compression_lock(
        "sess1", "pid-9999999-long-gone", ttl_seconds=3600
    ) is True
    _assert_append_ignores_the_lease(db, "lands despite stale lock")
    rows = db.get_messages("sess1")
    assert any(r["content"] == "lands despite stale lock" for r in rows)


def test_the_lock_owner_append_still_works(db: SessionDB) -> None:
    assert db.try_acquire_compression_lock("sess1", "compressor") is True
    db.append_message(
        "sess1",
        role="assistant",
        content="written by the compressor",
        compression_lock_holder="compressor",
    )
    rows = db.get_messages("sess1")
    assert any(r["content"] == "written by the compressor" for r in rows)


def test_a_lost_compression_lease_still_fails_fast(db: SessionDB) -> None:
    """``publish_compression_child`` with a lost lease is permanent — no retry."""
    with _statements(db) as ran, pytest.raises(CompressionSessionBusyError) as caught:
        db.publish_compression_child(
            parent_session_id="sess1",
            child_session_id="child1",
            source="test",
            messages=[{"role": "user", "content": "compacted"}],
            compression_lock_holder="not-the-holder",
            require_compression_lease=True,
        )
    # The transient subclass is what the writer retries; a lost lease must not spend that budget.
    assert not isinstance(caught.value, SessionCompressionInProgressError)
    assert sum(sql.startswith("BEGIN IMMEDIATE") for sql in ran) == 1, (
        "a lost lease is permanent and must not spend the retry budget"
    )
