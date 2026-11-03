"""move message and document vectors to vec.embeddings (LINA, lina#19)

LINA keeps every memory source's vectors in one table, ``vec.embeddings``,
written with one model at one width, so one search ranks them together. The
store creates the table (services/store/migrations/0003_vec_embeddings.sql)
before this job runs; a database without it (Honcho on its own, the tests) gets
it here, at ``EMBEDDING_VECTOR_DIMENSIONS``.

* ``documents`` gains ``vec_id``, the integer key of its vector row
  (``documents.id`` is text).
* Existing vectors whose width equals the table's are copied (kind ``document``
  keyed by ``vec_id``, kind ``message`` keyed by ``message_embeddings.id``,
  model = the configured ``EMBEDDING_MODEL_CONFIG`` model, agent_id = workspace).
  Other widths cannot be stored; they are counted in the log and left for
  re-embedding: message chunks go back to ``sync_state='pending'`` for the
  reconciler, conclusions are re-embedded by ``lina store reembed --source memory``.
* Triggers delete a row's vectors with the row.
* ``documents.embedding`` and ``message_embeddings.embedding`` are dropped.

Revision ID: l19_vec_embeddings
Revises: c4d8e2f6a1b3
Create Date: 2026-11-03

"""

import logging
from collections.abc import Sequence

import sqlalchemy as sa
from alembic import op

from migrations.utils import column_exists, get_schema, index_exists
from src.config import settings

# revision identifiers, used by Alembic.
revision: str = "l19_vec_embeddings"
down_revision: str | None = "c4d8e2f6a1b3"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None

schema = get_schema()
VEC_SCHEMA = "vec"
logger = logging.getLogger("alembic.runtime.migration")

# (owner table, vec kind, key column, old HNSW index)
_OWNERS = (
    ("documents", "document", "vec_id", "ix_documents_embedding_hnsw"),
    ("message_embeddings", "message", "id", "ix_message_embeddings_embedding_hnsw"),
)


def _vec_dim(connection: sa.Connection) -> int | None:
    return connection.execute(
        sa.text(
            """
            SELECT a.atttypmod FROM pg_attribute a
            WHERE a.attrelid = to_regclass(:table) AND a.attname = 'embedding'
            """
        ),
        {"table": f'"{VEC_SCHEMA}".embeddings'},
    ).scalar()


def _create_vec_table(dim: int) -> None:
    op.execute(f'CREATE SCHEMA IF NOT EXISTS "{VEC_SCHEMA}"')
    op.execute(
        f"""
        CREATE TABLE "{VEC_SCHEMA}".embeddings (
            kind text NOT NULL
                CHECK (kind IN ('message', 'chunk', 'summary', 'document')),
            item_id bigint NOT NULL,
            agent_id text NOT NULL,
            model text NOT NULL,
            dim int NOT NULL,
            embedding vector({dim}) NOT NULL,
            PRIMARY KEY (kind, item_id, model),
            CHECK (dim = vector_dims(embedding))
        )
        """
    )
    op.execute(
        f"""CREATE INDEX ix_embeddings_embedding_hnsw ON "{VEC_SCHEMA}".embeddings
            USING hnsw (embedding vector_cosine_ops)
            WITH (m = 16, ef_construction = 64)"""
    )


def _cleanup_trigger_statements() -> list[str]:
    statements: list[str] = []
    for table, kind, key, _index in _OWNERS:
        function = f'"{schema}".lina_drop_{table}_vectors'
        statements += [
            f"""CREATE OR REPLACE FUNCTION {function}() RETURNS trigger
                LANGUAGE plpgsql AS $fn$
                BEGIN
                    DELETE FROM "{VEC_SCHEMA}".embeddings e USING old_rows o
                    WHERE e.kind = '{kind}' AND e.item_id = o.{key};
                    RETURN NULL;
                END $fn$""",
            f'DROP TRIGGER IF EXISTS lina_drop_vectors ON "{schema}".{table}',
            f"""CREATE TRIGGER lina_drop_vectors AFTER DELETE ON "{schema}".{table}
                REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT
                EXECUTE FUNCTION {function}()""",
        ]
    return statements


def _copy_vectors(connection: sa.Connection, dim: int, model: str) -> None:
    for table, kind, key, _index in _OWNERS:
        copied = connection.execute(
            sa.text(
                f"""
                INSERT INTO "{VEC_SCHEMA}".embeddings
                    (kind, item_id, agent_id, model, dim, embedding)
                SELECT :kind, {key}, workspace_name, :model, :dim,
                       embedding::text::vector({dim})
                FROM "{schema}".{table}
                WHERE embedding IS NOT NULL AND vector_dims(embedding) = :dim
                ON CONFLICT (kind, item_id, model) DO NOTHING
                """
            ),
            {"kind": kind, "model": model, "dim": dim},
        ).rowcount
        other = connection.execute(
            sa.text(
                f"""SELECT count(*) FROM "{schema}".{table}
                    WHERE embedding IS NOT NULL AND vector_dims(embedding) <> :dim"""
            ),
            {"dim": dim},
        ).scalar_one()
        logger.info(
            "vec.embeddings: copied %d %s vectors at %d dims; %d of another width"
            + " left for re-embedding",
            copied,
            kind,
            dim,
            other,
        )
        if other and table == "message_embeddings":
            connection.execute(
                sa.text(
                    f"""UPDATE "{schema}".message_embeddings
                        SET sync_state = 'pending', sync_attempts = 0,
                            last_sync_at = NULL
                        WHERE embedding IS NOT NULL
                          AND vector_dims(embedding) <> :dim"""
                ),
                {"dim": dim},
            )


def upgrade() -> None:
    connection = op.get_bind()
    inspector = sa.inspect(connection)

    dim = _vec_dim(connection)
    if dim is None:
        dim = settings.EMBEDDING.VECTOR_DIMENSIONS
        _create_vec_table(dim)

    if not column_exists("documents", "vec_id", inspector):
        op.execute(
            f"""ALTER TABLE "{schema}".documents
                ADD COLUMN vec_id bigint GENERATED BY DEFAULT AS IDENTITY NOT NULL"""
        )
        op.create_unique_constraint(
            "uq_documents_vec_id", "documents", ["vec_id"], schema=schema
        )

    if column_exists("documents", "embedding", inspector):
        _copy_vectors(connection, dim, settings.EMBEDDING.MODEL_CONFIG.model)

    for statement in _cleanup_trigger_statements():
        op.execute(statement)

    for table, _kind, _key, index in _OWNERS:
        if index_exists(table, index, inspector):
            op.drop_index(index, table_name=table, schema=schema)
        if column_exists(table, "embedding", inspector):
            op.drop_column(table, "embedding", schema=schema)


def downgrade() -> None:
    connection = op.get_bind()
    inspector = sa.inspect(connection)
    dim = _vec_dim(connection) or settings.EMBEDDING.VECTOR_DIMENSIONS
    model = settings.EMBEDDING.MODEL_CONFIG.model

    for table, kind, key, index in _OWNERS:
        if not column_exists(table, "embedding", inspector):
            op.execute(
                f'ALTER TABLE "{schema}".{table} ADD COLUMN embedding vector({dim})'
            )
        if column_exists(table, key, inspector):
            connection.execute(
                sa.text(
                    f"""UPDATE "{schema}".{table} t SET embedding = e.embedding
                        FROM "{VEC_SCHEMA}".embeddings e
                        WHERE e.kind = :kind AND e.model = :model
                          AND e.item_id = t.{key}"""
                ),
                {"kind": kind, "model": model},
            )
        op.execute(
            f"""CREATE INDEX IF NOT EXISTS {index} ON "{schema}".{table}
                USING hnsw (embedding vector_cosine_ops)
                WITH (m = 16, ef_construction = 64)"""
        )
        op.execute(f'DROP TRIGGER IF EXISTS lina_drop_vectors ON "{schema}".{table}')
        op.execute(f'DROP FUNCTION IF EXISTS "{schema}".lina_drop_{table}_vectors()')
        connection.execute(
            sa.text(f'DELETE FROM "{VEC_SCHEMA}".embeddings WHERE kind = :kind'),
            {"kind": kind},
        )

    if column_exists("documents", "vec_id", inspector):
        op.drop_constraint("uq_documents_vec_id", "documents", schema=schema)
        op.drop_column("documents", "vec_id", schema=schema)
