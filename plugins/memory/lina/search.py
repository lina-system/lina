"""``memory_search`` and ``memory_open``: one search over the memory layer's sources, and the original behind each hit
(lina#22).

The sources:

- ``agent``: this agent's own turns in ``memory.messages`` (user and assistant text);
- ``messages``: the owner's external messages in ``memory.messages`` (``scope='agents'``, readable by every agent);
- ``honcho``: Honcho's raw messages and its conclusions, the lasting memory;
- ``lcm``: the LCM-X summaries of this agent's session, through LCM-X's own tools (``lcm_grep``, ``lcm_expand_query``,
  ``lcm_expand``), so the search does not care which database LCM-X keeps them in.

Each source ranks its own hits; the lists are merged by reciprocal rank fusion. A source that is off or fails is left
out and named in ``skipped``: the search never fails because one source did. Every hit carries an opaque ``ref`` that
``memory_open`` resolves: ``msg:<memory.messages uid>``, ``ltm:m:<message id>:<Honcho session>``, ``ltm:c:<conclusion
id>``, ``ctx:<summary node id>:<LCM session>``.
"""

from __future__ import annotations

import json
import logging
import re
from collections.abc import Callable, Sequence
from dataclasses import dataclass
from datetime import UTC, datetime
from typing import Any, Optional

logger = logging.getLogger(__name__)

SOURCES = ("agent", "messages", "honcho", "lcm")
_STORE_SOURCES = ("agent", "messages")
RRF_K = 60
EXCERPT_CHARS = 240
DEFAULT_LIMIT = 10
MAX_LIMIT = 50
SEARCH_TOOL = "memory_search"
OPEN_TOOL = "memory_open"
TOOL_NAMES = frozenset({SEARCH_TOOL, OPEN_TOOL})

SEARCH_SCHEMA = {
    "name": SEARCH_TOOL,
    "description": (
        "Search all of memory at once: this agent's past conversations, the owner's external messages "
        "(KakaoTalk, Discord, iMessage, LINE), long-term memory (Honcho messages and conclusions) and the "
        "conversation summaries. Returns ranked hits, each with a short excerpt and a ref; pass the ref to "
        "memory_open for the full original. Sources that are off or fail are listed in 'skipped'."
    ),
    "parameters": {
        "type": "object",
        "properties": {
            "query": {"type": "string", "description": "Words to look for; short words match inside longer ones."},
            "sources": {"type": "array", "items": {"type": "string", "enum": list(SOURCES)},
                        "description": "Only these sources (default: all)."},
            "limit": {"type": "integer", "minimum": 1, "maximum": MAX_LIMIT,
                      "description": f"Most hits to return (default {DEFAULT_LIMIT})."},
        },
        "required": ["query"],
    },
}

OPEN_SCHEMA = {
    "name": OPEN_TOOL,
    "description": "Open the original of a memory_search hit by its ref: the full message, conclusion or summary.",
    "parameters": {
        "type": "object",
        "properties": {"ref": {"type": "string", "description": "The ref of a memory_search hit."}},
        "required": ["ref"],
    },
}


class SourceOff(RuntimeError):
    """A source that is off or not set up for this agent."""


class RefError(ValueError):
    """A ref that is malformed or names nothing this agent can open."""


@dataclass(frozen=True)
class Hit:
    source: str
    ref: str
    excerpt: str
    time: Optional[str]
    author: Optional[str]
    speaker: str  # person | agent | conclusion | summary


@dataclass(frozen=True)
class Readers:
    """How the sources are reached, bound to one agent by the provider.

    ``store()`` is the agent's ``MessageStore``; ``honcho()`` returns ``(manager, session key)``; ``lcm(operation)``
    runs ``operation(engine)`` with the agent's LCM-X engine; ``embedder()`` is an ``EmbeddingClient`` or None when
    no embedding endpoint is configured. Each raises ``SourceOff`` when its source is off here."""
    store: Callable[[], Any]
    honcho: Callable[[], tuple[Any, str]]
    lcm: Callable[[Callable[[Any], Any]], Any]
    embedder: Callable[[], Any]


def utc_iso(value: Any) -> Optional[str]:
    """A time as ISO 8601 in UTC: from a datetime, an epoch number or an ISO string."""
    if value is None or value == "":
        return None
    if isinstance(value, (int, float)) and not isinstance(value, bool):
        value = datetime.fromtimestamp(float(value), tz=UTC)
    elif isinstance(value, str):
        try:
            value = datetime.fromisoformat(value)
        except ValueError:
            return value
    if isinstance(value, datetime):
        value = value if value.tzinfo else value.replace(tzinfo=UTC)
        return value.astimezone(UTC).isoformat(timespec="seconds")
    return str(value)


def excerpt(text: str, terms: Sequence[str], width: int = EXCERPT_CHARS) -> str:
    """About *width* characters of *text* around the first term it contains, whitespace collapsed."""
    flat = re.sub(r"\s+", " ", text or "").strip()
    if len(flat) <= width:
        return flat
    lowered = flat.lower()
    found = [i for i in (lowered.find(t.lower()) for t in terms if t) if i >= 0]
    start = max(0, min(found) - width // 3) if found else 0
    start = min(start, len(flat) - width)
    return ("…" if start else "") + flat[start:start + width].strip() + ("…" if start + width < len(flat) else "")


def fuse(lists: Sequence[Sequence[Hit]], limit: int) -> list[dict[str, Any]]:
    """Reciprocal rank fusion of ranked lists: a hit scores ``1 / (RRF_K + rank)`` in each list it is in."""
    scores: dict[str, float] = {}
    hits: dict[str, Hit] = {}
    for ranked in lists:
        for rank, hit in enumerate(ranked, 1):
            scores[hit.ref] = scores.get(hit.ref, 0.0) + 1.0 / (RRF_K + rank)
            hits.setdefault(hit.ref, hit)
    newest_first = sorted(hits, key=lambda ref: hits[ref].time or "", reverse=True)
    ordered = sorted(newest_first, key=lambda ref: scores[ref], reverse=True)[:limit]
    return [{"source": hits[ref].source, "ref": ref, "excerpt": hits[ref].excerpt, "time": hits[ref].time,
             "author": hits[ref].author, "speaker": hits[ref].speaker, "score": round(scores[ref], 6)}
            for ref in ordered]


def _row_hit(row: Any, terms: Sequence[str]) -> Hit:
    if row.source == "agent" and row.role == "assistant":
        speaker, author = "agent", row.author or (f"agent:{row.agent_id}" if row.agent_id else None)
    else:
        speaker, author = "person", row.author
    return Hit(row.source, f"msg:{row.uid}", excerpt(row.content or "", terms), utc_iso(row.at), author, speaker)


def _reason(exc: BaseException) -> str:
    text = str(exc).strip() or type(exc).__name__
    return text if isinstance(exc, SourceOff) else f"{type(exc).__name__}: {text}"[:300]


class MemorySearch:
    def __init__(self, readers: Readers) -> None:
        self._readers = readers

    # ── memory_search ──

    def search(self, query: str, sources: Optional[Sequence[str]] = None,
               limit: Optional[int] = None) -> dict[str, Any]:
        query = (query or "").strip()
        if not query:
            raise ValueError("memory_search needs a query")
        wanted = list(SOURCES) if not sources else list(dict.fromkeys(sources))
        unknown = [s for s in wanted if s not in SOURCES]
        if unknown:
            raise ValueError(f"unknown source(s) {', '.join(map(str, unknown))}; known: {', '.join(SOURCES)}")
        limit = max(1, min(MAX_LIMIT, int(limit or DEFAULT_LIMIT)))
        terms = query.split()
        lists: list[list[Hit]] = []
        skipped: list[dict[str, str]] = []

        def leg(names: Sequence[str], run: Callable[[], list[list[Hit]]]) -> None:
            try:
                lists.extend(run())
            except Exception as exc:  # health: allow BLE001 -- one source failing must not fail the search
                if not isinstance(exc, SourceOff):
                    logger.warning("memory_search: %s skipped: %s", "+".join(names), exc)
                skipped.extend({"source": name, "reason": _reason(exc)} for name in names)

        store_sources = [s for s in _STORE_SOURCES if s in wanted]
        if "messages" in store_sources:
            from hermes_cli.external_messages import enabled
            if not enabled():
                store_sources.remove("messages")
                skipped.append({"source": "messages", "reason": "External messages are off."})
        if store_sources:
            leg(store_sources, lambda: self._store_lists(query, terms, store_sources, limit))
            if not any(s["source"] in store_sources for s in skipped):
                leg(["vectors"], lambda: self._vector_lists(query, terms, store_sources, limit))
        if "honcho" in wanted:
            leg(["honcho"], lambda: self._honcho_lists(query, terms, limit))
        if "lcm" in wanted:
            leg(["lcm"], lambda: self._lcm_lists(query, terms, limit))
        return {"query": query, "results": fuse(lists, limit), "skipped": skipped}

    def _store_lists(self, query: str, terms: Sequence[str], sources: Sequence[str], limit: int) -> list[list[Hit]]:
        rows = self._readers.store().search_memory(query, sources=sources, limit=limit * len(sources))
        return [[_row_hit(row, terms) for row in rows if row.source == source] for source in sources]

    def _vector_lists(self, query: str, terms: Sequence[str], sources: Sequence[str], limit: int) -> list[list[Hit]]:
        """The embedding leg: only when an embedding endpoint is configured and rows are embedded with its model."""
        store = self._readers.store()
        embedder = self._readers.embedder()
        if embedder is None or not store.has_memory_vectors(model=embedder.model, sources=sources):
            return []
        rows = store.search_memory_vectors(embedder.embed_one(query), model=embedder.model, sources=sources,
                                           limit=limit * len(sources))
        return [[_row_hit(row, terms) for row in rows if row.source == source] for source in sources]

    def _honcho_lists(self, query: str, terms: Sequence[str], limit: int) -> list[list[Hit]]:
        manager, session_key = self._readers.honcho()
        messages = [Hit("honcho", f"ltm:m:{m['id']}:{m['session_id']}", excerpt(m["content"], terms),
                        utc_iso(m.get("created_at")), m.get("peer_id") or None, m.get("speaker") or "person")
                    for m in manager.search_messages(session_key, query, limit=limit)]
        conclusions = [Hit("honcho", f"ltm:c:{c['id']}", excerpt(c["content"], terms), utc_iso(c.get("created_at")),
                           c.get("observer_id") or None, "conclusion")
                       for c in manager.search_conclusions(session_key, query, limit=limit)]
        return [messages, conclusions]

    def _lcm_lists(self, query: str, terms: Sequence[str], limit: int) -> list[list[Hit]]:
        found = self._lcm_tool("lcm_grep", {"query": query, "limit": limit, "sort": "relevance"})
        return [[Hit("lcm", f"ctx:{r['node_id']}:{r.get('session_id') or ''}", excerpt(r.get("snippet") or "", terms),
                     utc_iso(r.get("latest_at") or r.get("earliest_at")), None, "summary")
                 for r in found.get("results") or () if r.get("type") == "summary"]]

    def _lcm_tool(self, name: str, args: dict[str, Any]) -> dict[str, Any]:
        raw = self._readers.lcm(lambda engine: engine.handle_tool_call(name, args))
        payload = json.loads(raw) if isinstance(raw, str) else raw
        if not isinstance(payload, dict):
            raise RuntimeError(f"{name} answered {type(payload).__name__}, not an object")
        if payload.get("error"):
            raise RuntimeError(f"{name}: {payload['error']}")
        return payload

    # ── memory_open ──

    def open(self, ref: str) -> dict[str, Any]:
        ref = (ref or "").strip()
        kind, _, rest = ref.partition(":")
        opener = {"msg": self._open_message, "ltm": self._open_honcho, "ctx": self._open_summary}.get(kind)
        if opener is None or not rest:
            raise RefError(f"malformed ref {ref!r}: expected msg:…, ltm:… or ctx:… from memory_search")
        return {"ref": ref, **opener(ref, rest)}

    def _open_message(self, ref: str, uid: str) -> dict[str, Any]:
        row = self._readers.store().open_memory(uid)
        if row is None:
            raise RefError(f"no message for ref {ref!r} (unknown, or not readable by this agent)")
        if row.source == "messages":
            from hermes_cli.external_messages import enabled
            if not enabled():
                raise SourceOff("External messages are off.")
        opened = {"source": row.source, "time": utc_iso(row.at), "author": row.author, "role": row.role,
                  "conversation_id": row.conversation_id, "session_id": row.session_id}
        if row.forgotten:
            return {**opened, "forgotten": True, "text": None, "note": "This message was forgotten; its text is gone."}
        return {**opened, "forgotten": False, "text": row.content}

    def _open_honcho(self, ref: str, rest: str) -> dict[str, Any]:
        sub, _, ident = rest.partition(":")
        manager, session_key = self._readers.honcho()
        if sub == "m":
            message_id, _, honcho_session = ident.partition(":")
            if not (message_id and honcho_session):
                raise RefError(f"malformed ref {ref!r}: a Honcho message ref is ltm:m:<id>:<session>")
            item = manager.get_message(session_key, honcho_session, message_id)
            return {"source": "honcho", "kind": "message", "text": item["content"], "time": utc_iso(item["created_at"]),
                    "author": item["peer_id"], "speaker": item["speaker"], "session_id": item["session_id"]}
        if sub == "c" and ident:
            item = manager.get_conclusion(session_key, ident)
            return {"source": "honcho", "kind": "conclusion", "text": item["content"],
                    "time": utc_iso(item["created_at"]), "observer": item["observer_id"],
                    "observed": item["observed_id"], "session_id": item["session_id"]}
        raise RefError(f"malformed ref {ref!r}: a Honcho ref is ltm:m:<id>:<session> or ltm:c:<id>")

    def _open_summary(self, ref: str, rest: str) -> dict[str, Any]:
        node, _, session_id = rest.partition(":")
        if not (node.isdigit() and session_id):
            raise RefError(f"malformed ref {ref!r}: a summary ref is ctx:<node id>:<session>")
        node_id = int(node)
        evidence = self._lcm_tool("lcm_expand_query", {
            "prompt": "Open this summary verbatim.", "node_ids": [node_id], "session_ids": [session_id],
            "output": "evidence"})
        summary = _summary_block(evidence, node_id)
        if summary is None:
            raise RefError(f"no summary for ref {ref!r}")
        expanded = self._lcm_tool("lcm_expand", {"node_id": node_id, "session_id": session_id})
        sources = [item for item in expanded.get("expanded") or () if isinstance(item, dict)]
        key = "store_id" if expanded.get("source_type") == "messages" else "node_id"
        return {"source": "lcm", "kind": "summary", "text": summary.get("summary"),
                "truncated": bool(summary.get("summary_truncated")), "session_id": session_id,
                "depth": expanded.get("depth"), "source_type": expanded.get("source_type"),
                "source_ids": [item.get(key) for item in sources if item.get(key) is not None]}


def _summary_block(evidence: Any, node_id: int) -> Optional[dict[str, Any]]:
    """The ``type: summary`` block of *node_id* anywhere in an ``lcm_expand_query`` evidence payload."""
    if isinstance(evidence, dict):
        if evidence.get("type") == "summary" and evidence.get("node_id") == node_id and "summary" in evidence:
            return evidence
        values = evidence.values()
    elif isinstance(evidence, list):
        values = evidence
    else:
        return None
    for value in values:
        found = _summary_block(value, node_id)
        if found is not None:
            return found
    return None


def handle_tool_call(search: MemorySearch, name: str, args: dict[str, Any]) -> str:
    """Run memory_search or memory_open; a bad argument or ref, or a source that cannot open it, is a tool error."""
    from tools.registry import tool_error
    try:
        if name == SEARCH_TOOL:
            sources = args.get("sources")
            if sources is not None and not isinstance(sources, list):
                return tool_error("sources must be a list of source names")
            result = search.search(str(args.get("query") or ""), sources=sources, limit=args.get("limit"))
        else:
            result = search.open(str(args.get("ref") or ""))
    except (ValueError, TypeError, SourceOff) as exc:
        return tool_error(str(exc))
    except Exception as exc:  # health: allow BLE001 -- the source's failure is the tool's answer
        logger.warning("%s failed: %s", name, exc, exc_info=True)
        return tool_error(f"{name} failed: {_reason(exc)}")
    return json.dumps(result, ensure_ascii=False, default=str)
