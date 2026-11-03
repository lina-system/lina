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
from contextlib import closing, contextmanager
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


def _row_payload_digest(row: dict[str, Any]) -> str:
    """The payload digest of an exported row, recomputed from its content and native columns."""
    return payload_digest(_values_from_row(row.get("content"), row.get("native") or {}))


def check_text_row(row: Any) -> Optional[str]:
    """Why an exported text row is damaged, or None: its content must match content_sha256 (the plain-content
    column) and its whole payload its payload_sha256 (the row's identity)."""
    if not isinstance(row, dict):
        return "not a row"
    if _sha256(row.get("content") or "") != row.get("content_sha256"):
        return "its content does not match its content_sha256"
    claimed = row.get("payload_sha256") or (row.get("native") or {}).get("payload_sha256")
    if claimed != _row_payload_digest(row):
        return "its payload does not match its payload_sha256"
    return None


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


def resolve_owner_dsn() -> str:
    """The store owner's DSN (role ``lina``, password ``LINA_STORE_PASSWORD`` in store.env): the one role row-level
    security lets move rows from one agent to another."""
    from hermes_cli.store import read_env, store_dir
    values = read_env(store_dir() / "store.env")
    password = values.get("LINA_STORE_PASSWORD")
    if not password:
        raise StoreUnavailableError(
            "the memory store is not set up (no store.env with LINA_STORE_PASSWORD); run `lina store install`")
    from psycopg.conninfo import make_conninfo
    return make_conninfo(host="127.0.0.1", port=values.get("LINA_STORE_PORT", "54320"), dbname="lina",
                         user="lina", password=password, connect_timeout="10")


def _owner_connection():
    import psycopg
    try:
        return psycopg.connect(resolve_owner_dsn())
    except psycopg.OperationalError as exc:
        raise StoreUnavailableError(f"cannot reach the memory store: {exc}".strip()) from exc


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

# A text row re-used after this long gets its observed_at refreshed; collection keeps rows younger than
# MIN_COLLECT_DAYS whatever it is asked, so a row a writer re-uses without touching is never a candidate.
_TOUCH_AFTER_HOURS = 24
MIN_COLLECT_DAYS = 2.0
DEFAULT_COLLECT_DAYS = 30.0
TEXT_EXPORT_NAME = "memory-messages.jsonl"
_EXPORT_COLUMNS = ("uid", "source", "session_id", "conversation_id", "seq_in_session", "role", "content", "tool_name",
                   "tool_call_id", "tool_calls", "scope", "native", "content_sha256")


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
        # Text rows the open write transaction wrote or read: published to TEXT_CACHE once it commits,
        # dropped on rollback (a cached uid whose insert rolled back would make a retry skip the insert).
        self._staged: dict[str, TextRow] = {}
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
        self._staged = {}
        try:
            yield
            if self._write_conn is not None and not self._write_conn.closed:
                self._write_conn.commit()
            for row in self._staged.values():
                TEXT_CACHE.put(self.agent_id, row)
        except BaseException:
            if self._write_conn is not None and not self._write_conn.closed:
                try:
                    self._write_conn.rollback()
                except Exception:
                    logger.debug("memory store rollback failed", exc_info=True)
                    self._discard_writer()
            raise
        finally:
            self._staged = {}
            self._write_owner, self._write_depth = None, 0

    def _writer_cursor(self):
        if self._write_owner != threading.get_ident():
            raise RuntimeError("memory store writes run inside MessageStore.write()")
        if self._write_conn is None or self._write_conn.closed or self._write_conn.broken:
            self._write_conn = self._connect(autocommit=False)
        return self._write_conn.cursor()

    def _in_write(self) -> bool:
        return self._write_owner == threading.get_ident()

    def _cached(self, uid: str) -> Optional[TextRow]:
        """A text row this process knows is committed, or that this thread's open write transaction holds."""
        if self._in_write() and uid in self._staged:
            return self._staged[uid]
        return TEXT_CACHE.get(self.agent_id, uid)

    def _remember(self, row: TextRow) -> None:
        """Cache a row read or written now: at once outside a write, after the commit inside one."""
        if self._in_write():
            self._staged[row.uid] = row
        else:
            TEXT_CACHE.put(self.agent_id, row)

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
        known = next((uid for uid in (base, versioned)
                      if (cached := self._cached(uid)) is not None and cached.values == values), None)
        text = _plain_text(values[0])
        row = (SOURCE_AGENT, self.agent_id, session_id, seq_in_session, role, text, _plain_text(tool_name),
               _plain_text(tool_call_id), _jsonb_tool_calls(values[1]), timestamp,
               json.dumps(_native(values, meta, digest)), _sha256(text or ""))
        # The cache may skip the read, never the write: another process's collection may have removed a row
        # this process still caches. A conflicting row is locked until this transaction commits, and one not
        # re-used for a day gets its observed_at refreshed, so collect_unreferenced() cannot take it meanwhile.
        insert = ("INSERT INTO memory.messages (uid, source, agent_id, session_id, seq_in_session, role, content,"
                  " tool_name, tool_call_id, tool_calls, ts, native, content_sha256)"
                  " VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s::jsonb, to_timestamp(%s), %s::jsonb, %s)"
                  " ON CONFLICT (uid) DO UPDATE SET observed_at = now()"
                  f" WHERE memory.messages.observed_at < now() - interval '{_TOUCH_AFTER_HOURS} hours'"
                  " RETURNING native->>'payload_sha256'")
        with self._writer_cursor() as cur:
            if known is not None:
                cur.execute(insert, (known, *row))
                cur.fetchall()
                uid = known
            else:
                cur.execute(insert, (base, *row))
                returned = cur.fetchone()
                if returned is None:  # a row written within the day: locked, not touched; read its version
                    cur.execute("SELECT native->>'payload_sha256' FROM memory.messages WHERE uid = %s", (base,))
                    returned = cur.fetchone()
                if returned is not None and returned[0] == digest:
                    uid = base
                else:
                    cur.execute(insert, (versioned, *row))
                    cur.fetchall()
                    uid = versioned
        self._remember(TextRow(uid, values))
        return uid

    def texts(self, uids: Iterable[Optional[str]]) -> dict[str, TextRow]:
        """Text rows by uid; a uid with no row is left out."""
        wanted = list(dict.fromkeys(u for u in uids if u))
        found: dict[str, TextRow] = {}
        missing = []
        for uid in wanted:
            row = self._cached(uid)
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
                    self._remember(row)
                    found[uid] = row
        return found

    def text_value(self, uid: Optional[str], field: str) -> Any:
        if not uid:
            return None
        row = self.texts([uid]).get(uid)
        return None if row is None else row.get(field)

    def session_messages(self, session_id: str) -> list[dict[str, Any]]:
        """The rows first written by *session_id*, in write order: the shape the other engines (LCM,
        Honcho) read, without Hermes's placement state."""
        with self._cursor() as cur:
            cur.execute("SELECT uid, role, content, tool_name, tool_call_id, ts, seq_in_session FROM memory.messages "
                        "WHERE session_id = %s AND forgotten_at IS NULL ORDER BY seq_in_session, message_id",
                        (session_id,))
            names = [d.name for d in cur.description]
            return [dict(zip(names, row)) for row in cur.fetchall()]

    def set_conversation(self, uids: Iterable[str], conversation_id: str) -> int:
        """Fill ``conversation_id`` (lina#20) on these rows (``base_uid`` uids), every text version of each, where it
        is still null. The one change a written row takes: the field is null until identity resolves it. The match
        is served by the partial index messages_unfilled_base (migration 0006), never a scan of memory.messages."""
        bases = [uid for uid in dict.fromkeys(uids) if uid]
        if not bases or not conversation_id:
            return 0
        with self._cursor() as cur:
            cur.execute("UPDATE memory.messages SET conversation_id = %s WHERE split_part(uid, '#', 1) = ANY(%s)"
                        " AND conversation_id IS NULL", (conversation_id, bases))
            return cur.rowcount

    def collect_unreferenced(self, still_held: Callable[[list[str]], set[str]], *,
                             older_than_days: float = DEFAULT_COLLECT_DAYS) -> int:
        """Delete this agent's text rows no transcript row points at any more (*still_held* names the uids its
        state.db still references), that Honcho does not hold, and that nobody wrote or re-used for *older_than_days* (at least
        MIN_COLLECT_DAYS): a snapshot or backup taken within that window restores with its text. Only agent turns
        (``source = 'agent'``, what ``put_text`` writes) are collected: state.db holds only the rows it wrote, and
        other sources (external messages, ...) have no transcript placement and keep their own lifecycle.
        Explicitly forgotten rows (``forgotten_at``) are left to the forget path. Returns the rows deleted."""
        if self._in_write():
            raise RuntimeError("collect_unreferenced runs outside a write transaction")
        seconds = max(float(older_than_days), MIN_COLLECT_DAYS) * 86400.0
        deleted, after = 0, 0
        while True:
            with self._cursor() as cur:
                cur.execute("SELECT message_id, uid FROM memory.messages WHERE message_id > %s AND source = %s"
                            " AND NOT memory.honcho_holds_message(message_id)"
                            " AND forgotten_at IS NULL AND observed_at < now() - make_interval(secs => %s)"
                            " ORDER BY message_id LIMIT 1000", (after, SOURCE_AGENT, seconds))
                page = cur.fetchall()
            if not page:
                return deleted
            after = page[-1][0]
            uids = [uid for _, uid in page]
            doomed = sorted(set(uids) - still_held(uids))
            if doomed:
                # observed_at is checked again here: a writer that re-used a row meanwhile refreshed it (and holds
                # its lock until it commits), so the row stays.
                with self._cursor() as cur:
                    cur.execute("DELETE FROM memory.messages WHERE uid = ANY(%s) AND source = %s AND forgotten_at IS NULL"
                                " AND NOT memory.honcho_holds_message(message_id)"
                                " AND observed_at < now() - make_interval(secs => %s) RETURNING uid",
                                (doomed, SOURCE_AGENT, seconds))
                    gone = [row[0] for row in cur.fetchall()]
                TEXT_CACHE.drop(self.agent_id, gone)
                deleted += len(gone)

    def export_texts(self, uids: Iterable[Optional[str]]) -> Iterator[dict[str, Any]]:
        """The text rows of *uids* as JSON-ready dicts (``import_texts`` takes them back)."""
        wanted = sorted({u for u in uids if u})
        columns = ", ".join(_EXPORT_COLUMNS)
        for start in range(0, len(wanted), 1000):
            with self._cursor() as cur:
                cur.execute(f"SELECT {columns}, extract(epoch FROM ts) FROM memory.messages WHERE uid = ANY(%s)",
                            (wanted[start:start + 1000],))
                for record in cur.fetchall():
                    row = dict(zip(_EXPORT_COLUMNS, record))
                    row["ts"] = None if record[-1] is None else float(record[-1])
                    row["payload_sha256"] = (row.get("native") or {}).get("payload_sha256")
                    yield row

    def import_texts(self, rows: Iterable[dict[str, Any]]) -> int:
        """Write exported text rows back under this agent, keeping rows already there (``ON CONFLICT DO
        NOTHING``) in one transaction. A damaged row (``check_text_row``) fails the import before anything is
        written. Returns the rows inserted."""
        rows = list(rows)
        for row in rows:
            if (damage := check_text_row(row)) is not None:
                raise ValueError(f"message text {row.get('uid')!r}: {damage}")
        inserted = 0
        names = [c for c in _EXPORT_COLUMNS if c != "uid"]
        sql = (f"INSERT INTO memory.messages (uid, agent_id, {', '.join(names)}, ts) VALUES (%s, %s, "
               + ", ".join("%s::jsonb" if c in ("tool_calls", "native") else "%s" for c in names)
               + ", to_timestamp(%s)) ON CONFLICT (uid) DO NOTHING RETURNING uid")
        with self.write():
            for row in rows:
                values = [json.dumps(row.get(c)) if c in ("tool_calls", "native") and row.get(c) is not None
                          else row.get(c) for c in names]
                with self._writer_cursor() as cur:
                    cur.execute(sql, (row["uid"], self.agent_id, *values, row.get("ts")))
                    inserted += len(cur.fetchall())
        return inserted

    def text_uids_with_digest(self, message_uid: str, content_sha256: str) -> list[str]:
        """The uids of *message_uid*'s text versions (``agent:<p>:<uid>`` and its ``#`` versions) whose content has
        *content_sha256*, first written first: how LCM-X finds the row Hermes wrote for a message it stores."""
        base = self.base_uid(message_uid)
        with self._cursor() as cur:
            cur.execute("SELECT uid FROM memory.messages WHERE (uid = %s OR uid LIKE %s) AND content_sha256 = %s"
                        " AND forgotten_at IS NULL ORDER BY message_id", (base, _like_escape(base) + "#%", content_sha256))
            return [row[0] for row in cur.fetchall()]

    def held_by_context(self, uids: Sequence[str]) -> set[str]:
        """The uids among *uids* an LCM-X message of this agent points at (``ctx.lcm_messages``, lina#16)."""
        with self._cursor() as cur:
            cur.execute("SELECT DISTINCT text_uid FROM ctx.lcm_messages WHERE text_uid = ANY(%s)", (list(uids),))
            return {row[0] for row in cur.fetchall()}

    def count(self) -> int:
        with self._cursor() as cur:
            cur.execute("SELECT count(*) FROM memory.messages")
            return int(cur.fetchone()[0])

    def all_uids(self) -> set[str]:
        with self._cursor() as cur:
            cur.execute("SELECT uid FROM memory.messages")
            return {row[0] for row in cur.fetchall()}

    # ── search ──

    def search_groups(self, groups: Sequence[Sequence[SearchTerm]], *, limit: int = 5000,
                      before_id: Optional[int] = None, source: Optional[str] = None) -> list[tuple[int, str]]:
        """``(message_id, uid)`` of the newest *limit* rows matching any group, below *before_id* when given (the
        next page starts below the last id of this one). A group matches when all its terms do (``NOT``
        terms must not match). A word term matches a word of the text (the ``simple`` tsvector; a prefix
        term any word starting with it); a substring term matches anywhere (``pg_trgm``), so a one- or
        two-character Korean, Chinese or Japanese term matches inside a longer word. Tool names and
        tool-call arguments match as substrings either way. *source* keeps only rows of that source, before the
        limit."""
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
        below = "" if before_id is None else "message_id < %s AND "
        of_source = "" if source is None else "source = %s AND "
        sql = (f"SELECT message_id, uid FROM memory.messages WHERE {below}{of_source}forgotten_at IS NULL AND "
               f"({' OR '.join(group_sql)}) ORDER BY message_id DESC LIMIT %s")
        args = (([] if before_id is None else [int(before_id)]) + ([] if source is None else [source]) + params
                + [int(limit)])
        with self._cursor() as cur:
            cur.execute(sql, args)
            return [(int(row[0]), row[1]) for row in cur.fetchall()]

    # ── memory_search / memory_open (lina#22) ──

    def search_memory(self, query: str, *, sources: Sequence[str], limit: int = 20) -> list[MemoryRow]:
        """The rows of *sources* (``agent``, ``messages``) this agent may read that match *query*, best first.

        A row matches when its words match (the ``simple`` tsvector) or when one of the query's terms is a
        substring of its text (``pg_trgm``), so a two-character Korean term hits inside a longer word. Rank: the
        tsvector rank plus the share of terms found as substrings. Agent rows are its turns (user and assistant
        text, no tool output); an external message edited later counts once, as its newest version. Forgotten
        rows are left out. Row-level security decides which rows are this agent's to read."""
        terms = list(dict.fromkeys(t for t in query.split() if t))[:_MEMORY_TERMS]
        if not terms or not sources:
            return []
        patterns = ["%" + _like_escape(term) + "%" for term in terms]
        hit_sum = " + ".join(["(content ILIKE %s)::int"] * len(patterns))
        any_hit = " OR ".join(["content ILIKE %s"] * len(patterns))
        sql = (
            f"SELECT {_MEMORY_FIELDS} FROM ("
            f" SELECT DISTINCT ON (split_part(uid, '#', 1)) {_MEMORY_COLUMNS},"
            f" ts_rank(tsv, plainto_tsquery('simple', %s)) + ({hit_sum})::float / %s AS score"
            " FROM memory.messages"
            " WHERE forgotten_at IS NULL AND content IS NOT NULL AND source = ANY(%s)"
            " AND (source <> 'agent' OR role IN ('user', 'assistant'))"
            f" AND (tsv @@ plainto_tsquery('simple', %s) OR {any_hit})"
            " ORDER BY split_part(uid, '#', 1), message_id DESC"
            ") hits ORDER BY score DESC, at DESC LIMIT %s")
        args = [query, *patterns, len(patterns), list(sources), query, *patterns, int(limit)]
        with self._cursor() as cur:
            cur.execute(sql, args)
            return [_memory_row(row) for row in cur.fetchall()]

    def has_memory_vectors(self, *, model: str, sources: Sequence[str]) -> bool:
        with self._cursor() as cur:
            cur.execute("SELECT 1 FROM vec.embeddings e JOIN vec.chunks c ON c.chunk_id = e.item_id"
                        " WHERE e.kind = 'chunk' AND e.model = %s AND c.source = ANY(%s) LIMIT 1", (model, list(sources)))
            return cur.fetchone() is not None

    def search_memory_vectors(self, embedding: Sequence[float], *, model: str, sources: Sequence[str],
                              limit: int = 20) -> list[MemoryRow]:
        """The rows of the embedded chunks (``vec.chunks``, kind ``chunk``) of *sources* nearest *embedding*, nearest
        first, each row once; empty when nothing of *sources* is embedded with *model*."""
        vector = "[" + ",".join(repr(float(v)) for v in embedding) + "]"
        with self._cursor() as cur:
            cur.execute(
                "SELECT c.message_ids FROM vec.embeddings e JOIN vec.chunks c ON c.chunk_id = e.item_id"
                " WHERE e.kind = 'chunk' AND e.model = %s AND c.source = ANY(%s)"
                " ORDER BY e.embedding <=> %s::vector LIMIT %s", (model, list(sources), vector, int(limit)))
            ordered = list(dict.fromkeys(int(i) for row in cur.fetchall() for i in row[0]))
            if not ordered:
                return []
            cur.execute(f"SELECT {_MEMORY_COLUMNS} FROM memory.messages WHERE message_id = ANY(%s)"
                        " AND forgotten_at IS NULL AND content IS NOT NULL", (ordered,))
            rows = {row.message_id: row for row in map(_memory_row, cur.fetchall())}
        return [rows[i] for i in ordered if i in rows][:int(limit)]

    def open_memory(self, uid: str) -> Optional[MemoryRow]:
        """The row *uid*, when this agent may read it (forgotten rows too: ``forgotten`` says so and the text is
        withheld); None when there is no such row or it is another agent's."""
        with self._cursor() as cur:
            cur.execute(f"SELECT {_MEMORY_COLUMNS} FROM memory.messages WHERE uid = %s", (uid,))
            row = cur.fetchone()
        return None if row is None else _memory_row(row)


_MEMORY_TERMS = 8
_MEMORY_COLUMNS = ("message_id, uid, source, role, author, agent_id, conversation_id, session_id,"
                   " coalesce(ts, observed_at) AS at, content, forgotten_at IS NOT NULL AS forgotten")
_MEMORY_FIELDS = ("message_id, uid, source, role, author, agent_id, conversation_id, session_id, at, content,"
                  " forgotten")


@dataclass(frozen=True)
class MemoryRow:
    """One ``memory.messages`` row as memory_search and memory_open show it."""
    message_id: int
    uid: str
    source: str
    role: Optional[str]
    author: Optional[str]
    agent_id: Optional[str]
    conversation_id: Optional[str]
    session_id: Optional[str]
    at: Any  # datetime
    content: Optional[str]
    forgotten: bool


def _memory_row(row: Sequence[Any]) -> MemoryRow:
    values = list(row[:11])
    if values[10]:
        values[9] = None
    return MemoryRow(*values)


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
    """``DELETE FROM transcript_rows WHERE <where_sql>``. The text stays in the store: rows are immutable and
    shared (compaction copies, branches, snapshots), and ``MessageStore.collect_unreferenced`` reclaims the
    ones nothing references after a grace period."""
    del store  # kept in the signature: every placement write goes through the store
    return conn.execute(f"DELETE FROM {PLACEMENT_TABLE} WHERE {where_sql}", tuple(params)).rowcount


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


def state_db_has_text(db_path: Path) -> bool:
    """Whether *db_path*'s transcript points at any message text: only then does moving or restoring it need
    the store."""
    return bool(Path(db_path).exists() and _state_db_text_uids(db_path))


def _state_db_text_uids(db_path: Path) -> list[str]:
    """The text uids *db_path*'s transcript references; none for a state.db without text_uid placements, or for a
    damaged image (the restore's own integrity check refuses that one)."""
    import sqlite3
    try:
        with closing(sqlite3.connect(f"{Path(db_path).resolve().as_uri()}?mode=ro", uri=True)) as conn:
            columns = {row[1] for row in conn.execute(f"PRAGMA table_info({PLACEMENT_TABLE})")}
            if "text_uid" not in columns:
                return []
            return [row[0] for row in conn.execute(
                f"SELECT DISTINCT text_uid FROM {PLACEMENT_TABLE} WHERE text_uid IS NOT NULL")]
    except sqlite3.DatabaseError:
        return []


def export_state_db_texts(db_path: Path, out_path: Path, *, dsn: Optional[str] = None) -> int:
    """Write the text rows *db_path*'s transcript points at to *out_path* (JSONL, one row per line, keyed by
    uid with its content_sha256): a backup or snapshot of state.db carries its message text. Returns the rows."""
    uids = _state_db_text_uids(db_path)
    store = MessageStore(agent_id_for(db_path), dsn=dsn)
    count = 0
    try:
        with open(out_path, "w", encoding="utf-8") as out:
            for row in store.export_texts(uids):
                out.write(json.dumps(row, ensure_ascii=False) + "\n")
                count += 1
    finally:
        store.close()
    return count


class MessageTextRestoreError(ValueError):
    """A restored state.db points at message text the store cannot give its agent."""


def _remapped_uid(uid: str, agent: str) -> str:
    """``agent:<other>:<rest>`` as ``agent:<agent>:<rest>``; uids of another shape are left alone."""
    source, _, rest = uid.partition(":")
    owner, sep, tail = rest.partition(":")
    if source != SOURCE_AGENT or not sep or owner == agent:
        return uid
    return f"{SOURCE_AGENT}:{agent}:{tail}"


def _remap_state_db_uids(db_path: Path, agent: str) -> list[str]:
    """Point *db_path*'s transcript rows at *agent*'s uids (an archive restored under another profile still
    names its old agent); returns the distinct text uids it then references."""
    import sqlite3
    conn = sqlite3.connect(Path(db_path), timeout=30)
    try:
        if conn.execute("SELECT 1 FROM sqlite_master WHERE type='table' AND name=?", (PLACEMENT_TABLE,)).fetchone() is None:
            return []
        uids = [row[0] for row in conn.execute(
            f"SELECT DISTINCT text_uid FROM {PLACEMENT_TABLE} WHERE text_uid IS NOT NULL")]
        moved = [(new, old) for old in uids if (new := _remapped_uid(old, agent)) != old]
        if moved:
            with conn:
                conn.executemany(f"UPDATE {PLACEMENT_TABLE} SET text_uid = ? WHERE text_uid = ?", moved)
        return sorted({_remapped_uid(uid, agent) for uid in uids})
    finally:
        conn.close()


def _sidecar_rows(sidecar: Optional[Path], agent: str) -> list[dict[str, Any]]:
    """A backup's text rows, each checked (``check_text_row``) and renamed to *agent*'s uid."""
    if sidecar is None:
        return []
    try:
        with open(sidecar, encoding="utf-8-sig") as src:
            rows = [json.loads(line) for line in src if line.strip()]
    except (OSError, ValueError) as exc:
        raise MessageTextRestoreError(f"cannot read the backup's message text {sidecar.name}: {exc}") from exc
    for row in rows:
        if (damage := check_text_row(row)) is not None:
            raise MessageTextRestoreError(
                f"message text {row.get('uid') if isinstance(row, dict) else row!r} in {sidecar.name}: {damage}; "
                "the backup is damaged")
        row["uid"] = _remapped_uid(row["uid"], agent)
    return rows


def _stage_with_uids(incoming_db: Path, uids: dict[str, str], workdir: Path) -> Path:
    """A copy of *incoming_db* in *workdir* whose text_uids are mapped through *uids*; the incoming file is never
    changed."""
    import sqlite3
    staged = Path(workdir) / "state.db"
    with closing(sqlite3.connect(f"{Path(incoming_db).resolve().as_uri()}?mode=ro", uri=True)) as src, \
            closing(sqlite3.connect(staged)) as dst:
        src.backup(dst)
        with dst:
            dst.executemany(f"UPDATE {PLACEMENT_TABLE} SET text_uid = ? WHERE text_uid = ?",
                            [(new, old) for old, new in uids.items() if new != old])
    return staged


def _stored_payloads(store: MessageStore, uids: Iterable[str]) -> dict[str, Optional[str]]:
    """``uid -> payload_sha256`` of the rows among *uids* the store holds for *store*'s agent."""
    wanted, found = sorted(set(uids)), {}
    for start in range(0, len(wanted), 1000):
        with store._cursor() as cur:
            cur.execute("SELECT uid, native->>'payload_sha256' FROM memory.messages WHERE uid = ANY(%s)",
                        (wanted[start:start + 1000],))
            found.update(cur.fetchall())
    return found


def _refuse_other_payloads(rows: list[dict[str, Any]], found: dict[str, Optional[str]]) -> None:
    """MessageTextRestoreError naming the first archived row whose uid the destination already holds with another
    payload. Rows are the same text only when their whole payload digest is: equal content with other tool calls
    or API content is a different message, and keeping either one silently would change a transcript."""
    for row in rows:
        if row["uid"] in found and found[row["uid"]] != _row_payload_digest(row):
            raise MessageTextRestoreError(
                f"message text {row['uid']} is already in the store with another payload than the backup's "
                "(same uid, different text, tool calls or API content). Nothing was restored.")


def stage_text_restore(incoming_db: Path, destination_db: Path, sidecar: Optional[Path], workdir: Path, *,
                       dsn: Optional[str] = None) -> Path:
    """Get the message text of a state.db that is about to replace *destination_db* into the store, before the
    caller replaces anything. Returns the file to publish: *incoming_db*, or a copy in *workdir* whose text_uids
    name the destination's agent (a backup restored under another profile).

    1. Validate, changing nothing: *sidecar* (the backup's JSONL, or None) is read and every row checked
       (``check_text_row``); every text_uid *incoming_db* references must be in it or already readable by the
       destination agent, and a uid the destination already holds must hold the same full payload (payload_sha256;
       content_sha256 alone never decides it).
    2. Write the rows (ON CONFLICT DO NOTHING, so a re-run changes nothing) and refresh observed_at of every
       referenced row in one transaction, so the collection cannot take one before the caller publishes.
    Any failure raises MessageTextRestoreError with nothing written. After this returns, a failure of the caller's
    swap leaves only extra immutable rows here, which ``collect_unreferenced`` reclaims after its grace period."""
    import psycopg
    agent = agent_id_for(destination_db)
    rows = _sidecar_rows(sidecar, agent)
    raw = _state_db_text_uids(incoming_db)
    if not raw:  # no text to bring back: the restore needs no store
        return incoming_db
    uid_map = {uid: _remapped_uid(uid, agent) for uid in raw}
    store = MessageStore(agent, dsn=dsn)
    try:
        found = _stored_payloads(store, [*uid_map.values(), *(row["uid"] for row in rows)])
        _refuse_other_payloads(rows, found)
        referenced = sorted(set(uid_map.values()))
        archived = {row["uid"] for row in rows}
        missing = [uid for uid in referenced if uid not in archived and uid not in found]
        if missing:
            raise MessageTextRestoreError(
                f"{destination_db}: {len(missing)} message text(s) its transcript references are in neither the "
                f"backup nor the store for agent {agent!r} (first: {missing[0]}). Nothing was restored.")
        staged = _stage_with_uids(incoming_db, uid_map, workdir) if any(
            new != uid for uid, new in uid_map.items()) else incoming_db
        with store.write():
            store.import_texts(rows)
            touched: set[str] = set()
            for start in range(0, len(referenced), 1000):
                with store._writer_cursor() as cur:
                    cur.execute("UPDATE memory.messages SET observed_at = now() WHERE uid = ANY(%s) RETURNING uid",
                                (referenced[start:start + 1000],))
                    touched.update(row[0] for row in cur.fetchall())
            if len(touched) != len(referenced):
                raise MessageTextRestoreError(
                    f"{destination_db}: {len(referenced) - len(touched)} message text(s) vanished while restoring. "
                    "Nothing was restored.")
    except (StoreUnavailableError, psycopg.Error) as exc:
        raise MessageTextRestoreError(f"{destination_db}: cannot restore its message text: {exc}. "
                                      "Nothing was restored.") from exc
    finally:
        store.close()
    return staged

def _agent_prefixes(old_agent: str, new_agent: str) -> tuple[str, str]:
    return f"{SOURCE_AGENT}:{old_agent}:", f"{SOURCE_AGENT}:{new_agent}:"


def check_agent_move(old_agent: str, new_agent: str) -> None:
    """Validate a move of *old_agent*'s text to *new_agent*, changing nothing: the owner role connects
    (StoreUnavailableError otherwise) and no new uid is held by different text (MessageTextRestoreError)."""
    import psycopg
    old_prefix, new_prefix = _agent_prefixes(old_agent, new_agent)
    conn = _owner_connection()
    try:
        clash = conn.execute(
            "SELECT count(*), min(n.uid) FROM memory.messages o JOIN memory.messages n ON n.uid = %s || substr(o.uid, %s)"
            " WHERE o.agent_id = %s AND starts_with(o.uid, %s)"
            " AND n.native->>'payload_sha256' IS DISTINCT FROM o.native->>'payload_sha256'",
            (new_prefix, len(old_prefix) + 1, old_agent, old_prefix)).fetchone()
    except psycopg.Error as exc:
        raise StoreUnavailableError(f"cannot check the memory store: {exc}".strip()) from exc
    finally:
        conn.close()
    if clash[0]:
        raise MessageTextRestoreError(
            f"{clash[0]} message text(s) of {old_agent!r} would take uids that other text of {new_agent!r} already "
            f"holds (first: {clash[1]}); nothing was moved")


def _follow_text_owner(cur, from_agent: str, to_agent: str) -> None:
    """Give *to_agent* the vec.chunks rows (and their kind ``chunk`` vectors) of *from_agent* whose message now
    belongs to *to_agent*: embedded text follows its memory.messages row. A store from before migration 0003 has
    no vec tables and nothing to move."""
    cur.execute("SELECT to_regclass('vec.chunks') IS NOT NULL")
    if not cur.fetchone()[0]:
        return
    cur.execute("UPDATE vec.chunks c SET agent_id = %s FROM memory.messages m"
                " WHERE m.message_id = c.message_ids[1] AND c.agent_id = %s AND m.agent_id = %s",
                (to_agent, from_agent, to_agent))
    cur.execute("UPDATE vec.embeddings e SET agent_id = %s FROM vec.chunks c"
                " WHERE e.kind = 'chunk' AND e.item_id = c.chunk_id AND e.agent_id = %s AND c.agent_id = %s",
                (to_agent, from_agent, to_agent))


def move_agent_rows(old_agent: str, new_agent: str) -> list[str]:
    """Hand *old_agent*'s text rows to *new_agent* in one transaction (as the owner role: row-level security keeps
    lina_app inside one agent): agent_id and the uid prefix ``agent:<old>:`` -> ``agent:<new>:``. A row whose new
    uid already holds the same text stays where it is (its copy is there); one held by other text fails the move.
    The embedded text and vectors of the moved rows (vec.chunks, kind ``chunk``) move with them. Returns the new uids, so ``return_agent_rows`` can undo exactly this move."""
    import psycopg
    old_prefix, new_prefix = _agent_prefixes(old_agent, new_agent)
    conn = _owner_connection()
    try:
        with conn.transaction(), conn.cursor() as cur:
            cur.execute(
                "UPDATE memory.messages o SET agent_id = %s, uid = %s || substr(o.uid, %s)"
                " WHERE o.agent_id = %s AND starts_with(o.uid, %s) AND NOT EXISTS (SELECT 1 FROM memory.messages n"
                " WHERE n.uid = %s || substr(o.uid, %s) AND n.native->>'payload_sha256' = o.native->>'payload_sha256')"
                " RETURNING o.uid",
                (new_agent, new_prefix, len(old_prefix) + 1, old_agent, old_prefix, new_prefix, len(old_prefix) + 1))
            moved = [row[0] for row in cur.fetchall()]
            _follow_text_owner(cur, old_agent, new_agent)
            return moved
    except psycopg.Error as exc:
        raise StoreUnavailableError(
            f"could not move message text from agent {old_agent!r} to {new_agent!r}: {exc}".strip()) from exc
    finally:
        conn.close()


def return_agent_rows(uids: Sequence[str], old_agent: str, new_agent: str) -> None:
    """Undo ``move_agent_rows``: the rows it moved (*uids*) go back to *old_agent*."""
    old_prefix, new_prefix = _agent_prefixes(old_agent, new_agent)
    conn = _owner_connection()
    try:
        with conn.transaction(), conn.cursor() as cur:
            cur.execute("UPDATE memory.messages SET agent_id = %s, uid = %s || substr(uid, %s) WHERE uid = ANY(%s)",
                        (old_agent, old_prefix, len(new_prefix) + 1, list(uids)))
            _follow_text_owner(cur, new_agent, old_agent)
    finally:
        conn.close()


def move_agent_texts(db_path: Path, old_agent: str, new_agent: str) -> int:
    """``move_agent_rows``, then point *db_path*'s transcript at the moved uids (one SQLite transaction). Both
    steps leave what is already on ``agent:<new>:`` alone, so running this again repairs an interrupted rename.
    Returns the rows moved."""
    moved = move_agent_rows(old_agent, new_agent)
    if Path(db_path).exists():
        _remap_state_db_uids(db_path, new_agent)
    return len(moved)

def collect_session_db_text(db: Any, *, older_than_days: float = DEFAULT_COLLECT_DAYS) -> int:
    """``collect_unreferenced`` for a SessionDB's agent, counting references in its state.db."""
    store = db.message_store
    return store.collect_unreferenced(
        lambda uids: db._read_retrying_ioerr(lambda conn: held_text_uids(conn, uids)) | store.held_by_context(uids),
        older_than_days=older_than_days)


@dataclass(frozen=True)
class ExternalMessage:
    """Sanitized collector payload; event identity is provenance, not version identity."""
    uid: str
    content: str
    author: str
    conversation_id: str
    timestamp: str
    native: str
    digest: str
    ingest_path: str


@dataclass(frozen=True)
class ExternalBatch:
    messages: tuple[ExternalMessage, ...]
    lines: int
    offset: int
    prefix_sha256: str


def _put_external_message(conn, message: ExternalMessage) -> int:
    """Preserve every distinct version and advance only the winning current-head projection."""
    base = conn.execute("SELECT native->>'payload_sha256' FROM memory.messages WHERE uid = %s",
                        (message.uid,)).fetchone()
    uid = message.uid if base is None or base[0] == message.digest else f"{message.uid}#{message.digest[:16]}"
    written = conn.execute(
        "INSERT INTO memory.messages (uid, source, agent_id, scope, role, content, author, conversation_id,"
        " ts, native, content_sha256) VALUES (%s, 'messages', NULL, 'agents', 'user', %s, %s, %s,"
        " %s::timestamptz, %s::jsonb, %s) ON CONFLICT (uid) DO NOTHING RETURNING uid",
        (uid, message.content, message.author, message.conversation_id, message.timestamp, message.native,
         _sha256(message.content))).fetchone()
    conn.execute(
        "INSERT INTO memory.external_message_heads (uid, current_uid, ingest_path) VALUES (%s, %s, %s)"
        " ON CONFLICT (uid) DO UPDATE SET current_uid = EXCLUDED.current_uid, ingest_path = EXCLUDED.ingest_path"
        " WHERE NOT (external_message_heads.ingest_path = 'archive' AND EXCLUDED.ingest_path = 'live')"
        " AND (external_message_heads.current_uid, external_message_heads.ingest_path)"
        " IS DISTINCT FROM (EXCLUDED.current_uid, EXCLUDED.ingest_path)",
        (message.uid, uid, message.ingest_path))
    return int(written is not None)


def ingest_external_batch(spool: str, read_batch: Callable[[int, str], ExternalBatch]) -> tuple[ExternalBatch, int]:
    """Atomically consume complete lines, their immutable versions and the owner-only cursor.

    A transaction lock serializes all spools: collector uids are global, so distinct paths must not race
    for the unversioned uid or its current head. The reader verifies the prefix while this lock is held.
    """
    with _owner_connection() as conn:
        conn.execute("SELECT pg_advisory_xact_lock(4210018)")
        cursor = conn.execute(
            "SELECT byte_offset, prefix_sha256 FROM memory.external_message_cursors WHERE spool = %s",
            (spool,)).fetchone()
        offset, digest = cursor if cursor else (0, hashlib.sha256(b'').hexdigest())
        batch = read_batch(offset, digest)
        inserted = sum(_put_external_message(conn, message) for message in batch.messages)
        if batch.offset != offset:
            conn.execute(
                "INSERT INTO memory.external_message_cursors (spool, byte_offset, prefix_sha256) VALUES (%s, %s, %s)"
                " ON CONFLICT (spool) DO UPDATE SET byte_offset = EXCLUDED.byte_offset,"
                " prefix_sha256 = EXCLUDED.prefix_sha256", (spool, batch.offset, batch.prefix_sha256))
        return batch, inserted
