"""Owner-managed profile lifetimes and collection of deleted agents' message text."""

from __future__ import annotations

from collections.abc import Iterator
from contextlib import contextmanager
from datetime import UTC, datetime, timedelta
from pathlib import Path

from hermes_state_store import DEFAULT_COLLECT_DAYS, MIN_COLLECT_DAYS, StoreUnavailableError


def _now() -> datetime:
    return datetime.now(UTC)


def store_owner_missing() -> StoreUnavailableError | None:
    """Why this machine has no store owner (no store.env with its password: `lina store install` never ran here),
    or None when it has one. Only the owner records and collects profile lifetimes."""
    from hermes_state_store import resolve_owner_dsn
    try:
        resolve_owner_dsn()
    except StoreUnavailableError as missing:
        return missing
    return None


def _holds_text(db_path: Path) -> bool:
    """Whether a store is configured (LINA_STORE_DSN or store.env) and *db_path*'s transcript points at text in it."""
    from hermes_state_store import resolve_dsn, state_db_has_text
    try:
        resolve_dsn()
    except StoreUnavailableError:
        return False
    return state_db_has_text(db_path)


@contextmanager
def profile_lifecycle(db_path: Path, *, deleted: bool = False) -> Iterator[None]:
    """Serialize profile publication/removal with GC, rolling back on failure.

    Where the store has an owner the transition is recorded, and a store that then fails fails the operation
    rather than lose it. Without an owner there is nothing to record (a home with no store, or a remote one), except
    for a profile whose text the configured store holds: that is refused, as a rename of it is.
    """
    from hermes_constants import clear_named_profile_deleted
    from hermes_state_store import _owner_connection, agent_id_for

    if db_path.name != "state.db" and db_path.resolve().name != "state.db":
        yield
        return
    missing = store_owner_missing()
    if missing is not None:
        if _holds_text(db_path):
            raise missing
        yield
        if not deleted:
            clear_named_profile_deleted(db_path.parent)
        return
    with _owner_connection() as conn:
        conn.execute(
            "INSERT INTO memory.agents (agent_id, deleted_at) VALUES (%s, %s)"
            " ON CONFLICT (agent_id) DO UPDATE SET deleted_at = EXCLUDED.deleted_at",
            (agent_id_for(db_path), _now() if deleted else None),
        )
        yield
        if not deleted:
            clear_named_profile_deleted(db_path.parent)


def collect_deleted_agents(*, older_than_days: float = DEFAULT_COLLECT_DAYS) -> dict[str, int]:
    """Collect text only after a recorded deletion has passed its full grace period."""
    from hermes_state_store import TEXT_CACHE, _owner_connection

    cutoff = _now() - timedelta(days=max(float(older_than_days), MIN_COLLECT_DAYS))
    removed: dict[str, list[str]] = {}
    with _owner_connection() as conn:
        # A concurrent restoration either clears the timestamp before this lock is
        # acquired, or waits until this transaction finishes before restoring text.
        agents = conn.execute(
            "SELECT agent_id FROM memory.agents WHERE deleted_at < %s"
            " ORDER BY agent_id FOR UPDATE", (cutoff,),
        ).fetchall()
        for (agent_id,) in agents:
            rows = conn.execute(
                "DELETE FROM memory.messages WHERE agent_id = %s RETURNING uid", (agent_id,),
            ).fetchall()
            removed[agent_id] = [uid for (uid,) in rows]
    for agent_id, uids in removed.items():
        TEXT_CACHE.drop(agent_id, uids)
    return {agent_id: len(uids) for agent_id, uids in removed.items()}
