"""``lina memory eval-search``: top-5 hit rate per source, today's search against the one-model search (lina#19).

The questions file is JSON Lines, one question per line:

    {"id": "q01", "source": "agent", "agent": "default", "question": "...", "expected": ["<ref>", ...]}
    {"id": "q31", "source": "memory", "agent": "default", "question": "...", "expected": ["<ref>"],
     "honcho": {"workspace": "hermes", "observer": "...", "observed": "..."}}

A ref names the item that answers the question: the ``memory.messages`` uid for ``agent`` and ``messages``, the
Honcho conclusion id (``ltm.documents.id``) for ``memory``. A question counts as a hit when one of its refs is
in the first five results.

* old: what each source searches with today. ``agent`` and ``messages``: the store's full-text search
  (``MessageStore.search_groups``, newest first, as ``session_search`` asks it), as the question's agent.
  ``memory``: Honcho's conclusion search (``POST /v3/workspaces/<ws>/conclusions/query``) at ``--honcho-url``;
  without it the old ``memory`` column is not run.
* new: one query vector from the configured model against every row of ``vec.embeddings`` the question's
  agent and Honcho workspace own, all kinds ranked together by cosine distance.
"""

from __future__ import annotations

import json
from collections.abc import Callable, Iterable, Mapping, Sequence
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Optional

TOP_K = 5
SOURCES = ("agent", "messages", "memory")


class EvalError(ValueError):
    pass


@dataclass(frozen=True)
class Question:
    id: str
    source: str
    agent: str
    question: str
    expected: tuple[str, ...]
    honcho: Mapping[str, str] = field(default_factory=dict)


@dataclass
class SourceScore:
    source: str
    questions: int = 0
    old_hits: int = 0
    old_run: int = 0
    new_hits: int = 0

    @property
    def old_rate(self) -> Optional[float]:
        return self.old_hits / self.old_run if self.old_run else None

    @property
    def new_rate(self) -> Optional[float]:
        return self.new_hits / self.questions if self.questions else None


def load_questions(path: Path) -> list[Question]:
    questions, seen = [], set()
    for number, line in enumerate(Path(path).read_text(encoding="utf-8-sig").splitlines(), start=1):
        if not line.strip():
            continue
        try:
            raw = json.loads(line)
            question = Question(id=str(raw["id"]), source=str(raw["source"]), agent=str(raw.get("agent") or "default"),
                                question=str(raw["question"]), expected=tuple(str(r) for r in raw["expected"]),
                                honcho=dict(raw.get("honcho") or {}))
        except (ValueError, KeyError, TypeError) as exc:
            raise EvalError(f"{path}:{number}: not a question line ({exc})") from exc
        if question.source not in SOURCES:
            raise EvalError(f"{path}:{number}: source {question.source!r} is not one of {', '.join(SOURCES)}")
        if not question.expected:
            raise EvalError(f"{path}:{number}: no expected refs")
        if question.id in seen:
            raise EvalError(f"{path}:{number}: question id {question.id!r} appears twice")
        seen.add(question.id)
        questions.append(question)
    return questions


def is_hit(expected: Iterable[str], results: Sequence[str], k: int = TOP_K) -> bool:
    wanted = set(expected)
    return any(ref in wanted for ref in results[:k])


def score(questions: Sequence[Question], old: Mapping[str, Optional[Sequence[str]]],
          new: Mapping[str, Sequence[str]], k: int = TOP_K) -> dict[str, SourceScore]:
    """Hit counts per source. *old*/*new* map a question id to its ranked refs; an old value of None means the
    old path was not run for that question (it counts in neither old_run nor old_hits)."""
    scores = {source: SourceScore(source) for source in SOURCES}
    for question in questions:
        entry = scores[question.source]
        entry.questions += 1
        entry.new_hits += is_hit(question.expected, new.get(question.id, ()), k)
        ranked = old.get(question.id)
        if ranked is not None:
            entry.old_run += 1
            entry.old_hits += is_hit(question.expected, ranked, k)
    return scores


def _rate(value: Optional[float]) -> str:
    return "n/a" if value is None else f"{value:.1%}"


def report_lines(scores: Mapping[str, SourceScore]) -> list[str]:
    lines = [f"{'source':<10}{'questions':>10}{'old top-5':>12}{'new top-5':>12}"]
    for source in SOURCES:
        entry = scores[source]
        if entry.questions:
            lines.append(f"{source:<10}{entry.questions:>10}{_rate(entry.old_rate):>12}{_rate(entry.new_rate):>12}")
    return lines


# ── the two search paths against a live store ──

def _words(question: str) -> list[list[Any]]:
    """The question as ``session_search`` sends it: each word a term, any of them may match."""
    import re

    from hermes_state_store import SearchTerm
    words = [w for w in re.findall(r"\w+", question) if len(w) > 1]
    return [[SearchTerm(word, substring=not word.isascii())] for word in words]


def old_fulltext(conn, dsn_for_agent: Callable[[str], str], question: Question, k: int = TOP_K) -> list[str]:
    from hermes_state_store import MessageStore
    store = MessageStore(question.agent, dsn=dsn_for_agent(question.agent))
    try:
        uids = [uid for _message_id, uid in store.search_groups(_words(question.question), limit=k * 20)]
    finally:
        store.close()
    if not uids:
        return []
    rows = conn.execute("SELECT uid FROM memory.messages WHERE uid = ANY(%s) AND source = %s",
                        (uids, question.source)).fetchall()
    of_source = {row[0] for row in rows}
    return [uid for uid in uids if uid in of_source][:k]


def old_honcho(honcho_url: str, question: Question, k: int = TOP_K, *, api_key: Optional[str] = None) -> list[str]:
    import httpx
    workspace = question.honcho.get("workspace", "hermes")
    body = {"query": question.question, "top_k": k,
            "filters": {"observer": question.honcho.get("observer"), "observed": question.honcho.get("observed")}}
    headers = {"Authorization": f"Bearer {api_key}"} if api_key else {}
    response = httpx.post(f"{honcho_url.rstrip('/')}/v3/workspaces/{workspace}/conclusions/query", json=body,
                          headers=headers, timeout=60)
    response.raise_for_status()
    return [item["id"] for item in response.json()][:k]


_NEW_SQL = """
    SELECT CASE e.kind WHEN 'document' THEN d.id ELSE m.uid END
    FROM vec.embeddings e
    LEFT JOIN vec.chunks c ON e.kind = 'chunk' AND c.chunk_id = e.item_id
    LEFT JOIN memory.messages m ON m.message_id = c.message_ids[1]
    LEFT JOIN ltm.documents d ON e.kind = 'document' AND d.vec_id = e.item_id
    WHERE e.model = %s AND e.agent_id = ANY(%s) AND e.kind IN ('chunk', 'document')
    ORDER BY e.embedding <=> %s::vector
    LIMIT %s"""


def new_vector(conn, query_vector: Sequence[float], model: str, question: Question, k: int = TOP_K) -> list[str]:
    owners = [question.agent, question.honcho.get("workspace", "hermes")]
    literal = "[" + ",".join(repr(float(v)) for v in query_vector) + "]"
    sql = _NEW_SQL
    if conn.execute("SELECT to_regclass('ltm.documents') IS NULL").fetchone()[0]:
        sql = _NEW_SQL.replace("LEFT JOIN ltm.documents d ON e.kind = 'document' AND d.vec_id = e.item_id",
                               "LEFT JOIN (SELECT NULL::text AS id, NULL::bigint AS vec_id) d ON false")
    return [row[0] for row in conn.execute(sql, (model, owners, literal, k)).fetchall() if row[0]]


def evaluate(questions: Sequence[Question], *, conn, client, dsn_for_agent: Callable[[str], str],
             honcho_url: Optional[str], honcho_key: Optional[str] = None) -> tuple[dict, dict]:
    vectors = client.embed([q.question for q in questions])
    old: dict[str, Optional[list[str]]] = {}
    new: dict[str, list[str]] = {}
    for question, vector in zip(questions, vectors, strict=True):
        new[question.id] = new_vector(conn, vector, client.model, question)
        if question.source == "memory":
            old[question.id] = old_honcho(honcho_url, question, api_key=honcho_key) if honcho_url else None
        else:
            old[question.id] = old_fulltext(conn, dsn_for_agent, question)
    return old, new


def run(args) -> int:
    """The CLI entry: ``lina memory eval-search --questions <file.jsonl>``."""
    import psycopg

    from agent.embedding_client import EmbeddingClient, EmbeddingError, EmbeddingSettings
    from hermes_cli.config import get_env_value
    from hermes_state_store import StoreUnavailableError, resolve_dsn, resolve_owner_dsn
    try:
        questions = load_questions(Path(args.questions))
        settings = EmbeddingSettings.from_config()
        with EmbeddingClient(settings) as client, psycopg.connect(resolve_owner_dsn(), autocommit=True) as conn:
            old, new = evaluate(questions, conn=conn, client=client, dsn_for_agent=lambda _agent: resolve_dsn(),
                                honcho_url=args.honcho_url, honcho_key=get_env_value("HONCHO_API_KEY"))
    except (EvalError, EmbeddingError, StoreUnavailableError, psycopg.Error, OSError) as exc:
        print(f"eval-search: {exc}")
        return 1
    scores = score(questions, old, new)
    for line in report_lines(scores):
        print(line)
    if args.json:
        Path(args.json).write_text(json.dumps({
            "questions": len(questions), "model": settings.model, "dimensions": settings.dimensions,
            "sources": {s: {"questions": v.questions, "old_run": v.old_run, "old_hits": v.old_hits,
                            "new_hits": v.new_hits} for s, v in scores.items()},
            "results": {q.id: {"old": old[q.id], "new": new[q.id], "expected": list(q.expected)} for q in questions},
        }, ensure_ascii=False, indent=2), encoding="utf-8")
    return 0
