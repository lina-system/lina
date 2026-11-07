"""``lina store reembed --source messages --from-index <db>``: import the collector's stored chunk vectors (lina#365).

The message collector keeps a derived SQLite index of its own vectors: one full-width vector per chunk of up to
ten consecutive messages of one room, keyed by collector message uid. Where that vector was written by the
configured model, ``truncate_stored`` cuts it to the width of ``vec.embeddings`` instead of embedding the text
again. A chunk becomes one ``vec.chunks`` row (``source = 'messages'``, ``message_ids`` = its messages'
``memory.messages`` ids in chunk order, ``text`` = their LINA text) and one ``chunk`` vector, owned by
``SHARED_VECTOR_OWNER`` as ``lina store reembed`` writes shared external messages (migration 0009).

The index is opened read-only and its shape is the one docs/store.md "Importing the collector's vectors" defines.
Chunks whose messages are not all shared rows of ``memory.messages``, chunks with no messages or no text,
vectors of another model and vectors that cannot be cut are counted and left for ``lina store reembed``. The
import keeps no cursor (not ``vec.reembed_progress``): a chunk is keyed by its message ids, so a rerun goes
through the index again and inserts nothing it inserted before.
"""

from __future__ import annotations

import sqlite3
import struct
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from agent.embedding_client import EmbeddingModelMismatch, truncate_stored
from hermes_cli.store_reembed import ReembedError, _vector_literal
from hermes_state_store import SHARED_VECTOR_OWNER

_CHUNKS = "SELECT chunk_id, model, dim, embedding FROM chunks WHERE chunk_id > ? ORDER BY chunk_id LIMIT ?"
_MESSAGES = "SELECT chunk_id, message_uid FROM chunk_messages WHERE chunk_id BETWEEN ? AND ? ORDER BY chunk_id, position"
_STORE_ROWS = """
    SELECT uid, message_id, content FROM memory.messages
    WHERE uid = ANY(%s) AND source = 'messages' AND agent_id IS NULL AND scope = 'agents' AND forgotten_at IS NULL"""


def model_key(name: str) -> str:
    """The form two model names are compared in: the part after the last ``/`` (the provider prefix some
    endpoints add, as in ``qwen/qwen3-embedding-8b``), otherwise exact."""
    return name.strip().rsplit("/", 1)[-1]


@dataclass
class ImportCounts:
    imported: int = 0
    present: int = 0
    missing_messages: int = 0
    empty: int = 0
    other_model: int = 0
    unusable: int = 0

    def line(self) -> str:
        return (f"messages index: {self.imported} imported, {self.present} already in the store; left for "
                f"`lina store reembed --source messages`: {self.missing_messages} with messages not in "
                f"memory.messages, {self.empty} empty, {self.other_model} of another model, "
                f"{self.unusable} unusable vectors")


def _open_index(path: Path) -> sqlite3.Connection:
    if not path.is_file():
        raise ReembedError(f"no collector index at {path}")
    conn = sqlite3.connect(f"{path.resolve().as_uri()}?mode=ro", uri=True)
    try:
        conn.execute("SELECT chunk_id, model, dim, embedding FROM chunks LIMIT 0")
        conn.execute("SELECT chunk_id, position, message_uid FROM chunk_messages LIMIT 0")
    except sqlite3.Error as exc:
        conn.close()
        raise ReembedError(f"{path} is not a collector index (docs/store.md): {exc}") from exc
    return conn


def _decode(blob: Any, dim: Any) -> list[float] | None:
    """The stored float32 little-endian vector, or None when the blob does not hold *dim* values."""
    if not isinstance(blob, bytes) or not isinstance(dim, int) or dim <= 0 or len(blob) != 4 * dim:
        return None
    return list(struct.unpack(f"<{dim}f", blob))


def _write_chunk(conn, message_ids: list[int], text: str, model: str, vector: list[float]) -> bool:
    row = conn.execute(
        "INSERT INTO vec.chunks (source, agent_id, message_ids, text) VALUES ('messages', %s, %s, %s) "
        "ON CONFLICT (source, message_ids, chunk_index, source_ref, lcm_identity_hash) DO NOTHING RETURNING chunk_id",
        (SHARED_VECTOR_OWNER, message_ids, text)).fetchone()
    if row is None:
        row = conn.execute(
            "SELECT chunk_id FROM vec.chunks WHERE source = 'messages' AND message_ids = %s::bigint[] "
            "AND chunk_index IS NULL AND source_ref IS NULL AND lcm_identity_hash IS NULL", (message_ids,)).fetchone()
    inserted = conn.execute(
        "INSERT INTO vec.embeddings (kind, item_id, agent_id, model, dim, embedding) "
        "VALUES ('chunk', %s, %s, %s, %s, %s::vector) ON CONFLICT (kind, item_id, model) DO NOTHING",
        (row[0], SHARED_VECTOR_OWNER, model, len(vector), _vector_literal(vector))).rowcount
    return inserted == 1


def _import_batch(conn, chunks: list[tuple], uids: dict[int, list[str]], counts: ImportCounts, *,
                  model: str, dimensions: int) -> None:
    wanted = sorted({f"messages:{uid}" for chunk_uids in uids.values() for uid in chunk_uids})
    rows = {uid: (int(mid), content) for uid, mid, content in conn.execute(_STORE_ROWS, (wanted,)).fetchall()}
    with conn.transaction():
        for chunk_id, stored_model, dim, blob in chunks:
            refs = [f"messages:{uid}" for uid in uids.get(chunk_id, [])]
            if not refs:
                counts.empty += 1
                continue
            if any(ref not in rows for ref in refs):
                counts.missing_messages += 1
                continue
            text = "\n".join(t for t in ((rows[ref][1] or "").strip() for ref in refs) if t)
            if not text:
                counts.empty += 1
                continue
            stored = _decode(blob, dim)
            try:
                if stored is None:
                    raise ValueError("blob width")
                vector = truncate_stored(stored, stored_model=model_key(str(stored_model)), model=model_key(model),
                                         dimensions=dimensions)
            except EmbeddingModelMismatch:
                counts.other_model += 1
                continue
            except ValueError:
                counts.unusable += 1
                continue
            if _write_chunk(conn, [rows[ref][0] for ref in refs], text, model, vector):
                counts.imported += 1
            else:
                counts.present += 1


def import_index(conn, index: Path, *, model: str, dimensions: int, batch_size: int,
                 out: Callable[[str], None] = print) -> ImportCounts:
    from hermes_cli.store_reembed import _table_width
    width = _table_width(conn)
    if width != dimensions:
        raise ReembedError(f"vec.embeddings holds {width}-wide vectors but embedding.dimensions is {dimensions}")
    counts = ImportCounts()
    source = _open_index(index)
    try:
        after = 0
        while True:
            chunks = source.execute(_CHUNKS, (after, batch_size)).fetchall()
            if not chunks:
                break
            uids: dict[int, list[str]] = {}
            for chunk_id, uid in source.execute(_MESSAGES, (chunks[0][0], chunks[-1][0])).fetchall():
                uids.setdefault(chunk_id, []).append(str(uid))
            _import_batch(conn, chunks, uids, counts, model=model, dimensions=dimensions)
            after = chunks[-1][0]
            out(f"messages index: up to chunk {after}, {counts.imported} imported")
    finally:
        source.close()
    out(counts.line())
    return counts


def run(args, config: dict) -> int:
    """``lina store reembed --source messages --from-index <db>``: needs ``embedding.model`` and ``dimensions``,
    not the endpoint, since nothing is embedded."""
    from agent.embedding_client import DEFAULT_DIMENSIONS, DEFAULT_MODEL
    from hermes_cli.store_reembed import _owner_connect
    from hermes_state_store import StoreUnavailableError
    section = config.get("embedding") or {}
    try:
        if args.source != "messages":
            raise ReembedError("--from-index imports external-message vectors: use it with --source messages")
        with _owner_connect() as conn:
            import_index(conn, Path(args.from_index), model=str(section.get("model") or DEFAULT_MODEL),
                         dimensions=int(section.get("dimensions") or DEFAULT_DIMENSIONS),
                         batch_size=args.batch_size or 500)
    except (ReembedError, StoreUnavailableError) as exc:
        print(f"reembed: {exc}")
        return 1
    return 0
