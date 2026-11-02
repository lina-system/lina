"""Message search and read views for SessionDB.

Search runs in two steps: the memory store finds the text versions that match (``pg_trgm`` substring
match for CJK and tool payloads, the ``simple`` tsvector for words: hermes_state_store.py), then
transcript_rows narrows them to visible rows of the wanted sessions, roles and sources. Korean, Chinese
and Japanese terms of one or two characters match inside longer words, as the old cjk bigram index did.

Plain mixin for ``hermes_state.SessionDB`` (no ``__init__``/state of its own).
Must never import hermes_state (cycle); shared constants live in hermes_state_common.
"""

import logging
import re
import sqlite3
import time
from typing import Any, Collection, Optional

from agent.skill_commands import describe_skill_invocation
from hermes_state_common import _placeholders, routed_sessions_setting
from hermes_state_store import SearchTerm, text_columns_sql, text_sql

# Pre-split logger identity so log filtering/capture is unchanged.
logger = logging.getLogger("hermes_state")

# Cap on user-controlled query input before parsing.
MAX_SEARCH_QUERY_CHARS = 2_048
# Matches the store hands back per query; transcript_rows filters and pages within them.
_CANDIDATE_LIMIT = 5_000  # store rows read per page of search candidates
_OPERATORS = frozenset({"AND", "OR", "NOT", "NEAR"})
_TOKEN_RE = re.compile(r'"[^"]+"|\S+')
# Unified Ideographs, Extension A, Extension B, CJK Symbols, Hiragana, Katakana, Hangul Syllables.
_CJK_RANGES = (
    (0x4E00, 0x9FFF), (0x3400, 0x4DBF), (0x20000, 0x2A6DF), (0x3000, 0x303F), (0x3040, 0x309F),
    (0x30A0, 0x30FF), (0xAC00, 0xD7AF),
)
_SORT_KEYS = {"newest": True, "oldest": False}
# Neighbors of each hit, for the one-line context around it.
_CONTEXT_WINDOW_SQL = """WITH target AS (
    SELECT session_id, timestamp, id FROM transcript_rows WHERE id IN ({ids})
)
SELECT t.id AS match_id, m.role, """ + text_sql("content", "m.") + """ AS content
FROM target t JOIN transcript_rows m ON m.id IN (
    t.id,
    (SELECT p.id FROM transcript_rows p
     WHERE p.session_id = t.session_id AND (p.timestamp, p.id) < (t.timestamp, t.id)
     ORDER BY p.timestamp DESC, p.id DESC LIMIT 1),
    (SELECT n.id FROM transcript_rows n
     WHERE n.session_id = t.session_id AND (n.timestamp, n.id) > (t.timestamp, t.id)
     ORDER BY n.timestamp, n.id LIMIT 1)
)
ORDER BY t.id, m.timestamp, m.id"""


def _search_slow_ms() -> float:
    """``sessions.search_slow_ms`` for the served profile (default 1000; 0 logs every call)."""
    value = routed_sessions_setting("search_slow_ms", "HERMES_SEARCH_SLOW_MS")
    try:
        return 1000.0 if value is None or str(value).strip() == "" else float(value)
    except (TypeError, ValueError):
        return 1000.0


def _meta_row(conn, key: str) -> Optional[sqlite3.Row]:
    """Point-read one ``state_meta`` row (``None`` when absent)."""
    return conn.execute("SELECT value FROM state_meta WHERE key = ?", (key,)).fetchone()


def _delete_meta(conn, *keys: str) -> None:
    conn.execute(f"DELETE FROM state_meta WHERE key IN ({','.join('?' for _ in keys)})", keys)


def _is_cjk(cp: int) -> bool:
    return any(lo <= cp <= hi for lo, hi in _CJK_RANGES)


def contains_cjk(text: str) -> bool:
    return any(_is_cjk(ord(ch)) for ch in text)


def _flatten_text(decoded: Any) -> str:
    """Multimodal part list -> joined text (or the placeholder); str passes through; else ''."""
    if isinstance(decoded, list):
        parts = [p.get("text", "") for p in decoded if isinstance(p, dict) and p.get("type") == "text"]
        return " ".join(t for t in parts if t).strip() or "[multimodal content]"
    return decoded if isinstance(decoded, str) else ""


def _positive_int(name: str, value: Any) -> None:
    if isinstance(value, bool) or not isinstance(value, int):
        raise TypeError(f"{name} must be an integer")
    if value <= 0:
        raise ValueError(f"{name} must be greater than zero")


def parse_search_query(query: str) -> list[list[SearchTerm]]:
    """The query as OR-groups of AND-ed terms: ``"quoted phrases"`` stay whole, ``NOT`` negates the next
    term, a trailing ``*`` asks for a word prefix, ``AND``/``NEAR`` are implicit. A group with no positive
    term is dropped (it cannot select anything)."""
    groups: list[list[SearchTerm]] = [[]]
    negate_next = False
    for raw in _TOKEN_RE.findall(query[:MAX_SEARCH_QUERY_CHARS]):
        operator = raw.upper()
        if operator == "OR":
            if groups[-1]:
                groups.append([])
            negate_next = False
            continue
        if operator in {"AND", "NEAR"}:
            continue
        if operator == "NOT":
            negate_next = True
            continue
        phrase = raw.startswith('"') and raw.endswith('"') and len(raw) > 1
        text = raw.strip('"')
        prefix = not phrase and text.endswith("*")
        text = text.rstrip("*").strip() if not phrase else text.strip()
        if text:
            groups[-1].append(SearchTerm(text, negated=negate_next, prefix=prefix, substring=contains_cjk(text)))
        negate_next = False
    return [g for g in groups if any(not t.negated for t in g)]


def _any_term(groups: list[list[SearchTerm]]) -> Optional[list[list[SearchTerm]]]:
    """An implicit-AND query relaxed to any of its terms; ``None`` when that changes nothing or the caller
    wrote OR/NOT (exact semantics asked for)."""
    if len(groups) != 1 or any(t.negated for t in groups[0]) or len(groups[0]) < 2:
        return None
    return [[t] for t in groups[0]]


def _as_substrings(groups: list[list[SearchTerm]]) -> list[list[SearchTerm]]:
    return [[SearchTerm(t.text, negated=t.negated, prefix=t.prefix, substring=True) for t in g] for g in groups]


def _search_filter_clauses(
    where: list[str], params: list, *, include_inactive: bool, source_filter: Optional[list[str]],
    exclude_sources: Optional[list[str]], role_filter: Optional[list[str]],
    after_ts: Optional[int] = None, before_ts: Optional[int] = None) -> None:
    """Append the visibility/source/role/session-start predicates every search route shares. Live
    rows (active=1) AND compaction-archived rows (compacted=1) are discoverable; only
    rewind/undo rows (active=0, compacted=0) are hidden. ``after_ts``/``before_ts`` bound
    ``sessions.started_at`` (inclusive / exclusive) inside the query so LIMIT cannot be
    filled by out-of-window hits."""
    if not include_inactive:
        where.append("(m.active = 1 OR m.compacted = 1)")
    # display_kind="hidden" rows are model-facing scaffolding the person never saw; a hit would confuse.
    where.append("COALESCE(m.display_kind, '') <> 'hidden'")
    if source_filter is not None:
        where.append(f"s.source IN ({','.join('?' for _ in source_filter)})")
        params.extend(source_filter)
    if exclude_sources is not None:
        where.append(f"s.source NOT IN ({','.join('?' for _ in exclude_sources)})")
        params.extend(exclude_sources)
    if role_filter:
        where.append(f"m.role IN ({','.join('?' for _ in role_filter)})")
        params.extend(role_filter)
    if after_ts is not None:
        where.append("s.started_at >= ?")
        params.append(int(after_ts))
    if before_ts is not None:
        where.append("s.started_at < ?")
        params.append(int(before_ts))


def _snippet(text: str, terms: list[str], width: int = 40) -> str:
    """A window of *text* around the first term found, the term marked >>>like this<<<."""
    low = text.lower()
    hits = [(low.find(t.lower()), t) for t in terms if t]
    hits = [(pos, t) for pos, t in hits if pos >= 0]
    if not hits:
        return text[: width * 3]
    pos, term = min(hits)
    start, end = max(0, pos - width), min(len(text), pos + len(term) + width)
    return (("..." if start else "") + text[start:pos] + ">>>" + text[pos:pos + len(term)] + "<<<"
            + text[pos + len(term):end] + ("..." if end < len(text) else ""))


class SessionSearchMixin:
    """See module docstring — mixin for SessionDB (Search cluster)."""

    _SEARCH_MESSAGE_RESULT_FIELDS = (
        "id", "session_id", "role", "snippet", "timestamp", "tool_name", "source", "model", "session_started", "context"
    )

    def _search_message_fields(cls, fields: Optional[Collection[str]]) -> Optional[tuple[str, ...]]:
            """Validate and canonically order an optional result projection."""
            if fields is None:
                return None
            if isinstance(fields, str):
                raise TypeError("search fields must be a collection of field names, not a string")
            requested = set(fields)
            unknown = requested.difference(cls._SEARCH_MESSAGE_RESULT_FIELDS)
            if unknown:
                raise ValueError(f"unknown search result field(s): {', '.join(sorted(unknown))}")
            return tuple(field for field in cls._SEARCH_MESSAGE_RESULT_FIELDS if field in requested)

    # ── Read views ─────────────────────────────────────────────────────────

    def get_anchored_view(
            self, session_id: str, around_message_id: int, window: int = 5, bookend: int = 3,
            keep_roles: Optional[tuple[str, ...]] = ("user", "assistant")) -> dict[str, Any]:
            """Anchored window (``get_messages_around``) plus session bookends, so one call yields the
            goal and the resolution of a long session. ``window`` is filtered to ``keep_roles``
            (None disables) EXCEPT the anchor; ``bookend_start`` / ``bookend_end`` are the
            first/last ``bookend`` non-empty-content messages with ids strictly outside the
            window (empty when it overlaps the head/tail). Empty result when the anchor isn't
            in the session."""
            bookend = max(bookend, 0)
            primitive = self.get_messages_around(session_id, around_message_id, window=window)
            window_rows = primitive["window"]
            if not window_rows:
                return {"window": [], "messages_before": 0, "messages_after": 0, "bookend_start": [], "bookend_end": []}

            filtered_window = window_rows
            if keep_roles is not None:
                keep_set = set(keep_roles)
                filtered_window = [m for m in window_rows if m.get("id") == around_message_id or m.get("role") in keep_set]
            bookend_start_rows: list[Any] = []
            bookend_end_rows: list[Any] = []
            if bookend > 0:
                role_clause = "" if keep_roles is None else f" AND role IN ({','.join('?' for _ in keep_roles)})"
                role_params = [] if keep_roles is None else list(keep_roles)
                with self._read_ctx() as conn:
                    def _bookend(op: str, boundary_id: int, order: str):
                        return conn.execute(
                            f"SELECT *, {text_columns_sql()} FROM transcript_rows "
                            f"WHERE session_id = ? AND id {op} ?{role_clause} "
                            f"AND length({text_sql('content')}) > 0 "
                            f"ORDER BY id {order} LIMIT ?",
                            (session_id, boundary_id, *role_params, bookend),
                        ).fetchall()
                    bookend_start_rows = _bookend("<", window_rows[0]["id"], "ASC")
                    # End rows come back DESC for the LIMIT cap; flip to ASC.
                    bookend_end_rows = list(reversed(_bookend(">", window_rows[-1]["id"], "DESC")))

            def _hydrate(row) -> dict[str, Any]:
                return self._row_to_message_dict(row, warn_context="get_anchored_view", summary_flag=False)
            return {
                "window": filtered_window, "messages_before": primitive["messages_before"],
                "messages_after": primitive["messages_after"],
                "bookend_start": [_hydrate(r) for r in bookend_start_rows],
                "bookend_end": [_hydrate(r) for r in bookend_end_rows],
            }

    def list_recent_user_messages(
            self, session_id: str, limit: int = 20, include_inactive: bool = False) -> list[dict[str, Any]]:
            """The *limit* most-recent real user turns, newest first, as ``{id, timestamp, preview}``
            (80 chars, whitespace collapsed); used by /rewind and ``/undo [N]``. Bookkeeping rows
            (``display_kind`` set) are excluded. Legacy compaction handoffs are role='user' rows
            with NO display_kind — invisible to SQL — so fetch with headroom and drop them in the
            decode loop; otherwise ``/undo N`` pairs an in-memory count that excludes handoffs
            with a DB pick that includes them."""
            active_clause = "" if include_inactive else " AND active = 1"
            # A /steer row is typed for the renderer but is human input: keep it so the DB pick agrees
            # with the in-memory user_originated_turn_view count.
            display_clause = " AND (display_kind IS NULL OR display_kind = '' OR display_kind = 'steer')"
            with self._read_ctx() as conn:
                rows = conn.execute(
                    f"SELECT id, timestamp, {text_sql('content')} AS content FROM transcript_rows "
                    "WHERE session_id = ? AND role = 'user'"
                    f"{active_clause}{display_clause} "
                    "ORDER BY id DESC LIMIT ?",
                    (session_id, int(limit) * 2 + 5),
                ).fetchall()
            from agent.context_compressor import ContextCompressor
            result: list[dict[str, Any]] = []
            for row in rows:
                if len(result) >= int(limit):
                    break
                decoded = self._decode_content(row["content"])
                if ContextCompressor._is_context_summary_content(decoded):
                    continue  # compaction handoff — never a user-originated turn
                if isinstance(decoded, str):  # a /skill turn embeds the whole skill body; show what was typed
                    preview = describe_skill_invocation(decoded) or decoded
                else:
                    preview = _flatten_text(decoded)
                preview = " ".join(preview.split())
                if len(preview) > 80:
                    preview = preview[:77] + "..."
                result.append({"id": row["id"], "timestamp": row["timestamp"], "preview": preview})
            return result

    # ── search_messages ────────────────────────────────────────────────────

    @staticmethod
    def _contains_cjk(text: str) -> bool:
        return contains_cjk(text)

    def search_messages(
        self, query: str, source_filter: list[str] | None = None, exclude_sources: list[str] | None = None,
        role_filter: list[str] | None = None, limit: int = 20, offset: int = 0, sort: str | None = None,
        include_inactive: bool = False, fields: Optional[Collection[str]] = None,
        after_ts: Optional[int] = None, before_ts: Optional[int] = None,
    ) -> list[dict[str, Any]]:
        """:meth:`_search_messages_impl` plus one log line per slow search. Threshold
        ``sessions.search_slow_ms`` (default 1000; 0 logs every call)."""
        started = time.time()
        rows = None
        try:
            rows = self._search_messages_impl(
                query, source_filter=source_filter, exclude_sources=exclude_sources, role_filter=role_filter,
                limit=limit, offset=offset, sort=sort, include_inactive=include_inactive, fields=fields,
                after_ts=after_ts, before_ts=before_ts)
            return rows
        finally:
            elapsed_ms = (time.time() - started) * 1000.0
            if elapsed_ms >= _search_slow_ms():
                logger.info("slow session search: elapsed=%.0fms rows=%s query=%r",
                            elapsed_ms, len(rows) if rows is not None else "err", query[:200])

    def _search_messages_impl(
        self, query: str, source_filter: list[str] | None = None, exclude_sources: list[str] | None = None,
        role_filter: list[str] | None = None, limit: int = 20, offset: int = 0, sort: str | None = None,
        include_inactive: bool = False, fields: Optional[Collection[str]] = None,
        after_ts: Optional[int] = None, before_ts: Optional[int] = None,
    ) -> list[dict[str, Any]]:
        """Search across session messages (keywords, ``"phrases"``, AND/OR/NOT, ``prefix*``). Returns a
        snippet + session metadata + 1-message context per hit; ``fields`` selects a projection.
        ``sort``: "newest"/"oldest" by timestamp; anything else is newest first. Rewound rows
        (``active=0, compacted=0``) are excluded by default; compaction-archived rows ARE included;
        ``include_inactive`` = every row. ``after_ts``/``before_ts`` bound ``sessions.started_at``.
        A word query that finds nothing is retried as substrings (a Latin word glued to CJK text), then
        with any of its terms (not when every term is CJK: those already match as substrings)."""
        result_fields = self._search_message_fields(fields)
        if not query or not query.strip():
            return []
        groups = parse_search_query(query)
        if not groups:
            return []
        filters = dict(include_inactive=include_inactive, source_filter=source_filter,
                       exclude_sources=exclude_sources, role_filter=role_filter,
                       after_ts=after_ts, before_ts=before_ts)
        newest = _SORT_KEYS.get(sort.strip().lower() if isinstance(sort, str) else "", True)
        attempts = [groups]
        if any(not t.substring for g in groups for t in g):
            attempts.append(_as_substrings(groups))
        relaxed = None if all(t.substring for g in groups for t in g) else _any_term(groups)
        if relaxed is not None:
            attempts.append(relaxed)
        matches: list[dict[str, Any]] = []
        for attempt in attempts:
            matches = self._matching_rows(attempt, limit=limit, offset=offset, newest=newest, **filters)
            if matches:
                break
        terms = [t.text for g in groups for t in g if not t.negated]
        return self._finalize_search_matches(matches, terms, result_fields=result_fields)

    def _matching_rows(self, groups: list[list[SearchTerm]], *, limit: int, offset: int, newest: bool,
                       **filters) -> list[dict[str, Any]]:
        # Every matching text version is a candidate: the source, session-date and visibility filters live in
        # state.db, so the store is read a page at a time and each page filtered here, keeping the best
        # offset+limit rows. A single capped read would hide older matches behind newer filtered-out ones.
        keep = max(offset, 0) + max(limit, 0)
        rows: list[dict[str, Any]] = []
        before_id = None
        while keep:
            page = self.message_store.search_groups(groups, limit=_CANDIDATE_LIMIT, before_id=before_id)
            if not page:
                break
            before_id = page[-1][0]
            uids = [uid for _, uid in page]
            for start in range(0, len(uids), 900):
                chunk = uids[start:start + 900]
                where = [f"m.text_uid IN ({_placeholders(chunk)})"]
                params: list = list(chunk)
                _search_filter_clauses(where, params, **filters)
                rows.extend(dict(row) for row in self._read_all(
                    "SELECT m.id, m.session_id, m.role, m.text_uid, m.timestamp, m.tool_name, s.source, s.model, "
                    "s.started_at AS session_started FROM transcript_rows m JOIN sessions s ON s.id = m.session_id "
                    f"WHERE {' AND '.join(where)}", params))
            rows.sort(key=lambda r: (r["timestamp"] or 0, r["id"]), reverse=newest)
            del rows[keep:]
            if len(page) < _CANDIDATE_LIMIT:
                break
        return rows[offset:offset + limit]

    def _finalize_search_matches(self, matches: list[dict[str, Any]], terms: list[str],
                                 result_fields: Optional[Collection[str]] = None) -> list[dict[str, Any]]:
        """Snippets from the store, plus the neighboring messages when context is requested."""
        texts = self.message_store.texts(m["text_uid"] for m in matches)
        for match in matches:
            row = texts.get(match.pop("text_uid"))
            body = _flatten_text(self._decode_content(row.get("content"))) if row else ""
            if not body and row is not None:
                body = " ".join(str(v) for v in (match.get("tool_name"), row.get("tool_calls")) if v)
            match["snippet"] = _snippet(body, terms)
        if result_fields is None or "context" in result_fields:
            for start in range(0, len(matches), 500):
                batch = matches[start:start + 500]
                contexts: dict[Any, list] = {match["id"]: [] for match in batch}
                if contexts:
                    with self._read_ctx() as conn:
                        for row in conn.execute(_CONTEXT_WINDOW_SQL.format(ids=_placeholders(contexts)), list(contexts)):
                            contexts[row["match_id"]].append(row)
                for match in batch:
                    match["context"] = [
                        {"role": row["role"], "content": _flatten_text(self._decode_content(row["content"]))[:200]}
                        for row in contexts.get(match["id"], [])]
        if result_fields is not None:
            matches = [{field: match[field] for field in result_fields if field in match} for match in matches]
        return matches

    def search_sessions_by_id(
            self, query: str, limit: int = 20, include_archived: bool = True, source: str | None = None,
            sources: list[str] | None = None, exclude_sources: list[str] | None = None) -> list[dict[str, Any]]:
            """Search surfaced sessions by exact/prefix/substring session id. Also matches
            ``_lineage_root_id`` so an old compression root id resolves to the live continuation."""
            needle = (query or "").strip().lower()
            if not needle or limit <= 0:
                return []
            # list_sessions_rich pushes the id LIKE filter (own id + forward compression
            # chain) into SQL; over-fetch so the in-Python ranking has candidates.
            candidates = self.list_sessions_rich(
                source=source, sources=sources, exclude_sources=exclude_sources, limit=max(limit * 4, limit),
                offset=0, include_archived=include_archived, order_by_last_active=True, id_query=needle)

            def score(row: dict[str, Any]) -> int:
                normalized = [v.lower() for v in (str(row.get("id") or ""), str(row.get("_lineage_root_id") or "")) if v]
                if any(value == needle for value in normalized):
                    return 0
                return 1 if any(value.startswith(needle) for value in normalized) else 2
            ranked = sorted(enumerate(candidates), key=lambda item: (score(item[1]), item[0]))
            return [row for _, row in ranked[:limit]]
