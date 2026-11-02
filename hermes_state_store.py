"""The message store: every message's text lives once in ``memory.messages`` (LINA's Postgres, lina#12).

This module is the only code that runs SQL against ``memory.messages``. A profile's ``state.db`` keeps
the text-free placement of each message (``transcript_rows``: which session holds it, its position,
whether it is live, compacted or rewound) and points at its text by ``text_uid``.

One message is one row. Hermes writes the same message (one ``message_uid``) several times as a
transcript is compacted or handed to a compression child; those copies share one row. A copy whose
text differs (a pruned tool result beside the archived original) is a new version:
``agent:<profile>:<message_uid>`` is the first, ``...#<digest>`` a later one. A row is never changed
after it is written, so a cached row is always current; it is deleted once no placement row of its
profile points at it.

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
from collections.abc import Iterable, Iterator, Sequence
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


def _plain_text(content: Any) -> Optional[str]:
    """The searchable text of an encoded content value (the text parts of multimodal content)."""
    if content is None:
        return None
    if isinstance(content, str) and content.startswith(CONTENT_JSON_PREFIX):
        try:
            parts = json.loads(content[len(CONTENT_JSON_PREFIX):])
        except ValueError:
            return content.replace("\x00", "")
        if isinstance(parts, str):
            return parts.replace("\x00", "")
        texts = []
        for part in parts if isinstance(parts, list) else [parts]:
            if isinstance(part, dict) and isinstance(part.get("text"), str):
                texts.append(part["text"])
            elif isinstance(part, str):
                texts.append(part)
        return "\n".join(texts).replace("\x00", "")
    if isinstance(content, str):
        try:
            content.encode("utf-8")
        except UnicodeEncodeError:
            content = content.encode("utf-8", "replace").decode("utf-8")
        return content.replace("\x00", "")
    if isinstance(content, bytes):
        return None
    return str(content)


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
    text = json.dumps(parsed, ensure_ascii=True)
    return None if "\\u0000" in text else text


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
    """The profile a state.db belongs to: ``default`` for the root's, ``<name>`` under ``profiles/``."""
    from hermes_constants import get_default_hermes_root
    home = Path(db_path).resolve().parent
    try:
        if home == get_default_hermes_root().resolve():
            return "default"
    except OSError:
        pass
    if home.parent.name == "profiles":
        return home.name
    from hermes_cli.profiles import current_profile_name
    return current_profile_name("default") or "default"


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


@dataclass(frozen=True)
class SearchHit:
    uid: str
    snippet: str


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
        try:
            yield
            if self._write_conn is not None and not self._write_conn.closed:
                self._write_conn.commit()
        except BaseException:
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
        with self._writer_cursor() as cur:
            cur.execute("SELECT uid, native->>'payload_sha256' FROM memory.messages WHERE uid = ANY(%s)",
                        ([base, versioned],))
            found = dict(cur.fetchall())
            if found.get(base) == digest:
                uid = base
            elif versioned in found:
                uid = versioned
            else:
                uid = versioned if base in found else base
                text = _plain_text(values[0])
                cur.execute(
                    "INSERT INTO memory.messages (uid, source, agent_id, session_id, seq_in_session, role, content,"
                    " tool_name, tool_call_id, tool_calls, ts, native, content_sha256)"
                    " VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s::jsonb, to_timestamp(%s), %s::jsonb, %s)"
                    " ON CONFLICT (uid) DO NOTHING",
                    (uid, SOURCE_AGENT, self.agent_id, session_id, seq_in_session, role, text,
                     _plain_text(tool_name), _plain_text(tool_call_id), _jsonb_tool_calls(values[1]),
                     timestamp, json.dumps(_native(values, meta, digest)), _sha256(text or "")))
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

    def forget(self, uids: Iterable[str]) -> int:
        """Delete text rows nothing points at any more (inside ``write()``)."""
        doomed = list(dict.fromkeys(u for u in uids if u))
        if not doomed:
            return 0
        with self._writer_cursor() as cur:
            cur.execute("DELETE FROM memory.messages WHERE uid = ANY(%s)", (doomed,))
            count = cur.rowcount
        TEXT_CACHE.drop(self.agent_id, doomed)
        return count

    def count(self) -> int:
        with self._cursor() as cur:
            cur.execute("SELECT count(*) FROM memory.messages")
            return int(cur.fetchone()[0])

    def all_uids(self) -> set[str]:
        with self._cursor() as cur:
            cur.execute("SELECT uid FROM memory.messages")
            return {row[0] for row in cur.fetchall()}

    # ── search ──

    def search(self, terms: Sequence[str], *, phrases: Sequence[str] = (), any_term: bool = False,
               uids: Optional[Sequence[str]] = None, limit: int = 2000) -> list[str]:
        """Uids of rows whose text holds every term (any term with *any_term*) and every phrase.

        Terms are matched as case-insensitive substrings (pg_trgm), so a one- or two-character Korean,
        Chinese or Japanese term matches inside a longer word, as the old cjk bigram index did. Newest
        rows first.
        """
        clauses, params = [], []
        term_clauses = []
        for term in terms:
            term_clauses.append("(content ILIKE %s OR tool_name ILIKE %s OR tool_calls::text ILIKE %s)")
            pattern = "%" + _like_escape(term) + "%"
            params += [pattern, pattern, pattern]
        if term_clauses:
            clauses.append("(" + (" OR " if any_term else " AND ").join(term_clauses) + ")")
        for phrase in phrases:
            clauses.append("content ILIKE %s")
            params.append("%" + _like_escape(phrase) + "%")
        if uids is not None:
            clauses.append("uid = ANY(%s)")
            params.append(list(uids))
        if not clauses:
            return []
        sql = (f"SELECT uid FROM memory.messages WHERE forgotten_at IS NULL AND {' AND '.join(clauses)} "
               "ORDER BY message_id DESC LIMIT %s")
        with self._cursor() as cur:
            cur.execute(sql, (*params, int(limit)))
            return [row[0] for row in cur.fetchall()]

    def snippets(self, uids: Sequence[str], terms: Sequence[str], *, width: int = 40) -> dict[str, str]:
        """A short window of each row's text around its first matching term, the term marked >>>like this<<<."""
        rows = self.texts(uids)
        out = {}
        lowered_terms = [t.lower() for t in terms if t]
        for uid, row in rows.items():
            text = _plain_text(row.get("content")) or ""
            low = text.lower()
            hit = min((p for p in (low.find(t) for t in lowered_terms) if p >= 0), default=-1)
            if hit < 0:
                out[uid] = text[: width * 2]
                continue
            term = next(t for t in lowered_terms if low.find(t) == hit)
            start = max(0, hit - width)
            end = min(len(text), hit + len(term) + width)
            out[uid] = (("..." if start else "") + text[start:hit] + ">>>" + text[hit:hit + len(term)] + "<<<"
                        + text[hit + len(term):end] + ("..." if end < len(text) else ""))
        return out


def _like_escape(text: str) -> str:
    return text.replace("\\", "\\\\").replace("%", "\\%").replace("_", "\\_")
