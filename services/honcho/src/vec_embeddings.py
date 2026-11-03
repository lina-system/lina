"""Writes and searches of Honcho's vectors in LINA's vec.embeddings (lina#19).

The ORM keeps ``Document.embedding`` / ``MessageEmbedding.embedding`` working for
rows it loads and creates (``models.VecEmbedded``); the helpers here are for the
statement-level paths: bulk vector writes and nearest-neighbour searches.
"""

from __future__ import annotations

from collections.abc import Iterable, Sequence
from typing import Any

from sqlalchemy import Select, and_, delete
from sqlalchemy.dialects.postgresql import insert
from sqlalchemy.ext.asyncio import AsyncSession

from src import models


def _join_condition(kind: str) -> Any:
    owner = models.Document if kind == "document" else models.MessageEmbedding
    key = owner.vec_id if kind == "document" else owner.id
    return and_(
        models.VecEmbedding.kind == kind,
        models.VecEmbedding.item_id == key,
        models.VecEmbedding.model == models.embedding_model_name(),
    )


def join_vectors(stmt: Select[Any], kind: str) -> Select[Any]:
    """*stmt* inner-joined to the owners' vec.embeddings rows: only rows with a vector remain."""
    return stmt.join(models.VecEmbedding, _join_condition(kind))


def cosine_distance(query: Sequence[float]) -> Any:
    return models.VecEmbedding.embedding.cosine_distance(query)


async def put_vectors(
    db: AsyncSession,
    kind: str,
    rows: Iterable[tuple[int, str, Sequence[float]]],
) -> None:
    """Insert or replace the vectors of (item_id, workspace_name, vector) rows."""
    values = [
        {
            "kind": kind,
            "item_id": item_id,
            "model": models.embedding_model_name(),
            "agent_id": workspace_name,
            "dim": len(vector),
            "embedding": [float(x) for x in vector],
        }
        for item_id, workspace_name, vector in rows
    ]
    if not values:
        return
    stmt = insert(models.VecEmbedding).values(values)
    await db.execute(
        stmt.on_conflict_do_update(
            index_elements=["kind", "item_id", "model"],
            set_={
                "agent_id": stmt.excluded.agent_id,
                "dim": stmt.excluded.dim,
                "embedding": stmt.excluded.embedding,
            },
        )
    )


async def delete_vectors(db: AsyncSession, kind: str, item_ids: Iterable[int]) -> None:
    ids = list(item_ids)
    if not ids:
        return
    await db.execute(
        delete(models.VecEmbedding).where(
            models.VecEmbedding.kind == kind, models.VecEmbedding.item_id.in_(ids)
        )
    )
