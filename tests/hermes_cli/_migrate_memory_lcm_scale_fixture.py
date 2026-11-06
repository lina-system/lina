"""Standalone LCM-X scale source; schema comes from the real SQLite stores.

Import ``generate_lcm`` like the adjacent Round2 ``generate`` helper. Only the
schema bootstrap uses store objects; bulk content bypasses per-message ingest,
tokenization and commits. No importer, host store or external service is needed.
"""

from __future__ import annotations

import json
import sqlite3
from contextlib import closing
from pathlib import Path
from typing import Final, TypedDict

from tests.plugins.context_engine.test_lcm_x_on_store import _lcm

EPOCH: Final = 1700000000.0
BATCH_SIZE: Final = 256


class LCMFixtureMetadata(TypedDict):
    schema_version: int
    rows: int
    sessions: int
    summary_nodes: int
    max_depth: int
    content_bytes: int
    bytes: int
    target_bytes: int
    batch_size: int
    tables: list[str]
    table_counts: dict[str, int]


def _bootstrap(path: Path) -> None:
    """Materialize core and every optional durable table family without APIs."""
    with closing(_lcm("store").MessageStore(path)) as store:
        store._ensure_identity_anchor_schema()
        store._ensure_host_uid_schema()
    with closing(_lcm("dag").SummaryDAG(path)):
        pass
    bootstrap = _lcm("db_bootstrap")
    with closing(sqlite3.connect(path)) as conn:
        bootstrap.ensure_embedding_tables(conn)
        bootstrap.ensure_chunk_tables(conn)
        bootstrap.ensure_assertion_tables(conn)
        bootstrap.ensure_entity_tables(conn)
        bootstrap.ensure_temporal_rollup_tables(conn)
        bootstrap.ensure_temporal_rollup_invalidation_triggers(conn)
        _lcm("teams.catalog").ensure_teams_catalog(conn)
        _lcm("fork_summary_quarantine")._ensure_table(conn)
        _lcm("command")._ensure_inflight_table(conn)
        conn.commit()
    with closing(_lcm("query_view_store").QueryViewStore(path)):
        pass
    trajectory = _lcm("trajectory_store")
    identity = trajectory.CorpusIdentity(
        dataset_name="synthetic-r7", dataset_revision="1", harness_commit="fixture",
        tier="scale", domain="engineering", ingest_config_digest="synthetic-r7-v1",
    )
    with closing(trajectory.TrajectoryStore(path, identity, asset_root=path.parent)) as store:
        store._ensure_semantic_schema()
        store._ensure_state_semantic_schema()


def _content(number: int) -> str:
    """Varied engineering conversation text, JSON tool output and code excerpts."""
    topic = ("deployment", "database", "billing", "search", "gateway", "scheduler")[number % 6]
    header = f"Synthetic incident {number}: investigate {topic} for workspace {number // 256}.\n"
    lines = [
        f"Check {step}: service={topic}-{(number + step) % 37}; "
        f"request=req-{number:08d}-{step:02d}; latency_ms={(number * 17 + step * 31) % 1900}; "
        f"expected_rows={number % 97 + step}; actual_rows={number % 97 + step}; "
        f"decision={'retain the transaction boundary' if step % 2 else 'verify the source reference'}; "
        f"next=inspect shard {(number + step * 3) % 23} before release."
        for step in range(3 + number % 21)
    ]
    forms = (
        "Please review these observations and explain the next diagnostic step.\n",
        "The evidence supports a bounded repair. Preserve row identity and rerun the query.\n",
        json.dumps({"command": f"inspect {topic}", "exit_code": 0, "request": number}) + "\n",
        f"Proposed regression:\n```python\nassert lookup({number}) == {number}\n```\n",
    )
    return header + forms[number % 4] + "\n".join(lines)


def _summaries(conn: sqlite3.Connection, session: str, first: int) -> None:
    """One bounded D0/D1/D2 DAG per batch, with exact source-token accounting."""
    level = []
    for offset in range(0, BATCH_SIZE, 32):
        ids = list(range(first + offset, first + offset + 32))
        tokens = conn.execute(
            "SELECT sum(token_estimate) FROM messages WHERE store_id BETWEEN ? AND ?",
            (ids[0], ids[-1]),
        ).fetchone()[0]
        level.append((ids, tokens, ids[0], ids[-1]))
    for depth in range(3):
        nodes = []
        for ids, tokens, start, end in level:
            summary = (f"Synthetic workspace {session}, depth {depth}: reviewed messages {start}-{end}. "
                       "Deployment checks preserved transaction boundaries, source identity and row counts. "
                       "Pending action: verify the shard before release; expand sources for diagnostic details.")
            cursor = conn.execute(
                "INSERT INTO summary_nodes(session_id, depth, summary, token_count, source_token_count, "
                "source_ids, source_type, created_at, earliest_at, latest_at, expand_hint) "
                "VALUES (?, ?, ?, 60, ?, ?, ?, ?, ?, ?, ?)",
                (session, depth, summary, tokens, json.dumps(ids), "messages" if depth == 0 else "nodes",
                 EPOCH + end + depth + 1, EPOCH + start, EPOCH + end,
                 f"Expand diagnostic evidence for messages {start}-{end}"),
            )
            node = cursor.lastrowid
            conn.execute("INSERT INTO summary_node_provenance VALUES (?, ?, ?, ?)",
                         (node, depth, "synthetic-offline-v1", EPOCH + end + depth + 1))
            nodes.append((node, start, end))
        level = [([node for node, _, _ in nodes[i:i + 4]], 60 * len(nodes[i:i + 4]),
                  nodes[i][1], nodes[min(i + 3, len(nodes) - 1)][2])
                 for i in range(0, len(nodes), 4)]


def generate_lcm(path: Path, target_bytes: int = 420 * 1024**2) -> LCMFixtureMetadata:
    """Create a fresh source with at least target bytes of genuine message text.

    The checkpointed main file also reaches the target. Batches hold at most 256
    messages and 11 summary nodes. Refuse existing paths rather than appending to
    a previous proof. Optional vectors/assertions/entities/rollups/query views/
    trajectories are present but empty (except their corpus metadata). No paired
    state fixture is emitted. All output stays under the caller's path.
    """
    path = Path(path)
    if path.exists():
        raise FileExistsError(path)
    if target_bytes < 0:
        raise ValueError("target_bytes must be nonnegative")
    path.parent.mkdir(parents=True, exist_ok=True)
    _bootstrap(path)
    rows = content_bytes = sessions = 0
    with closing(sqlite3.connect(path)) as conn:
        conn.execute("PRAGMA foreign_keys=ON")
        conn.execute("PRAGMA synchronous=NORMAL")
        conn.execute("PRAGMA cache_size=-32768")
        page_size = conn.execute("PRAGMA page_size").fetchone()[0]
        allocated = 0
        while rows == 0 or content_bytes < target_bytes or allocated < target_bytes:
            session = f"lcm-scale-{sessions:06d}"
            batch = []
            for number in range(rows, rows + BATCH_SIZE):
                content = _content(number)
                content_bytes += len(content.encode("utf-8"))
                role = ("user", "assistant", "tool", "assistant")[number % 4]
                call_id = f"call-{number // 4:08d}"
                batch.append((number + 1, session, "telegram", f"telegram:synthetic:{sessions}",
                              role, content, call_id if number % 4 == 2 else None,
                              json.dumps([{"id": call_id, "type": "function", "function": {
                                  "name": "terminal", "arguments": json.dumps({"command": "inspect shard"})}}])
                              if number % 4 == 1 else None,
                              "terminal" if number % 4 == 2 else None, EPOCH + number + 1,
                              (len(content) + 3) // 4, int(number % 127 == 0),
                              EPOCH + number + 2, EPOCH + number + 1, "host", "synthetic-owner", "telegram"))
            conn.executemany(
                "INSERT INTO messages(store_id, session_id, source, conversation_id, role, content, "
                "tool_call_id, tool_calls, tool_name, timestamp, token_estimate, pinned, ingested_at, "
                "observed_at, observed_at_source, sender_id, sender_source) "
                "VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", batch,
            )
            _summaries(conn, session, rows + 1)
            rows += BATCH_SIZE
            sessions += 1
            conn.commit()
            allocated = page_size * conn.execute("PRAGMA page_count").fetchone()[0]
        # Bootstrap and change-log triggers use wall time; source rows never do.
        conn.execute("UPDATE lcm_migration_state SET completed_at=?", (EPOCH,))
        conn.execute("UPDATE lcm_query_corpus_state SET changed_at=?", (EPOCH + rows,))
        conn.execute("UPDATE lcm_query_corpus_events SET changed_at=? + store_id", (EPOCH,))
        conn.execute("UPDATE lcm_rollup_invalidations SET created_at=? + event_id", (EPOCH,))
        conn.execute("UPDATE lcm_trajectory_corpora SET created_at=?", (EPOCH,))
        conn.execute("UPDATE metadata SET value=? WHERE key LIKE 'fts_integrity_checked_at:%'", (str(EPOCH),))
        conn.commit()
        conn.execute("PRAGMA wal_checkpoint(TRUNCATE)")
        tables = [row[0] for row in conn.execute(
            "SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")]
        counts = {table: conn.execute(f'SELECT count(*) FROM "{table}"').fetchone()[0] for table in tables}
        return LCMFixtureMetadata(
            schema_version=_lcm("db_bootstrap").get_schema_version(conn), rows=rows,
            sessions=sessions, summary_nodes=counts["summary_nodes"], max_depth=2,
            content_bytes=content_bytes, bytes=path.stat().st_size, target_bytes=target_bytes,
            batch_size=BATCH_SIZE, tables=tables, table_counts=counts,
        )
