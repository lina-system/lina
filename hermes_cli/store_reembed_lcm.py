"""LCM-X summary batches for the existing memory source, using its store vector view."""

from __future__ import annotations

import importlib
from collections.abc import Callable

from psycopg import Connection

from hermes_cli.store_reembed import SourceCounts, _progress, _vector_literal


def _write_batch(conn: Connection, agent: str, model: str, identity: str,
                 rows: list[tuple[int, str, int]], vectors: list[list[float]], progress: str) -> None:
    """Publish through LCM's RLS-scoped view and commit its cursor in the same transaction."""
    with conn.transaction():
        # The owner enumerates all homes, but LCM's views intentionally rely on RLS for joins.
        conn.execute("SET LOCAL ROLE lina_app")
        conn.execute("SELECT set_config('lina.agent_id', %s, true)", (agent,))
        conn.execute(
            "INSERT INTO ctx.lcm_embedding_profile (identity_hash, provider, model_name, dim, task) "
            "VALUES (%s, 'openai-compatible', %s, 1024, 'summary') "
            "ON CONFLICT (agent_id, identity_hash) DO NOTHING", (identity, model))
        for (node, text, tokens), vector in zip((r for r in rows if r[1]), vectors, strict=True):
            conn.execute(
                "INSERT INTO ctx.lcm_embedding_vectors (embedded_id, identity_hash, vec) "
                "VALUES (%s, %s, %s::vector)", (str(node), identity, _vector_literal(vector)))
            conn.execute(
                "INSERT INTO ctx.lcm_embedding_meta (embedded_id, embedded_kind, identity_hash, "
                "embedded_at, source_token_count, archived) VALUES (%s, 'summary', %s, now()::text, %s, 0) "
                "ON CONFLICT (agent_id, embedded_id, embedded_kind, identity_hash) DO UPDATE SET "
                "embedded_at = EXCLUDED.embedded_at, source_token_count = EXCLUDED.source_token_count, archived = 0",
                (str(node), identity, tokens))
            conn.execute("DELETE FROM ctx.lcm_embedding_binary WHERE embedded_id = %s AND identity_hash = %s",
                         (str(node), identity))
        conn.execute("UPDATE ctx.lcm_embedding_profile SET data_version = data_version + 1 WHERE identity_hash = %s",
                     (identity,))
        conn.execute("SET LOCAL ROLE NONE")
        conn.execute(
            "INSERT INTO vec.reembed_progress (source, model, last_item_id) VALUES (%s, %s, %s) "
            "ON CONFLICT (source, model) DO UPDATE SET last_item_id = EXCLUDED.last_item_id, updated_at = now()",
            (progress, model, rows[-1][0]))


def reembed_summaries(conn: Connection, embed: Callable[[list[str]], list[list[float]]], counts: SourceCounts,
                      *, model: str, batch_size: int, from_start: bool, max_chars: int,
                      max_batches: int | None, out: Callable[[str], None]) -> None:
    """Add summary counts while preserving Honcho's cursor and empty-ctx reporting."""
    if conn.execute("SELECT to_regclass('ctx.summary_nodes') IS NULL").fetchone()[0]:
        return
    agents = conn.execute("SELECT DISTINCT agent_id FROM ctx.summary_nodes ORDER BY agent_id").fetchall()
    if not agents:
        return
    identity_type = importlib.import_module("plugins.context_engine.lcm-x.vector_store").EmbeddingIdentity
    identity = identity_type.canonical("openai-compatible", model, "", 1024, "float32", "little", "summary").identity_hash
    for (agent,) in agents:
        progress = f"memory:lcm:{agent}"
        if from_start:
            conn.execute("DELETE FROM vec.reembed_progress WHERE source = %s AND model = %s", (progress, model))
        after = _progress(conn, progress, model)
        resumed = after
        embedded = 0
        while max_batches is None or counts.batches < max_batches:
            rows = [(int(node), (text or "").strip()[:max_chars], int(tokens or 0)) for node, text, tokens in
                    conn.execute("SELECT node_id, summary, source_token_count FROM ctx.summary_nodes "
                                 "WHERE agent_id = %s AND node_id > %s ORDER BY node_id LIMIT %s",
                                 (agent, after, batch_size)).fetchall()]
            if not rows:
                break
            texts = [text for _, text, _ in rows if text]
            vectors = embed(texts) if texts else []
            _write_batch(conn, agent, model, identity, rows, vectors, progress)
            after = rows[-1][0]
            counts.batches += 1
            counts.embedded += len(texts)
            embedded += len(texts)
            counts.skipped += len(rows) - len(texts)
            out(f"memory: LCM {agent}: up to id {after}, {embedded} embedded")
        counts.notes.append(f"LCM {agent}: {embedded} embedded, resumed after id {resumed}, last id {after}")
