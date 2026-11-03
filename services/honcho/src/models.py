import datetime
import re
from logging import getLogger
from typing import Any, ClassVar, cast, final, override

from dotenv import load_dotenv
from nanoid import generate as generate_nanoid
from pgvector.sqlalchemy import Vector
from sqlalchemy import (
    DDL,
    BigInteger,
    Boolean,
    CheckConstraint,
    Column,
    DateTime,
    ForeignKey,
    ForeignKeyConstraint,
    Identity,
    Index,
    Integer,
    Table,
    UniqueConstraint,
    and_,
    event,
    inspect,
    select,
    text,
)
from sqlalchemy.dialects.postgresql import JSONB, TEXT
from sqlalchemy.orm import (
    Mapped,
    MappedColumn,
    column_property,
    foreign,
    mapped_column,
    relationship,
)
from sqlalchemy.orm import Session as OrmSession
from sqlalchemy.sql import func

from src.config import settings
from src.message_store import memory_messages, persist_message_text, reject_text_update
from src.utils.types import DocumentLevel, TaskType, VectorSyncState

from .db import Base

load_dotenv(override=True)

_VECTOR_DIM: int = settings.EMBEDDING.VECTOR_DIMENSIONS

# LINA: vectors live in the store's shared vec.embeddings (lina#19), not in Honcho's own tables.
VEC_SCHEMA = "vec"


def embedding_model_name() -> str:
    """The model name vec.embeddings rows are written and searched under."""
    return settings.EMBEDDING.MODEL_CONFIG.model


logger = getLogger(__name__)


# Association table for many-to-many relationship between sessions and peers
session_peers_table = Table(
    "session_peers",
    Base.metadata,
    Column(
        "workspace_name",
        TEXT,
        ForeignKey("workspaces.name"),
        primary_key=True,
        nullable=False,
    ),
    Column(
        "session_name",
        TEXT,
        primary_key=True,
        nullable=False,
    ),
    Column("peer_name", TEXT, primary_key=True, nullable=False),
    Column(
        "configuration",
        JSONB,
        default=dict,
        nullable=False,
        server_default=text("'{}'::jsonb"),
    ),
    Column(
        "internal_metadata",
        JSONB,
        default=dict,
        nullable=False,
        server_default=text("'{}'::jsonb"),
    ),
    Column(
        "joined_at",
        DateTime(timezone=True),
        nullable=False,
        server_default=func.now(),
    ),
    Column(
        "left_at",
        DateTime(timezone=True),
        nullable=True,
    ),
    # Composite foreign key constraint for sessions
    ForeignKeyConstraint(
        ["session_name", "workspace_name"],
        ["sessions.name", "sessions.workspace_name"],
    ),
    # Composite foreign key constraint for peers
    ForeignKeyConstraint(
        ["peer_name", "workspace_name"],
        ["peers.name", "peers.workspace_name"],
    ),
    # Lets the peer-sessions list start from the peer's rows instead of walking every session
    Index(
        "ix_session_peers_workspace_peer",
        "workspace_name",
        "peer_name",
        "session_name",
    ),
)


@final
class Workspace(Base):
    __tablename__: str = "workspaces"
    deleted_at: Mapped[datetime.datetime | None] = mapped_column(
        DateTime(timezone=True), nullable=True
    )
    id: Mapped[str] = mapped_column(TEXT, default=generate_nanoid, primary_key=True)
    name: Mapped[str] = mapped_column(TEXT, unique=True)
    created_at: Mapped[datetime.datetime] = mapped_column(
        DateTime(timezone=True), server_default=func.now(), index=True
    )
    h_metadata: Mapped[dict[str, Any]] = mapped_column(
        "metadata", JSONB, default=dict, server_default=text("'{}'::jsonb")
    )
    internal_metadata: Mapped[dict[str, Any]] = mapped_column(
        "internal_metadata", JSONB, default=dict, server_default=text("'{}'::jsonb")
    )
    configuration: Mapped[dict[str, Any]] = mapped_column(
        JSONB, default=dict, server_default=text("'{}'::jsonb")
    )
    # Generation counter for the deletion lifecycle barrier
    deletion_generation: Mapped[int] = mapped_column(
        Integer, default=0, server_default=text("0")
    )

    sessions = relationship(
        "Session", back_populates="workspace", cascade="all, delete, delete-orphan"
    )
    peers = relationship(
        "Peer", back_populates="workspace", cascade="all, delete, delete-orphan"
    )
    webhook_endpoints = relationship("WebhookEndpoint", back_populates="workspace")

    __table_args__ = (
        CheckConstraint("length(id) = 21", name="id_length"),
        CheckConstraint("length(name) <= 512", name="name_length"),
        CheckConstraint("id ~ '^[A-Za-z0-9_-]+$'", name="id_format"),
    )


@final
class Peer(Base):
    __tablename__: str = "peers"
    deleted_at: Mapped[datetime.datetime | None] = mapped_column(
        DateTime(timezone=True), nullable=True
    )
    id: Mapped[str] = mapped_column(TEXT, default=generate_nanoid, primary_key=True)
    name: Mapped[str] = mapped_column(TEXT, nullable=False)
    h_metadata: Mapped[dict[str, Any]] = mapped_column(
        "metadata", JSONB, default=dict, server_default=text("'{}'::jsonb")
    )
    internal_metadata: Mapped[dict[str, Any]] = mapped_column(
        "internal_metadata", JSONB, default=dict, server_default=text("'{}'::jsonb")
    )
    created_at: Mapped[datetime.datetime] = mapped_column(
        DateTime(timezone=True), server_default=func.now(), index=True
    )
    workspace_name: Mapped[str] = mapped_column(
        ForeignKey("workspaces.name"), nullable=False, index=True
    )
    configuration: Mapped[dict[str, Any]] = mapped_column(
        JSONB, default=dict, server_default=text("'{}'::jsonb")
    )

    workspace = relationship("Workspace", back_populates="peers")
    sessions = relationship(
        "Session", secondary=session_peers_table, back_populates="peers"
    )

    __table_args__ = (
        UniqueConstraint("name", "workspace_name"),
        CheckConstraint("length(id) = 21", name="id_length"),
        CheckConstraint("length(name) <= 512", name="name_length"),
        CheckConstraint("id ~ '^[A-Za-z0-9_-]+$'", name="id_format"),
    )

    def __repr__(self) -> str:
        return f"Peer(id={self.id}, name={self.name}, workspace_name={self.workspace_name}, created_at={self.created_at}, h_metadata={self.h_metadata}, configuration={self.configuration})"


@final
class Session(Base):
    __tablename__: str = "sessions"
    deleted_at: Mapped[datetime.datetime | None] = mapped_column(
        DateTime(timezone=True), nullable=True
    )
    id: Mapped[str] = mapped_column(TEXT, primary_key=True, default=generate_nanoid)
    name: Mapped[str] = mapped_column(TEXT)
    is_active: Mapped[bool] = mapped_column(default=True, server_default=text("true"))
    h_metadata: Mapped[dict[str, Any]] = mapped_column(
        "metadata", JSONB, default=dict, server_default=text("'{}'::jsonb")
    )
    internal_metadata: Mapped[dict[str, Any]] = mapped_column(
        "internal_metadata", JSONB, default=dict, server_default=text("'{}'::jsonb")
    )
    created_at: Mapped[datetime.datetime] = mapped_column(
        DateTime(timezone=True), server_default=func.now(), index=True
    )
    workspace_name: Mapped[str] = mapped_column(
        ForeignKey("workspaces.name"), nullable=False, index=True
    )
    configuration: Mapped[dict[str, Any]] = mapped_column(
        JSONB, default=dict, server_default=text("'{}'::jsonb")
    )
    # Generation counter for the deletion lifecycle barrier
    deletion_generation: Mapped[int] = mapped_column(
        Integer, default=0, server_default=text("0")
    )

    workspace = relationship("Workspace", back_populates="sessions")
    peers = relationship(
        "Peer", secondary=session_peers_table, back_populates="sessions"
    )
    messages = relationship("Message", back_populates="session")

    __table_args__ = (
        UniqueConstraint("name", "workspace_name"),
        CheckConstraint("length(name) <= 512", name="name_length"),
        CheckConstraint("length(id) = 21", name="id_length"),
        CheckConstraint("id ~ '^[A-Za-z0-9_-]+$'", name="id_format"),
    )

    def __repr__(self) -> str:
        return f"Session(id={self.id}, name={self.name}, workspace_name={self.workspace_name}, is_active={self.is_active}, created_at={self.created_at}, h_metadata={self.h_metadata})"


@final
class Message(Base):
    __tablename__: str = "message_meta"
    deleted_at: Mapped[datetime.datetime | None] = mapped_column(
        DateTime(timezone=True), nullable=True
    )
    id: Mapped[int] = mapped_column(
        "message_id",
        BigInteger,
        ForeignKey("memory.messages.message_id", ondelete="CASCADE"),
        primary_key=True,
        autoincrement=False,
    )
    public_id: Mapped[str] = mapped_column(
        TEXT,
        unique=True,
        default=generate_nanoid,
    )
    # NOTE: Messages in Honcho 2.0 could historically be stored outside of a session.
    # We have since assigned all of these messages to a default session.
    session_name: Mapped[str] = mapped_column(TEXT, nullable=False)
    content: Mapped[str] = column_property(
        select(memory_messages.c.content)
        .where(memory_messages.c.message_id == id)
        .correlate_except(memory_messages)
        .scalar_subquery(),
        expire_on_flush=False,
    )
    h_metadata: Mapped[dict[str, Any]] = mapped_column(
        "metadata", JSONB, default=dict, server_default=text("'{}'::jsonb")
    )
    internal_metadata: Mapped[dict[str, Any]] = mapped_column(
        "internal_metadata", JSONB, default=dict, server_default=text("'{}'::jsonb")
    )
    token_count: Mapped[int] = mapped_column(Integer, default=0, nullable=False)
    seq_in_session: Mapped[int] = mapped_column(BigInteger, nullable=False)

    created_at: Mapped[datetime.datetime] = mapped_column(
        DateTime(timezone=True), server_default=func.now(), index=True
    )
    # Note: Foreign key relationships established via composite ForeignKeyConstraint below
    peer_name: Mapped[str] = mapped_column(TEXT, index=True)
    workspace_name: Mapped[str] = mapped_column(TEXT, index=True)

    session = relationship("Session", back_populates="messages")

    __table_args__ = (
        CheckConstraint("length(public_id) = 21", name="public_id_length"),
        CheckConstraint("public_id ~ '^[A-Za-z0-9_-]+$'", name="public_id_format"),
        # Composite foreign key constraint for sessions
        ForeignKeyConstraint(
            ["session_name", "workspace_name"],
            ["sessions.name", "sessions.workspace_name"],
        ),
        # Composite foreign key constraint for peers
        ForeignKeyConstraint(
            ["peer_name", "workspace_name"],
            ["peers.name", "peers.workspace_name"],
        ),
        Index(
            "ix_messages_session_lookup",
            "session_name",
            "message_id",
            postgresql_include=["message_id", "created_at"],
        ),
        UniqueConstraint(
            "workspace_name",
            "session_name",
            "seq_in_session",
        ),
    )

    @override
    def __repr__(self) -> str:
        return f"Message(id={self.id}, session_name={self.session_name}, peer_name={self.peer_name}, content={self.content})"


event.listen(Message, "before_insert", persist_message_text)
event.listen(Message, "before_update", reject_text_update)
event.listen(
    Message.__table__,
    "after_create",
    DDL("""
        CREATE OR REPLACE FUNCTION %(schema)s.detach_message_sources()
        RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
        SET search_path = pg_catalog AS $$ BEGIN
            DELETE FROM %(schema)s.document_sources
            WHERE source_id = OLD.public_id
              AND workspace_name = OLD.workspace_name;
            RETURN OLD;
        END $$;
        CREATE TRIGGER detach_message_sources BEFORE DELETE ON %(fullname)s
        FOR EACH ROW EXECUTE FUNCTION %(schema)s.detach_message_sources();
    """),
)


@final
class VecEmbedding(Base):
    """One row of LINA's vec.embeddings (services/store/migrations/0003_vec_embeddings.sql).

    Honcho writes kind ``message`` (item_id = message_embeddings.id) and kind
    ``document`` (item_id = documents.vec_id); agent_id is the workspace name.
    """

    __tablename__: str = "embeddings"

    kind: Mapped[str] = mapped_column(TEXT, primary_key=True)
    item_id: Mapped[int] = mapped_column(BigInteger, primary_key=True)
    model: Mapped[str] = mapped_column(TEXT, primary_key=True)
    agent_id: Mapped[str] = mapped_column(TEXT, nullable=False)
    dim: Mapped[int] = mapped_column(Integer, nullable=False)
    embedding: MappedColumn[Any] = mapped_column(Vector(_VECTOR_DIM), nullable=False)

    __table_args__ = (
        Index(
            "ix_embeddings_embedding_hnsw",
            "embedding",
            postgresql_using="hnsw",
            postgresql_with={"m": 16, "ef_construction": 64},
            postgresql_ops={"embedding": "vector_cosine_ops"},
        ),
        {"schema": VEC_SCHEMA},
    )


class VecEmbedded:
    """``embedding`` of a row whose vector is kept in vec.embeddings.

    Reading gives the vector (or None); assigning a vector writes the
    vec.embeddings row with the owning row's flush, assigning None deletes it.
    """

    vec_kind: ClassVar[str]
    vec_row: Any
    workspace_name: Any

    @property
    def embedding(self) -> Any:
        row = self.vec_row
        return None if row is None else row.embedding

    @embedding.setter
    def embedding(self, value: Any) -> None:
        if value is None:
            self.vec_row = None
            return
        if self.vec_row is not None:
            self.vec_row.embedding = value
            self.vec_row.dim = len(value)
            return
        self.vec_row = VecEmbedding(
            kind=self.vec_kind,
            model=embedding_model_name(),
            dim=len(value),
            embedding=value,
        )


def _vec_relationship(owner: str, key_column: str) -> Any:
    return relationship(
        VecEmbedding,
        primaryjoin=lambda: and_(
            foreign(VecEmbedding.item_id) == getattr(_OWNERS[owner], key_column),
            VecEmbedding.kind == _OWNERS[owner].vec_kind,
            VecEmbedding.model == embedding_model_name(),
        ),
        uselist=False,
        lazy="selectin",
        cascade="all, delete-orphan",
        single_parent=True,
        # Both owners write item_id; kind keeps their rows apart.
        overlaps="vec_row",
    )


@final
class MessageEmbedding(VecEmbedded, Base):
    __tablename__: str = "message_embeddings"
    vec_kind: ClassVar[str] = "message"

    id: Mapped[int] = mapped_column(
        BigInteger, Identity(), primary_key=True, autoincrement=True
    )
    content: Mapped[str] = mapped_column(TEXT)
    vec_row: Mapped[VecEmbedding | None] = _vec_relationship("message", "id")
    message_id: Mapped[str] = mapped_column(
        ForeignKey("message_meta.public_id", ondelete="CASCADE"),
        nullable=False,
        index=True,
    )
    workspace_name: Mapped[str] = mapped_column(
        ForeignKey("workspaces.name"), nullable=False, index=True
    )
    session_name: Mapped[str] = mapped_column(TEXT, nullable=False, index=True)
    peer_name: Mapped[str] = mapped_column(TEXT, nullable=False, index=True)
    created_at: Mapped[datetime.datetime] = mapped_column(
        DateTime(timezone=True), server_default=func.now(), index=True
    )
    # Vector sync state tracking
    sync_state: Mapped[VectorSyncState] = mapped_column(
        TEXT, nullable=False, server_default="pending", index=True
    )
    last_sync_at: Mapped[datetime.datetime | None] = mapped_column(
        DateTime(timezone=True), nullable=True
    )
    sync_attempts: Mapped[int] = mapped_column(
        Integer, nullable=False, default=0, server_default=text("0")
    )

    __table_args__ = (
        # Compound foreign key constraints
        ForeignKeyConstraint(
            ["session_name", "workspace_name"],
            ["sessions.name", "sessions.workspace_name"],
        ),
        ForeignKeyConstraint(
            ["peer_name", "workspace_name"],
            ["peers.name", "peers.workspace_name"],
        ),
        # Composite index for efficient reconciliation queries
        Index(
            "ix_message_embeddings_sync_state_last_sync_at",
            "sync_state",
            "last_sync_at",
        ),
    )


@final
class Collection(Base):
    __tablename__: str = "collections"

    id: Mapped[str] = mapped_column(TEXT, default=generate_nanoid, primary_key=True)
    observer: Mapped[str] = mapped_column(TEXT, index=True)
    observed: Mapped[str] = mapped_column(TEXT, index=True)
    created_at: Mapped[datetime.datetime] = mapped_column(
        DateTime(timezone=True), server_default=func.now(), index=True
    )
    h_metadata: Mapped[dict[str, Any]] = mapped_column(
        "metadata", JSONB, default=dict, server_default=text("'{}'::jsonb")
    )
    internal_metadata: Mapped[dict[str, Any]] = mapped_column(
        "internal_metadata", JSONB, default=dict, server_default=text("'{}'::jsonb")
    )
    documents = relationship(
        "Document", back_populates="collection", cascade="all, delete, delete-orphan"
    )
    workspace_name: Mapped[str] = mapped_column(
        ForeignKey("workspaces.name"), nullable=False, index=True
    )
    # Generation counter for the deletion lifecycle barrier
    deletion_generation: Mapped[int] = mapped_column(
        Integer, default=0, server_default=text("0")
    )

    __table_args__ = (
        UniqueConstraint(
            "observer",
            "observed",
            "workspace_name",
        ),
        CheckConstraint("length(id) = 21", name="id_length"),
        CheckConstraint("id ~ '^[A-Za-z0-9_-]+$'", name="id_format"),
        # Composite foreign key constraint for observer peer
        ForeignKeyConstraint(
            ["observer", "workspace_name"],
            ["peers.name", "peers.workspace_name"],
        ),
        # Composite foreign key constraint for observed peer
        ForeignKeyConstraint(
            ["observed", "workspace_name"],
            ["peers.name", "peers.workspace_name"],
        ),
    )


# Shape of a document id; legacy linkage arrays occasionally hold other refs.
SOURCE_ID_RE = re.compile(r"^[A-Za-z0-9_-]{21}$")


@final
class Document(VecEmbedded, Base):
    __tablename__: str = "documents"
    vec_kind: ClassVar[str] = "document"
    # vec_id is server-generated and keys the vec.embeddings row: fetch it on insert.
    __mapper_args__: ClassVar[dict[str, Any]] = {"eager_defaults": True}
    id: Mapped[str] = mapped_column(TEXT, default=generate_nanoid, primary_key=True)
    # The integer id vec.embeddings.item_id needs (id is text).
    vec_id: Mapped[int] = mapped_column(
        BigInteger, Identity(), nullable=False, unique=True
    )
    internal_metadata: Mapped[dict[str, Any]] = mapped_column(
        "internal_metadata", JSONB, default=dict, server_default=text("'{}'::jsonb")
    )
    content: Mapped[str] = mapped_column(TEXT)
    level: Mapped[DocumentLevel] = mapped_column(
        TEXT, nullable=False, server_default="explicit"
    )
    times_derived: Mapped[int] = mapped_column(
        Integer, nullable=False, server_default=text("1")
    )
    vec_row: Mapped[VecEmbedding | None] = _vec_relationship("document", "vec_id")
    created_at: Mapped[datetime.datetime] = mapped_column(
        DateTime(timezone=True), server_default=func.now(), index=True
    )

    observer: Mapped[str] = mapped_column(TEXT, index=True)
    observed: Mapped[str] = mapped_column(TEXT, index=True)
    workspace_name: Mapped[str] = mapped_column(
        ForeignKey("workspaces.name"), nullable=False, index=True
    )
    session_name: Mapped[str | None] = mapped_column(TEXT, nullable=True, index=True)
    deleted_at: Mapped[datetime.datetime | None] = mapped_column(
        DateTime(timezone=True), nullable=True, index=True, default=None
    )

    # Vector sync state tracking
    sync_state: Mapped[VectorSyncState] = mapped_column(
        TEXT, nullable=False, server_default="pending", index=True
    )
    last_sync_at: Mapped[datetime.datetime | None] = mapped_column(
        DateTime(timezone=True), nullable=True
    )
    sync_attempts: Mapped[int] = mapped_column(
        Integer, nullable=False, default=0, server_default=text("0")
    )

    collection = relationship("Collection", back_populates="documents")

    # selectin (not lazy) is required: async lazy-loads raise MissingGreenlet.
    source_links: Mapped[list["DocumentSource"]] = relationship(
        "DocumentSource",
        order_by="DocumentSource.position",
        cascade="all, delete-orphan",
        passive_deletes=True,
        lazy="selectin",
    )

    # Pre-document_sources linkage. Read-only: the reconciler drains it into
    # document_sources and NULLs it; a follow-up migration drops the column.
    legacy_source_ids: Mapped[list[str] | None] = mapped_column(
        "source_ids", JSONB(none_as_null=True), nullable=True
    )

    @property
    def source_ids(self) -> list[str] | None:
        """Parent conclusion IDs in original order; None when unlinked.

        Falls back to the legacy JSONB locations for rows the reconciler has
        not drained yet, in the same precedence order as the drain.
        """
        if self.source_links:
            return [link.source_id for link in self.source_links]
        for candidate in (
            self.legacy_source_ids,
            self.internal_metadata.get("source_ids"),
            self.internal_metadata.get("premise_ids"),
        ):
            if isinstance(candidate, list):
                ids = [
                    sid
                    for sid in dict.fromkeys(cast(list[Any], candidate))
                    if isinstance(sid, str) and SOURCE_ID_RE.match(sid)
                ]
                return ids or None
        return None

    @source_ids.setter
    def source_ids(self, value: list[str] | None) -> None:
        # Constructor convenience: kwargs apply in order, so workspace_name
        # must be passed before source_ids. Dedupes (PK is (derived_id,
        # source_id)); crud.build_source_links additionally drops malformed
        # IDs from LLM output before they reach this point.
        self.source_links = [
            DocumentSource(
                source_id=sid, position=i, workspace_name=self.workspace_name
            )
            for i, sid in enumerate(dict.fromkeys(value or []))
        ]

    # Messages the deriver cited as evidence for an explicit conclusion.
    source_message_links: Mapped[list["ConclusionSourceMessage"]] = relationship(
        "ConclusionSourceMessage",
        order_by="ConclusionSourceMessage.position",
        cascade="all, delete-orphan",
        passive_deletes=True,
        lazy="selectin",
    )

    @property
    def source_message_ids(self) -> list[str] | None:
        """Public ids of the cited messages in citation order; None when uncited."""
        if self.source_message_links:
            return [link.message_id for link in self.source_message_links]
        return None

    @source_message_ids.setter
    def source_message_ids(self, value: list[str] | None) -> None:
        # Same constructor ordering caveat as source_ids: workspace_name first.
        self.source_message_links = [
            ConclusionSourceMessage(
                message_id=mid, position=i, workspace_name=self.workspace_name
            )
            for i, mid in enumerate(dict.fromkeys(value or []))
        ]

    __table_args__ = (
        CheckConstraint("length(id) = 21", name="id_length"),
        CheckConstraint("length(content) <= 65535", name="content_length"),
        CheckConstraint("id ~ '^[A-Za-z0-9_-]+$'", name="id_format"),
        # Composite foreign key constraint for collections
        ForeignKeyConstraint(
            ["observer", "observed", "workspace_name"],
            [
                "collections.observer",
                "collections.observed",
                "collections.workspace_name",
            ],
        ),
        # Composite foreign key constraint for observer peer
        ForeignKeyConstraint(
            ["observer", "workspace_name"],
            ["peers.name", "peers.workspace_name"],
        ),
        # Composite foreign key constraint for observed peer
        ForeignKeyConstraint(
            ["observed", "workspace_name"],
            ["peers.name", "peers.workspace_name"],
        ),
        # Composite foreign key constraint for sessions
        ForeignKeyConstraint(
            ["session_name", "workspace_name"],
            ["sessions.name", "sessions.workspace_name"],
        ),
        # Composite index for efficient reconciliation queries
        Index(
            "ix_documents_sync_state_last_sync_at",
            "sync_state",
            "last_sync_at",
        ),
        # Serves reverse traversal on undrained legacy rows; dropped with the
        # column in the follow-up migration.
        Index(
            "ix_documents_source_ids_gin",
            "source_ids",
            postgresql_using="gin",
        ),
        # Rows the reconciler still has to drain; empty once the drain
        # finishes. Dropped with the column in the follow-up migration.
        Index(
            "ix_documents_legacy_sources_pending",
            "id",
            postgresql_where=text(
                "source_ids IS NOT NULL OR internal_metadata ?| ARRAY['source_ids', 'premise_ids']"  # noqa: E501
            ),
        ),
    )


@final
class DocumentSource(Base):
    """One reasoning-tree edge: derived_id was concluded from source_id."""

    __tablename__: str = "document_sources"

    derived_id: Mapped[str] = mapped_column(
        ForeignKey("documents.id", ondelete="CASCADE"), primary_key=True
    )
    # Deliberately not an FK: the dreamer can emit IDs that never resolve,
    # and sources may be deleted independently of their children.
    source_id: Mapped[str] = mapped_column(TEXT, primary_key=True)
    position: Mapped[int] = mapped_column(
        Integer, nullable=False, server_default=text("0")
    )
    workspace_name: Mapped[str] = mapped_column(
        ForeignKey("workspaces.name"), nullable=False
    )

    __table_args__ = (
        # Reverse traversal ("who derived from me?") — replaces the old GIN index
        Index("ix_document_sources_source_id", "source_id", "workspace_name"),
        CheckConstraint("length(source_id) = 21", name="source_id_length"),
        CheckConstraint("source_id ~ '^[A-Za-z0-9_-]+$'", name="source_id_format"),
    )


@final
class ConclusionSourceMessage(Base):
    """One evidence edge: conclusion_id cites message_id as evidence."""

    __tablename__: str = "conclusion_source_messages"

    conclusion_id: Mapped[str] = mapped_column(
        ForeignKey("documents.id", ondelete="CASCADE"), primary_key=True
    )
    message_id: Mapped[str] = mapped_column(
        ForeignKey("message_meta.public_id", ondelete="CASCADE"), primary_key=True
    )
    position: Mapped[int] = mapped_column(
        Integer, nullable=False, server_default=text("0")
    )
    workspace_name: Mapped[str] = mapped_column(
        ForeignKey("workspaces.name"), nullable=False
    )

    __table_args__ = (
        Index(
            "ix_conclusion_source_messages_message_id", "message_id", "workspace_name"
        ),
        CheckConstraint("length(message_id) = 21", name="message_id_length"),
        CheckConstraint("message_id ~ '^[A-Za-z0-9_-]+$'", name="message_id_format"),
    )


@final
class QueueItem(Base):
    __tablename__: str = "queue"
    id: Mapped[int] = mapped_column(
        BigInteger, Identity(), primary_key=True, autoincrement=True
    )
    session_id: Mapped[str | None] = mapped_column(
        ForeignKey("sessions.id"), nullable=True, index=True
    )
    work_unit_key: Mapped[str] = mapped_column(TEXT, nullable=False)

    task_type: Mapped[TaskType] = mapped_column(TEXT, nullable=False)
    payload: Mapped[dict[str, Any]] = mapped_column(JSONB, nullable=False)
    processed: Mapped[bool] = mapped_column(
        Boolean, default=False, server_default=text("false"), index=True
    )
    error: Mapped[str | None] = mapped_column(TEXT, nullable=True)
    created_at: Mapped[datetime.datetime] = mapped_column(
        DateTime(timezone=True), server_default=func.now(), index=True
    )
    workspace_name: Mapped[str | None] = mapped_column(
        ForeignKey("workspaces.name"), nullable=True, index=True
    )
    message_id: Mapped[int | None] = mapped_column(
        BigInteger,
        ForeignKey("message_meta.message_id", ondelete="CASCADE"),
        nullable=True,
    )

    __table_args__ = (
        Index(
            "ix_queue_message_id_not_null",
            "message_id",
            postgresql_where=text("message_id IS NOT NULL"),
        ),
        Index(
            "ix_queue_work_unit_key_processed_id",
            "work_unit_key",
            "processed",
            "id",
        ),
        # Partial unique index for reconciler task deduplication
        Index(
            "uq_queue_reconciler_pending_work_unit_key",
            "work_unit_key",
            unique=True,
            postgresql_where=text("task_type = 'reconciler' AND processed = false"),
        ),
        # Partial unique index for dream task deduplication
        Index(
            "uq_queue_dream_pending_work_unit_key",
            "work_unit_key",
            unique=True,
            postgresql_where=text("task_type = 'dream' AND processed = false"),
        ),
        # Pending-work lookups by the peer or session a payload references.
        # The observers index needs btree_gin for its workspace_name column.
        Index(
            "ix_queue_pending_observers",
            "workspace_name",
            text("(payload -> 'observers')"),
            postgresql_using="gin",
            postgresql_where=text(
                "processed = false AND (payload -> 'observers') IS NOT NULL"
            ),
        ),
        *(
            Index(
                f"ix_queue_pending_{key}",
                "workspace_name",
                text(f"(payload ->> '{key}')"),
                postgresql_where=text(
                    f"processed = false AND (payload ->> '{key}') IS NOT NULL"
                ),
            )
            for key in ("observed", "observer", "scope_peer", "session_name")
        ),
    )

    def __repr__(self) -> str:
        return f"QueueItem(id={self.id}, session_id={self.session_id}, work_unit_key={self.work_unit_key}, task_type={self.task_type}, payload={self.payload}, processed={self.processed}, workspace_name={self.workspace_name}, message_id={self.message_id})"


@final
class ActiveQueueSession(Base):
    __tablename__: str = "active_queue_sessions"

    id: Mapped[str] = mapped_column(TEXT, default=generate_nanoid, primary_key=True)

    work_unit_key: Mapped[str] = mapped_column(TEXT, unique=True)

    last_updated: Mapped[datetime.datetime] = mapped_column(
        DateTime(timezone=True), server_default=func.now(), onupdate=func.now()
    )


@final
class WebhookEndpoint(Base):
    __tablename__: str = "webhook_endpoints"
    id: Mapped[str] = mapped_column(TEXT, default=generate_nanoid, primary_key=True)
    workspace_name: Mapped[str] = mapped_column(
        ForeignKey("workspaces.name"), nullable=False, index=True
    )
    url: Mapped[str] = mapped_column(TEXT, nullable=False)
    created_at: Mapped[datetime.datetime] = mapped_column(
        DateTime(timezone=True), server_default=func.now()
    )

    workspace = relationship("Workspace", back_populates="webhook_endpoints")

    __table_args__ = (CheckConstraint("length(url) <= 2048", name="url_length"),)

    def __repr__(self) -> str:
        return f"WebhookEndpoint(id={self.id}, workspace_name={self.workspace_name}, url={self.url})"


@final
class SessionPeer(Base):
    __table__: Table = session_peers_table

    # Type annotations for the columns
    workspace_name: Mapped[str]
    session_name: Mapped[str]
    peer_name: Mapped[str]
    configuration: Mapped[dict[str, Any]]
    internal_metadata: Mapped[dict[str, Any]]
    joined_at: Mapped[datetime.datetime]
    left_at: Mapped[datetime.datetime | None]


_OWNERS: dict[str, type[Any]] = {"message": MessageEmbedding, "document": Document}


@event.listens_for(OrmSession, "before_flush")
def _vec_rows_carry_workspace(
    session: OrmSession, _flush_context: Any, _instances: Any
) -> None:
    """A new vec.embeddings row takes its owner's workspace as agent_id."""
    for obj in (*session.new, *session.dirty):
        if not isinstance(obj, VecEmbedded) or "vec_row" in inspect(obj).unloaded:
            continue
        row = obj.vec_row
        if row is not None and row.agent_id is None:
            row.agent_id = obj.workspace_name


def vector_cleanup_statements(owner_schema: str, vec_schema: str) -> list[str]:
    """Triggers that delete an owner row's vec.embeddings rows with it, however it is
    deleted (ORM, bulk DELETE, foreign-key cascade). The migration installs the same."""
    statements: list[str] = []
    for table, kind, key in (
        ("documents", "document", "vec_id"),
        ("message_embeddings", "message", "id"),
    ):
        function = f'"{owner_schema}".lina_drop_{table}_vectors'
        statements += [
            f"""CREATE OR REPLACE FUNCTION {function}() RETURNS trigger
                LANGUAGE plpgsql AS $fn$
                BEGIN
                    DELETE FROM "{vec_schema}".embeddings e USING old_rows o
                    WHERE e.kind = '{kind}' AND e.item_id = o.{key};
                    RETURN NULL;
                END $fn$""",
            f'DROP TRIGGER IF EXISTS lina_drop_vectors ON "{owner_schema}".{table}',
            f"""CREATE TRIGGER lina_drop_vectors AFTER DELETE ON "{owner_schema}".{table}
                REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT
                EXECUTE FUNCTION {function}()""",
        ]
    return statements


@event.listens_for(Base.metadata, "after_create")
def _install_vector_cleanup(_target: Any, connection: Any, **_kw: Any) -> None:
    owner_schema = Document.__table__.schema or "public"
    vec_schema = VecEmbedding.__table__.schema or "public"
    for statement in vector_cleanup_statements(owner_schema, vec_schema):
        connection.exec_driver_sql(statement)
