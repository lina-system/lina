"""LCM-X on the memory store (lina#16): a ``sqlite3.Connection`` look-alike over Postgres schema ``ctx``.

LCM-X is a vendored subtree that speaks SQLite. Its stores open their connection through
``sqlite_session.open_connection``; when the database path is routed here (``route``), that call returns a
``PgConnection`` instead, and LCM-X's own SQL runs on Postgres through ``translate``. The schema is
``services/store/migrations/0007_ctx_lcm.sql``: LCM-X's tables under their own names, each with ``agent_id``
(row-level security), so most statements reach Postgres unchanged.

Message text is never LCM's: ``messages`` is a view over the placement table ``ctx.lcm_messages`` and
``memory.messages``. An ``INSERT INTO messages`` stores the placement only; the text is the row Hermes already
wrote (found by ``message_uid`` and digest) or, when there is none, a row written once through
``hermes_state_store.MessageStore.put_text``, the one module that writes ``memory.messages``.
"""

from __future__ import annotations

import hashlib
import json
import logging
import os
import re
import sqlite3
import threading
import time
from collections.abc import Callable, Iterable, Sequence
from array import array
from dataclasses import dataclass, field
from decimal import Decimal
from functools import lru_cache
from pathlib import Path
from typing import Any, Optional

logger = logging.getLogger(__name__)

_ROUTES: dict[str, str] = {}
_ROUTES_LOCK = threading.Lock()


def _route_key(database: str | Path) -> str:
    return str(Path(str(database)).expanduser().resolve())


def route(database: str | Path, agent_id: Optional[str] = None) -> str:
    """Send every LCM-X connection to *database* (an ``lcm.db`` path, which need not exist) to the store, acting
    for *agent_id* (default: the profile of the home the path is in). Returns the agent id."""
    from hermes_state_store import agent_id_for
    agent = agent_id or agent_id_for(Path(str(database)))
    with _ROUTES_LOCK:
        _ROUTES[_route_key(database)] = agent
    return agent


def store_identity(database: str | Path) -> Optional[dict]:
    """What a routed *database* is on the store (the agent that owns its rows and the store database), for code
    that binds a record to its target database the way LCM-X binds one to the ``lcm.db`` file's inode; None when
    the path is not routed."""
    factory = backend_for(database)
    if factory is None:
        return None
    conn = factory(readonly=True)
    try:
        (name,) = conn._pg.execute("SELECT current_database()").fetchone()
        return {"store": name, "agent_id": conn.agent_id}
    finally:
        conn.close()


def route_home(database: str | Path) -> bool:
    """``lcm.store_backend: auto|postgres``: a home with no ``lcm.db`` (or ``:memory:``) keeps its LCM-X data on the
    store; an existing ``lcm.db`` stays the home's store until lina#24 moves it in (nothing is read from it here)."""
    path = Path(str(database))
    if str(database).startswith((":memory:", "file:")):
        return False
    if path.exists():
        logger.warning("LCM-X keeps using %s: a home's lcm.db moves to the memory store with lina#24", path)
        return False
    route(path)
    return True


def unroute(database: str | Path) -> None:
    with _ROUTES_LOCK:
        _ROUTES.pop(_route_key(database), None)


_route_every: Optional[Callable[[str], str]] = None


def route_every_database(agent_for: Optional[Callable[[str], str]]) -> None:
    """Tests only: route every LCM-X database path, acting for ``agent_for(path)`` (None turns it off)."""
    global _route_every
    _route_every = agent_for
    with _ROUTES_LOCK:
        _route_every_seen.clear()


_route_every_seen: set[str] = set()


def backend_for(database: Any, *, opening: bool = False) -> Optional[Callable[..., PgConnection]]:
    """A connection factory when *database* is routed to the store, else None (LCM-X opens SQLite). Under
    ``route_every_database`` a path is routed once an LCM-X store opens it (*opening*); other connects to it follow."""
    if not isinstance(database, (str, Path)) or str(database).startswith(("file:", ":memory:")):
        return None
    key = _route_key(database)
    with _ROUTES_LOCK:
        agent = _ROUTES.get(key) if _ROUTES else None
        if agent is None and _route_every is not None and Path(key).name != "state.db":
            if opening:
                _route_every_seen.add(key)
            if key in _route_every_seen:
                agent = _route_every(key)
    if agent is None:
        return None
    def connect(**connect_kwargs: Any) -> PgConnection:
        return PgConnection(str(database), agent, timeout=connect_kwargs.get("timeout", 5.0),
                            isolation_level=connect_kwargs.get("isolation_level", ""),
                            readonly=bool(connect_kwargs.get("readonly")))
    return connect


# ── errors ──

def _sqlite_error(exc: Exception) -> sqlite3.Error:
    """The ``sqlite3`` exception LCM-X catches for a psycopg error: integrity (class 23) or operational."""
    import psycopg
    sqlstate = getattr(exc, "sqlstate", None) or ""
    message = str(exc).strip()
    if sqlstate in ("55P03", "40P01", "40001"):
        # Lock timeout, deadlock, serialization: what SQLite reports as a busy database.
        err: sqlite3.Error = sqlite3.OperationalError(f"database is locked ({message})")
    elif sqlstate.startswith("23"):
        err = sqlite3.IntegrityError(message)
    elif isinstance(exc, psycopg.errors.FeatureNotSupported):
        err = sqlite3.NotSupportedError(message)
    elif isinstance(exc, (psycopg.ProgrammingError, psycopg.DataError)) and not sqlstate.startswith("57"):
        err = sqlite3.OperationalError(message)
    else:
        err = sqlite3.OperationalError(message)
    err.sqlstate = sqlstate  # type: ignore[attr-defined]
    return err


# ── SQL translation ──

_FTS_COLUMNS = {"messages_fts": "content", "messages_fts_stem": "content", "nodes_fts": "summary",
                "lcm_trajectory_states_fts": "search_text"}
_TOKEN = re.compile(
    r"'(?:[^']|'')*'"            # string literal
    r'|"(?:[^"]|"")*"'           # quoted identifier
    r"|--[^\n]*"                 # line comment
    r"|/\*.*?\*/"                # block comment
    r"|\?\d*|:[A-Za-z_]\w*|@[A-Za-z_]\w*|\$[A-Za-z_]\w*"
    r"|%", re.DOTALL)
_DDL = re.compile(r"^\s*(CREATE|DROP|ALTER|REINDEX|VACUUM|ANALYZE|ATTACH|DETACH)\b", re.IGNORECASE)
_TEMP_DDL = re.compile(r"^\s*(CREATE\s+(TEMP|TEMPORARY)\s+TABLE|DROP\s+TABLE\s+(IF\s+EXISTS\s+)?temp\.)", re.IGNORECASE)
_FTS_MAINTENANCE = re.compile(r"^\s*INSERT\s+INTO\s+(messages_fts|messages_fts_stem|nodes_fts|"
                              r"lcm_trajectory_states_fts)\b", re.IGNORECASE)
_CONTROL = re.compile(r"^\s*(BEGIN|COMMIT|END|ROLLBACK|SAVEPOINT|RELEASE)\b(.*)$", re.IGNORECASE | re.DOTALL)
_PRAGMA = re.compile(r"^\s*PRAGMA\s+(?:\w+\.)?(\w+)\s*(?:\(\s*['\"]?([\w.]+)['\"]?\s*\)|=\s*(.+))?\s*;?\s*$", re.IGNORECASE | re.DOTALL)
_MESSAGE_INSERT = re.compile(r"^\s*INSERT\s+(?:OR\s+(\w+)\s+)?INTO\s+messages\s*(?:\(([^)]*)\))?\s*((?:VALUES|SELECT|WITH)\b.*?);?\s*$",
                             re.IGNORECASE | re.DOTALL)


@dataclass(frozen=True)
class Template:
    """A translated statement: SQL parts with parameter slots filled per execute."""
    parts: tuple  # str, or ("param", index) / ("match", index, alias) / ("rank", index, alias) / ("snippet", index, alias)
    kind: str = "sql"  # sql | skip | control | pragma | message_insert | message_content_update
    returning: Optional[str] = None
    detail: tuple = ()


def _mask(sql: str) -> tuple[str, list[str], list[str]]:
    """*sql* with literals and comments replaced by ``\\x00N\\x00`` and placeholders by ``\\x01N\\x01``."""
    literals: list[str] = []
    slots: list[str] = []

    def repl(m: re.Match) -> str:
        tok = m.group(0)
        if tok == "%":
            literals.append("%%")
            return f"\x00{len(literals) - 1}\x00"
        if tok[0] in "?:@$" and (len(tok) == 1 or tok[1] != ":"):
            slots.append(tok)
            return f"\x01{len(slots) - 1}\x01"
        if tok.startswith(("--", "/*")):
            return " "
        literals.append(tok.replace("%", "%%"))
        return f"\x00{len(literals) - 1}\x00"
    return _TOKEN.sub(repl, sql), literals, slots


def _between_bound(m: re.Match) -> bool:
    """The AND of `x BETWEEN low AND high` joins two bounds, not two conditions."""
    return m.group(1).upper() == "AND" and re.search(r"\bBETWEEN\s+[^\s()]+\s*$", m.string[:m.start()], re.IGNORECASE) is not None


_REWRITES: tuple[tuple[re.Pattern, str], ...] = (
    (re.compile(r"\bCAST\s*\(((?:[^()]|\([^()]*\))*?)\s+AS\s+INTEGER\s*\)", re.IGNORECASE), r"CAST(\1 AS bigint)"),
    (re.compile(r"\bCAST\s*\(((?:[^()]|\([^()]*\))*?)\s+AS\s+REAL\s*\)", re.IGNORECASE), r"CAST(\1 AS double precision)"),
    (re.compile(r"\bIS\s+NOT\s+(?!NULL\b|DISTINCT\b|TRUE\b|FALSE\b)", re.IGNORECASE), "IS DISTINCT FROM "),
    (re.compile(r"\bIS\s+(?!NOT\b|NULL\b|DISTINCT\b|TRUE\b|FALSE\b)", re.IGNORECASE), "IS NOT DISTINCT FROM "),
    (re.compile(r"\bNOT\s+GLOB\b", re.IGNORECASE), "OPERATOR(ctx.!~#)"),
    (re.compile(r"\bGLOB\b", re.IGNORECASE), "OPERATOR(ctx.~#)"),
    (re.compile(r"\bNOT\s+LIKE\b", re.IGNORECASE), "NOT ILIKE"),
    (re.compile(r"(?<!NOT )(?<!I)\bLIKE\b", re.IGNORECASE), "ILIKE"),
    (re.compile(r"\b(json_extract|json_type|json_array_length|json_each|json_valid|json_group_array|json_set|"
                r"json_remove|json_array|json_object|json_insert|json_replace|json_quote|json)\s*\(", re.IGNORECASE),
     lambda m: f"ctx.{m.group(1).lower()}("),
    (re.compile(r"\bpragma_table_info\s*\(", re.IGNORECASE), "ctx.pragma_table_info("),
    (re.compile(r"\b(?:sqlite_schema|sqlite_master)\b", re.IGNORECASE), "ctx.sqlite_master"),
    (re.compile(r"\btemp\.(\w+)", re.IGNORECASE), r"pg_temp.\1"),
    (re.compile(r"\bBEGIN\s+(IMMEDIATE|EXCLUSIVE|DEFERRED)\b", re.IGNORECASE), "BEGIN"),
    (re.compile(r"\bLIMIT\s+-1\b", re.IGNORECASE), "LIMIT ALL"),
    (re.compile(r"\s+WITHOUT\s+ROWID\b", re.IGNORECASE), ""),
    (re.compile(r"\s+(?:INDEXED\s+BY\s+\w+|NOT\s+INDEXED)\b", re.IGNORECASE), ""),
    # SQLite's CROSS JOIN with ON is an inner join that fixes the join order; Postgres takes no ON there.
    (re.compile(r"\bCROSS\s+JOIN(\s+[\w.]+(?:\s+(?:AS\s+)?(?!ON\b)\w+)?\s+ON\b)", re.IGNORECASE), r"JOIN\1"),
    # SQLite's 0/1 in a boolean position (`NOT 0`, `(0 AND x)`, `WHERE 1`) and a parameter there (`AND ?`).
    (re.compile(r"\b(NOT|AND|OR|WHERE|ON)\s+([01])\b(?!\s*(?:[-=<>!+*/%|,.]|IS\b|IN\b|BETWEEN\b|LIKE\b))",
                re.IGNORECASE),
     lambda m: m.group(0) if _between_bound(m) else f"{m.group(1)} {'TRUE' if m.group(2) == '1' else 'FALSE'}"),
    (re.compile(r"\b(AND|OR|WHERE|NOT|ON)\s*\(\s*([01])\s*\)", re.IGNORECASE),
     lambda m: f"{m.group(1)} ({'TRUE' if m.group(2) == '1' else 'FALSE'})"),
    (re.compile(r"\(\s*([01])\s+(AND|OR)\b", re.IGNORECASE),
     lambda m: f"({'TRUE' if m.group(1) == '1' else 'FALSE'} {m.group(2)}"),
    (re.compile(r"\b(AND|OR|NOT)\s+(\x01\d+\x01)(?=\s*(?:\)|AND\b|OR\b|$))", re.IGNORECASE),
     lambda m: m.group(0) if _between_bound(m) else f"{m.group(1)} ({m.group(2)} <> 0)"),
    (re.compile(r"\bhex\s*\(", re.IGNORECASE), "ctx.hex("),
    (re.compile(r"\bchar\s*\(", re.IGNORECASE), "ctx.char("),
)


def _apply_rewrites(masked: str) -> str:
    for pattern, repl in _REWRITES:
        masked = pattern.sub(repl, masked)
    return _mixed_json_arrays(masked)


def _mixed_json_arrays(masked: str) -> str:
    """``json_array(id, length(bits))`` mixes text and numbers, which no SQL-language function signature takes;
    ``jsonb_build_array`` takes any arguments and prints like ``ctx.json_array``."""
    out, pos = [], 0
    for m in re.finditer(r"\bctx\.json_array\(", masked):
        if m.start() < pos:
            continue
        depth, end = 1, m.end()
        while end < len(masked) and depth:
            depth += {"(": 1, ")": -1}.get(masked[end], 0)
            end += 1
        if depth or not masked[m.end():end - 1].strip():
            continue
        inner = _mixed_json_arrays(masked[m.end():end - 1])
        # Inside json_group_array the array stays JSON (SQLite's JSON subtype), not a quoted string.
        nested = masked[:m.start()].rstrip().endswith("json_group_array(")
        out.append(f"{masked[pos:m.start()]}(jsonb_build_array({inner}){'' if nested else '::text'})")
        pos = end
    out.append(masked[pos:])
    return "".join(out)


def _insert_or(masked: str, catalog: Catalog) -> str:
    """``INSERT OR IGNORE`` / ``INSERT OR REPLACE`` / ``REPLACE INTO`` → ``INSERT ... ON CONFLICT``."""
    m = re.match(r"\s*(?:INSERT\s+OR\s+(IGNORE|REPLACE|ABORT|FAIL|ROLLBACK)|(REPLACE))\s+INTO\s+((?:temp\.)?\w+)\s*"
                 r"(\(([^)]*)\))?", masked, re.IGNORECASE)
    if not m:
        return masked
    mode = (m.group(1) or m.group(2)).upper()
    table = m.group(3)
    cols = [c.strip().strip('"') for c in m.group(5).split(",")] if m.group(5) else catalog.columns(table)
    head = f"INSERT INTO {table} ({', '.join(cols)})"
    body = masked[m.end():]
    if mode == "IGNORE":
        tail = " ON CONFLICT DO NOTHING"
    elif mode == "REPLACE":
        key = catalog.primary_key(table)
        update = [c for c in cols if c not in key]
        tail = (f" ON CONFLICT ({', '.join(key)}) DO UPDATE SET "
                + ", ".join(f"{c} = EXCLUDED.{c}" for c in update)) if update else " ON CONFLICT DO NOTHING"
    else:
        tail = ""
    body, returning = _split_returning(body)
    return head + body.rstrip().rstrip(";") + tail + returning


def _split_returning(body: str) -> tuple[str, str]:
    m = re.search(r"\sRETURNING\s", body, re.IGNORECASE)
    return (body, "") if not m else (body[:m.start()], body[m.start():])


def _conflict_targets(masked: str, catalog: Catalog) -> str:
    """``ON CONFLICT (cols)`` on an agent-keyed table gets ``agent_id`` first, as its keys have."""
    m = re.match(r"\s*INSERT\s+INTO\s+(\w+)", masked, re.IGNORECASE)
    if not m:
        return masked
    table = m.group(1)

    def repl(c: re.Match) -> str:
        cols = [x.strip() for x in c.group(1).split(",")]
        if "agent_id" in cols or not catalog.agent_keyed(table, cols):
            return c.group(0)
        return f"ON CONFLICT (agent_id, {', '.join(cols)})"
    return re.sub(r"ON\s+CONFLICT\s*\(([^)]*)\)", repl, masked, flags=re.IGNORECASE)


_FROM_ITEM = re.compile(r"\b(?:FROM|JOIN)\s+(\w+)(?:\s+(?:AS\s+)?(?!(?:WHERE|JOIN|ON|CROSS|LEFT|INNER|ORDER|GROUP|LIMIT|"
                        r"USING|NATURAL|UNION|HAVING|WINDOW|OFFSET|EXCEPT|INTERSECT|RETURNING|SET|VALUES)\b)(\w+))?", re.IGNORECASE)


def _expand_stars(masked: str, catalog: Catalog) -> str:
    """``alias.*`` and a lone ``SELECT *`` over a ctx table → LCM-X's own columns: the store's ``agent_id`` (and a
    view's extra columns) must not shift the positional reads LCM-X does."""
    if "*" not in masked:
        return masked
    tables = {}
    for m in _FROM_ITEM.finditer(masked):
        tables.setdefault(m.group(2) or m.group(1), m.group(1))
        tables.setdefault(m.group(1), m.group(1))

    def qualified(m: re.Match) -> str:
        cols = catalog.columns(tables.get(m.group(1), ""))
        return ", ".join(f"{m.group(1)}.{c}" for c in cols) if cols else m.group(0)
    masked = re.sub(r"\b(\w+)\.\*", qualified, masked)

    def lone(m: re.Match) -> str:
        cols = catalog.columns(m.group(3))
        return f"{m.group(1)}{', '.join(cols)}{m.group(2)}{m.group(3)}" if cols else m.group(0)
    return re.sub(r"(\bSELECT\s+(?:DISTINCT\s+)?)\*(\s+FROM\s+)(\w+)(?!\s*\.)", lone, masked, flags=re.IGNORECASE)


def _item_end(text: str, pos: int) -> int:
    """End of the FROM item starting at *pos*: a name or a call (balanced parentheses), then an optional alias."""
    m = re.compile(r"\s*[\w.]+").match(text, pos)
    if not m:
        return pos
    end = m.end()
    call = re.compile(r"\s*\(").match(text, end)
    if call:
        end = call.end() - 1
        depth = 0
        for i in range(end, len(text)):
            depth += {"(": 1, ")": -1}.get(text[i], 0)
            if depth == 0:
                end = i + 1
                break
    alias = re.compile(r"\s+(?:AS\s+)?(?!(?:ON|USING|WHERE|JOIN|CROSS|LEFT|INNER|NATURAL|ORDER|GROUP|LIMIT|UNION)\b)\w+",
                       re.IGNORECASE).match(text, end)
    return alias.end() if alias else end


def _bare_joins(masked: str) -> str:
    """SQLite's ``JOIN x`` with no ``ON``/``USING`` is a cross join; Postgres needs it spelled."""
    out, pos = [], 0
    for m in re.finditer(r"(?<!CROSS )(?<!NATURAL )(?<!LEFT )(?<!OUTER )\bJOIN\b", masked, re.IGNORECASE):
        if m.start() < pos:
            continue
        end = _item_end(masked, m.end())
        if not re.match(r"\s*(ON|USING)\b", masked[end:], re.IGNORECASE):
            out.append(masked[pos:m.start()] + "CROSS JOIN" + masked[m.end():end])
            pos = end
    out.append(masked[pos:])
    return "".join(out)


def _split_masked_commas(masked: str) -> list[str]:
    """Already masked text split at its top-level commas."""
    parts, depth, start = [], 0, 0
    for i, ch in enumerate(masked):
        depth += {"(": 1, ")": -1}.get(ch, 0)
        if ch == "," and depth == 0:
            parts.append(masked[start:i].strip())
            start = i + 1
    parts.append(masked[start:].strip())
    return parts


def _qualify_upsert(masked: str, catalog: Catalog) -> str:
    """In ``ON CONFLICT ... DO UPDATE``, a bare column of the target table is ambiguous in Postgres (``excluded`` is in
    scope too); SQLite reads it as the existing row. Qualify it with the table name."""
    m = re.match(r"\s*INSERT\s+INTO\s+(\w+)", masked, re.IGNORECASE)
    upsert = re.search(r"\bDO\s+UPDATE\s+SET\b", masked, re.IGNORECASE)
    if not m or not upsert:
        return masked
    table = m.group(1)
    columns = set(catalog.columns(table))
    if not columns:
        return masked
    head, tail = masked[:upsert.end()], masked[upsert.end():]
    tail, returning = _split_returning(tail)

    def qualify(text: str) -> str:
        return re.sub(r"(?<![\w.\x00\x01])([A-Za-z_]\w*)\b(?!\s*\()",
                      lambda c: f"{table}.{c.group(1)}" if c.group(1) in columns else c.group(1), text)
    pieces = re.split(r"(\bWHERE\b)", tail, maxsplit=1, flags=re.IGNORECASE)
    assignments = _split_masked_commas(pieces[0]) if pieces[0].strip() else []
    out = ", ".join(f"{a.split('=', 1)[0].strip()} = {qualify(a.split('=', 1)[1])}" if "=" in a else a
                    for a in assignments)
    where = "".join(pieces[1:2]) + (qualify(pieces[2]) if len(pieces) > 2 else "")
    return f"{head} {out} {where}".rstrip() + returning


def _messages_target(masked: str) -> str:
    """Writes to LCM's ``messages`` go to the placement table; reads keep the view."""
    head = re.match(r"^(\s*(?:UPDATE|DELETE\s+FROM)\s+)messages\b(.*?)\bWHERE\b(.*?)(\s+RETURNING\b.*)?$", masked,
                    re.IGNORECASE | re.DOTALL)
    if head and re.search(r"\b(?:role|content|tool_call_id|tool_calls|tool_name)\b", head.group(3), re.IGNORECASE):
        # The WHERE reads the message's text, which lives in memory.messages: pick the rows through the view.
        return (f"{head.group(1)}ctx.lcm_messages AS lcm_target{head.group(2)}WHERE lcm_target.agent_id = "
                f"public.lina_current_agent() AND lcm_target.store_id IN (SELECT messages.store_id FROM messages "
                f"WHERE{head.group(3)}){head.group(4) or ''}")
    masked = re.sub(r"^(\s*UPDATE\s+)messages\b", r"\1ctx.lcm_messages", masked, flags=re.IGNORECASE)
    return re.sub(r"^(\s*DELETE\s+FROM\s+)messages\b", r"\1ctx.lcm_messages", masked, flags=re.IGNORECASE)


def _fts_aliases(masked: str) -> dict[str, str]:
    """FTS view name → the alias it has in this statement (or its own name)."""
    aliases = {}
    stop = r"(?!(?:WHERE|JOIN|ON|CROSS|LEFT|INNER|ORDER|GROUP|LIMIT|USING|AND|OR|SET|NATURAL|MATCH)\b)"
    for name in _FTS_COLUMNS:
        m = re.search(rf"\b{name}\s+(?:AS\s+)?{stop}([A-Za-z_]\w*)", masked, re.IGNORECASE)
        aliases[name] = m.group(1) if m else name
    return aliases


def _fts_parts(masked: str) -> list:
    """Split *masked* into text and FTS slots: ``x MATCH ?``, bare ``rank``, ``bm25(x)``, ``snippet(x, ...)``."""
    if not re.search(r"\bMATCH\b", masked, re.IGNORECASE):
        return [masked]
    aliases = _fts_aliases(masked)
    known = set(aliases) | set(aliases.values())
    matches = [(m, aliases.get(m.group(1), m.group(1))) for m in re.finditer(r"\b(\w+)(?:\.\w+)?\s+MATCH\s+\x01(\d+)\x01", masked, re.IGNORECASE)
               if m.group(1) in known]
    if not matches:
        return [masked]
    first_slot = int(matches[0][0].group(2))
    marks = []
    for m, alias in matches:
        marks.append((m.start(), m.end(), ("match", int(m.group(2)), alias)))
    for m in re.finditer(r"\bbm25\s*\(\s*(\w+)\s*\)", masked, re.IGNORECASE):
        marks.append((m.start(), m.end(), ("rank", first_slot, aliases.get(m.group(1), m.group(1)))))
    for m in re.finditer(r"\bsnippet\s*\(\s*(\w+)\s*,[^()]*\)", masked, re.IGNORECASE):
        marks.append((m.start(), m.end(), ("snippet", first_slot, aliases.get(m.group(1), m.group(1)))))
    default_alias = matches[0][1]
    # `bm25(x) AS rank` names a column: the bare `rank` after it (ORDER BY rank) reads that column.
    named_rank = re.search(r"\bAS\s+rank\b", masked, re.IGNORECASE)
    for m in ([] if named_rank else re.finditer(r"(?<![\w.])rank\b(?!\s*\()", masked, re.IGNORECASE)):
        if not any(s <= m.start() < e for s, e, _ in marks):
            marks.append((m.start(), m.end(), ("rank", first_slot, default_alias)))
    marks.sort()
    parts: list = []
    pos = 0
    for start, end, slot in marks:
        parts.append(masked[pos:start])
        parts.append(slot)
        pos = end
    parts.append(masked[pos:])
    return parts


def _slot_indices(slots: list[str]) -> list[int]:
    """The argument each placeholder reads: ``?`` the next one, ``?N`` the Nth (SQLite's numbering), a name its
    position."""
    out, following = [], 0
    for tok in slots:
        if tok[0] == "?" and tok[1:].isdigit():
            out.append(int(tok[1:]) - 1)
            following = max(following, int(tok[1:]))
        elif tok == "?":
            out.append(following)
            following += 1
        else:
            out.append(len(out))
    return out


def _unmask(text: str, literals: list[str], indices: Optional[list[int]] = None) -> list:
    """Restore literals and turn placeholder markers into ``("param", i)`` slots."""
    out: list = []
    for piece in re.split(r"(\x01\d+\x01)", re.sub(r"\x00(\d+)\x00", lambda m: literals[int(m.group(1))], text)):
        if piece.startswith("\x01"):
            slot = int(piece.strip("\x01"))
            out.append(("param", indices[slot] if indices is not None else slot))
        elif piece:
            out.append(piece)
    return out


def _named_order(slots: list[str]) -> Optional[list[str]]:
    names = [s[1:] for s in slots if s[0] in ":@$"]
    return names if names and len(names) == len(slots) else None


@lru_cache(maxsize=4096)
def _translate_cached(sql: str, catalog_key: int) -> Template:
    catalog = Catalog.registry[catalog_key]
    stripped = sql.strip()
    if not stripped:
        return Template((), kind="skip")
    if _CONTROL.match(stripped):
        return Template((stripped,), kind="control")
    if _PRAGMA.match(stripped):
        return Template((stripped,), kind="pragma")
    if _FTS_MAINTENANCE.match(stripped):
        return Template((), kind="skip")
    if _DDL.match(stripped) and not _TEMP_DDL.match(stripped):
        return Template((), kind="skip")
    insert = _MESSAGE_INSERT.match(stripped)
    if insert:
        cols = tuple(c.strip().strip('"') for c in insert.group(2).split(",")) if insert.group(2) else ()
        return Template((), kind="message_insert", detail=((insert.group(1) or "").upper(), cols, insert.group(3)))
    if re.match(r"\s*UPDATE\s+messages\s+SET\s+(?:.*?,\s*)?(content|role|tool_call_id|tool_calls|tool_name)\s*=",
                stripped, re.IGNORECASE | re.DOTALL):
        return Template((stripped,), kind="message_content_update")
    # strftime('%s','now') is SQLite's "epoch seconds now"; as text it would not go into a REAL column here.
    stripped = re.sub(r"\bstrftime\s*\(\s*'%s'\s*,\s*'now'\s*\)", "ctx.unixepoch()", stripped, flags=re.IGNORECASE)
    masked, literals, slots = _mask(stripped)
    masked = _insert_or(masked, catalog)
    masked = _conflict_targets(masked, catalog)
    masked = _qualify_upsert(masked, catalog)
    masked = _messages_target(masked)
    if _TEMP_DDL.match(masked):
        masked = re.sub(r"\bINTEGER\b", "bigint", masked, flags=re.IGNORECASE)
        masked = re.sub(r"\bREAL\b", "double precision", masked, flags=re.IGNORECASE)
        masked = re.sub(r"\bBLOB\b", "bytea", masked, flags=re.IGNORECASE)
    masked = _expand_stars(masked, catalog)
    masked = _bare_joins(masked)
    masked = _apply_rewrites(masked)
    masked = masked.rstrip().rstrip(";")
    returning = None
    m = re.match(r"\s*INSERT\s+INTO\s+(\w+)", masked, re.IGNORECASE)
    if m and not re.search(r"\bRETURNING\b", masked, re.IGNORECASE):
        returning = catalog.identity(m.group(1))
        if returning:
            masked += f" RETURNING {returning}"
    parts: list = []
    indices = _slot_indices(slots)
    for piece in _fts_parts(masked):
        parts.extend(_unmask(piece, literals, indices) if isinstance(piece, str)
                     else [(piece[0], indices[piece[1]], piece[2])])
    names = _named_order(slots)
    return Template(tuple(parts), returning=returning, detail=tuple(names) if names else ())


def translate(sql: str, catalog: Catalog) -> Template:
    return _translate_cached(sql, catalog.key)


# ── full text: an FTS5 query value → a tsquery expression ──

_CJK = re.compile(r"[\u3040-\u30ff\u3400-\u4dbf\u4e00-\u9fff\uac00-\ud7af\uff00-\uffef]")
_QUERY_TOKEN = re.compile(r'"[^"]*"|\(|\)|[^\s()"]+')


def _fts_terms(query: str) -> list[tuple[str, str]]:
    """``(op, term)`` pairs: op ``and``/``or``/``not`` joining the term to what came before."""
    tokens = _QUERY_TOKEN.findall(query or "")
    out: list[tuple[str, str]] = []
    op = "and"
    for tok in tokens:
        if tok in ("AND", "(", ")"):
            continue
        if tok in ("OR", "NOT"):
            op = tok.lower()
            continue
        if tok.startswith("NEAR"):
            continue
        out.append((op, tok))
        op = "and"
    return out


def tsquery_sql(query: str) -> tuple[str, list[Any]]:
    """A Postgres tsquery expression (``simple`` configuration) for an FTS5 query, and its parameters."""
    sql, params = "", []
    for op, term in _fts_terms(query):
        if term.startswith('"'):
            piece, value = "phraseto_tsquery('simple', %s)", term.strip('"')
        elif term.endswith("*") and len(term) > 1:
            lexeme = term[:-1].lower().replace("'", "''").replace("\\", "\\\\")
            piece, value = "to_tsquery('simple', %s)", f"'{lexeme}':*"
        else:
            piece, value = "plainto_tsquery('simple', %s)", term
        if not sql:
            sql = piece if op != "not" else f"!!{piece}"
        else:
            sql = f"({sql}) {'||' if op == 'or' else '&&'} {'!!' if op == 'not' else ''}{piece}"
        params.append(value)
    return (sql or "plainto_tsquery('simple', '')"), params


def _fts_slot(slot: tuple, args: Sequence[Any]) -> tuple[str, list[Any]]:
    kind, index, alias = slot
    tsq, params = tsquery_sql(str(args[index]))
    if kind == "match":
        return f"({alias}.tsv @@ ({tsq}))", params
    if kind == "rank":
        return f"(-ts_rank_cd({alias}.tsv, ({tsq})))", params
    column = _FTS_COLUMNS.get(alias, None) or "content"
    for name, col in _FTS_COLUMNS.items():
        if alias == name:
            column = col
    return (f"ts_headline('simple', {alias}.{column}, ({tsq}), "
            "'StartSel=>>>, StopSel=<<<, MaxWords=40, MinWords=10, FragmentDelimiter=...')"), params


def render(template: Template, args: Any) -> tuple[str, list[Any]]:
    """The final SQL and parameter list of *template* for one execute's *args*."""
    if isinstance(args, dict):
        names = template.detail
        args = [args[name] for name in names] if names else []
    args = list(args or ())
    sql: list[str] = []
    params: list[Any] = []
    for part in template.parts:
        if isinstance(part, str):
            sql.append(part)
        elif part[0] == "param":
            sql.append("%s")
            params.append(_adapt(args[part[1]]))
        else:
            piece, piece_params = _fts_slot(part, args)
            sql.append(piece)
            params.extend(piece_params)
    return "".join(sql), params


_VECTOR_INSERT = re.compile(r"^\s*INSERT\s+INTO\s+(?:lcm_embedding_vectors|lcm_chunk_vectors)\b", re.IGNORECASE)


_STORE_FUNCTIONS = frozenset({"lcm_summary_has_text"})

_LIMIT_SLOT = re.compile(r"\bLIMIT\s+%s", re.IGNORECASE)


def _unbounded_limits(text: str, args: list) -> list:
    """SQLite reads a negative LIMIT as no limit; Postgres refuses it and reads LIMIT NULL that way."""
    args = list(args)
    for index, m in enumerate(re.finditer(r"%s", text)):
        if re.search(r"\bLIMIT\s+$", text[max(m.start() - 16, 0):m.start()], re.IGNORECASE) \
                and isinstance(args[index], int) and args[index] < 0:
            args[index] = None
    return args


def _vector_text(value: Any) -> Any:
    """LCM-X's float32 vector BLOB as pgvector's text form (``vec.embeddings.embedding``)."""
    if not isinstance(value, (bytes, bytearray, memoryview)) or len(value) % 4:
        return value
    floats = array("f")
    floats.frombytes(bytes(value))
    return "[" + ",".join(repr(f) for f in floats) + "]"


def _vector_blob(text: Any) -> Any:
    """pgvector's text form back to the float32 BLOB LCM-X decodes."""
    if not isinstance(text, str):
        return text
    return array("f", (float(x) for x in text.strip("[]").split(",") if x)).tobytes()


def _adapt(value: Any) -> Any:
    if isinstance(value, bool):
        return int(value)
    if isinstance(value, (bytearray, memoryview)):
        return bytes(value)
    return value


# ── catalog: keys and columns of the ctx tables, read once per process ──

_HIDDEN_COLUMNS = {"messages": ("text_uid", "content_sha256"), "messages_fts": ("tsv",),
                   "messages_fts_stem": ("tsv",), "nodes_fts": ("tsv",), "lcm_trajectory_states_fts": ("tsv",)}


class Catalog:
    registry: dict[int, Catalog] = {}
    _lock = threading.Lock()

    def __init__(self, conn) -> None:
        self.key = id(self)
        self._columns: dict[str, list[str]] = {}
        self._pk: dict[str, list[str]] = {}
        self._unique: dict[str, list[tuple[str, ...]]] = {}
        self._identity: dict[str, str] = {}
        rows = conn.execute(
            "SELECT c.relname, a.attname, a.attnum, a.attidentity FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid "
            "WHERE c.relnamespace = 'ctx'::regnamespace AND c.relkind IN ('r', 'v') AND a.attnum > 0 "
            "AND NOT a.attisdropped ORDER BY c.relname, a.attnum").fetchall()
        for table, column, _num, _identity in rows:
            if column not in _HIDDEN_COLUMNS.get(table, ()) and column not in ("agent_id", "rowid", "vec_item_id"):
                self._columns.setdefault(table, []).append(column)
        # The ids LCM-X lets SQLite allocate are filled per agent by the lcm_assign_id trigger (its argument names
        # the column).
        for table, args in conn.execute(
                "SELECT c.relname, encode(t.tgargs, 'escape') FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid "
                "WHERE c.relnamespace = 'ctx'::regnamespace AND t.tgname = 'lcm_assign_id'").fetchall():
            self._identity[table] = args.split("\\000")[0]
        for table, indisprimary, cols in conn.execute(
                "SELECT c.relname, i.indisprimary, array_agg(a.attname ORDER BY k.ord) "
                "FROM pg_index i JOIN pg_class c ON c.oid = i.indrelid "
                "CROSS JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord) "
                "JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = k.attnum "
                "WHERE c.relnamespace = 'ctx'::regnamespace AND i.indisunique GROUP BY c.relname, i.indexrelid, "
                "i.indisprimary").fetchall():
            self._unique.setdefault(table, []).append(tuple(cols))
            if indisprimary:
                self._pk[table] = list(cols)
        Catalog.registry[self.key] = self

    _shared: Optional[Catalog] = None

    @classmethod
    def shared(cls, conn) -> Catalog:
        with cls._lock:
            if cls._shared is None:
                cls._shared = Catalog(conn)
            return cls._shared

    def columns(self, table: str) -> list[str]:
        return list(self._columns.get(table, []))

    def primary_key(self, table: str) -> list[str]:
        return list(self._pk.get(table, []))

    def identity(self, table: str) -> Optional[str]:
        return self._identity.get(table)

    def agent_keyed(self, table: str, cols: Sequence[str]) -> bool:
        return ("agent_id", *cols) in self._unique.get(table, [])


# ── rows and cursors ──

class Row:
    """``sqlite3.Row``'s interface: index and column-name access, ``keys()``, equality."""
    __slots__ = ("_names", "_values")

    def __init__(self, cursor: PgCursor, values: tuple) -> None:
        self._names = cursor._names
        self._values = values

    def keys(self) -> list[str]:
        return list(self._names)

    def __getitem__(self, key):
        if isinstance(key, str):
            for i, name in enumerate(self._names):
                if name.lower() == key.lower():
                    return self._values[i]
            raise IndexError(f"No item with that key: {key}")
        return self._values[key]

    def __len__(self) -> int:
        return len(self._values)

    def __iter__(self):
        return iter(self._values)

    def __eq__(self, other) -> bool:
        return isinstance(other, Row) and self._names == other._names and self._values == other._values

    def __hash__(self) -> int:
        return hash((self._names, self._values))


def _plain(value: Any) -> Any:
    if isinstance(value, bool):
        return int(value)
    if isinstance(value, Decimal):
        return int(value) if value == value.to_integral_value() else float(value)
    if isinstance(value, memoryview):
        return bytes(value)
    return value


class PgCursor:
    arraysize = 1

    def __init__(self, connection: PgConnection) -> None:
        self.connection = connection
        self.description: Optional[tuple] = None
        self.rowcount = -1
        self.lastrowid: Optional[int] = None
        self._rows: list[tuple] = []
        self._pos = 0
        self._names: tuple[str, ...] = ()
        self.row_factory = None

    def execute(self, sql: str, parameters: Any = ()) -> PgCursor:
        self.connection._run(self, sql, parameters)
        return self

    def executemany(self, sql: str, seq: Iterable[Any]) -> PgCursor:
        total = 0
        for params in seq:
            self.connection._run(self, sql, params)
            total += max(self.rowcount, 0)
        self.rowcount = total
        return self

    def executescript(self, script: str) -> PgCursor:
        self.connection.executescript(script)
        return self

    def _set_result(self, description, rows: list[tuple], rowcount: int, lastrowid: Optional[int]) -> None:
        self._names = tuple(d[0] for d in description) if description else ()
        self.description = tuple((d[0], None, None, None, None, None, None) for d in description) if description else None
        self._rows = [tuple(_plain(v) for v in row) for row in rows]
        self._pos = 0
        self.rowcount = rowcount
        if lastrowid is not None:
            self.lastrowid = lastrowid

    def _make(self, values: tuple):
        factory = self.row_factory or self.connection.row_factory
        if factory is None:
            return values
        if factory is sqlite3.Row or factory is Row:
            return Row(self, values)
        return factory(self, values)

    def fetchone(self):
        if self._pos >= len(self._rows):
            return None
        row = self._rows[self._pos]
        self._pos += 1
        return self._make(row)

    def fetchmany(self, size: Optional[int] = None):
        size = size or self.arraysize
        rows = self._rows[self._pos:self._pos + size]
        self._pos += len(rows)
        return [self._make(r) for r in rows]

    def fetchall(self):
        rows = self._rows[self._pos:]
        self._pos = len(self._rows)
        return [self._make(r) for r in rows]

    def __iter__(self):
        while True:
            row = self.fetchone()
            if row is None:
                return
            yield row

    def close(self) -> None:
        self._rows = []


# ── PRAGMA ──

_PRAGMA_READS: dict[str, tuple] = {
    "foreign_keys": ((1,),), "journal_mode": (("wal",),), "busy_timeout": ((5000,),), "synchronous": ((1,),),
    "query_only": ((0,),), "user_version": ((0,),), "page_size": ((4096,),), "page_count": ((0,),),
    "freelist_count": ((0,),), "integrity_check": (("ok",),), "quick_check": (("ok",),),
    "wal_checkpoint": ((0, 0, 0),), "schema_version": ((1,),), "encoding": (("UTF-8",),),
    "cache_size": ((-2000,),), "temp_store": ((0,),), "mmap_size": ((0,),), "wal_autocheckpoint": ((1000,),),
    "auto_vacuum": ((0,),), "secure_delete": ((0,),), "recursive_triggers": ((0,),),
    "compile_options": (("ENABLE_FTS5",),), "application_id": ((0,),), "cell_size_check": ((0,),),
}


# ── message text ──

def _text_digest(content: Any) -> str:
    from hermes_state_store import _plain_text, _sha256
    return _sha256(_plain_text(content) or "")


def _same_tool_calls(a: Any, b: Any) -> bool:
    if a == b:
        return True
    try:
        return json.loads(a) == json.loads(b)
    except (TypeError, ValueError):
        return False


@dataclass
class _TextLink:
    """Finds or writes the ``memory.messages`` row of a message LCM stores."""
    agent_id: str
    _store: Any = field(default=None, repr=False)

    def store(self):
        if self._store is None:
            from hermes_state_store import MessageStore
            self._store = MessageStore(self.agent_id)
        return self._store

    def resolve(self, *, message_uid: Optional[str], session_id: Optional[str], role: Optional[str], content: Any,
                tool_call_id: Optional[str], tool_calls: Optional[str], tool_name: Optional[str]) -> tuple[str, str]:
        from hermes_state_store import TEXT_FIELDS
        digest = _text_digest(content)
        store = self.store()
        if message_uid:
            candidates = store.text_uids_with_digest(message_uid, digest)
            if candidates:
                rows = store.texts(candidates)
                for uid in candidates:
                    row = rows.get(uid)
                    if row is not None and row.values[0] == content and _same_tool_calls(row.values[1], tool_calls):
                        return uid, digest
        key = message_uid or "lcm-" + hashlib.sha256(json.dumps(
            [role, content, tool_call_id, tool_name, tool_calls], ensure_ascii=False).encode(
                "utf-8", "surrogatepass")).hexdigest()[:32]
        values = (content, tool_calls) + (None,) * (len(TEXT_FIELDS) - 2)
        with store.write():
            uid = store.put_text(message_uid=key, values=values, role=role, tool_name=tool_name,
                                 tool_call_id=tool_call_id, timestamp=None, session_id=session_id,
                                 seq_in_session=None, meta={"message_uid": message_uid} if message_uid else {})
        return uid, digest

    def close(self) -> None:
        if self._store is not None:
            self._store.close()
            self._store = None


# ── the connection ──

class PgConnection:
    """The subset of ``sqlite3.Connection`` LCM-X uses, on the store as ``lina_app`` for one agent.

    Transactions follow ``sqlite3``'s legacy control: with ``isolation_level`` other than None, a write opens a
    transaction that ``commit()``/``rollback()`` end; ``BEGIN``/``SAVEPOINT`` statements pass through. A failed
    statement inside a transaction rolls back only itself (an implicit savepoint), as in SQLite.
    """

    is_lina_store = True

    def __init__(self, database: str, agent_id: str, *, dsn: Optional[str] = None, timeout: float = 5.0,
                 isolation_level: Optional[str] = "", readonly: bool = False) -> None:
        import psycopg
        from hermes_state_store import StoreUnavailableError, resolve_dsn
        self.database = database
        self.agent_id = agent_id
        try:
            # Client-side binding, as SQLite binds: a parameter needs no type of its own (`? IS NULL OR x = ?`).
            self._pg = psycopg.connect(dsn or resolve_dsn(), autocommit=True, cursor_factory=psycopg.ClientCursor)
        except psycopg.OperationalError as exc:
            raise StoreUnavailableError(f"cannot reach the memory store: {exc}".strip()) from exc
        # lock_timeout stands in for SQLite's busy timeout: a writer waiting on another connection gives up with
        # "database is locked" instead of waiting forever.
        self._pg.execute("SELECT set_config('lina.agent_id', %s, false), set_config('search_path', 'ctx, public', false), "
                         "set_config('lock_timeout', %s, false), set_config('default_transaction_read_only', %s, false)",
                         (agent_id, f"{max(int(float(timeout) * 1000), 1)}ms", "on" if readonly else "off"))
        self._catalog = Catalog.shared(self._pg)
        self._vector_oid = self._pg.execute("SELECT to_regtype('vector')::oid").fetchone()[0]
        self._lock = threading.RLock()
        self._in_tx = False
        self._savepoints: list[str] = []
        self._tx_from_savepoint = False
        self.isolation_level: Optional[str] = isolation_level
        self.row_factory = None
        self.text_factory = str
        self.total_changes = 0
        self._message_uid: Optional[str] = None
        self._returned: list[int] = []
        self._trace: Optional[Callable[[str], None]] = None
        self._text = _TextLink(agent_id)
        self._closed = False

    # sqlite3.Connection surface
    @property
    def in_transaction(self) -> bool:
        return self._in_tx

    def cursor(self, factory=None) -> PgCursor:
        return PgCursor(self)

    def execute(self, sql: str, parameters: Any = ()) -> PgCursor:
        return self.cursor().execute(sql, parameters)

    def executemany(self, sql: str, seq: Iterable[Any]) -> PgCursor:
        return self.cursor().executemany(sql, seq)

    def executescript(self, script: str) -> PgCursor:
        self.commit()
        cur = self.cursor()
        for statement in split_statements(script):
            cur.execute(statement)
        return cur

    def commit(self) -> None:
        with self._lock:
            if self._in_tx:
                self._raw("COMMIT")
                self._end_tx()

    def rollback(self) -> None:
        with self._lock:
            if self._in_tx:
                self._raw("ROLLBACK")
                self._end_tx()

    def close(self) -> None:
        if self._closed:
            return
        self._closed = True
        try:
            if self._in_tx:
                self._raw("ROLLBACK")
        finally:
            self._pg.close()
            self._text.close()

    def __enter__(self) -> PgConnection:
        return self

    def __exit__(self, exc_type, exc, tb) -> bool:
        if exc_type is None:
            self.commit()
        else:
            self.rollback()
        return False

    def create_function(self, name: str, *args, **kwargs) -> None:
        if name in _STORE_FUNCTIONS:
            return  # the store's migration defines it in SQL (ctx.<name>)
        raise sqlite3.NotSupportedError(f"{name}: SQL functions in Python do not run on the memory store")

    def backup(self, *args, **kwargs) -> None:
        raise sqlite3.NotSupportedError("LCM-X data on the memory store is backed up by `lina store backup`")

    def set_progress_handler(self, handler, n) -> None:
        self._progress = (handler, n)

    def set_trace_callback(self, callback) -> None:
        """Statements are reported as LCM-X wrote them (its SQLite SQL), as SQLite reports what it runs."""
        self._trace = callback

    def set_authorizer(self, callback) -> None:
        """SQLite's statement authorizer; on the store a read-only connection is a read-only transaction."""
        self._authorizer = callback

    def setlimit(self, category: int, limit: int) -> int:
        """SQLite's per-connection limits have no store counterpart; the old value is reported as unlimited."""
        return 2**31 - 1

    def getlimit(self, category: int) -> int:
        return 2**31 - 1

    def interrupt(self) -> None:
        self._pg.cancel_safe()

    def note_message_uid(self, message_uid: Optional[str]) -> None:
        """The host ``message_uid`` of the message the next ``INSERT INTO messages`` stores."""
        self._message_uid = message_uid if isinstance(message_uid, str) and message_uid else None

    # internals
    def _end_tx(self) -> None:
        self._in_tx = False
        self._savepoints = []
        self._tx_from_savepoint = False

    def _raw(self, sql: str, params: Optional[Sequence[Any]] = None):
        import psycopg
        try:
            return self._pg.execute(sql, params)
        except psycopg.Error as exc:
            raise _sqlite_error(exc) from exc

    def _begin(self, *, traced: bool = True) -> None:
        if not self._in_tx:
            if traced and self._trace is not None:
                self._trace("BEGIN ")
            self._raw("BEGIN")
            self._in_tx = True

    def _run(self, cur: PgCursor, sql: str, params: Any) -> None:
        if self._closed:
            raise sqlite3.ProgrammingError("Cannot operate on a closed database.")
        template = translate(sql, self._catalog)
        if self._trace is not None:
            self._trace(sql)
        with self._lock:
            handler = _KINDS.get(template.kind)
            if handler is not None:
                handler(self, cur, template, params)
                return
            self._run_sql(cur, template, params)

    def _run_sql(self, cur: PgCursor, template: Template, params: Any) -> None:
        text, args = render(template, params)
        if _VECTOR_INSERT.match(text):
            args = [_vector_text(a) for a in args]
        if _LIMIT_SLOT.search(text):
            args = _unbounded_limits(text, args)
        writes = bool(re.match(r"\s*(INSERT|UPDATE|DELETE|REPLACE|WITH\b.*\b(INSERT|UPDATE|DELETE)\b)", text, re.IGNORECASE | re.DOTALL))
        if writes and self.isolation_level is not None:
            self._begin()
        self._statement(cur, text, args, template.returning)

    def _statement(self, cur: PgCursor, text: str, args: list[Any], returning: Optional[str]) -> None:
        import psycopg
        guarded = self._in_tx
        try:
            if guarded:
                self._pg.execute("SAVEPOINT lcm_x_statement")
            result = self._pg.execute(text, args or None)
            rows = result.fetchall() if result.description else []
            if guarded:
                self._pg.execute("RELEASE SAVEPOINT lcm_x_statement")
        except psycopg.Error as exc:
            if guarded and not self._pg.closed:
                try:
                    self._pg.execute("ROLLBACK TO SAVEPOINT lcm_x_statement")
                except psycopg.Error:
                    logger.debug("rollback to the statement savepoint failed", exc_info=True)
            raise _sqlite_error(exc) from exc
        lastrowid = None
        self._returned = [int(r[0]) for r in rows if r and isinstance(r[0], int)] if returning else []
        description = result.description
        vector_columns = [i for i, d in enumerate(description or ()) if d.type_code == self._vector_oid]
        if vector_columns:
            rows = [tuple(_vector_blob(v) if i in vector_columns else v for i, v in enumerate(row)) for row in rows]
        if returning and rows:
            lastrowid = int(rows[-1][0])
            description, rows = None, []
        count = result.rowcount
        if count and count > 0 and text.lstrip()[:6].upper() in ("INSERT", "UPDATE", "DELETE"):
            self.total_changes += count
        cur._set_result([(d.name,) for d in description] if description else None, rows, count, lastrowid)

    def _control(self, cur: PgCursor, template: Template, params: Any) -> None:
        stmt = _apply_rewrites(template.parts[0]).strip().rstrip(";")
        word = stmt.split()[0].upper()
        if word == "BEGIN":
            self._begin(traced=False)
        elif word in ("COMMIT", "END"):
            self.commit()
        elif word == "ROLLBACK" and not re.search(r"\bTO\b", stmt, re.IGNORECASE):
            self.rollback()
        elif word == "SAVEPOINT":
            if not self._in_tx:
                self._begin()
                self._tx_from_savepoint = True
            self._raw(stmt)
            self._savepoints.append(stmt.split()[-1].lower())
        elif word == "RELEASE":
            name = stmt.split()[-1].lower()
            self._raw(stmt)
            if name in self._savepoints:
                # SQLite releases the most recent savepoint of that name (they nest under one name).
                del self._savepoints[len(self._savepoints) - 1 - self._savepoints[::-1].index(name):]
            if self._tx_from_savepoint and not self._savepoints:
                self.commit()
        else:
            self._raw(stmt)
        cur._set_result(None, [], -1, None)

    def _pragma(self, cur: PgCursor, template: Template, params: Any) -> None:
        m = _PRAGMA.match(template.parts[0])
        name, arg, value = m.group(1).lower(), m.group(2), m.group(3)
        if value is not None:
            cur._set_result(None, [], -1, None)
            return
        if name in ("table_info", "table_xinfo"):
            self._statement(cur, "SELECT * FROM ctx.pragma_table_info(%s)", [arg], None)
            return
        if name == "data_version":
            self._statement(cur, "SELECT ('x' || substr(md5(pg_current_wal_lsn()::text), 1, 8))::bit(32)::int "
                                 "AS data_version", [], None)
            return
        rows = _PRAGMA_READS.get(name, ())
        width = len(rows[0]) if rows else 1
        cur._set_result([(name,)] + [(f"c{i}",) for i in range(1, width)], list(rows), -1, None)

    def _skip(self, cur: PgCursor, template: Template, params: Any) -> None:
        cur._set_result(None, [], -1, None)

    def _message_insert(self, cur: PgCursor, template: Template, params: Any) -> None:
        mode, cols, source = template.detail
        query = f"SELECT * FROM ({source}) AS lcm_values" if source.lstrip()[:6].upper() == "VALUES" else source
        probe = self.cursor()
        self._run(probe, query, params)
        names = list(cols) or self._catalog.columns("messages")
        conflict = {"IGNORE": " ON CONFLICT DO NOTHING"}.get(mode, "")
        stored: list[int] = []
        for values in probe._rows:
            row = dict(zip(names, values))
            # A row that names only some columns (a test's minimal messages table) gets what the store's own
            # write fills: no session, written now. A column named with NULL is still refused.
            row.setdefault("session_id", "")
            row.setdefault("timestamp", time.time())
            uid, digest = self._text.resolve(
                message_uid=self._message_uid, session_id=row.get("session_id"), role=row.pop("role", None),
                content=row.pop("content", None), tool_call_id=row.pop("tool_call_id", None),
                tool_calls=row.pop("tool_calls", None), tool_name=row.pop("tool_name", None))
            row["text_uid"], row["content_sha256"] = uid, digest
            row = {k: v for k, v in row.items() if k not in ("text_uid", "content_sha256") or v is not None}
            columns = list(row)
            update = conflict
            if mode == "REPLACE":
                update = " ON CONFLICT (store_id) DO UPDATE SET " + ", ".join(
                    f"{c} = EXCLUDED.{c}" for c in columns if c != "store_id")
            if self.isolation_level is not None:
                self._begin()
            self._statement(cur, f"INSERT INTO ctx.lcm_messages ({', '.join(columns)}) VALUES "
                                 f"({', '.join(['%s'] * len(columns))}){update} RETURNING store_id",
                            [_adapt(row[c]) for c in columns], "store_id")
            stored.extend(self._returned)
        self._message_uid = None
        cur._set_result(None, [], len(stored), stored[-1] if stored else None)

    def _message_content_update(self, cur: PgCursor, template: Template, params: Any) -> None:
        m = re.match(r"\s*UPDATE\s+messages\s+SET\s+(.*?)(?:\s+WHERE\s+(.*?))?;?\s*$", template.parts[0],
                     re.IGNORECASE | re.DOTALL)
        assignments = _split_top_level(m.group(1))
        set_params = sum(_mask(a)[2].__len__() for a in assignments)
        params = list(params or ())
        targets = [row[0] for row in self.execute(f"SELECT store_id FROM messages WHERE {m.group(2) or 'TRUE'}",
                                                  params[set_params:]).fetchall()]
        changed = 0
        for store_id in targets:
            current = self.execute("SELECT session_id, role, tool_call_id, tool_calls, tool_name, content FROM messages "
                                   "WHERE store_id = ?", (store_id,)).fetchone()
            values, pos = {}, 0
            for assignment in assignments:
                name, expr = (x.strip() for x in assignment.split("=", 1))
                count = len(_mask(expr)[2])
                values[name] = (self.execute(f"SELECT {expr}", params[pos:pos + count]).fetchone()[0]
                                if expr != "?" else params[pos])
                pos += count
            text = dict(zip(("session_id", "role", "tool_call_id", "tool_calls", "tool_name", "content"), current))
            for field in ("role", "tool_call_id", "tool_calls", "tool_name", "content"):
                if field in values:
                    text[field] = values.pop(field)
            uid, digest = self._text.resolve(message_uid=None, session_id=text["session_id"], role=text["role"],
                                             content=text["content"], tool_call_id=text["tool_call_id"],
                                             tool_calls=text["tool_calls"], tool_name=text["tool_name"])
            values["text_uid"], values["content_sha256"] = uid, digest
            names = list(values)
            if self.isolation_level is not None:
                self._begin()
            self._statement(cur, f"UPDATE ctx.lcm_messages SET {', '.join(f'{n} = %s' for n in names)} "
                                 "WHERE store_id = %s", [_adapt(values[n]) for n in names] + [store_id], None)
            changed += 1
        cur._set_result(None, [], changed, None)


def _split_top_level(text: str) -> list[str]:
    """*text* split at the commas outside parentheses and string literals."""
    masked, literals, slots = _mask(text)
    parts, depth, start = [], 0, 0
    for i, ch in enumerate(masked):
        depth += {"(": 1, ")": -1}.get(ch, 0)
        if ch == "," and depth == 0:
            parts.append(masked[start:i])
            start = i + 1
    parts.append(masked[start:])

    def restore(piece: str) -> str:
        piece = re.sub(r"\x00(\d+)\x00", lambda m: literals[int(m.group(1))].replace("%%", "%"), piece)
        return re.sub(r"\x01(\d+)\x01", lambda m: slots[int(m.group(1))], piece).strip()
    return [restore(p) for p in parts]

_KINDS: dict[str, Callable[..., None]] = {
    "skip": PgConnection._skip,
    "control": PgConnection._control,
    "pragma": PgConnection._pragma,
    "message_insert": PgConnection._message_insert,
    "message_content_update": PgConnection._message_content_update,
}


def split_statements(script: str) -> list[str]:
    """The statements of an ``executescript`` script; a ``CREATE TRIGGER ... END;`` body stays whole."""
    masked, literals, _slots = _mask(script)
    out, depth_trigger, current = [], False, []
    for piece in re.split(r"(;)", masked):
        if piece == ";":
            current.append(piece)
            joined = "".join(current)
            if depth_trigger and not re.search(r"\bEND\s*;\s*$", joined, re.IGNORECASE):
                continue
            out.append(joined)
            current, depth_trigger = [], False
            continue
        if not current and re.match(r"\s*CREATE\s+(TEMP\s+|TEMPORARY\s+)?TRIGGER\b", piece, re.IGNORECASE):
            depth_trigger = True
        current.append(piece)
    if "".join(current).strip():
        out.append("".join(current))

    def restore(text: str) -> str:
        text = re.sub(r"\x00(\d+)\x00", lambda m: literals[int(m.group(1))].replace("%%", "%"), text)
        return re.sub(r"\x01(\d+)\x01", lambda m: _slots[int(m.group(1))], text)
    return [restore(s).strip() for s in out if restore(s).strip().strip(";").strip()]

# Tests only: the suite plugin (lcm_x_store_pytest) publishes the paths it routed, so a script a test runs as a
# child process routes them to the same agents.
TEST_ROUTES_ENV = "LINA_LCM_X_TEST_ROUTES"


def _routes_from_environment() -> None:
    raw = os.environ.get(TEST_ROUTES_ENV)
    if raw:
        for path, agent in json.loads(raw).items():
            route(path, agent)


_routes_from_environment()

