"""Hooks for revision l19_vec_embeddings (vectors move to vec.embeddings).

The store the way LINA has it: vec.embeddings already exists at 1024 dims. Without
EMBEDDING_LEGACY_MODEL no legacy vector is copied (test_l19_legacy_model.py covers
the copy); the message chunk goes back to pending, and deleting a document deletes
its vector row.
"""

from __future__ import annotations

from nanoid import generate as generate_nanoid
from sqlalchemy import text

from tests.alembic.registry import register_after_upgrade, register_before_upgrade
from tests.alembic.verifier import MigrationVerifier

WORKSPACE_NAME = "vec_ws"
PEER_NAME = "vec_peer"
SESSION_NAME = "vec_session"
DOC_ID = generate_nanoid()
MESSAGE_ID = generate_nanoid()
VEC_DIM = 1024


def _vector(dim: int, first: float) -> str:
    return "[" + ",".join([str(first)] + ["0"] * (dim - 1)) + "]"


@register_before_upgrade("l19_vec_embeddings")
def prepare_vec_embeddings(verifier: MigrationVerifier) -> None:
    schema = verifier.schema
    conn = verifier.conn
    conn.execute(text("DROP SCHEMA IF EXISTS vec CASCADE"))
    conn.execute(text("CREATE SCHEMA vec"))
    conn.execute(
        text(
            f"""CREATE TABLE vec.embeddings (
                kind text NOT NULL, item_id bigint NOT NULL, agent_id text NOT NULL,
                model text NOT NULL, dim int NOT NULL,
                embedding vector({VEC_DIM}) NOT NULL,
                PRIMARY KEY (kind, item_id, model))"""
        )
    )
    conn.execute(
        text(
            f'ALTER TABLE "{schema}".documents'
            + f" ALTER COLUMN embedding TYPE vector({VEC_DIM}) USING NULL"
        )
    )
    conn.execute(
        text(f'INSERT INTO "{schema}"."workspaces" ("id", "name") VALUES (:id, :n)'),
        {"id": generate_nanoid(), "n": WORKSPACE_NAME},
    )
    conn.execute(
        text(
            f"""INSERT INTO "{schema}"."peers" ("id", "name", "workspace_name")
            VALUES (:id, :n, :w)"""
        ),
        {"id": generate_nanoid(), "n": PEER_NAME, "w": WORKSPACE_NAME},
    )
    conn.execute(
        text(
            f"""INSERT INTO "{schema}"."sessions" ("id", "name", "workspace_name")
            VALUES (:id, :n, :w)"""
        ),
        {"id": generate_nanoid(), "n": SESSION_NAME, "w": WORKSPACE_NAME},
    )
    conn.execute(
        text(
            f"""INSERT INTO "{schema}"."messages"
                ("public_id", "session_name", "workspace_name", "peer_name",
                 "content", "seq_in_session")
            VALUES (:id, :s, :w, :p, 'hello', 1)"""
        ),
        {"id": MESSAGE_ID, "s": SESSION_NAME, "w": WORKSPACE_NAME, "p": PEER_NAME},
    )
    conn.execute(
        text(
            f"""INSERT INTO "{schema}"."message_embeddings"
                ("content", "embedding", "message_id", "workspace_name",
                 "session_name", "peer_name", "sync_state")
            VALUES ('hello', CAST(:e AS vector), :m, :w, :s, :p, 'synced')"""
        ),
        {
            "e": _vector(1536, 1.0),
            "m": MESSAGE_ID,
            "w": WORKSPACE_NAME,
            "s": SESSION_NAME,
            "p": PEER_NAME,
        },
    )
    conn.execute(
        text(
            f"""INSERT INTO "{schema}"."collections"
                ("id", "workspace_name", "observer", "observed")
            VALUES (:id, :w, :p, :p)"""
        ),
        {"id": generate_nanoid(), "w": WORKSPACE_NAME, "p": PEER_NAME},
    )
    conn.execute(
        text(
            f"""INSERT INTO "{schema}"."documents"
                ("id", "workspace_name", "observer", "observed", "content",
                 "level", "session_name", "embedding")
            VALUES (:id, :w, :p, :p, 'peer said hello', 'explicit', :s,
                    CAST(:e AS vector))"""
        ),
        {
            "id": DOC_ID,
            "w": WORKSPACE_NAME,
            "p": PEER_NAME,
            "s": SESSION_NAME,
            "e": _vector(VEC_DIM, 1.0),
        },
    )


@register_after_upgrade("l19_vec_embeddings")
def verify_vec_embeddings(verifier: MigrationVerifier) -> None:
    schema = verifier.schema
    conn = verifier.conn
    verifier.assert_column_exists("documents", "embedding", exists=False)
    verifier.assert_column_exists("message_embeddings", "embedding", exists=False)
    verifier.assert_column_exists("documents", "vec_id")

    # No EMBEDDING_LEGACY_MODEL: nothing says which model wrote the old vectors.
    assert conn.execute(text("SELECT count(*) FROM vec.embeddings")).scalar_one() == 0
    vec_id = conn.execute(
        text(f'SELECT vec_id FROM "{schema}".documents WHERE id = :id'),
        {"id": DOC_ID},
    ).scalar_one()
    conn.execute(
        text(
            "INSERT INTO vec.embeddings VALUES"
            + " ('document', :i, :w, 'm', :d, CAST(:e AS vector))"
        ),
        {"i": vec_id, "w": WORKSPACE_NAME, "d": VEC_DIM, "e": _vector(VEC_DIM, 1.0)},
    )

    state = conn.execute(
        text(
            f'SELECT sync_state FROM "{schema}".message_embeddings'
            + " WHERE message_id = :m"
        ),
        {"m": MESSAGE_ID},
    ).scalar_one()
    assert state == "pending"

    conn.execute(
        text(f'DELETE FROM "{schema}".documents WHERE id = :id'), {"id": DOC_ID}
    )
    left = conn.execute(
        text("SELECT count(*) FROM vec.embeddings WHERE kind = 'document'")
    ).scalar_one()
    assert left == 0
