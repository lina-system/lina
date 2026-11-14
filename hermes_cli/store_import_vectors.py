"""``lina store reembed --source messages --from-index <db>``: import the collector's stored chunk vectors (lina#365).

The message collector keeps a derived SQLite index: ``chunks`` of up to ten consecutive messages of one room,
``chunk_messages`` (their collector uids by ``ordinal``) and a sqlite-vec ``vec0`` table ``chunk_vectors``
(``rowid = chunks.id``), with the model and width once in ``meta``. LINA does not depend on sqlite-vec, so the
vectors are read from vec0's 0.1 shadow tables. When ``meta.vector_model`` is the configured model,
``truncate_stored`` cuts each vector to the width of ``vec.embeddings`` instead of embedding the text again.

A chunk becomes one ``vec.chunks`` row (``source = 'messages'``, ``source_ref = 'collector'``, ``message_ids`` =
its messages' ``memory.messages`` ids in chunk order, ``text`` = their LINA text) and one ``chunk`` vector, owned
by ``SHARED_VECTOR_OWNER`` as ``lina store reembed`` writes shared external messages (migration 0009).
``reembed --source messages`` skips the messages such a chunk covers. Chunks whose messages are not all shared
rows of ``memory.messages``, empty chunks, an index of another model and vectors that cannot be cut are counted
and left for ``reembed``. The import keeps no cursor: a chunk is keyed by its message ids, so a rerun inserts
nothing it inserted before.
"""

from __future__ import annotations

import sqlite3
import struct
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path

from agent.embedding_client import truncate_stored
from hermes_cli.store_reembed import ReembedError, _vector_literal
from hermes_state_store import SHARED_VECTOR_OWNER

IMPORTED_REF = "collector"
_VEC0_VERSION = "v0.1."
_REQUIRED = {
    "meta": ("key", "value"),
    "chunks": ("id",),
    "chunk_messages": ("chunk_id", "uid", "ordinal"),
    "chunk_vectors_info": ("key", "value"),
    "chunk_vectors_rowids": ("rowid", "chunk_id", "chunk_offset"),
    "chunk_vectors_chunks": ("chunk_id", "size", "validity"),
    "chunk_vectors_vector_chunks00": ("rowid", "vectors"),
}
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


@dataclass(frozen=True)
class _Index:
    conn: sqlite3.Connection
    model: str
    dim: int


def _open_index(path: Path) -> _Index:
    if not path.is_file():
        raise ReembedError(f"no collector index at {path}")
    conn = sqlite3.connect(f"{path.resolve().as_uri()}?mode=ro", uri=True)
    try:
        for table, columns in _REQUIRED.items():
            conn.execute(f"SELECT {', '.join(columns)} FROM {table} LIMIT 0")
        version = conn.execute("SELECT value FROM chunk_vectors_info WHERE key = 'CREATE_VERSION'").fetchone()
        meta = dict(conn.execute("SELECT key, value FROM meta WHERE key IN ('vector_model', 'vector_dimension')"))
    except sqlite3.Error as exc:
        conn.close()
        raise ReembedError(f"{path} is not a collector index (docs/store.md): {exc}") from exc
    if version is None or not str(version[0]).startswith(_VEC0_VERSION):
        conn.close()
        raise ReembedError(f"{path}: chunk_vectors was made by sqlite-vec {version[0] if version else 'unknown'}, "
                           f"whose storage this import does not read (it reads {_VEC0_VERSION}x shadow tables)")
    try:
        return _Index(conn, str(meta["vector_model"]), int(meta["vector_dimension"]))
    except (KeyError, ValueError) as exc:
        conn.close()
        raise ReembedError(f"{path}: meta has no usable vector_model and vector_dimension") from exc


def _stored_vectors(index: _Index, first: int, last: int) -> dict[int, list[float]]:
    """The live vec0 vectors of chunks *first*..*last*: a cell whose validity bit is 0 was deleted, and a block
    blob that is not ``size`` vectors of ``dim`` float32 values yields none."""
    places = index.conn.execute("SELECT rowid, chunk_id, chunk_offset FROM chunk_vectors_rowids "
                                "WHERE rowid BETWEEN ? AND ?", (first, last)).fetchall()
    blocks = sorted({block for _, block, _ in places})
    if not blocks:
        return {}
    marks = ",".join("?" * len(blocks))
    validity = {block: (int(size), bytes(bits)) for block, size, bits in index.conn.execute(
        f"SELECT chunk_id, size, validity FROM chunk_vectors_chunks WHERE chunk_id IN ({marks})", blocks)}
    blobs = dict(index.conn.execute(
        f"SELECT rowid, vectors FROM chunk_vectors_vector_chunks00 WHERE rowid IN ({marks})", blocks))
    width = index.dim * 4
    vectors = {}
    for rowid, block, offset in places:
        size, bits = validity.get(block, (0, b""))
        blob = blobs.get(block)
        if not (0 <= offset < size and offset // 8 < len(bits) and bits[offset // 8] >> (offset % 8) & 1):
            continue
        if not isinstance(blob, bytes) or len(blob) != size * width:
            continue
        vectors[int(rowid)] = list(struct.unpack(f"<{index.dim}f", blob[offset * width:(offset + 1) * width]))
    return vectors


def _write_chunk(conn, message_ids: list[int], text: str, model: str, vector: list[float]) -> bool:
    row = conn.execute(
        "INSERT INTO vec.chunks (source, agent_id, message_ids, source_ref, text) VALUES ('messages', %s, %s, %s, %s) "
        "ON CONFLICT (source, message_ids, chunk_index, source_ref, lcm_identity_hash) DO NOTHING RETURNING chunk_id",
        (SHARED_VECTOR_OWNER, message_ids, IMPORTED_REF, text)).fetchone()
    if row is None:
        row = conn.execute(
            "SELECT chunk_id FROM vec.chunks WHERE source = 'messages' AND message_ids = %s::bigint[] "
            "AND chunk_index IS NULL AND source_ref = %s AND lcm_identity_hash IS NULL",
            (message_ids, IMPORTED_REF)).fetchone()
    inserted = conn.execute(
        "INSERT INTO vec.embeddings (kind, item_id, agent_id, model, dim, embedding) "
        "VALUES ('chunk', %s, %s, %s, %s, %s::vector) ON CONFLICT (kind, item_id, model) DO NOTHING",
        (row[0], SHARED_VECTOR_OWNER, model, len(vector), _vector_literal(vector))).rowcount
    return inserted == 1


def _import_batch(conn, index: _Index, chunk_ids: list[int], counts: ImportCounts, *,
                  model: str, dimensions: int) -> None:
    uids: dict[int, list[str]] = {}
    for chunk_id, uid in index.conn.execute(
            "SELECT chunk_id, uid FROM chunk_messages WHERE chunk_id BETWEEN ? AND ? ORDER BY chunk_id, ordinal",
            (chunk_ids[0], chunk_ids[-1])):
        uids.setdefault(int(chunk_id), []).append(f"messages:{uid}")
    stored = _stored_vectors(index, chunk_ids[0], chunk_ids[-1])
    wanted = sorted({ref for refs in uids.values() for ref in refs})
    rows = {uid: (int(mid), content) for uid, mid, content in conn.execute(_STORE_ROWS, (wanted,)).fetchall()}
    with conn.transaction():
        for chunk_id in chunk_ids:
            refs = uids.get(chunk_id, [])
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
            try:
                if chunk_id not in stored:
                    raise ValueError("no live stored vector")
                vector = truncate_stored(stored[chunk_id], stored_model=model_key(index.model),
                                         model=model_key(model), dimensions=dimensions)
            except ValueError:
                counts.unusable += 1
                continue
            if _write_chunk(conn, [rows[ref][0] for ref in refs], text, model, vector):
                counts.imported += 1
            else:
                counts.present += 1


def import_index(conn, path: Path, *, model: str, dimensions: int, batch_size: int,
                 out: Callable[[str], None] = print) -> ImportCounts:
    from hermes_cli.store_reembed import _table_width
    width = _table_width(conn)
    if width != dimensions:
        raise ReembedError(f"vec.embeddings holds {width}-wide vectors but embedding.dimensions is {dimensions}")
    counts = ImportCounts()
    index = _open_index(path)
    try:
        if model_key(index.model) != model_key(model):
            counts.other_model = int(index.conn.execute("SELECT count(*) FROM chunks").fetchone()[0])
            # Name only the index's model (read from the index), not the configured value: the config section is
            # one CodeQL treats as secret-bearing, and the count line already says the import stopped.
            out(f"messages index: vectors of {index.model!r}, not the configured embedding.model; nothing imported")
        else:
            after = 0
            while True:
                chunk_ids = [int(r[0]) for r in index.conn.execute(
                    "SELECT id FROM chunks WHERE id > ? ORDER BY id LIMIT ?", (after, batch_size))]
                if not chunk_ids:
                    break
                _import_batch(conn, index, chunk_ids, counts, model=model, dimensions=dimensions)
                after = chunk_ids[-1]
                out(f"messages index: up to chunk {after}, {counts.imported} imported")
    finally:
        index.conn.close()
    out(counts.line())
    return counts


def run(args, config: dict) -> int:
    """``lina store reembed --source messages --from-index <db>``: needs ``embedding.model`` and ``dimensions``,
    not the endpoint, since nothing is embedded."""
    from agent.embedding_client import DEFAULT_DIMENSIONS, home_model
    from hermes_cli.store_reembed import _owner_connect
    from hermes_state_store import StoreUnavailableError
    section = config.get("embedding") or {}
    try:
        if args.source != "messages":
            raise ReembedError("--from-index imports external-message vectors: use it with --source messages")
        with _owner_connect() as conn:
            import_index(conn, Path(args.from_index), model=home_model(),
                         dimensions=int(section.get("dimensions") or DEFAULT_DIMENSIONS),
                         batch_size=args.batch_size or 500)
    except (ReembedError, StoreUnavailableError) as exc:
        print(f"reembed: {exc}")
        return 1
    return 0
