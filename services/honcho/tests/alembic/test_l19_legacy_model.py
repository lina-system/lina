"""Revision l19_vec_embeddings copies a legacy vector only when its model is known.

Honcho's old vector columns keep no per-row model, so the width of a vector
does not say which model wrote it. Only EMBEDDING_LEGACY_MODEL, the operator's
statement for this migration, does: equal to the configured model, the
vectors are copied; unset or another model, nothing is copied, message chunks
go back to pending and conclusions wait for ``lina store reembed``.
"""

from __future__ import annotations

import pytest
from alembic import command
from alembic.config import Config
from sqlalchemy import Engine, text

from src.config import settings
from tests.alembic.revisions.test_l19_vec_embeddings import (
    VEC_DIM,
    prepare_vec_embeddings,
)
from tests.alembic.verifier import MigrationVerifier

REVISION = "l19_vec_embeddings"
DOWN_REVISION = "c4d8e2f6a1b3"
CONFIGURED = "qwen3-embedding-8b"


def _seed_same_width(alembic_engine: Engine) -> None:
    with alembic_engine.begin() as conn:
        verifier = MigrationVerifier(conn, REVISION)
        prepare_vec_embeddings(verifier)
        schema = verifier.schema
        conn.execute(
            text(
                f'ALTER TABLE "{schema}".message_embeddings'
                + f" ALTER COLUMN embedding TYPE vector({VEC_DIM}) USING NULL"
            )
        )
        vector = "[" + ",".join(["1"] + ["0"] * (VEC_DIM - 1)) + "]"
        conn.execute(
            text(
                f'UPDATE "{schema}".message_embeddings'
                + " SET embedding = CAST(:v AS vector), sync_state = 'synced'"
            ),
            {"v": vector},
        )


def _state(alembic_engine: Engine) -> tuple[list[tuple[str, str]], set[str]]:
    schema = settings.DB.SCHEMA
    with alembic_engine.connect() as conn:
        copied = [
            (row.kind, row.model)
            for row in conn.execute(
                text("SELECT kind, model FROM vec.embeddings ORDER BY kind")
            )
        ]
        states = {
            row[0]
            for row in conn.execute(
                text(f'SELECT sync_state FROM "{schema}".message_embeddings')
            )
        }
    return copied, states


@pytest.mark.parametrize(
    ("legacy", "copied", "states"),
    [
        (None, [], {"pending"}),
        ("text-embedding-3-small", [], {"pending"}),
        (
            CONFIGURED,
            [("document", CONFIGURED), ("message", CONFIGURED)],
            {"synced"},
        ),
    ],
    ids=["no-provenance", "other-model", "same-model"],
)
def test_same_width_vectors_are_copied_only_from_the_configured_model(
    alembic_cfg: Config,
    alembic_engine: Engine,
    monkeypatch: pytest.MonkeyPatch,
    legacy: str | None,
    copied: list[tuple[str, str]],
    states: set[str],
) -> None:
    monkeypatch.setattr(settings.EMBEDDING.MODEL_CONFIG, "model", CONFIGURED)
    monkeypatch.setattr(settings.EMBEDDING, "LEGACY_MODEL", legacy, raising=False)
    command.upgrade(alembic_cfg, DOWN_REVISION)
    _seed_same_width(alembic_engine)

    command.upgrade(alembic_cfg, REVISION)

    assert _state(alembic_engine) == (copied, states)
