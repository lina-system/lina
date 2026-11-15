"""The identity registry (``memory.identities`` / ``memory.identity_keys``, migration 0006): one stable id per
person, agent and conversation, found through the source keys it came from (lina#20).

An id is derived from the first source key it was registered under, so a key resolves to the same id on
every machine and on every run, before and after the registry holds it. A later key of the same identity is
attached to the id an earlier key already has; two keys that already name different identities are never
merged by the registry itself.

A handle (a person's key on one channel: ``gateway_user``, ``messenger_author``, ``local_user``, ``honcho_peer``) joins
another person only through a link in ``memory.identity_links`` (migration 0013, lina#313): confirmed by the owner,
or a candidate that keeps its evidence and joins nothing. A name match is never a link."""

from __future__ import annotations

import hashlib
import logging
import threading
from typing import Optional, Sequence

logger = logging.getLogger(__name__)

SourceKey = tuple[str, str]
_PREFIX = {"person": "person", "conversation": "conv"}
LINK_SOURCES = ("gateway_user", "messenger_author", "local_user", "honcho_peer")
# The machine owner on local surfaces (lina-memory's ``_person_keys``): the person a shared room's speaker is the owner
# as only through a confirmed link of their handle.
OWNER_HANDLE: SourceKey = ("local_user", "owner")
# Transaction-scoped, shared by every connection. An instance lock cannot see another process.
_IDENTITY_LOCK_NS = 190006


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
            # Two first resolutions that share a key but not a first key must not each insert an id.
            cur.execute("SELECT pg_advisory_xact_lock(%s, hashtext(%s))", (_IDENTITY_LOCK_NS, kind))
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

    # ── links (lina#313) ──

    def person_of(self, handle: SourceKey) -> str:
        """The person *handle* is confirmed-linked to, else the identity it is registered under (registered now, the
        first time the handle is named)."""
        with self._lock:
            with self._connection().cursor() as cur:
                cur.execute("SELECT person_id FROM memory.identity_links WHERE source = %s AND key = %s"
                            " AND status = 'confirmed'", handle)
                row = cur.fetchone()
            return row[0] if row else self._resolve("person", [handle])

    def handles(self, person_id: str) -> list[SourceKey]:
        """Every handle of *person_id*: the keys it was registered under and the handles confirmed-linked to it."""
        with self._lock, self._connection().cursor() as cur:
            cur.execute("SELECT source, key FROM memory.identity_keys WHERE kind = 'person' AND identity_id = %s"
                        " UNION SELECT source, key FROM memory.identity_links WHERE person_id = %s"
                        " AND status = 'confirmed' ORDER BY 1, 2", (person_id, person_id))
            return [tuple(row) for row in cur.fetchall()]

    def link(self, handle: SourceKey, person_id: str, *, status: str, evidence: str) -> str:
        """Record a link of *handle* to *person_id*: ``confirmed`` (the owner said so; a handle confirmed to another
        person before is undone, with the same evidence), ``candidate`` (never replaces a confirmed or undone link)
        or ``undone`` (only a confirmed link is undone). Returns the link's status afterwards ("" for none)."""
        if handle[0] not in LINK_SOURCES or not handle[1]:
            raise ValueError(f"not a person handle: {handle[0]}:{handle[1]} (sources: {', '.join(LINK_SOURCES)})")
        if status not in ("confirmed", "candidate", "undone") or not evidence.strip():
            raise ValueError("a link needs a status (confirmed, candidate, undone) and its evidence")
        conn = self._connection()
        with self._lock, conn.transaction(), conn.cursor() as cur:
            cur.execute("SELECT pg_advisory_xact_lock(%s, hashtext(%s))", (_IDENTITY_LOCK_NS, "link"))
            if status == "undone":
                cur.execute("UPDATE memory.identity_links SET status = 'undone', evidence = %s, updated_at = now()"
                            " WHERE source = %s AND key = %s AND person_id = %s AND status = 'confirmed'",
                            (evidence, *handle, person_id))
            else:
                cur.execute("INSERT INTO memory.identities (identity_id, kind) VALUES (%s, 'person')"
                            " ON CONFLICT (identity_id) DO NOTHING", (person_id,))
                if status == "confirmed":
                    cur.execute("UPDATE memory.identity_links SET status = 'undone', evidence = %s, updated_at = now()"
                                " WHERE source = %s AND key = %s AND person_id <> %s AND status = 'confirmed'",
                                (evidence, *handle, person_id))
                cur.execute("INSERT INTO memory.identity_links (source, key, person_id, status, evidence)"
                            " VALUES (%s, %s, %s, %s, %s) ON CONFLICT (source, key, person_id) DO UPDATE"
                            " SET status = EXCLUDED.status, evidence = EXCLUDED.evidence, updated_at = now()"
                            " WHERE EXCLUDED.status = 'confirmed'", (*handle, person_id, status, evidence))
            cur.execute("SELECT status FROM memory.identity_links WHERE source = %s AND key = %s AND person_id = %s",
                        (*handle, person_id))
            row = cur.fetchone()
        return row[0] if row else ""

    def links(self, person_id: str) -> list[dict[str, str]]:
        """Every link of *person_id*, confirmed, candidate and undone, with its evidence."""
        with self._lock, self._connection().cursor() as cur:
            cur.execute("SELECT source, key, status, evidence FROM memory.identity_links WHERE person_id = %s"
                        " ORDER BY source, key", (person_id,))
            return [{"handle": f"{source}:{key}", "status": status, "evidence": evidence}
                    for source, key, status, evidence in cur.fetchall()]

    def close(self) -> None:
        conn, self._conn = self._conn, None
        if conn is not None:
            try:
                conn.close()
            except Exception:  # health: allow BLE001 -- closing a broken connection is best-effort
                logger.debug("closing the identity registry connection failed", exc_info=True)
