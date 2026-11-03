"""Move message text to the immutable shared store.

Revision ID: l13_message_split
Revises: c4d8e2f6a1b3

One shared ID can own only one metadata row. In old-ID order, the first
matching placement claims it; later placements get their own
honcho:<workspace>:<public_id> UID, preserving both their public IDs and text.
Restore the pre-upgrade backup to undo this irreversible identity merge.
"""

import hashlib
from collections.abc import Sequence

import sqlalchemy as sa
from alembic import op

from migrations.utils import get_schema

revision: str = "l13_message_split"
down_revision: str | None = "c4d8e2f6a1b3"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    connection = op.get_bind()
    schema = get_schema()
    quote = connection.dialect.identifier_preparer.quote
    ns = quote(schema)
    # Alembic runs the whole revision in one transaction. A completed direct
    # invocation is also a no-op; a failed invocation rolls back all DDL/data.
    if sa.inspect(connection).has_table("message_meta", schema=schema):
        return
    connection.execute(
        sa.text(f"LOCK TABLE {ns}.messages, {ns}.queue IN ACCESS EXCLUSIVE MODE")
    )
    connection.execute(
        sa.text(
            "CREATE TEMP TABLE message_split_ids (old_id bigint PRIMARY KEY, new_id bigint UNIQUE NOT NULL, staging_id bigint UNIQUE) ON COMMIT DROP"
        )
    )
    claimed: set[int] = set()
    rows = connection.execute(
        sa.text(f"SELECT * FROM {ns}.messages ORDER BY id")
    ).mappings()
    for row in rows:
        metadata = row["metadata"] | row["internal_metadata"]
        fallback_uid = f"honcho:{row['workspace_name']}:{row['public_id']}"
        uid = metadata.get("uid") or fallback_uid
        agent_id = metadata.get("agent_id") or row["workspace_name"]
        session_id = metadata.get("session_id") or row["session_name"]
        digest = hashlib.sha256(row["content"].encode("utf-8")).hexdigest()
        params = {
            "uid": uid,
            "agent_id": agent_id,
            "session_id": session_id,
            "ts": row["created_at"],
            "digest": digest,
            "content": row["content"],
        }
        candidate = (
            connection.execute(
                sa.text(
                    "SELECT message_id, content, content_sha256, agent_id FROM memory.messages WHERE uid = :uid"
                ),
                params,
            )
            .mappings()
            .first()
        )
        if candidate is not None and (
            candidate["content"] != row["content"]
            or candidate["content_sha256"] != digest
            or candidate["agent_id"] != agent_id
        ):
            raise RuntimeError(
                f"Shared message UID {uid!r} has different content or agent"
            )
        if candidate is None:
            candidates = connection.execute(
                sa.text("""
                SELECT message_id, content FROM memory.messages
                WHERE content_sha256 = :digest AND session_id = :session_id
                  AND ts = :ts AND agent_id = :agent_id ORDER BY message_id
            """),
                params,
            ).mappings()
            candidate = next(
                (
                    item
                    for item in candidates
                    if item["message_id"] not in claimed
                    and item["content"] == row["content"]
                ),
                None,
            )
        if candidate is None or candidate["message_id"] in claimed:
            # A duplicate claim must not collapse Honcho placements. The private
            # UID is stable across retries and never mutates a shared text row.
            params.update(
                uid=fallback_uid if candidate is not None else uid,
                source=metadata.get("source") or "agent",
                role=metadata.get("role"),
                seq=row["seq_in_session"],
            )
            connection.execute(
                sa.text("""
                INSERT INTO memory.messages
                    (uid, source, agent_id, session_id, seq_in_session, role, content, ts, content_sha256)
                VALUES (:uid, :source, :agent_id, :session_id, :seq, :role, :content, :ts, :digest)
                ON CONFLICT (uid) DO NOTHING
            """),
                params,
            )
            candidate = (
                connection.execute(
                    sa.text(
                        "SELECT message_id, content, content_sha256, agent_id FROM memory.messages WHERE uid = :uid"
                    ),
                    params,
                )
                .mappings()
                .one()
            )
            if (
                candidate["content"] != row["content"]
                or candidate["content_sha256"] != digest
                or candidate["agent_id"] != agent_id
                or candidate["message_id"] in claimed
            ):
                raise RuntimeError(f"Cannot claim fallback UID {fallback_uid!r}")
        new_id = candidate["message_id"]
        claimed.add(new_id)
        connection.execute(
            sa.text(
                "INSERT INTO message_split_ids (old_id, new_id) VALUES (:old, :new)"
            ),
            {"old": row["id"], "new": new_id},
        )

    queue_fks = sa.inspect(connection).get_foreign_keys("queue", schema=schema)
    for fk in queue_fks:
        if fk["referred_table"] == "messages" and fk["constrained_columns"] == [
            "message_id"
        ]:
            assert fk["name"] is not None
            connection.execute(
                sa.text(f"ALTER TABLE {ns}.queue DROP CONSTRAINT {quote(fk['name'])}")
            )
    connection.execute(sa.text(f"ALTER TABLE {ns}.messages RENAME TO message_meta"))
    connection.execute(
        sa.text(f"ALTER TABLE {ns}.message_meta RENAME COLUMN id TO message_id")
    )
    connection.execute(
        sa.text(f"ALTER TABLE {ns}.message_meta ALTER COLUMN message_id DROP IDENTITY")
    )
    # Two disjoint key spaces avoid collisions even when old and new IDs swap.
    connection.execute(
        sa.text("""
        UPDATE message_split_ids m SET staging_id = numbered.staging_id
        FROM (SELECT old_id, least(0, min(old_id) OVER (), min(new_id) OVER ())
              - row_number() OVER (ORDER BY old_id) AS staging_id
              FROM message_split_ids) numbered WHERE numbered.old_id = m.old_id
    """)
    )
    connection.execute(
        sa.text(
            f"UPDATE {ns}.message_meta m SET message_id = ids.staging_id FROM message_split_ids ids WHERE m.message_id = ids.old_id"
        )
    )
    connection.execute(
        sa.text(
            f"UPDATE {ns}.message_meta m SET message_id = ids.new_id FROM message_split_ids ids WHERE m.message_id = ids.staging_id"
        )
    )
    connection.execute(
        sa.text(
            f"UPDATE {ns}.queue q SET message_id = ids.new_id FROM message_split_ids ids WHERE q.message_id = ids.old_id"
        )
    )
    connection.execute(
        sa.text(
            f"ALTER TABLE {ns}.message_meta ADD CONSTRAINT fk_message_meta_message_id_memory FOREIGN KEY (message_id) REFERENCES memory.messages(message_id) ON DELETE CASCADE"
        )
    )
    for fk in queue_fks:
        if fk["referred_table"] == "messages" and fk["constrained_columns"] == [
            "message_id"
        ]:
            assert fk["name"] is not None
            connection.execute(
                sa.text(
                    f"ALTER TABLE {ns}.queue ADD CONSTRAINT {quote(fk['name'])} FOREIGN KEY (message_id) REFERENCES {ns}.message_meta(message_id) ON DELETE CASCADE"
                )
            )
    # Dropping content also removes its check and text-search indexes; every
    # metadata index and the public-ID reference targets survive the rename.
    connection.execute(sa.text(f"ALTER TABLE {ns}.message_meta DROP COLUMN content"))
    connection.execute(
        sa.text(f"""
        CREATE FUNCTION {ns}.detach_message_sources() RETURNS trigger
        LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$ BEGIN
            DELETE FROM {ns}.document_sources
            WHERE source_id = OLD.public_id AND workspace_name = OLD.workspace_name;
            RETURN OLD;
        END $$
    """)
    )
    connection.execute(
        sa.text(
            f"CREATE TRIGGER detach_message_sources BEFORE DELETE ON {ns}.message_meta FOR EACH ROW EXECUTE FUNCTION {ns}.detach_message_sources()"
        )
    )


def downgrade() -> None:
    raise RuntimeError(
        "Message text split is irreversible; restore the pre-upgrade backup"
    )
