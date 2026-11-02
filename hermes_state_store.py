"""The message store: every message's text lives once in ``memory.messages`` (LINA's Postgres, lina#12).

This module is the only code that runs SQL against ``memory.messages``. A profile's ``state.db`` keeps
the text-free placement of each message (``transcript_rows``: which session holds it, its position,
whether it is live, compacted or rewound) and points at its text by ``text_uid``.

One message is one row. Hermes writes the same message (one ``message_uid``) several times as a
transcript is compacted or handed to a compression child; those copies share one row. A copy whose
text differs (a pruned tool result beside the archived original) is a new version:
``agent:<profile>:<message_uid>`` is the first, ``...#<digest>`` a later one. A row is never changed
after it is written, so a cached row is always current. It is deleted with its session's transcript,
unless a row of another session still points at it.

Connections log in as ``lina_app`` and set ``lina.agent_id`` to the profile, so row-level security
keeps every profile to its own rows.
"""

from __future__ import annotations

import base64
import hashlib
import json
import logging
import os
import threading
from collections import OrderedDict
from collections.abc import Callable, Iterable, Iterator, Sequence
from contextlib import contextmanager
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Optional

logger = logging.getLogger(__name__)

CONTENT_JSON_PREFIX = "\x00json:"
# Text columns of a Hermes message; state.db holds none of them.
TEXT_FIELDS = ("content", "tool_calls", "reasoning", "reasoning_content", "reasoning_details",
               "codex_reasoning_items", "codex_message_items", "api_content")
_NATIVE_TEXT_FIELDS = TEXT_FIELDS[2:]
# Per-message Hermes columns copied into ``native`` when a row is first written (for the other engines;
# Hermes itself reads them from transcript_rows).
NATIVE_META_FIELDS = ("message_uid", "platform_message_id", "finish_reason", "token_count", "effect_disposition",
                      "observed", "_compressed_summary", "absorbed_message_uids", "tool_call_uids", "tool_call_uid")
SOURCE_AGENT = "agent"
_CACHE_ROWS = 50_000


class StoreUnavailableError(RuntimeError):
    """The memory store cannot be reached or is not set up."""


@dataclass(frozen=True)
class TextRow:
    """One message's text, in the form Hermes wrote it (content still encoded, JSON columns as text)."""
    uid: str
    values: tuple  # in TEXT_FIELDS order

    def get(self, field: str) -> Any:
        return self.values[TEXT_FIELDS.index(field)]


def _sha256(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8", "surrogatepass")).hexdigest()


def content_digest(encoded_content: Any) -> Optional[str]:
    """Digest of a message's encoded content, kept in transcript_rows for equality lookups."""
    if encoded_content is None:
        return None
    return _sha256(_tagged(encoded_content)[1] if not isinstance(encoded_content, str) else encoded_content)


def _tagged(value: Any) -> tuple[str, str]:
    """A value Postgres can hold in a jsonb string: (kind, text). Text with NUL or lone surrogates goes base64."""
    if isinstance(value, bytes):
        return "b", base64.b64encode(value).decode("ascii")
    if isinstance(value, bool) or not isinstance(value, (str, int, float)):
        return "j", json.dumps(value)
    if isinstance(value, (int, float)):
        return ("i" if isinstance(value, int) else "f"), repr(value)
    try:
        value.encode("utf-8")
    except UnicodeEncodeError:
        return "u", base64.b64encode(value.encode("utf-8", "surrogatepass")).decode("ascii")
    if "\x00" in value:
        return "u", base64.b64encode(value.encode("utf-8")).decode("ascii")
    return "s", value


def _untagged(kind: str, text: str) -> Any:
    if kind == "s":
        return text
    if kind == "u":
        return base64.b64decode(text).decode("utf-8", "surrogatepass")
    if kind == "b":
        return base64.b64decode(text)
    if kind == "i":
        return int(text)
    if kind == "f":
        return float(text)
    return json.loads(text)


def _pg_text(text: str) -> str:
    """*text* as Postgres text: no NUL, lone surrogates replaced (the exact value stays in ``native``)."""
    try:
        text.encode("utf-8")
    except UnicodeEncodeError:
        text = text.encode("utf-8", "replace").decode("utf-8")
    return text.replace("\x00", "")


def _plain_text(content: Any) -> Optional[str]:
    """The searchable text of an encoded content value (the text parts of multimodal content)."""
    if content is None or isinstance(content, bytes):
        return None
    if isinstance(content, str) and content.startswith(CONTENT_JSON_PREFIX):
        try:
            parts = json.loads(content[len(CONTENT_JSON_PREFIX):])
        except ValueError:
            return _pg_text(content)
        if isinstance(parts, str):
            return _pg_text(parts)
        texts = []
        for part in parts if isinstance(parts, list) else [parts]:
            if isinstance(part, dict) and isinstance(part.get("text"), str):
                texts.append(part["text"])
            elif isinstance(part, str):
                texts.append(part)
        return _pg_text("\n".join(texts))
    return _pg_text(content if isinstance(content, str) else str(content))


def payload_digest(values: Sequence[Any]) -> str:
    """Identity of one text version: every text column, exactly as Hermes wrote it."""
    return _sha256(json.dumps([None if v is None else _tagged(v) for v in values], separators=(",", ":")))


def _native(values: Sequence[Any], meta: dict[str, Any], digest: str) -> dict[str, Any]:
    native: dict[str, Any] = {"payload_sha256": digest}
    content = values[0]
    if content is not None and not (isinstance(content, str) and _tagged(content)[0] == "s"
                                    and not content.startswith(CONTENT_JSON_PREFIX)):
        native["content_raw"] = list(_tagged(content))
    if values[1] is not None:
        native["tool_calls_raw"] = list(_tagged(values[1]))
    for field, value in zip(_NATIVE_TEXT_FIELDS, values[2:]):
        if value is not None:
            native[field] = list(_tagged(value))
    for field in NATIVE_META_FIELDS:
        if meta.get(field) not in (None, 0, False, ""):
            native[field] = meta[field]
    return native


def _values_from_row(content: Optional[str], native: dict[str, Any]) -> tuple:
    raw = native.get("content_raw")
    values = [_untagged(*raw) if raw else content]
    tool_calls = native.get("tool_calls_raw")
    values.append(_untagged(*tool_calls) if tool_calls else None)
    for field in _NATIVE_TEXT_FIELDS:
        stored = native.get(field)
        values.append(_untagged(*stored) if stored else None)
    return tuple(values)


def _jsonb_tool_calls(raw: Any) -> Optional[str]:
    if not isinstance(raw, str):
        return None
    try:
        parsed = json.loads(raw)
    except ValueError:
        return None
    text = json.dumps(parsed, ensure_ascii=False)
    try:
        text.encode("utf-8")  # a lone surrogate cannot go into jsonb; the exact text stays in native
    except UnicodeEncodeError:
        return None
    return None if "\x00" in text or "\\u0000" in text else text


def resolve_dsn() -> str:
    """``LINA_STORE_DSN`` (a secret: it carries the password) when set, else the store LINA installed."""
    explicit = os.environ.get("LINA_STORE_DSN", "").strip()
    if explicit:
        return explicit
    from hermes_cli.store import read_env, store_dir
    values = read_env(store_dir() / "store.env")
    password = values.get("LINA_APP_PASSWORD")
    if not password:
        raise StoreUnavailableError(
            "the memory store is not set up (no store.env with LINA_APP_PASSWORD); run `lina store install`")
    from psycopg.conninfo import make_conninfo
    return make_conninfo(host="127.0.0.1", port=values.get("LINA_STORE_PORT", "54320"), dbname="lina",
                         user="lina_app", password=password, connect_timeout="10")


def agent_id_for(db_path: Path) -> str:
    """The profile a state.db belongs to, from its path alone: ``<name>`` for ``profiles/<name>/state.db``,
    else ``default``. Never from the process's active profile: one path must name one agent everywhere."""
    home = Path(db_path).resolve().parent
    return home.name if home.parent.name == "profiles" else "default"


class _TextCache:
    """Process-wide cache of text rows by (agent, uid). Rows never change, so entries never go stale."""

    def __init__(self, limit: int = _CACHE_ROWS) -> None:
        self._rows: OrderedDict[tuple[str, str], TextRow] = OrderedDict()
        self._lock = threading.Lock()
        self._limit = limit

    def get(self, agent: str, uid: str) -> Optional[TextRow]:
        with self._lock:
            row = self._rows.get((agent, uid))
            if row is not None:
                self._rows.move_to_end((agent, uid))
            return row

    def put(self, agent: str, row: TextRow) -> None:
        with self._lock:
            self._rows[(agent, row.uid)] = row
            self._rows.move_to_end((agent, row.uid))
            while len(self._rows) > self._limit:
                self._rows.popitem(last=False)

    def drop(self, agent: str, uids: Iterable[str]) -> None:
        with self._lock:
            for uid in uids:
                self._rows.pop((agent, uid), None)

    def clear(self) -> None:
        with self._lock:
            self._rows.clear()


TEXT_CACHE = _TextCache()


class MessageStore:
    """One profile's view of ``memory.messages``.

    Reads run on an autocommit connection. Writes run inside ``write()``, which the SessionDB opens
    around each state.db write transaction: the Postgres transaction commits right before state.db's,
    and a rollback or retry of the state.db write rolls it back too.
    """

    def __init__(self, agent_id: str, *, dsn: Optional[str] = None) -> None:
        self.agent_id = agent_id
        self._dsn = dsn
        self._read_conn = None
        self._read_lock = threading.RLock()
        self._write_conn = None
        self._write_owner: Optional[int] = None
        self._write_depth = 0
        self._pending_sessions: list[str] = []
        self._closed = False

    # ── connections ──

    def _connect(self, *, autocommit: bool):
        import psycopg
        dsn = self._dsn or resolve_dsn()
        try:
            conn = psycopg.connect(dsn, autocommit=True)
        except psycopg.OperationalError as exc:
            raise StoreUnavailableError(f"cannot reach the memory store: {exc}".strip()) from exc
        conn.execute("SELECT set_config('lina.agent_id', %s, false)", (self.agent_id,))
        conn.autocommit = autocommit
        return conn

    def _reader(self):
        if self._read_conn is None or self._read_conn.closed:
            self._read_conn = self._connect(autocommit=True)
        return self._read_conn

    @contextmanager
    def _cursor(self) -> Iterator[Any]:
        """The write transaction's cursor on its own thread, else a read cursor."""
        if self._write_conn is not None and self._write_owner == threading.get_ident():
            with self._write_conn.cursor() as cur:
                yield cur
            return
        with self._read_lock:
            conn = self._reader()
            try:
                with conn.cursor() as cur:
                    yield cur
            except Exception:
                if conn.closed or conn.broken:
                    self._read_conn = None
                raise

    @contextmanager
    def write(self) -> Iterator[None]:
        """Bind a Postgres transaction to this thread for the length of one state.db write.

        Nested calls join the outer transaction. The transaction starts on the first statement, so a
        state.db write that touches no message text never talks to Postgres.
        """
        me = threading.get_ident()
        if self._write_owner == me:
            self._write_depth += 1
            try:
                yield
            finally:
                self._write_depth -= 1
            return
        self._write_owner, self._write_depth = me, 1
        self._pending_sessions = []
        try:
            yield
            if self._write_conn is not None and not self._write_conn.closed:
                self._write_conn.commit()
        except BaseException:
            self._pending_sessions = []
            if self._write_conn is not None and not self._write_conn.closed:
                try:
                    self._write_conn.rollback()
                except Exception:
                    logger.debug("memory store rollback failed", exc_info=True)
                    self._discard_writer()
            raise
        finally:
            self._write_owner, self._write_depth = None, 0

    def _writer_cursor(self):
        if self._write_owner != threading.get_ident():
            raise RuntimeError("memory store writes run inside MessageStore.write()")
        if self._write_conn is None or self._write_conn.closed or self._write_conn.broken:
            self._write_conn = self._connect(autocommit=False)
        return self._write_conn.cursor()

    def _discard_writer(self) -> None:
        conn, self._write_conn = self._write_conn, None
        if conn is not None:
            try:
                conn.close()
            except Exception:
                logger.debug("closing a broken memory store writer failed", exc_info=True)

    def close(self) -> None:
        self._closed = True
        for name in ("_read_conn", "_write_conn"):
            conn = getattr(self, name)
            setattr(self, name, None)
            if conn is not None:
                try:
                    conn.close()
                except Exception:
                    logger.debug("closing a memory store connection failed", exc_info=True)

    # ── text rows ──

    def base_uid(self, message_uid: str) -> str:
        return f"{SOURCE_AGENT}:{self.agent_id}:{message_uid}"

    def put_text(self, *, message_uid: str, values: Sequence[Any], role: Optional[str], tool_name: Optional[str],
                 tool_call_id: Optional[str], timestamp: Optional[float], session_id: Optional[str],
                 seq_in_session: Optional[int], meta: dict[str, Any]) -> str:
        """The uid of this message's text version, writing it when it is new (inside ``write()``)."""
        values = tuple(values)
        digest = payload_digest(values)
        base = self.base_uid(message_uid)
        versioned = f"{base}#{digest[:16]}"
        for uid in (base, versioned):
            cached = TEXT_CACHE.get(self.agent_id, uid)
            if cached is not None and cached.values == values:
                return uid
        text = _plain_text(values[0])
        row = (SOURCE_AGENT, self.agent_id, session_id, seq_in_session, role, text, _plain_text(tool_name),
               _plain_text(tool_call_id), _jsonb_tool_calls(values[1]), timestamp,
               json.dumps(_native(values, meta, digest)), _sha256(text or ""))
        insert = ("INSERT INTO memory.messages (uid, source, agent_id, session_id, seq_in_session, role, content,"
                  " tool_name, tool_call_id, tool_calls, ts, native, content_sha256)"
                  " VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s::jsonb, to_timestamp(%s), %s::jsonb, %s)"
                  " ON CONFLICT (uid) DO NOTHING RETURNING uid")
        with self._writer_cursor() as cur:
            # A new message (the usual case) is one round trip; a known one shows which version it is.
            cur.execute(insert, (base, *row))
            uid = base if cur.fetchone() else None
            if uid is None:
                cur.execute("SELECT native->>'payload_sha256' FROM memory.messages WHERE uid = %s", (base,))
                stored = cur.fetchone()
                if stored is not None and stored[0] == digest:
                    uid = base
                else:
                    cur.execute(insert, (versioned, *row))
                    cur.fetchall()
                    uid = versioned
        TEXT_CACHE.put(self.agent_id, TextRow(uid, values))
        return uid

    def texts(self, uids: Iterable[Optional[str]]) -> dict[str, TextRow]:
        """Text rows by uid; a uid with no row is left out."""
        wanted = list(dict.fromkeys(u for u in uids if u))
        found: dict[str, TextRow] = {}
        missing = []
        for uid in wanted:
            row = TEXT_CACHE.get(self.agent_id, uid)
            if row is None:
                missing.append(uid)
            else:
                found[uid] = row
        for start in range(0, len(missing), 1000):
            chunk = missing[start:start + 1000]
            with self._cursor() as cur:
                cur.execute("SELECT uid, content, native FROM memory.messages WHERE uid = ANY(%s)", (chunk,))
                for uid, content, native in cur.fetchall():
                    row = TextRow(uid, _values_from_row(content, native or {}))
                    TEXT_CACHE.put(self.agent_id, row)
                    found[uid] = row
        return found

    def text_value(self, uid: Optional[str], field: str) -> Any:
        if not uid:
            return None
        row = self.texts([uid]).get(uid)
        return None if row is None else row.get(field)

    def forget_sessions_after_commit(self, session_ids: Iterable[str]) -> None:
        """Queue sessions whose transcript this write emptied: once it commits (``flush_forgotten``) their
        text goes, except versions another session still points at. Text of a session that still has rows
        stays: a reader may hold a snapshot of a row a rewrite or replace just moved off it."""
        if self._write_owner != threading.get_ident():
            raise RuntimeError("memory store writes run inside MessageStore.write()")
        self._pending_sessions.extend(s for s in session_ids if s)

    def flush_forgotten(self, still_held: Callable[[list[str]], set[str]]) -> int:
        """Delete the text of the sessions queued by the last committed write; *still_held* names the
        uids state.db rows still point at (a compression child shares its parent's text)."""
        sessions, self._pending_sessions = list(dict.fromkeys(self._pending_sessions)), []
        if not sessions:
            return 0
        with self._cursor() as cur:
            cur.execute("SELECT uid FROM memory.messages WHERE session_id = ANY(%s)", (sessions,))
            uids = [row[0] for row in cur.fetchall()]
        doomed = sorted(set(uids) - still_held(uids))
        if not doomed:
            return 0
        with self._cursor() as cur:
            cur.execute("DELETE FROM memory.messages WHERE uid = ANY(%s)", (doomed,))
            count = cur.rowcount
        TEXT_CACHE.drop(self.agent_id, doomed)
        return count

    def session_messages(self, session_id: str) -> list[dict[str, Any]]:
        """The rows first written by *session_id*, in write order: the shape the other engines (LCM,
        Honcho) read, without Hermes's placement state."""
        with self._cursor() as cur:
            cur.execute("SELECT uid, role, content, tool_name, tool_call_id, ts, seq_in_session FROM memory.messages "
                        "WHERE session_id = %s AND forgotten_at IS NULL ORDER BY seq_in_session, message_id",
                        (session_id,))
            names = [d.name for d in cur.description]
            return [dict(zip(names, row)) for row in cur.fetchall()]

    def count(self) -> int:
        with self._cursor() as cur:
            cur.execute("SELECT count(*) FROM memory.messages")
            return int(cur.fetchone()[0])

    def all_uids(self) -> set[str]:
        with self._cursor() as cur:
            cur.execute("SELECT uid FROM memory.messages")
            return {row[0] for row in cur.fetchall()}

    # ── search ──

    def search_groups(self, groups: Sequence[Sequence[SearchTerm]], *, limit: int = 5000) -> list[str]:
        """Uids of the newest rows matching any group, a group matching when all its terms do (``NOT``
        terms must not match). A word term matches a word of the text (the ``simple`` tsvector; a prefix
        term any word starting with it); a substring term matches anywhere (``pg_trgm``), so a one- or
        two-character Korean, Chinese or Japanese term matches inside a longer word. Tool names and
        tool-call arguments match as substrings either way."""
        group_sql, params = [], []
        for group in groups:
            parts = []
            for term in group:
                clause, term_params = _term_sql(term)
                parts.append(f"NOT {clause}" if term.negated else clause)
                params += term_params
            if parts:
                group_sql.append("(" + " AND ".join(parts) + ")")
        if not group_sql:
            return []
        sql = (f"SELECT uid FROM memory.messages WHERE forgotten_at IS NULL AND ({' OR '.join(group_sql)}) "
               "ORDER BY message_id DESC LIMIT %s")
        with self._cursor() as cur:
            cur.execute(sql, (*params, int(limit)))
            return [row[0] for row in cur.fetchall()]


@dataclass(frozen=True)
class SearchTerm:
    text: str
    negated: bool = False
    prefix: bool = False
    substring: bool = False


_WORD_RE = None


def _term_sql(term: SearchTerm) -> tuple[str, list[Any]]:
    import re
    global _WORD_RE
    _WORD_RE = _WORD_RE or re.compile(r"^\w+$")
    pattern = "%" + _like_escape(term.text) + "%"
    side = "(coalesce(tool_name, '') ILIKE %s OR coalesce(tool_calls::text, '') ILIKE %s)"
    if term.substring:
        return f"(coalesce(content, '') ILIKE %s OR {side})", [pattern, pattern, pattern]
    if term.prefix and _WORD_RE.match(term.text):
        return f"(tsv @@ to_tsquery('simple', %s) OR {side})", [term.text.lower() + ":*", pattern, pattern]
    return f"(tsv @@ phraseto_tsquery('simple', %s) OR {side})", [term.text, pattern, pattern]


def _like_escape(text: str) -> str:
    return text.replace("\\", "\\\\").replace("%", "\\%").replace("_", "\\_")


# ── state.db side: text-free placement rows and their text ──

PLACEMENT_TABLE = "transcript_rows"


def text_sql(field: str, prefix: str = "") -> str:
    """SQL expression reading one text column of a transcript_rows row (``prefix``: its alias + ``.``)."""
    if field not in TEXT_FIELDS:
        raise ValueError(f"{field!r} is not a message text column")
    return f"msg_text({prefix}text_uid, '{field}')"


def text_columns_sql(prefix: str = "", fields: Sequence[str] = TEXT_FIELDS) -> str:
    """``msg_text(...) AS content, ...``: the text columns of a transcript_rows SELECT, under their old names."""
    return ", ".join(f"{text_sql(field, prefix)} AS {field}" for field in fields)


def install_sqlite_functions(conn, store_of: Callable[[], MessageStore]) -> None:
    """Register ``msg_text(text_uid, field)`` on a state.db connection."""
    def msg_text(uid, field):
        return store_of().text_value(uid, field)
    conn.create_function("msg_text", 2, msg_text)


def tool_call_count(raw_tool_calls: Any) -> int:
    if not raw_tool_calls:
        return 0
    try:
        parsed = json.loads(raw_tool_calls) if isinstance(raw_tool_calls, str) else raw_tool_calls
    except ValueError:
        return 1
    return len(parsed) if isinstance(parsed, list) else 1


def _row_meta(row: dict[str, Any]) -> dict[str, Any]:
    return {field: row.get(field) for field in NATIVE_META_FIELDS}


def put_row_text(store: MessageStore, row: dict[str, Any], *, row_id: Optional[int]) -> dict[str, Any]:
    """Write *row*'s text (a message row in the old ``messages`` column shape) and return its placement
    columns: the row without its text, plus ``text_uid``, ``content_digest`` and ``tool_call_count``."""
    from agent.message_metadata import mint_uid
    message_uid = row.get("message_uid") or mint_uid()
    values = tuple(row.get(field) for field in TEXT_FIELDS)
    uid = store.put_text(
        message_uid=message_uid, values=values, role=row.get("role"), tool_name=row.get("tool_name"),
        tool_call_id=row.get("tool_call_id"), timestamp=row.get("timestamp"), session_id=row.get("session_id"),
        seq_in_session=row_id, meta={**_row_meta(row), "message_uid": message_uid})
    placement = {k: v for k, v in row.items() if k not in TEXT_FIELDS}
    placement.update(message_uid=message_uid, text_uid=uid, content_digest=content_digest(values[0]),
                     tool_call_count=tool_call_count(values[1]))
    return placement


def _next_row_id(conn) -> int:
    row = conn.execute("SELECT seq FROM sqlite_sequence WHERE name = ?", (PLACEMENT_TABLE,)).fetchone()
    high = conn.execute(f"SELECT COALESCE(MAX(id), 0) FROM {PLACEMENT_TABLE}").fetchone()[0]
    return max(int(row[0]) if row else 0, int(high)) + 1


def insert_rows(conn, store: MessageStore, rows: Sequence[dict[str, Any]]) -> list[int]:
    """Insert message rows (old ``messages`` column shape) inside the caller's state.db write; returns ids.

    The text goes to the store first, under the id the row is about to get (``seq_in_session``); the
    caller holds state.db's write lock, so that id cannot be taken in between.
    """
    ids = []
    for row in rows:
        row_id = _next_row_id(conn)
        placement = put_row_text(store, row, row_id=row_id)
        placement["id"] = row_id
        columns = list(placement)
        conn.execute(f"INSERT INTO {PLACEMENT_TABLE} ({', '.join(columns)}) VALUES ({', '.join('?' * len(columns))})",
                     [placement[c] for c in columns])
        ids.append(row_id)
    return ids


def rewrite_rows(conn, store: MessageStore, row_ids: Sequence[int], changes: dict[str, Any], *,
                 where_sql: str = "", where_params: Sequence[Any] = ()) -> int:
    """``UPDATE ... SET <changes> WHERE id = ? <where_sql>`` for rows whose changes may touch text.

    A text change writes the row's new text version and points the row at it; the old version stays until
    its session's transcript is deleted (a reader may still hold it). Returns the rows changed.
    """
    text_changes = {k: v for k, v in changes.items() if k in TEXT_FIELDS}
    plain = {k: v for k, v in changes.items() if k not in TEXT_FIELDS}
    changed = 0
    for row_id in row_ids:
        current = conn.execute(
            f"SELECT * FROM {PLACEMENT_TABLE} WHERE id = ?{where_sql}", (row_id, *where_params)).fetchone()
        if current is None:
            continue
        assignments = dict(plain)
        if text_changes:
            old = store.texts([current["text_uid"]]).get(current["text_uid"])
            row = dict(current)
            row.update({f: (old.get(f) if old else None) for f in TEXT_FIELDS})
            row.update(text_changes)
            row.update(plain)
            placement = put_row_text(store, row, row_id=row_id)
            if placement["text_uid"] != current["text_uid"]:
                assignments.update(text_uid=placement["text_uid"], content_digest=placement["content_digest"],
                                   tool_call_count=placement["tool_call_count"])
        if not assignments:
            changed += 1
            continue
        conn.execute(f"UPDATE {PLACEMENT_TABLE} SET {', '.join(f'{c} = ?' for c in assignments)} WHERE id = ?",
                     [*assignments.values(), row_id])
        changed += 1
    return changed


def delete_rows(conn, store: MessageStore, where_sql: str, params: Sequence[Any] = ()) -> int:
    """``DELETE FROM transcript_rows WHERE <where_sql>``; a session left without rows has its text forgotten
    after the commit."""
    sessions = [row[0] for row in conn.execute(
        f"SELECT DISTINCT session_id FROM {PLACEMENT_TABLE} WHERE {where_sql}", tuple(params)).fetchall()]
    count = conn.execute(f"DELETE FROM {PLACEMENT_TABLE} WHERE {where_sql}", tuple(params)).rowcount
    store.forget_sessions_after_commit(
        sid for sid in sessions
        if conn.execute(f"SELECT 1 FROM {PLACEMENT_TABLE} WHERE session_id = ? LIMIT 1", (sid,)).fetchone() is None)
    return count


def held_text_uids(conn, uids: Sequence[str]) -> set[str]:
    """The uids among *uids* some transcript row of this state.db points at."""
    held: set[str] = set()
    for start in range(0, len(uids), 500):
        chunk = list(uids[start:start + 500])
        held.update(row[0] for row in conn.execute(
            f"SELECT text_uid FROM {PLACEMENT_TABLE} WHERE text_uid IN ({', '.join('?' * len(chunk))})", chunk))
    return held


def rows_with_text(store: MessageStore, rows: Iterable[Any]) -> list[dict[str, Any]]:
    """transcript_rows rows as dicts carrying their text columns, fetched from the store in one batch."""
    rows = [dict(row) for row in rows]
    texts = store.texts(row.get("text_uid") for row in rows)
    for row in rows:
        text = texts.get(row.get("text_uid"))
        for field in TEXT_FIELDS:
            row[field] = text.get(field) if text is not None else None
    return rows
