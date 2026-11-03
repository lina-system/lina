"""Immutable shared text, written on the metadata flush's transaction."""

import hashlib
from datetime import UTC, datetime
from typing import TYPE_CHECKING

from nanoid import generate as generate_nanoid
from sqlalchemy import (
    BigInteger,
    CheckConstraint,
    Column,
    Computed,
    DateTime,
    Identity,
    Table,
    select,
    text,
)
from sqlalchemy.dialects.postgresql import JSONB, TEXT, TSVECTOR, insert
from sqlalchemy.engine import Connection
from sqlalchemy.orm import Mapper
from sqlalchemy.orm.attributes import get_history

from src.db import Base

if TYPE_CHECKING:
    from src.models import Message

# Store migrations own this schema. The complete fixed definition also lets
# Honcho's isolated create_all fixtures exercise the same column contract.
memory_messages = Table(
    "messages",
    Base.metadata,
    Column("message_id", BigInteger, Identity(always=True), primary_key=True),
    Column("uid", TEXT, nullable=False, unique=True),
    Column("source", TEXT, nullable=False),
    Column("agent_id", TEXT),
    Column("session_id", TEXT),
    Column("conversation_id", TEXT),
    Column("seq_in_session", BigInteger),
    Column("role", TEXT),
    Column("content", TEXT),
    Column("tool_name", TEXT),
    Column("tool_call_id", TEXT),
    Column("tool_calls", JSONB),
    Column("ts", DateTime(timezone=True)),
    Column(
        "observed_at",
        DateTime(timezone=True),
        nullable=False,
        server_default=text("now()"),
    ),
    Column("scope", TEXT, nullable=False, server_default="agent"),
    Column("native", JSONB),
    Column("content_sha256", TEXT, nullable=False),
    Column("forgotten_at", DateTime(timezone=True)),
    Column(
        "tsv",
        TSVECTOR,
        Computed("to_tsvector('simple', coalesce(content, ''))", persisted=True),
    ),
    CheckConstraint("scope IN ('agent', 'agents', 'person')", name="scope"),
    schema="memory",
)


def persist_message_text(
    _mapper: Mapper["Message"], connection: Connection, target: "Message"
) -> None:
    """Resolve text before inserting metadata, without committing either row."""
    target.public_id = target.public_id or generate_nanoid()
    target.created_at = target.created_at or datetime.now(UTC)
    identity = target.internal_metadata or {}
    agent_id = identity.get("agent_id") or target.workspace_name
    uid = identity.get("uid") or f"honcho:{target.workspace_name}:{target.public_id}"
    content = target.content
    if len(content) > 65535:
        raise ValueError("Message content exceeds 65535 characters")
    digest = hashlib.sha256(content.encode("utf-8")).hexdigest()
    values = {
        "uid": uid,
        "source": identity.get("source") or "agent",
        "agent_id": agent_id,
        "session_id": target.session_name,
        "seq_in_session": target.seq_in_session,
        "content": content,
        "content_sha256": digest,
        "ts": target.created_at,
    }
    connection.execute(
        insert(memory_messages)
        .values(**values)
        .on_conflict_do_nothing(index_elements=["uid"])
    )
    row = (
        connection.execute(select(memory_messages).where(memory_messages.c.uid == uid))
        .mappings()
        .one()
    )
    if (
        row["content"] != content
        or row["content_sha256"] != digest
        or row["agent_id"] != agent_id
    ):
        raise ValueError(
            "Shared message identity does not match its immutable text or agent"
        )
    target.id = row["message_id"]


def reject_text_update(
    _mapper: Mapper["Message"], _connection: Connection, target: "Message"
) -> None:
    """A computed ORM attribute must not silently accept a text rewrite."""
    if get_history(target, "content").has_changes():
        raise ValueError("Shared message text is immutable")
