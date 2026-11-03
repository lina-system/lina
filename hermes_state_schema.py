"""Schema creation and column reconciliation for SessionDB.

Plain mixin for ``hermes_state.SessionDB`` (no ``__init__``/state of its own).
Must never import hermes_state (cycle); shared constants live in hermes_state_common.
"""

import contextlib
import datetime
import hashlib
import logging
import json
import os
import sqlite3
import tempfile
import uuid
from typing import Optional


from hermes_constants import get_hermes_home
from hermes_startup_watchdog import report_startup_progress
from hermes_state_common import DEFERRED_INDEX_SQL, SCHEMA_SQL, SCHEMA_VERSION, _ephemeral_child_sql, _sql_json_extract
from hermes_state_errors import is_sqlite_lock_error
from hermes_state_titles import next_title_in_lineage

# Pre-split logger identity so log filtering/capture is unchanged.
logger = logging.getLogger("hermes_state")

# schema_read_probe_statements() cache (parses SCHEMA_SQL in an in-memory DB; once per process).
_READ_PROBE_STATEMENTS: Optional[tuple] = None

_SESSION_MODEL_USAGE_INDEX_SQL = (
    "CREATE INDEX IF NOT EXISTS idx_session_model_usage_session ON session_model_usage(session_id)",
    "CREATE INDEX IF NOT EXISTS idx_session_model_usage_model ON session_model_usage(model)",
)
_SESSION_MODEL_USAGE_HEAL_DDL = """CREATE TABLE session_model_usage (
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    model TEXT NOT NULL,
    billing_provider TEXT NOT NULL DEFAULT '',
    billing_base_url TEXT NOT NULL DEFAULT '',
    billing_mode TEXT NOT NULL DEFAULT '',
    task TEXT NOT NULL DEFAULT '',
    api_call_count INTEGER NOT NULL DEFAULT 0,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens INTEGER NOT NULL DEFAULT 0,
    estimated_cost_usd REAL NOT NULL DEFAULT 0,
    actual_cost_usd REAL NOT NULL DEFAULT 0,
    cost_status TEXT,
    cost_source TEXT,
    first_seen REAL,
    last_seen REAL,
    PRIMARY KEY (session_id, model, billing_provider, billing_base_url, billing_mode, task)
)"""
# v22-migration rendering of the same table (column lines at 35 spaces, paren at 31; pinned SQL).
_SESSION_MODEL_USAGE_V22_DDL = "\n".join(
    [_SESSION_MODEL_USAGE_HEAL_DDL.splitlines()[0]]
    + [" " * 35 + ln.strip() for ln in _SESSION_MODEL_USAGE_HEAL_DDL.splitlines()[1:-1]]
    + [" " * 31 + ")"]
)
# Statement text pinned by the SQL trace harness (whitespace included).
_SESSION_MODEL_USAGE_V20_SEED_SQL = """INSERT OR IGNORE INTO session_model_usage (
                               session_id, model, billing_provider,
                               billing_base_url, billing_mode,
                               api_call_count, input_tokens,
                               output_tokens, cache_read_tokens,
                               cache_write_tokens, reasoning_tokens,
                               estimated_cost_usd, actual_cost_usd,
                               cost_status, cost_source, first_seen, last_seen
                           )
                           SELECT id, COALESCE(model, 'unknown'),
                                  COALESCE(billing_provider, ''),
                                  COALESCE(billing_base_url, ''),
                                  COALESCE(billing_mode, ''),
                                  COALESCE(api_call_count, 0),
                                  COALESCE(input_tokens, 0),
                                  COALESCE(output_tokens, 0),
                                  COALESCE(cache_read_tokens, 0),
                                  COALESCE(cache_write_tokens, 0),
                                  COALESCE(reasoning_tokens, 0),
                                  COALESCE(estimated_cost_usd, 0),
                                  COALESCE(actual_cost_usd, 0),
                                  cost_status, cost_source,
                                  started_at, COALESCE(ended_at, started_at)
                           FROM sessions
                           WHERE COALESCE(input_tokens, 0)
                                 + COALESCE(output_tokens, 0)
                                 + COALESCE(cache_read_tokens, 0)
                                 + COALESCE(cache_write_tokens, 0)
                                 + COALESCE(reasoning_tokens, 0) > 0"""
_TITLE_UNIQUE_INDEX_SQL = (
    "CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_title_unique ON sessions(title) WHERE title IS NOT NULL"
)
def _q(ident: str) -> str:
    """Double-quote an SQL identifier."""
    return '"' + ident.replace('"', '""') + '"'


def schema_read_probe_statements() -> tuple:
    """SELECT statements that fail iff a live store is behind SCHEMA_SQL. Read-only opens skip
    ``_reconcile_columns()`` (no DDL against another profile's live DB), so healing callers
    run these afterwards: a missing table/column raises at prepare time. Derived from
    SCHEMA_SQL (a hand-maintained list went stale within days). Columns are
    table-qualified: an unqualified double-quoted identifier that fails to resolve silently
    degrades to a string literal (SQLite misfeature) and would pass on the stale store."""
    global _READ_PROBE_STATEMENTS
    if _READ_PROBE_STATEMENTS is None:
        tables = SessionSchemaMixin._parse_schema_columns(SCHEMA_SQL)
        _READ_PROBE_STATEMENTS = tuple(
            "SELECT {} FROM {} LIMIT 0".format(", ".join(f"{_q(table)}.{_q(col)}" for col in cols), _q(table))
            for table, cols in sorted(tables.items())
        )
    return _READ_PROBE_STATEMENTS


class SessionSchemaMixin:
    """See module docstring — mixin for SessionDB (Schema cluster)."""

    def _dedupe_legacy_system_prompts(self, cursor: sqlite3.Cursor) -> None:
        """Move inline prompt snapshots into the shared content-addressed table. Any
        ``OperationalError`` mid-loop returns instead of raising: partial migration is safe
        (the legacy column stays a read fallback; next init resumes), whereas propagating
        left the version below 25 and re-ran this on every open (gateway crash loop)."""
        try:
            rows = cursor.execute("SELECT id, system_prompt FROM sessions WHERE system_prompt IS NOT NULL").fetchall()
        except sqlite3.OperationalError:
            return
        for session_id, prompt in rows:
            try:
                prompt_hash = self._store_system_prompt(cursor, prompt)
                cursor.execute(
                    "UPDATE sessions SET system_prompt_hash = ?, system_prompt = NULL WHERE id = ?",
                    (prompt_hash, session_id),
                )
            except sqlite3.OperationalError as exc:
                logger.warning(
                    "v25 prompt dedupe paused after contention (%s); "
                    "unmigrated rows keep the legacy inline prompt and the next schema init resumes the migration.",
                    exc,
                )
                return







    @staticmethod
    def _execute_ddl_skipping_settled_triggers(cursor: sqlite3.Cursor, ddl: str) -> None:
        """Run *ddl* statement by statement, skipping each ``DROP TRIGGER IF EXISTS x`` /
        ``CREATE TRIGGER IF NOT EXISTS x …`` pair whose trigger already exists with the same body.

        ``DROP TRIGGER IF EXISTS`` on an existing trigger takes SQLite's write lock (the
        ``CREATE … IF NOT EXISTS`` forms do not), so the unconditional drop+recreate that lets a
        changed trigger body roll out would otherwise block every open of a settled database
        behind a sibling process's transaction. ``sqlite_master.sql`` stores the CREATE text
        verbatim minus ``IF NOT EXISTS`` and the ``;``, so an exact comparison decides.
        """
        stored = dict(cursor.execute("SELECT name, sql FROM sqlite_master WHERE type = 'trigger'").fetchall())
        pending_drop: Optional[str] = None
        statement = ""
        for line in ddl.splitlines():
            statement += line + "\n"
            if not sqlite3.complete_statement(statement):
                continue
            statement, current = "", statement.strip()
            upper = current.upper()
            if upper.startswith("DROP TRIGGER IF EXISTS "):
                pending_drop = current
                continue
            if upper.startswith("CREATE TRIGGER IF NOT EXISTS "):
                desired = current.replace("IF NOT EXISTS ", "", 1).rstrip(";")
                if stored.get(desired.split(None, 3)[2]) == desired:
                    pending_drop = None
                    continue
            if pending_drop is not None:
                cursor.execute(pending_drop)
                pending_drop = None
            cursor.execute(current)
        if statement.strip():
            raise sqlite3.OperationalError("incomplete DDL statement")

    @staticmethod
    def _execute_ddl_script_transactional(cursor: sqlite3.Cursor, ddl: str) -> None:
        """Execute a DDL script without ``executescript``'s implicit commit."""
        statement = ""
        for line in ddl.splitlines():
            statement += line + "\n"
            if sqlite3.complete_statement(statement):
                cursor.execute(statement)
                statement = ""
        if statement.strip():
            raise sqlite3.OperationalError("incomplete FTS DDL statement")


    @staticmethod
    def _sqlite_table_exists(cursor: sqlite3.Cursor, name: str) -> bool:
        return cursor.execute(
            "SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?", (name,),
        ).fetchone() is not None





    # ── Stale-FTS recovery ─────────────────────────────────────────────────






    # ── Declarative column reconciliation ──────────────────────────────────

    @staticmethod
    def _parse_schema_columns(schema_sql: str) -> dict[str, dict[str, str]]:
        """Expected columns per table: execute SCHEMA_SQL in an in-memory database and read
        PRAGMA table_info (no regex). Memoized on disk keyed by a DDL hash (~85ms per
        startup otherwise); only the reference-side parse is cached — diffing the LIVE
        database still runs every startup. A corrupt/stale cache degrades to recomputation."""
        cache_path = None
        schema_hash = hashlib.sha256(schema_sql.encode("utf-8")).hexdigest()
        with contextlib.suppress(Exception):  # missing/corrupt cache → recompute below
            # Late import: resolves a test-patched hermes_constants.get_hermes_home.
            from hermes_constants import get_hermes_home as _home
            cache_path = _home() / "cache" / "schema_columns.json"
            blob = json.loads(cache_path.read_text(encoding="utf-8"))
            tables = blob.get("tables") if isinstance(blob, dict) and blob.get("schema_hash") == schema_hash else None
            if isinstance(tables, dict) and all(
                isinstance(cols, dict) and all(isinstance(v, str) for v in cols.values()) for cols in tables.values()
            ):
                return tables

        ref = sqlite3.connect(":memory:")
        try:
            ref.executescript(schema_sql)
            table_columns: dict[str, dict[str, str]] = {}
            for (tbl,) in ref.execute(
                "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'"
            ).fetchall():
                cols: dict[str, str] = {}
                info = ref.execute(f'PRAGMA table_info("{tbl}")').fetchall()
                for _cid, col_name, col_type, notnull, default, pk in info:
                    # Reconstruct the type expression for ALTER TABLE ADD COLUMN
                    parts = [col_type] if col_type else []
                    if notnull and not pk:
                        parts.append("NOT NULL")
                    if default is not None:
                        parts.append(f"DEFAULT {default}")
                    cols[col_name] = " ".join(parts)
                table_columns[tbl] = cols
        finally:
            ref.close()

        if cache_path is not None:
            with contextlib.suppress(Exception):  # cache write is best-effort
                cache_path.parent.mkdir(parents=True, exist_ok=True)
                fd, tmp = tempfile.mkstemp(dir=str(cache_path.parent), prefix=".schema_columns.")
                with os.fdopen(fd, "w", encoding="utf-8") as fh:
                    json.dump({"schema_hash": schema_hash, "tables": table_columns}, fh)
                os.replace(tmp, cache_path)
        return table_columns

    def _reconcile_columns(self, cursor: sqlite3.Cursor) -> None:
        """ADD every SCHEMA_SQL column missing from the live tables (SCHEMA_SQL is the single
        source of truth; column additions need no version-gated migration)."""
        expected = self._parse_schema_columns(SCHEMA_SQL)
        for table_name, declared_cols in expected.items():
            try:
                rows = cursor.execute(f'PRAGMA table_info("{table_name}")').fetchall()
            except sqlite3.OperationalError:
                continue  # Table doesn't exist yet (shouldn't happen after executescript)
            if not rows:
                continue  # PRAGMA table_info returns [] for a missing table; the DDL creates it whole
            # PRAGMA table_info rows: (cid, name, type, notnull, dflt_value, pk)
            live_cols = {row[1] for row in rows}
            for col_name, col_type in declared_cols.items():
                if col_name in live_cols:
                    continue
                try:
                    cursor.execute(f'ALTER TABLE "{table_name}" ADD COLUMN {_q(col_name)} {col_type}')
                except sqlite3.OperationalError as exc:
                    message = str(exc).lower()
                    if "duplicate column" in message:
                        # A sibling process won the ADD race; store is correct.
                        logger.debug("reconcile %s.%s: %s", table_name, col_name, exc)
                        continue
                    if is_sqlite_lock_error(exc):
                        # Swallowing lock contention left the store half-reconciled ("no such
                        # column" on every read). Re-raise so the lock-patience wrapper retries init.
                        raise
                    # Anything else permanently strands the store behind SCHEMA_SQL — be loud.
                    logger.warning(
                        "reconcile %s.%s failed; store remains behind SCHEMA_SQL: %s", table_name, col_name, exc,
                    )

    @staticmethod
    def _live_pk_columns(cursor: sqlite3.Cursor, table: str) -> Optional[list[str]]:
        """PRIMARY KEY column names of *table* in key order; None when the table is
        missing or has no columns (SCHEMA_SQL creates it correctly)."""
        try:
            rows = cursor.execute(f'PRAGMA table_info("{table}")').fetchall()
        except sqlite3.OperationalError:
            rows = None
        if not rows:
            return None
        # row: (cid, name, type, notnull, dflt_value, pk)
        return [r[1] for r in sorted((r for r in rows if r[5]), key=lambda r: r[5])]

    @staticmethod
    def _rebuild_table(cursor: sqlite3.Cursor, table: str, legacy_name: str, ddl: str, copy_sql: str, indexes=()) -> None:
        """RENAME *table* to *legacy_name*, CREATE it fresh from *ddl*, copy rows back with
        *copy_sql*, DROP the legacy copy, recreate *indexes* — as ONE write transaction.

        The writer connection is autocommit (``isolation_level=None``), so without an explicit
        BEGIN each statement commits on its own and a sibling process opening the same state.db
        between RENAME and CREATE runs SCHEMA_SQL's ``CREATE TABLE IF NOT EXISTS`` first: our
        CREATE then fails with "table already exists", every row is stranded in *legacy_name*
        and the live table is empty. BEGIN IMMEDIATE holds the write lock for the whole rebuild
        so the sibling blocks (and retries under its lock patience) instead of interleaving;
        any failure rolls the RENAME back."""
        conn = cursor.connection
        if conn.in_transaction:  # caller already owns the transaction
            SessionSchemaMixin._rebuild_table_statements(cursor, table, legacy_name, ddl, copy_sql, indexes)
            return
        cursor.execute("BEGIN IMMEDIATE")
        try:
            SessionSchemaMixin._rebuild_table_statements(cursor, table, legacy_name, ddl, copy_sql, indexes)
        except BaseException:
            cursor.execute("ROLLBACK")
            raise
        cursor.execute("COMMIT")

    @staticmethod
    def _rebuild_table_statements(cursor, table, legacy_name, ddl, copy_sql, indexes) -> None:
        cursor.execute(f"ALTER TABLE {table} RENAME TO {legacy_name}")
        cursor.execute(ddl)
        cursor.execute(copy_sql)
        cursor.execute(f"DROP TABLE {legacy_name}")
        for sql in indexes:
            cursor.execute(sql)

    def _heal_gateway_routing_pk(self, cursor: sqlite3.Cursor) -> None:
        """Rebuild ``gateway_routing`` when its PRIMARY KEY predates scoping (``session_key TEXT
        PRIMARY KEY``): the reconciler ADDs ``scope`` but SQLite cannot ALTER a PK, so every
        routing write fails (ON CONFLICT mismatch / cross-scope UNIQUE violation). Newest
        row wins a cross-scope session_key collision (INSERT OR REPLACE in updated_at order).

        Early builds of the routing-index migration (#59203) created the table with ``session_key TEXT
        PRIMARY KEY`` and no ``scope`` column. ``_reconcile_columns()`` ADDs the missing ``scope`` column on
        those databases, but SQLite cannot ALTER a primary key, so the shipped composite ``PRIMARY KEY
        (scope, session_key)`` never lands. On such tables every write path is broken:
        """
        pk_cols = self._live_pk_columns(cursor, "gateway_routing")
        if pk_cols is None or pk_cols == ["scope", "session_key"]:
            return
        logger.info(
            "gateway_routing has legacy primary key %r; rebuilding with composite (scope, session_key) key", pk_cols,
        )
        self._rebuild_table(
            cursor, "gateway_routing", "gateway_routing_legacy_pk",
            """CREATE TABLE gateway_routing (
    scope TEXT NOT NULL DEFAULT '',
    session_key TEXT NOT NULL,
    entry_json TEXT NOT NULL,
    updated_at REAL NOT NULL,
    PRIMARY KEY (scope, session_key)
)""",
            "INSERT OR REPLACE INTO gateway_routing (scope, session_key, entry_json, updated_at) "
            "SELECT COALESCE(scope, ''), session_key, entry_json, updated_at "
            "FROM gateway_routing_legacy_pk ORDER BY updated_at ASC",
        )

    def _heal_session_model_usage_pk(self, cursor: sqlite3.Cursor) -> None:
        """Rebuild ``session_model_usage`` when its PRIMARY KEY lacks ``task``: installs already at
        v22+ when ``task`` landed carry the 5-column PK, the reconciler ADDs ``task`` but
        SQLite cannot ALTER a PK, and the v22 rebuild is unreachable — every upsert then
        fails (ON CONFLICT mismatch), silently zeroing accounting. Idempotent. FK-off
        window: INSERT OR IGNORE does NOT suppress FK violations, so an orphaned usage row
        would abort the rebuild (PRAGMA foreign_keys is a no-op inside a transaction; none
        is open here). OR IGNORE: COALESCE(task, '') on legacy NULL rows can collide with a
        genuine ''-task row — keep the first.

        Installs whose ``state.db`` reached ``schema_version >= 22`` before the ``task`` dimension was added
        carry a 5-column PRIMARY KEY ``(session_id, model, billing_provider, billing_base_url,
        billing_mode)``. See #73823.
        """
        pk_cols = self._live_pk_columns(cursor, "session_model_usage")
        if pk_cols is None or "task" in pk_cols:
            return
        logger.info(
            "session_model_usage has legacy primary key %r (missing task); rebuilding with composite 6-column key",
            sorted(pk_cols),
        )
        cursor.execute("PRAGMA foreign_keys=OFF")
        try:
            self._rebuild_table(
                cursor, "session_model_usage", "session_model_usage_legacy_pk", _SESSION_MODEL_USAGE_HEAL_DDL,
                # v20: per-model usage attribution (issue #51607). Going forward update_token_counts()
                # records each API call into session_model_usage keyed by the live model, but existing
                # sessions only have their aggregate totals on the sessions row. Seed one usage row per
                # historical session from those aggregates so insights reads uniformly from the new table.
                # INSERT OR IGNORE keeps it idempotent: if newer code already wrote a (session_id, model,
                # provider) row for a session, the PK conflict skips the stale aggregate rather than
                # doubling it.
                """INSERT OR IGNORE INTO session_model_usage (
                       session_id, model, billing_provider, billing_base_url,
                       billing_mode, task, api_call_count, input_tokens,
                       output_tokens, cache_read_tokens, cache_write_tokens,
                       reasoning_tokens, estimated_cost_usd, actual_cost_usd,
                       cost_status, cost_source, first_seen, last_seen
                   )
                   SELECT session_id, model,
                          COALESCE(billing_provider, ''),
                          COALESCE(billing_base_url, ''),
                          COALESCE(billing_mode, ''),
                          COALESCE(task, ''),
                          api_call_count, input_tokens,
                          output_tokens, cache_read_tokens, cache_write_tokens,
                          reasoning_tokens, estimated_cost_usd, actual_cost_usd,
                          cost_status, cost_source, first_seen, last_seen
                   FROM session_model_usage_legacy_pk""",
                _SESSION_MODEL_USAGE_INDEX_SQL,
            )
        except sqlite3.OperationalError as exc:
            logger.debug("session_model_usage PK heal skipped: %s", exc)
        finally:
            cursor.execute("PRAGMA foreign_keys=ON")

    # ── _init_schema ───────────────────────────────────────────────────────

    def _init_schema(self):
        """Create tables if missing, reconcile columns, run data migrations. Column
        additions are declarative via _reconcile_columns(), so reordered migrations can
        never skip a column; schema_version remains for data migrations only."""
        # Startup-watchdog lease: on multi-GB files this is I/O-bound (near-zero CPU), which
        # the watchdog's CPU fallback would misread as a parked deadlock.
        # Declare a startup-watchdog progress lease before potentially long synchronous work: on multi-GB
        # state.db files the reconciliation + version-gated data migrations below are legitimately slow and
        # can be I/O-bound (near-zero CPU), which the watchdog's CPU fallback would misread as a parked
        # deadlock (OOF-298 / PR #89750). Single lease is deliberate: this is the one pre-loop phase that
        # can legitimately exceed the 300s default deadline (multi-GB DBs), and the lease is clamped to
        # _MAX_LEASE_S=900. Honest worst case: a genuinely wedged DB init delays supervisor respawn by up to
        # the lease duration. Per-chunk renewal would shrink that, but adds complexity to the migration
        # loops for a rare failure mode.
        report_startup_progress(600.0, phase="state_db_init_schema")
        cursor = self._conn.cursor()
        cursor.executescript(SCHEMA_SQL)

        # Column reconciliation, then the two table-shape repairs ADD COLUMN cannot express.
        self._reconcile_columns(cursor)
        self._heal_gateway_routing_pk(cursor)
        # Rebuild session_model_usage if its PRIMARY KEY lacks the ``task`` column (5-column PK on installs
        # already at v22+ when the column landed — the version-gated rebuild is unreachable there, #73823).
        # Same PK-rebuild constraint as gateway_routing above.
        self._heal_session_model_usage_pk(cursor)

        # Indexes referencing reconciler-added columns must be created AFTER _reconcile_columns
        # (in SCHEMA_SQL the executescript would fail on legacy DBs).
        self._execute_ddl_skipping_settled_triggers(cursor, DEFERRED_INDEX_SQL)

        row = cursor.execute("SELECT version FROM schema_version LIMIT 1").fetchone()
        if row is None:
            cursor.execute("INSERT INTO schema_version (version) VALUES (?)", (SCHEMA_VERSION,))
            # Store provenance so fresh vs wiped stores are distinguishable.
            # See #97568.
            now_iso = datetime.datetime.now(datetime.UTC).isoformat()
            cursor.executemany(
                "INSERT OR IGNORE INTO state_meta (key, value) VALUES (?, ?)",
                [("store_instance_id", str(uuid.uuid4())), ("store_created_at_utc", now_iso),
                 ],
            )
        else:
            self._run_data_migrations(cursor, row[0])

        self._ensure_unique_title_index(cursor)
        self._conn.commit()


    def _run_data_migrations(self, cursor: sqlite3.Cursor, current_version: int) -> None:
        """Version-gated chain for DATA migrations only (row backfills); column additions never
        belong here. Advances schema_version at the end."""
        # Renew the lease: the chain can rewrite whole tables on large DBs.
        report_startup_progress(600.0, phase="state_db_data_migrations")
        if current_version < 16:
            # v16: tag delegate subagent rows so pickers stay clean after parent deletes orphan them.
            with contextlib.suppress(sqlite3.OperationalError):
                cursor.execute(
                    "UPDATE sessions SET model_config = json_set("
                    "COALESCE(model_config, '{}'), '$._delegate_from', parent_session_id) "
                    f"WHERE parent_session_id IS NOT NULL "
                    f"AND {_sql_json_extract('model_config', '$._delegate_from')} IS NULL "
                    f"AND {_ephemeral_child_sql('sessions')}"
                )
                cursor.execute(
                    "UPDATE sessions SET model_config = json_set("
                    "COALESCE(model_config, '{}'), '$._delegate_from', '__orphaned__') WHERE parent_session_id IS NULL "
                    f"AND {_sql_json_extract('model_config', '$._delegate_from')} IS NULL "
                    f"AND {_sql_json_extract('model_config', '$._branched_from')} IS NULL "
                    "AND title IS NULL AND message_count <= 25 AND EXISTS (SELECT 1 FROM transcript_rows m "
                    "            WHERE m.session_id = sessions.id AND m.role = 'tool') "
                    "AND NOT EXISTS (SELECT 1 FROM sessions ch "
                    "                WHERE ch.parent_session_id = sessions.id)"
                )
        if current_version < 18:
            # v18: best-effort gateway metadata backfill from sessions.json.
            try:
                # Backfill display_name / origin_json / expiry_finalized from sessions.json so pre-migration
                # gateway sessions are discoverable from state.db without the JSON index. See #9006.
                self._backfill_gateway_metadata_from_sessions_json(cursor)
            except Exception as exc:
                logger.debug("v18 gateway metadata backfill skipped: %s", exc)
        if current_version < 20:
            # v20: seed session_model_usage from sessions aggregates (OR IGNORE: newer rows win).
            with contextlib.suppress(sqlite3.OperationalError):
                cursor.execute(_SESSION_MODEL_USAGE_V20_SEED_SQL)
        if current_version < 22:
            self._migrate_v22_session_model_usage(cursor)
        if current_version < 25:
            # v25: de-duplicate system prompt snapshots (old column stays a read fallback).
            self._dedupe_legacy_system_prompts(cursor)
        # v33 (lina#12): transcripts moved to transcript_rows + memory.messages. A pre-v33 store keeps its
        # old ``messages`` table untouched; moving that data in is lina#24's import, not an open-time job.
        if current_version < SCHEMA_VERSION:
            cursor.execute("UPDATE schema_version SET version = ?", (SCHEMA_VERSION,))

    def _migrate_v22_session_model_usage(self, cursor: sqlite3.Cursor) -> None:
        """v22: ``task`` joins the session_model_usage PRIMARY KEY ('' = main loop; aux calls
        named). SQLite cannot ALTER a PK, so rebuild; existing rows → task=''."""
        try:
            # v22: task-dimension usage attribution (issue #23270). session_model_usage gains a ``task``
            # column ('' = main agent loop; 'vision'/'compression'/'title_generation'/... = auxiliary calls)
            # so aux model spend is visible in analytics. The reconciler will have already ADDed the plain
            # column on legacy DBs (harmless); the rebuild bakes it into the PK properly.
            legacy_pk = cursor.execute(
                "SELECT COUNT(*) FROM pragma_table_info('session_model_usage') WHERE name = 'task' AND pk > 0"
            ).fetchone()[0]
            if legacy_pk:
                return
            self._rebuild_table(
                cursor, "session_model_usage", "session_model_usage_v21", _SESSION_MODEL_USAGE_V22_DDL,
                """INSERT INTO session_model_usage (
                                   session_id, model, billing_provider, billing_base_url,
                                   billing_mode, task, api_call_count, input_tokens,
                                   output_tokens, cache_read_tokens, cache_write_tokens,
                                   reasoning_tokens, estimated_cost_usd, actual_cost_usd,
                                   cost_status, cost_source, first_seen, last_seen
                               )
                               SELECT session_id, model, billing_provider, billing_base_url,
                                      billing_mode, '', api_call_count, input_tokens,
                                      output_tokens, cache_read_tokens, cache_write_tokens,
                                      reasoning_tokens, estimated_cost_usd, actual_cost_usd,
                                      cost_status, cost_source, first_seen, last_seen
                               FROM session_model_usage_v21""",
                _SESSION_MODEL_USAGE_INDEX_SQL,
            )
        except sqlite3.OperationalError as exc:
            logger.debug("v22 session_model_usage rebuild skipped: %s", exc)

    def _ensure_unique_title_index(self, cursor: sqlite3.Cursor) -> None:
        """Unique title index. Older DBs may hold duplicate titles from before the constraint.
        Per title a user-owned (user/NULL) row beats llm > derived (``_title_rank``). User rows are
        never dropped: the oldest keeps the title and newer ones become ``Title #k`` in started_at
        order, so the "#N"-preferring lookup still opens the newest (#126764). Among auto titles
        only, the newest highest-ranked keeps it; lower-ranked auto titles are cleared. Each change
        is logged. Must never abort opening the DB, so the repair is guarded."""
        try:
            cursor.execute(_TITLE_UNIQUE_INDEX_SQL)
        except sqlite3.IntegrityError:
            # Savepoint: a failure part-way must not leave a half-repaired store for
            # _init_schema to commit without the index.
            cursor.execute("SAVEPOINT title_repair")
            try:
                rows = cursor.execute(
                    "SELECT rowid, title, title_source, started_at FROM sessions WHERE title IN "
                    "(SELECT title FROM sessions WHERE title IS NOT NULL GROUP BY title HAVING COUNT(*) > 1)"
                ).fetchall()
                groups: dict[str, list] = {}
                for row in sorted(rows, key=lambda r: (self._title_rank(r[2]), r[3], r[0]), reverse=True):
                    groups.setdefault(row[1], []).append(row)
                user_rank = self._TITLE_SOURCE_RANK[self.TITLE_SOURCE_USER]
                for title, group in groups.items():
                    # User titles: the OLDEST keeps the base and newer ones get "#k" in
                    # chronological order, so resolve_session_by_title (which prefers the
                    # latest "#N") still lands on the newest session.
                    users = [r for r in reversed(group) if self._title_rank(r[2]) == user_rank]
                    keep = users[0] if users else group[0]
                    for rowid, *_ in users[1:]:
                        renamed = next_title_in_lineage(cursor.connection, title)
                        cursor.execute("UPDATE sessions SET title = ? WHERE rowid = ?", (renamed, rowid))
                        logger.warning("Renamed duplicate user session title %r to %r", title, renamed)
                    for rowid, _title, source, _started in group:
                        if rowid != keep[0] and self._title_rank(source) != user_rank:
                            cursor.execute("UPDATE sessions SET title = NULL WHERE rowid = ?", (rowid,))
                            logger.warning("Cleared duplicate %s session title %r", source, title)
                cursor.execute(_TITLE_UNIQUE_INDEX_SQL)
                cursor.execute("RELEASE title_repair")
            except sqlite3.Error:
                cursor.execute("ROLLBACK TO title_repair")
                cursor.execute("RELEASE title_repair")
                logger.exception("Could not repair duplicate session titles; unique title index not created")
        except sqlite3.OperationalError:
            pass  # Index already exists



    def _backfill_gateway_metadata_from_sessions_json(self, cursor: sqlite3.Cursor) -> None:
        """One-time v18 backfill of gateway metadata from sessions.json. Only fills NULL
        columns — never overwrites data written by newer code."""
        sessions_file = get_hermes_home() / "sessions" / "sessions.json"
        if not sessions_file.exists():
            return
        with open(sessions_file, "r", encoding="utf-8") as f:
            data = json.load(f)
        if not isinstance(data, dict):
            return
        for key, entry in data.items():
            if str(key).startswith("_") or not isinstance(entry, dict):
                continue
            session_id = entry.get("session_id")
            if not session_id:
                continue
            origin = entry.get("origin")
            origin_dict = origin if isinstance(origin, dict) else None
            cursor.execute(
                """UPDATE sessions
                   SET session_key = COALESCE(session_key, ?),
                       chat_id = COALESCE(chat_id, ?),
                       chat_type = COALESCE(chat_type, ?),
                       thread_id = COALESCE(thread_id, ?),
                       display_name = COALESCE(display_name, ?),
                       origin_json = COALESCE(origin_json, ?),
                       expiry_finalized = CASE
                           WHEN COALESCE(expiry_finalized, 0) = 0 AND ? = 1 THEN 1
                           ELSE expiry_finalized
                       END
                   WHERE id = ?""",
                (
                    entry.get("session_key") or key, origin_dict.get("chat_id") if origin_dict is not None else None,
                    entry.get("chat_type"), origin_dict.get("thread_id") if origin_dict is not None else None,
                    entry.get("display_name"), json.dumps(origin) if origin_dict is not None else None,
                    1 if entry.get("expiry_finalized") or entry.get("memory_flushed") else 0, str(session_id),
                ),
            )


def apply_declared_schema(cursor: sqlite3.Cursor) -> None:
    """The one additive catch-up order for raw ``state.db`` connections: columns (SCHEMA_SQL indexes may
    name columns an old table lacks), then the canonical DDL statement by statement (no
    ``executescript`` implicit COMMIT, so callers keep their own transaction), then columns again
    (a no-op for tables the script just created whole). Locking, snapshots and commit stay with the
    callers. ``_reconcile_columns`` only touches the staticmethod ``_parse_schema_columns``, so a bare
    instance is enough and the column reconciler keeps one implementation."""
    shim = object.__new__(SessionSchemaMixin)
    shim._reconcile_columns(cursor)
    SessionSchemaMixin._execute_ddl_script_transactional(cursor, SCHEMA_SQL)
    shim._reconcile_columns(cursor)


def reconcile_state_schema(conn: sqlite3.Connection) -> None:
    """Bring a raw ``state.db`` connection to the canonical SCHEMA_SQL shape and commit.

    Single durable-shape authority for callers that open ``state.db``
    outside SessionDB. The async-delegation tool used to carry its own
    CREATE TABLE and ALTER column list for ``async_delegations``; it drifted
    from SCHEMA_SQL (same-name columns with different nullability/defaults
    depending on which authority touched the database first, #94691). This
    helper instead replays the canonical DDL (every statement is
    IF NOT EXISTS/idempotent) through :func:`apply_declared_schema`.
    """
    apply_declared_schema(conn.cursor())
    if conn.in_transaction:
        conn.commit()
