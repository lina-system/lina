"""``lina store reembed``: fill ``vec.embeddings`` with the one embedding model (lina#19).

Sources:

* ``agent``: agent turns in ``memory.messages`` (``source = 'agent'``, roles user and assistant);
* ``messages``: external messages in ``memory.messages`` (``source = 'messages'``);
* ``memory``: Honcho conclusions and LCM-X summary nodes (through its vector view).

A ``memory.messages`` row becomes one ``vec.chunks`` row (its text, cut at ``embedding.max_chars``) and one
``vec.embeddings`` row of kind ``chunk``, owned by the message's agent; an external message every agent reads
(NULL ``agent_id``, ``scope = 'agents'``) gets the shared owner ``*`` (``SHARED_VECTOR_OWNER``), which every
agent reads through migration 0009's policies. An external message that a chunk imported from the collector's index
(``--from-index``, ``hermes_cli/store_import_vectors.py``) covers with a vector of the model is skipped and counted. Rows go in id order, a batch per transaction, and
``vec.reembed_progress`` records the last id of each committed batch, so a stopped run continues where it
stopped; ``--from-start`` forgets that and goes through everything again. It runs as the store owner, which
row-level security does not limit, so it covers every agent's rows.
"""

from __future__ import annotations

from collections.abc import Callable, Iterator, Sequence
from dataclasses import dataclass, field
from typing import Any, Optional

from hermes_state_store import SHARED_VECTOR_OWNER

SOURCES = ("agent", "messages", "memory")
DEFAULT_MAX_CHARS = 8000

# A message an imported collector chunk with a vector of this model covers (hermes_cli/store_import_vectors.py).
_COVERED = """EXISTS (SELECT 1 FROM vec.chunks c JOIN vec.embeddings e ON e.kind = 'chunk' AND e.item_id = c.chunk_id
               WHERE c.source = 'messages' AND c.source_ref = 'collector' AND e.model = %(model)s
                 AND c.message_ids @> ARRAY[m.message_id])"""
_MESSAGE_ROWS = """
    SELECT message_id, coalesce(agent_id, CASE WHEN scope = 'agents' THEN %(shared)s END), content, {covered}
    FROM memory.messages AS m
    WHERE source = %(source)s AND message_id > %(after)s {roles}
    ORDER BY message_id LIMIT %(limit)s"""
_DOCUMENT_ROWS = """
    SELECT vec_id, workspace_name, content FROM ltm.documents
    WHERE vec_id > %(after)s AND deleted_at IS NULL
    ORDER BY vec_id LIMIT %(limit)s"""


class ReembedError(RuntimeError):
    pass


@dataclass
class SourceCounts:
    source: str
    resumed_after: int = 0
    embedded: int = 0
    skipped: int = 0
    covered: int = 0
    batches: int = 0
    last_item_id: int = 0
    notes: list[str] = field(default_factory=list)

    def line(self) -> str:
        text = (f"{self.source}: {self.embedded} embedded, {self.skipped} skipped (no text), {self.batches} batches, "
                f"resumed after id {self.resumed_after}, last id {self.last_item_id}")
        if self.source == "messages":
            text += f", {self.covered} covered by imported collector chunks"
        return "; ".join([text, *self.notes])


@dataclass(frozen=True)
class _Item:
    item_id: int
    agent_id: str
    text: Optional[str]
    covered: bool = False


def expand_sources(source: str) -> tuple[str, ...]:
    if source == "all":
        return SOURCES
    if source not in SOURCES:
        raise ReembedError(f"unknown source {source!r}; one of {', '.join((*SOURCES, 'all'))}")
    return (source,)


def _vector_literal(vector: Sequence[float]) -> str:
    return "[" + ",".join(repr(float(v)) for v in vector) + "]"


def _table_width(conn) -> int:
    row = conn.execute("SELECT atttypmod FROM pg_attribute WHERE attrelid = to_regclass('vec.embeddings') "
                       "AND attname = 'embedding'").fetchone()
    if row is None:
        raise ReembedError("vec.embeddings does not exist; run `lina store upgrade` first")
    return int(row[0])


def _progress(conn, source: str, model: str) -> int:
    row = conn.execute("SELECT last_item_id FROM vec.reembed_progress WHERE source = %s AND model = %s",
                       (source, model)).fetchone()
    return int(row[0]) if row else 0


def _items(conn, source: str, model: str, after: int, limit: int, max_chars: int) -> list[_Item]:
    if source == "memory":
        if conn.execute("SELECT to_regclass('ltm.documents') IS NULL").fetchone()[0]:
            return []
        rows = conn.execute(_DOCUMENT_ROWS, {"after": after, "limit": limit}).fetchall()
    else:
        roles = "AND role IN ('user', 'assistant') AND forgotten_at IS NULL" if source == "agent" \
            else "AND forgotten_at IS NULL"
        covered = _COVERED if source == "messages" else "false"
        rows = conn.execute(_MESSAGE_ROWS.format(roles=roles, covered=covered),
                            {"source": source, "after": after, "limit": limit, "model": model,
                             "shared": SHARED_VECTOR_OWNER}).fetchall()
    items = []
    for item_id, agent_id, content, *covered_by in rows:
        if covered_by and covered_by[0]:
            items.append(_Item(int(item_id), str(agent_id), None, covered=True))
            continue
        # A row with no owner (NULL agent_id outside scope 'agents') is readable by no agent: not embedded.
        text = (content or "").strip() if agent_id is not None else ""
        items.append(_Item(int(item_id), str(agent_id), text[:max_chars] if text else None))
    return items


def _write_batch(conn, source: str, model: str, items: list[_Item], vectors: list[list[float]]) -> None:
    kind = "document" if source == "memory" else "chunk"
    with conn.transaction():
        for item, vector in zip((i for i in items if i.text), vectors, strict=True):
            item_id = item.item_id
            if kind == "chunk":
                item_id = conn.execute(
                    "INSERT INTO vec.chunks (source, agent_id, message_ids, text) VALUES (%s, %s, %s, %s) "
                    "ON CONFLICT (source, message_ids, chunk_index, source_ref, lcm_identity_hash) DO UPDATE SET text = EXCLUDED.text, agent_id = EXCLUDED.agent_id "
                    "RETURNING chunk_id", (source, item.agent_id, [item.item_id], item.text)).fetchone()[0]
            conn.execute(
                "INSERT INTO vec.embeddings (kind, item_id, agent_id, model, dim, embedding) "
                "VALUES (%s, %s, %s, %s, %s, %s::vector) ON CONFLICT (kind, item_id, model) DO UPDATE "
                "SET agent_id = EXCLUDED.agent_id, dim = EXCLUDED.dim, embedding = EXCLUDED.embedding",
                (kind, item_id, item.agent_id, model, len(vector), _vector_literal(vector)))
        conn.execute(
            "INSERT INTO vec.reembed_progress (source, model, last_item_id) VALUES (%s, %s, %s) "
            "ON CONFLICT (source, model) DO UPDATE SET last_item_id = EXCLUDED.last_item_id, updated_at = now()",
            (source, model, items[-1].item_id))


def reembed_source(conn, embed: Callable[[list[str]], list[list[float]]], *, source: str, model: str,
                   batch_size: int, from_start: bool = False, max_chars: int = DEFAULT_MAX_CHARS,
                   max_batches: Optional[int] = None, out: Callable[[str], None] = print) -> SourceCounts:
    """Embed *source* in batches of *batch_size*, committing each batch with its progress row."""
    if from_start:
        conn.execute("DELETE FROM vec.reembed_progress WHERE source = %s AND model = %s", (source, model))
    counts = SourceCounts(source, resumed_after=_progress(conn, source, model))
    counts.last_item_id = counts.resumed_after
    if source == "memory" and conn.execute("SELECT to_regclass('ltm.documents') IS NULL").fetchone()[0]:
        counts.notes.append("no Honcho tables (ltm.documents) in this store")
    while max_batches is None or counts.batches < max_batches:
        items = _items(conn, source, model, counts.last_item_id, batch_size, max_chars)
        if not items:
            break
        texts = [item.text for item in items if item.text]
        vectors = embed(texts) if texts else []
        _write_batch(conn, source, model, items, vectors)
        counts.batches += 1
        counts.embedded += len(texts)
        counts.covered += sum(item.covered for item in items)
        counts.skipped += len(items) - len(texts) - sum(item.covered for item in items)
        counts.last_item_id = items[-1].item_id
        out(f"{source}: batch {counts.batches}, up to id {counts.last_item_id}, {counts.embedded} embedded")
    return counts


def reembed(connect: Callable[[], Any], client: Any, sources: Sequence[str], *, batch_size: int,
            from_start: bool = False, max_chars: int = DEFAULT_MAX_CHARS, max_batches: Optional[int] = None,
            out: Callable[[str], None] = print) -> list[SourceCounts]:
    with connect() as conn:
        width = _table_width(conn)
        if width != client.dimensions:
            raise ReembedError(f"vec.embeddings holds {width}-wide vectors but embedding.dimensions is "
                               f"{client.dimensions}")
        results = []
        for source in sources:
            counts = reembed_source(conn, client.embed, source=source, model=client.model, batch_size=batch_size,
                                    from_start=from_start, max_chars=max_chars, max_batches=max_batches, out=out)
            if source == "memory":
                from hermes_cli.store_reembed_lcm import reembed_summaries
                reembed_summaries(conn, client.embed, counts, model=client.model, batch_size=batch_size,
                                  from_start=from_start, max_chars=max_chars, max_batches=max_batches, out=out)
            out(counts.line())
            results.append(counts)
        return results


def _owner_connect() -> Iterator[Any]:
    import psycopg

    from hermes_state_store import StoreUnavailableError, resolve_owner_dsn
    try:
        return psycopg.connect(resolve_owner_dsn(), autocommit=True)
    except psycopg.OperationalError as exc:
        raise StoreUnavailableError(f"cannot reach the memory store: {exc}".strip()) from exc


def run(args) -> int:
    """The CLI entry: ``lina store reembed --source agent|memory|messages|all``."""
    from agent.embedding_client import EmbeddingClient, EmbeddingError, EmbeddingSettings
    from hermes_cli.config import load_config
    from hermes_state_store import StoreUnavailableError
    config = load_config()
    if args.from_index:
        from hermes_cli.store_import_vectors import run as import_run
        return import_run(args, config)
    try:
        settings = EmbeddingSettings.from_config(config)
        max_chars = int((config.get("embedding") or {}).get("max_chars") or DEFAULT_MAX_CHARS)
        with EmbeddingClient(settings) as client:
            reembed(_owner_connect, client, expand_sources(args.source),
                    batch_size=args.batch_size or settings.batch_size, from_start=args.from_start,
                    max_chars=max_chars, max_batches=args.max_batches)
    except (ReembedError, EmbeddingError, StoreUnavailableError) as exc:
        print(f"reembed: {exc}")
        return 1
    return 0
