"""Move a copied pre-v33 agent transcript through the shared message writer."""

from __future__ import annotations

import hashlib
import json
import sqlite3
from collections import Counter
from contextlib import closing
from pathlib import Path

from hermes_cli.migrate_memory_report import ManifestRow, SourceReport
from hermes_state_common import SCHEMA_VERSION
from hermes_state_schema import apply_declared_schema
from hermes_state_store import (
    TEXT_FIELDS, MessageStore, agent_id_for, check_text_row,
    payload_digest, put_row_text,
)
from plugins.memory.lina.identity import IdentityRegistry, SourceKey


def _readonly(path: Path) -> sqlite3.Connection:
    conn = sqlite3.connect(path.resolve().as_uri() + "?mode=ro", uri=True)
    conn.row_factory = sqlite3.Row
    return conn


def _source(conn: sqlite3.Connection) -> list[dict]:
    versions = conn.execute("SELECT version FROM schema_version").fetchall()
    if len(versions) != 1 or not isinstance(versions[0][0], int) or not 0 < versions[0][0] <= 33:
        raise ValueError("expected one supported legacy schema version")
    columns = {r[1] for r in conn.execute("PRAGMA table_info(messages)")}
    if not {"id", "session_id", "role", "content", "timestamp"} <= columns:
        raise ValueError("malformed legacy messages table")
    if conn.execute("SELECT 1 FROM sqlite_master WHERE type='table' AND name='transcript_rows'").fetchone():
        if conn.execute("SELECT 1 FROM transcript_rows LIMIT 1").fetchone():
            raise ValueError("mixed legacy messages and nonempty transcript_rows")
    sessions = {r[0] for r in conn.execute("SELECT id FROM sessions")}
    rows = [dict(r) for r in conn.execute("SELECT * FROM messages ORDER BY id")]
    for row in rows:
        if row["session_id"] not in sessions or not row["role"] or row["timestamp"] is None:
            raise ValueError("malformed legacy message placement")
        if not row.get("message_uid"):
            key = json.dumps([row["session_id"], row["id"]], separators=(",", ":"))
            row["message_uid"] = "legacy:" + hashlib.sha256(key.encode()).hexdigest()
    return rows


def _native_id(row: dict) -> str:
    return f"{row['session_id']}:{row['id']}"


def _manifest(row: dict) -> ManifestRow:
    return ManifestRow(_native_id(row), payload_digest(tuple(row.get(f) for f in TEXT_FIELDS)))


def _register(registry: IdentityRegistry, kind: str, keys: list[SourceKey]) -> str:
    identity = registry.resolve(kind, keys)
    # resolve() deliberately falls back for live turns. An import must prove durability instead.
    conn = registry._connection()
    for source, key in keys:
        found = conn.execute(
            "SELECT k.identity_id FROM memory.identity_keys k JOIN memory.identities i USING (identity_id)"
            " WHERE k.kind = %s AND k.source = %s AND k.key = %s AND i.kind = %s",
            (kind, source, key, kind)).fetchone()
        if found is None or found[0] != identity:
            raise ValueError(f"identity registration did not persist: {kind}/{source}/{key}")
    if identity is None:
        raise ValueError("identity registration returned no identity")
    return identity


def _conversations(conn: sqlite3.Connection, agent: str, registry: IdentityRegistry) -> dict[str, str]:
    _register(registry, "agent", [("profile", agent)])
    source_path = Path(conn.execute("PRAGMA database_list").fetchone()[2])
    lcm_path = source_path.parent / "lcm.db"
    lcm_keys = {}
    if lcm_path.is_file():
        with closing(_readonly(lcm_path)) as lcm:
            columns = {row[1] for row in lcm.execute("PRAGMA table_info(messages)")}
            if {"session_id", "conversation_id"} <= columns:
                for session_id, conversation in lcm.execute(
                        "SELECT DISTINCT session_id, conversation_id FROM messages WHERE conversation_id <> ''"):
                    lcm_keys.setdefault(session_id, []).append(("lcm_conversation", f"{agent}:{conversation}"))
    conversations = {}
    for record in conn.execute("SELECT * FROM sessions ORDER BY started_at, id"):
        session = dict(record)
        origin = json.loads(session.get("origin_json") or "{}")
        platform = origin.get("platform") or session.get("source") or ""
        user = origin.get("user_id") or session.get("user_id")
        if user:
            _register(registry, "person", [("gateway_user", f"{platform}:{user}")])
        elif platform in {"cli", "tui", "local", "desktop", "acp", "api_server", ""}:
            _register(registry, "person", [("local_user", "owner")])
        chat = session.get("session_key")
        keys = [("gateway_chat", f"{agent}:{chat}")] if chat else []
        keys.extend(lcm_keys.get(session["id"], []))
        keys.append(("hermes_session", f"{agent}:{session['id']}"))
        conversations[session["id"]] = _register(registry, "conversation", keys)
    return conversations


def _report(source: list[dict], dest: sqlite3.Connection, store: MessageStore, inserted: int) -> SourceReport:
    placements = [dict(r) for r in dest.execute("SELECT * FROM transcript_rows ORDER BY id")]
    # export_texts always queries PostgreSQL; texts()/rows_with_text alone may hit stale cache.
    exported = {r["uid"]: r for r in store.export_texts(r["text_uid"] for r in placements)}
    expected = {row["id"]: row for row in source}
    destination = []
    for placement in placements:
        text = exported.get(placement["text_uid"])
        if text is None:
            if placement["id"] not in expected:
                destination.append(ManifestRow(_native_id(placement), "missing-text"))
            continue
        if check_text_row(text):
            raise ValueError(f"damaged destination text: {placement['text_uid']}")
        # Freshly exported values, never a process cache, are the verification authority.
        from hermes_state_store import _values_from_row
        values = _values_from_row(text["content"], text["native"])
        joined = {**placement, **dict(zip(TEXT_FIELDS, values))}
        original = expected.get(placement["id"])
        if original is not None:
            for field, value in original.items():
                if joined.get(field) != value:
                    raise ValueError(f"destination differs at {_native_id(original)} field {field}")
            if payload_digest(values) != payload_digest(tuple(original.get(f) for f in TEXT_FIELDS)):
                raise ValueError(f"destination payload differs at {_native_id(original)}")
        destination.append(_manifest(joined))
    return SourceReport(f"agent:{store.agent_id}", tuple(map(_manifest, source)), tuple(destination),
                        len(source) - inserted, inserted)


def _check_existing_text(rows: list[dict], store: MessageStore) -> None:
    """Only reuse UIDs that already hold a variant present in this source."""
    allowed = {}
    wanted = []
    for row in rows:
        base = store.base_uid(row["message_uid"])
        digest = payload_digest(tuple(row.get(f) for f in TEXT_FIELDS))
        allowed.setdefault(base, set()).add(digest)
        wanted.extend((base, f"{base}#{digest[:16]}"))
    for text in store.export_texts(wanted):
        base = text["uid"].split("#", 1)[0]
        if check_text_row(text) or text["payload_sha256"] not in allowed[base]:
            raise ValueError(f"unrelated destination text UID: {text['uid']}")


def _paths(snapshot: Path, destination: Path, agent: str) -> None:
    if snapshot.resolve() == destination.resolve() or (destination.exists() and snapshot.samefile(destination)):
        raise ValueError("snapshot and destination must be separate copies")
    if destination.name != "state.db" or destination.parent.parent.name != "profiles" or agent_id_for(destination) != agent:
        raise ValueError("destination must be profiles/<agent>/state.db")


def verify_agent(snapshot_state: Path, destination_state: Path, agent: str) -> SourceReport:
    """Compare every source placement to the actual SQLite/Postgres join, including extras."""
    _paths(snapshot_state, destination_state, agent)
    with closing(_readonly(snapshot_state)) as source, closing(_readonly(destination_state)) as dest, \
            closing(MessageStore(agent)) as store:
        return _report(_source(source), dest, store, 0)


def _same_copy(source: sqlite3.Connection, destination: sqlite3.Connection) -> None:
    """An interrupted, still-legacy destination must be the same operational snapshot."""
    for (name,) in source.execute("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'"):
        quoted = '"' + name.replace('"', '""') + '"'
        left = Counter(tuple(r) for r in source.execute(f"SELECT * FROM {quoted}"))
        right = Counter(tuple(r) for r in destination.execute(f"SELECT * FROM {quoted}"))
        if left != right:
            raise ValueError(f"destination is not the source copy: {name}")


def migrate_agent(snapshot_state: Path, destination_state: Path, agent: str) -> SourceReport:
    """Import a legacy snapshot without opening SessionDB or changing the snapshot.

    PostgreSQL commits before SQLite. A failed SQLite commit can leave unreferenced text,
    which the deterministic UIDs reuse on retry; it cannot publish a dangling placement.
    A completed rerun verifies instead of repairing or skipping missing destination rows.
    """
    _paths(snapshot_state, destination_state, agent)
    with closing(_readonly(snapshot_state)) as source:
        rows = _source(source)
        new = not destination_state.exists()
        destination_state.parent.mkdir(parents=True, exist_ok=True)
        with closing(sqlite3.connect(destination_state)) as dest, closing(MessageStore(agent)) as store, \
                closing(IdentityRegistry()) as registry:
            dest.row_factory = sqlite3.Row
            if new:
                source.backup(dest)
            else:
                version = dest.execute("SELECT version FROM schema_version").fetchall()
                legacy = dest.execute("SELECT 1 FROM sqlite_master WHERE type='table' AND name='messages'").fetchone()
                if len(version) == 1 and version[0][0] == SCHEMA_VERSION and not legacy:
                    _conversations(source, agent, registry)
                    report = _report(rows, dest, store, 0)
                    if not report.ok:
                        raise ValueError(f"destination verification failed: {report.as_dict()}")
                    return report
                _same_copy(source, dest)
            dest.execute("PRAGMA secure_delete = ON")
            with dest:
                dest.execute("BEGIN IMMEDIATE")
                apply_declared_schema(dest.cursor())
                if dest.execute("SELECT 1 FROM transcript_rows LIMIT 1").fetchone():
                    raise ValueError("destination contains unrelated transcript placements")
                columns = {r[1] for r in dest.execute("PRAGMA table_info(transcript_rows)")}
                conversations = _conversations(source, agent, registry)
                _check_existing_text(rows, store)
                with store.write():
                    for row in rows:
                        placement = put_row_text(store, row, row_id=row["id"])
                        if set(placement) - columns:
                            raise ValueError(f"unsupported legacy placement columns: {set(placement) - columns}")
                        names = list(placement)
                        dest.execute(f"INSERT INTO transcript_rows ({', '.join(names)}) VALUES"
                                     f" ({', '.join('?' for _ in names)})", tuple(placement.values()))
                    for session, conversation in conversations.items():
                        store.set_conversation([store.base_uid(r["message_uid"]) for r in rows
                                                if r["session_id"] == session], conversation)
                    report = _report(rows, dest, store, len(rows))
                    if not report.ok:
                        raise ValueError(f"destination verification failed: {report.as_dict()}")
                # Remove legacy FTS copies as well as messages; secure_delete + VACUUM removes freed text.
                for name, sql in dest.execute("SELECT name, sql FROM sqlite_master WHERE type='trigger'").fetchall():
                    if "messages" in (sql or "").lower():
                        dest.execute('DROP TRIGGER "' + name.replace('"', '""') + '"')
                for (name,) in dest.execute("SELECT name FROM sqlite_master WHERE type='table' AND "
                                            "sql LIKE '%VIRTUAL TABLE%' AND name LIKE '%messages%'").fetchall():
                    dest.execute('DROP TABLE "' + name.replace('"', '""') + '"')
                dest.execute("DROP TABLE messages")
                dest.execute("UPDATE schema_version SET version = ?", (SCHEMA_VERSION,))
            dest.execute("VACUUM")
            return report
