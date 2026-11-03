"""The identity registry (``memory.identities`` / ``memory.identity_keys``, migration 0006): one stable id per
person, agent and conversation, found through the source keys it came from (lina#20).

An id is derived from the first source key it was registered under, so a key resolves to the same id on
every machine and on every run, before and after the registry holds it. A later key of the same identity is
attached to the id an earlier key already has; two keys that already name different identities are never
merged (matching people across sources is a later, high-confidence-only design)."""

from __future__ import annotations

import hashlib
import logging
import threading
from typing import Optional, Sequence

logger = logging.getLogger(__name__)

SourceKey = tuple[str, str]
_PREFIX = {"person": "person", "conversation": "conv"}


def derived_id(kind: str, source: str, key: str) -> str:
    if kind == "agent" and source == "profile":
        return f"agent:{key}"
    digest = hashlib.sha256(f"{kind}\x1f{source}\x1f{key}".encode()).hexdigest()[:20]
    return f"{_PREFIX.get(kind, kind)}:{digest}"


class IdentityRegistry:
    """Resolves source keys to ids through the store. Without a reachable store it still answers, with the id
    the first key derives, so a turn is never held back; the keys are registered on a later turn."""

    def __init__(self) -> None:
        self._conn = None
        self._lock = threading.Lock()

    def _connection(self):
        import psycopg

        import hermes_state_store
        if self._conn is None or self._conn.closed:
            self._conn = psycopg.connect(hermes_state_store.resolve_dsn(), autocommit=True)
        return self._conn

    def resolve(self, kind: str, keys: Sequence[SourceKey]) -> Optional[str]:
        keys = [(source, key) for source, key in dict.fromkeys(keys) if key]
        if not keys:
            return None
        try:
            with self._lock:
                return self._resolve(kind, keys)
        except Exception as exc:  # health: allow BLE001 -- an unreachable store must not cost the turn
            logger.warning("Identity registry unavailable (%s); using derived ids for this turn", exc)
            self.close()
            return derived_id(kind, *keys[0])

    def _resolve(self, kind: str, keys: list[SourceKey]) -> str:
        conn = self._connection()
        with conn.transaction(), conn.cursor() as cur:
            cur.execute("SELECT identity_id FROM memory.identity_keys WHERE kind = %s AND (source, key) IN "
                        f"({', '.join(['(%s, %s)'] * len(keys))}) ORDER BY first_seen, identity_id LIMIT 1",
                        (kind, *[part for pair in keys for part in pair]))
            row = cur.fetchone()
            identity_id = row[0] if row else derived_id(kind, *keys[0])
            cur.execute("INSERT INTO memory.identities (identity_id, kind) VALUES (%s, %s) "
                        "ON CONFLICT (identity_id) DO NOTHING", (identity_id, kind))
            cur.executemany("INSERT INTO memory.identity_keys (kind, source, key, identity_id) VALUES (%s, %s, %s, %s) "
                            "ON CONFLICT (kind, source, key) DO NOTHING",
                            [(kind, source, key, identity_id) for source, key in keys])
        return identity_id

    def close(self) -> None:
        conn, self._conn = self._conn, None
        if conn is not None:
            try:
                conn.close()
            except Exception:  # health: allow BLE001 -- closing a broken connection is best-effort
                logger.debug("closing the identity registry connection failed", exc_info=True)
