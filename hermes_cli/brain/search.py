"""Brain-first lookup (lina#446; design lina#439 section 3, lina#438 item 6): find the brain pages for a query, open
one page with its timeline, and walk its typed links.

Two legs rank pages, each on its own; the agent's search fuses them with its other sources
(``plugins/memory/lina/search.py``):

- **semantic**: one vector per page in ``vec.embeddings`` (kind ``page``, ``item_id`` = ``brain.pages.page_id``,
  owner ``*``, migration 0014) of the page's search text (``search_text``: title, names, compiled truth), with the
  home's embedding model; nearest first.
- **keyword**: BM25 over the page's keyword terms in ``brain.search_terms`` (migration 0020). Korean writes particles
  and endings onto words (서윤이, 푸른소리랑) and joins or spaces compounds at will (기타줄, 기타 줄), so a term is
  not a space-separated word: with Kiwi (the ``korean`` extra) it is a morpheme (a noun as written, a predicate in
  its dictionary form) or a run of nouns joined; without Kiwi it is a character bigram of each Hangul run. Both sets
  are indexed; a query uses Kiwi's when this process can import Kiwi, else the bigrams. A page's names count
  ``NAME_WEIGHT`` times.

``lina brain search-index`` writes both as the store owner, the only writer of the shared brain (docs/brain.md):
the vectors of pages that have none for the model (a changed file drops its vector, trigger ``pages_drop_vector``),
the terms of pages whose file changed. The 20-query Korean set under ``tests/fixtures/brain_search`` measured the
legs (``lina brain search-eval``): see docs/brain.md, Search.

A page's grade (lina#440) is its frontmatter ``grade`` when that is one of the grades, else ``external``: nothing
else grades compiled truth yet, and an item of unknown origin is external (``plugins/memory/lina/grade.py``).
"""

from __future__ import annotations

import hashlib
import importlib.util
import json
import re
import unicodedata
from collections import Counter
from collections.abc import Callable, Iterable, Sequence
from dataclasses import dataclass, field
from datetime import date
from pathlib import Path
from typing import Any, Optional

from hermes_cli.brain.page import GRADES, alias_key

ANALYZERS = ("kiwi", "bigram")
LEGS = ("semantic", "keyword")
NAME_WEIGHT = 3
BM25_K1 = 1.2
BM25_B = 0.75
# What one page vector is made of: the head of a long page says what it is about.
EMBED_TEXT_CHARS = 4000
EMBED_BATCH = 32
TIMELINE_ENTRIES = 20
MAX_TIMELINE_ENTRIES = 100
MAX_DEPTH = 2
MAX_LINK_NODES = 50

_NOUN_TAGS = frozenset({"NNG", "NNP", "NR", "XR", "SH", "SL", "SN"})
_PREDICATE_TAGS = frozenset({"VV", "VA"})
_WIKI_LINK = re.compile(r"\[\[([^\]|#]+)(?:#[^\]|]*)?(?:\|([^\]]+))?\]\]")
_RUNS = re.compile(r"[가-힣]+|[^\W_가-힣]+")
_HANGUL = re.compile(r"[가-힣]")


class BrainSearchError(ValueError):
    """A name that names no page, or names several."""


def _normal(text: str) -> str:
    return unicodedata.normalize("NFC", text or "").casefold()


def kiwi():
    """This process's Kiwi when the ``korean`` extra is installed, else None. Never installs it: a search inside a
    turn falls back to bigrams rather than wait for an install."""
    if importlib.util.find_spec("kiwipiepy") is None:
        return None
    from hermes_cli.korean.lookup import analyzer
    return analyzer()


def _bigrams(text: str) -> Counter:
    found: Counter = Counter()
    for run in _RUNS.findall(_normal(text)):
        if not _HANGUL.match(run) or len(run) == 1:
            found[run] += 1
            continue
        found.update(run[i:i + 2] for i in range(len(run) - 1))
    return found


def _tag(token: Any) -> str:
    return str(token.tag).split("-")[0]


def _kiwi_terms(text: str) -> Counter:
    found: Counter = Counter()
    run: list[str] = []
    for token in kiwi().tokenize(unicodedata.normalize("NFC", text or "")):
        tag = _tag(token)
        if tag in _NOUN_TAGS:
            found[_normal(token.form)] += 1
            run.append(_normal(token.form))
            continue
        if len(run) > 1:
            found["".join(run)] += 1
        run = []
        if tag in _PREDICATE_TAGS:
            found[_normal(token.lemma)] += 1
    if len(run) > 1:
        found["".join(run)] += 1
    return found


def terms(text: str, analyzer: str) -> Counter:
    """The keyword terms of *text* with their counts."""
    return _kiwi_terms(text) if analyzer == "kiwi" else _bigrams(text)


def plain(text: str) -> str:
    """Page text with wiki links as their words (``[[여름 정원|앨범]]`` reads 앨범)."""
    return _WIKI_LINK.sub(lambda m: (m.group(2) or m.group(1)).strip(), text or "")


def search_text(title: str, names: Sequence[str], compiled: str) -> str:
    """What a page's vector is made of: its title, its other names, then its compiled truth."""
    others = [name for name in dict.fromkeys(names) if alias_key(name) != alias_key(title)]
    head = title + (f"\n{', '.join(others)}" if others else "")
    return (head + "\n\n" + plain(compiled).strip())[:EMBED_TEXT_CHARS]


def page_terms(title: str, names: Sequence[str], compiled: str, analyzer: str) -> Counter:
    found = terms(plain(compiled), analyzer)
    for name in dict.fromkeys([title, *names]):
        for term, count in terms(name, analyzer).items():
            found[term] += NAME_WEIGHT * count
    return found


def page_grade(frontmatter: Any) -> str:
    value = frontmatter.get("grade") if isinstance(frontmatter, dict) else None
    return value if value in GRADES else "external"


def _vector(values: Sequence[float]) -> str:
    return "[" + ",".join(repr(float(v)) for v in values) + "]"


# ── the index (store owner) ──

@dataclass
class IndexReport:
    pages: int = 0
    rewritten_terms: dict[str, int] = field(default_factory=dict)
    vectors: int = 0
    vector_model: Optional[str] = None
    no_vectors_reason: Optional[str] = None

    def as_json(self) -> dict:
        return {"pages": self.pages, "rewritten_terms": self.rewritten_terms, "vectors": self.vectors,
                "vector_model": self.vector_model, "no_vectors_reason": self.no_vectors_reason}


def _pages_to_index(conn, root_id: Optional[int]) -> list[tuple]:
    where = "" if root_id is None else " WHERE p.root_id = %s"
    return conn.execute(
        "SELECT p.slug, p.page_id, p.title, p.compiled_truth, p.content_sha256,"
        " coalesce(array_agg(a.alias ORDER BY a.alias) FILTER (WHERE a.origin <> 'slug'), '{}')"
        " FROM brain.pages AS p LEFT JOIN brain.aliases AS a USING (slug)" + where +
        " GROUP BY p.slug ORDER BY p.slug", () if root_id is None else (root_id,)).fetchall()


def _index_key(sha: str, names: Sequence[str]) -> str:
    """What the keyword index of a page was built from: its content and its other names (a merge moves names to a
    page without changing its content)."""
    return hashlib.sha256("\0".join([sha, *names]).encode("utf-8")).hexdigest()


def _index_terms(conn, pages: list[tuple], analyzer: str) -> int:
    indexed = dict(conn.execute("SELECT slug, content_sha256 FROM brain.search_docs WHERE analyzer = %s",
                                (analyzer,)).fetchall())
    stale = [page for page in pages if indexed.get(page[0]) != _index_key(page[4], page[5])]
    with conn.transaction(), conn.cursor() as cur:
        cur.executemany("DELETE FROM brain.search_docs WHERE slug = %s AND analyzer = %s",
                        [(page[0], analyzer) for page in stale if page[0] in indexed])
        # A page whose terms were built from other names has a vector built from them too: embed it again.
        cur.execute("DELETE FROM vec.embeddings WHERE kind = 'page' AND item_id = ANY(%s)",
                    ([page[1] for page in stale if page[0] in indexed],))
        for slug, _page_id, title, compiled, sha, names in stale:
            found = page_terms(title, names, compiled, analyzer)
            cur.execute("INSERT INTO brain.search_docs (slug, analyzer, content_sha256, length) "
                        "VALUES (%s, %s, %s, %s)", (slug, analyzer, _index_key(sha, names), sum(found.values())))
            cur.executemany("INSERT INTO brain.search_terms (slug, analyzer, term, tf) VALUES (%s, %s, %s, %s)",
                            [(slug, analyzer, term, count) for term, count in sorted(found.items())])
    return len(stale)


def _index_vectors(conn, pages: list[tuple], embedder: Any) -> int:
    have = {row[0] for row in conn.execute(
        "SELECT item_id FROM vec.embeddings WHERE kind = 'page' AND model = %s", (embedder.model,)).fetchall()}
    missing = [page for page in pages if page[1] not in have]
    for start in range(0, len(missing), EMBED_BATCH):
        batch = missing[start:start + EMBED_BATCH]
        vectors = embedder.embed([search_text(title, names, compiled)
                                  for _slug, _id, title, compiled, _sha, names in batch])
        with conn.transaction(), conn.cursor() as cur:
            cur.executemany(
                "INSERT INTO vec.embeddings (kind, item_id, agent_id, model, dim, embedding) "
                "VALUES ('page', %s, '*', %s, %s, %s::vector) ON CONFLICT (kind, item_id, model) DO NOTHING",
                [(page[1], embedder.model, len(vector), _vector(vector)) for page, vector in zip(batch, vectors)])
    return len(missing)


def build_index(conn, embedder: Any, *, root_id: Optional[int] = None,
                analyzers: Sequence[str] = ANALYZERS) -> IndexReport:
    """Bring the keyword terms and page vectors of the brain (or of one imported directory) up to date with its
    pages. *embedder* is an ``EmbeddingClient`` or None (then no vectors are written). Kiwi terms need Kiwi."""
    pages = _pages_to_index(conn, root_id)
    report = IndexReport(pages=len(pages))
    for analyzer in analyzers:
        if analyzer == "kiwi" and kiwi() is None:
            continue
        report.rewritten_terms[analyzer] = _index_terms(conn, pages, analyzer)
    if embedder is None:
        report.no_vectors_reason = "no embedding endpoint is configured (embedding: in config.yaml)"
    else:
        report.vector_model = embedder.model
        report.vectors = _index_vectors(conn, pages, embedder)
    return report


# ── reading (any agent) ──

@dataclass(frozen=True)
class PageHit:
    slug: str
    title: str
    type: str
    path: str
    dated: Optional[date]
    compiled_truth: str
    grade: str


_PAGE_FIELDS = "p.slug, p.title, p.type, p.path, p.dated, p.compiled_truth, p.frontmatter"


def _page_hit(row: Sequence[Any]) -> PageHit:
    return PageHit(row[0], row[1], row[2], row[3], row[4], row[5], page_grade(row[6]))


def _by_type(types: Optional[Sequence[str]]) -> tuple[str, list]:
    return ("", []) if not types else (" AND p.type = ANY(%s)", [list(types)])


class BrainReader:
    """Reads the brain over a connection from *connect* (role ``lina_app`` in an agent: it may SELECT the brain and
    the shared page vectors). *legs* and *analyzer* choose what ranks pages; the defaults are what agents get, the
    others are for measuring (``lina brain search-eval``)."""

    def __init__(self, connect: Callable[[], Any], *, legs: Sequence[str] = LEGS,
                 analyzer: Optional[str] = None) -> None:
        self._connect = connect
        self._conn = None
        self.legs = tuple(legs)
        self._analyzer = analyzer

    def _db(self):
        if self._conn is None or self._conn.closed:
            self._conn = self._connect()
        return self._conn

    def close(self) -> None:
        if self._conn is not None:
            self._conn.close()
            self._conn = None

    def analyzer(self) -> str:
        """The analyzer queries use: the one asked for, else Kiwi when importable and indexed, else bigrams."""
        if self._analyzer:
            return self._analyzer
        if kiwi() is not None and self._db().execute(
                "SELECT 1 FROM brain.search_docs WHERE analyzer = 'kiwi' LIMIT 1").fetchone():
            return "kiwi"
        return "bigram"

    def unindexed(self, model: Optional[str]) -> list[str]:
        """The legs that have nothing to read: no page terms, or no page vector of *model* (None: no embedder)."""
        conn = self._db()
        missing = []
        if "keyword" in self.legs and not conn.execute("SELECT 1 FROM brain.search_docs LIMIT 1").fetchone():
            missing.append("keyword")
        if "semantic" in self.legs and (model is None or not conn.execute(
                "SELECT 1 FROM vec.embeddings WHERE kind = 'page' AND model = %s LIMIT 1", (model,)).fetchone()):
            missing.append("semantic")
        return missing

    def semantic(self, embedding: Sequence[float], *, model: str, limit: int,
                 types: Optional[Sequence[str]] = None) -> list[PageHit]:
        from hermes_state_store import HNSW_FILTERED_SEARCH
        by_type, args = _by_type(types)
        conn = self._db()
        with conn.transaction():
            for setting in HNSW_FILTERED_SEARCH:
                conn.execute(setting)
            rows = conn.execute(
                f"SELECT {_PAGE_FIELDS} FROM vec.embeddings AS e JOIN brain.pages AS p ON p.page_id = e.item_id"
                f" WHERE e.kind = 'page' AND e.model = %s{by_type} ORDER BY e.embedding <=> %s::vector LIMIT %s",
                (model, *args, _vector(embedding), int(limit))).fetchall()
        return [_page_hit(row) for row in rows]

    def keyword(self, query: str, *, limit: int, types: Optional[Sequence[str]] = None) -> list[PageHit]:
        analyzer = self.analyzer()
        wanted = sorted(terms(query, analyzer))
        if not wanted:
            return []
        by_type, args = _by_type(types)
        rows = self._db().execute(
            "WITH n AS (SELECT count(*)::float AS docs, avg(length)::float AS avgdl FROM brain.search_docs"
            "           WHERE analyzer = %s),"
            " df AS (SELECT term, count(*)::float AS df FROM brain.search_terms"
            "        WHERE analyzer = %s AND term = ANY(%s) GROUP BY term),"
            " scored AS (SELECT t.slug, sum(ln(1 + (n.docs - df.df + 0.5) / (df.df + 0.5))"
            "                  * t.tf * (%s + 1) / (t.tf + %s * (1 - %s + %s * d.length / n.avgdl))) AS score"
            "            FROM brain.search_terms AS t JOIN df USING (term)"
            "            JOIN brain.search_docs AS d ON d.slug = t.slug AND d.analyzer = t.analyzer, n"
            "            WHERE t.analyzer = %s GROUP BY t.slug)"
            f" SELECT {_PAGE_FIELDS} FROM scored AS s JOIN brain.pages AS p USING (slug)"
            f" WHERE true{by_type} ORDER BY s.score DESC, p.slug LIMIT %s",
            (analyzer, analyzer, wanted, BM25_K1, BM25_K1, BM25_B, BM25_B, analyzer, *args, int(limit))).fetchall()
        return [_page_hit(row) for row in rows]

    # ── brain_get ──

    def resolve(self, name: str) -> str:
        """The slug of the page *name* names: a slug, a path (with or without ``.md``) or one of its names."""
        name = unicodedata.normalize("NFC", (name or "").strip())
        if not name:
            raise BrainSearchError("brain_get needs a slug, path or page name")
        conn = self._db()
        path = name if name.endswith(".md") else name + ".md"
        found = [row[0] for row in conn.execute(
            "SELECT slug FROM brain.pages WHERE slug = %s OR path = %s ORDER BY slug", (name, path)).fetchall()]
        if name in found:
            return name
        if len(found) == 1:
            return found[0]
        if found:
            raise BrainSearchError(f"{name!r} names several pages: {', '.join(found)}")
        named = conn.execute("SELECT DISTINCT slug FROM brain.aliases WHERE alias_key = %s ORDER BY slug",
                             (alias_key(name),)).fetchall()
        if len(named) == 1:
            return named[0][0]
        if named:
            raise BrainSearchError(f"{name!r} names several pages: {', '.join(row[0] for row in named)}")
        raise BrainSearchError(f"no brain page is named {name!r}")

    def page(self, name: str, *, timeline_limit: int = TIMELINE_ENTRIES) -> dict[str, Any]:
        """The page *name* names: its compiled truth, names, grade and its newest *timeline_limit* entries."""
        slug = self.resolve(name)
        conn = self._db()
        row = conn.execute(f"SELECT {_PAGE_FIELDS}, r.path FROM brain.pages AS p JOIN brain.roots AS r "
                           "USING (root_id) WHERE p.slug = %s", (slug,)).fetchone()
        hit = _page_hit(row)
        names = [alias for (alias,) in conn.execute(
            "SELECT alias FROM brain.aliases WHERE slug = %s AND origin <> 'slug' ORDER BY origin, alias", (slug,))]
        limit = max(1, min(MAX_TIMELINE_ENTRIES, int(timeline_limit)))
        total = conn.execute("SELECT count(*) FROM brain.timeline WHERE page_slug = %s", (slug,)).fetchone()[0]
        entries = conn.execute(
            "SELECT entry_date, grade, source_ref, text FROM brain.timeline WHERE page_slug = %s"
            " ORDER BY entry_date DESC NULLS LAST, line DESC LIMIT %s", (slug, limit)).fetchall()
        return {"slug": hit.slug, "title": hit.title, "type": hit.type, "path": hit.path, "root": row[7],
                "dated": hit.dated.isoformat() if hit.dated else None, "names": names, "grade": hit.grade,
                "text": hit.compiled_truth,
                "timeline": [{"date": when.isoformat() if when else None, "grade": grade, "source": source,
                              "text": text} for when, grade, source, text in entries],
                "timeline_total": total, "timeline_truncated": total > len(entries)}

    # ── brain_links ──

    def links(self, name: str, *, relations: Optional[Sequence[str]] = None, depth: int = 1,
              direction: str = "both", limit: int = MAX_LINK_NODES) -> dict[str, Any]:
        """The pages within *depth* links of the page *name* names, by edge type, nearest first. An edge closed
        before today (``valid_until``) is not walked."""
        if not 1 <= int(depth) <= MAX_DEPTH:
            raise BrainSearchError(f"depth is 1 or {MAX_DEPTH}")
        if direction not in ("out", "in", "both"):
            raise BrainSearchError("direction is out, in or both")
        start = self.resolve(name)
        conn = self._db()
        by_type = "" if not relations else " AND type = ANY(%s)"
        ends = {"out": "from_slug = ANY(%s)", "in": "to_slug = ANY(%s)",
                "both": "(from_slug = ANY(%s) OR to_slug = ANY(%s))"}[direction]
        seen = {start: 0}
        edges: list[dict[str, Any]] = []
        frontier = [start]
        for level in range(1, int(depth) + 1):
            args = [frontier] * (2 if direction == "both" else 1) + ([list(relations)] if relations else [])
            rows = conn.execute(
                f"SELECT from_slug, to_slug, type, source_ref FROM brain.edges WHERE {ends}{by_type}"
                " AND (valid_until IS NULL OR valid_until >= current_date) ORDER BY from_slug, to_slug, type",
                args).fetchall()
            reached = []
            for from_slug, to_slug, kind, source_ref in rows:
                edge = {"from": from_slug, "to": to_slug, "type": kind, "source": source_ref}
                if edge not in edges:
                    edges.append(edge)
                for slug in (from_slug, to_slug):
                    if slug not in seen and len(seen) <= limit:
                        seen[slug] = level
                        reached.append(slug)
            frontier = reached
            if not frontier:
                break
        rows = conn.execute(f"SELECT {_PAGE_FIELDS} FROM brain.pages AS p WHERE p.slug = ANY(%s)",
                            (list(seen),)).fetchall()
        pages = {hit.slug: hit for hit in map(_page_hit, rows)}
        nodes = [{"slug": slug, "title": pages[slug].title, "type": pages[slug].type, "depth": level,
                  "grade": pages[slug].grade}
                 for slug, level in sorted(seen.items(), key=lambda item: (item[1], item[0])) if level]
        kept = set(seen)
        return {"slug": start, "title": pages[start].title, "depth": int(depth), "direction": direction,
                "relations": sorted({edge["type"] for edge in edges}),
                "nodes": nodes, "edges": [edge for edge in edges if edge["from"] in kept and edge["to"] in kept],
                "truncated": len(seen) > limit}


# ── lina brain search-index / search-eval ──

def _embedder():
    from agent.embedding_client import EmbeddingClient, EmbeddingError, EmbeddingSettings
    try:
        return EmbeddingClient(EmbeddingSettings.from_config())
    except EmbeddingError:
        return None


def run_index(args) -> int:
    from hermes_cli.brain.page import nfc
    from hermes_cli.brain.store import BrainStoreError, dumps, owner_connection, root_of
    embedder = _embedder()
    try:
        with owner_connection() as conn:
            root_id = None
            if args.root:
                known = root_of(conn, nfc(str(Path(args.root).resolve())))
                if known is None:
                    raise BrainStoreError(f"{args.root} is not an imported directory; run `lina brain import` first")
                root_id = known[0]
            report = build_index(conn, embedder, root_id=root_id)
    finally:
        if embedder is not None:
            embedder.close()
    if args.json:
        print(dumps(report.as_json()))
        return 0
    written = ", ".join(f"{name} {count}" for name, count in report.rewritten_terms.items()) or "none"
    no_kiwi = "" if "kiwi" in report.rewritten_terms else " (no Kiwi here: bigrams only; install the korean extra)"
    print(f"Indexed {report.pages} pages. Keyword terms rewritten: {written}{no_kiwi}.")
    print(f"Page vectors written: {report.vectors} ({report.vector_model})." if report.vector_model
          else f"Page vectors: none, {report.no_vectors_reason}.")
    return 0


EVAL_CONFIGS = (("semantic", ("semantic",), None), ("keyword (kiwi)", ("keyword",), "kiwi"),
                ("keyword (bigram)", ("keyword",), "bigram"), ("semantic + keyword (kiwi)", LEGS, "kiwi"),
                ("semantic + keyword (bigram)", LEGS, "bigram"))


def recall(found: Sequence[str], relevant: Iterable[str]) -> float:
    relevant = set(relevant)
    return len(relevant & set(found)) / len(relevant)


def evaluate(queries: Sequence[dict], search: Callable[[str], list[str]], k: int) -> dict[str, Any]:
    """recall@*k* of *search* (query -> ranked slugs) over *queries* (``query``, ``relevant`` slugs)."""
    rows = []
    for item in queries:
        found = search(item["query"])[:k]
        rows.append({"id": item["id"], "kind": item.get("kind"), "recall": recall(found, item["relevant"]),
                     "found": found})
    return {"recall": sum(row["recall"] for row in rows) / len(rows), "queries": rows,
            "misses": [row["id"] for row in rows if row["recall"] < 1.0]}


def run_eval(args) -> int:
    """recall@k of the fixed query set through the agent's own brain_search path, per leg configuration."""
    from hermes_cli.brain.store import dumps, owner_connection
    from plugins.memory.lina import search as memory_search
    spec = json.loads(Path(args.queries).read_text(encoding="utf-8"))
    k = int(spec.get("k", 5))
    embedder = _embedder()
    results: dict[str, Any] = {}
    try:
        for label, legs, analyzer in EVAL_CONFIGS:
            if analyzer == "kiwi" and kiwi() is None or "semantic" in legs and embedder is None:
                results[label] = {"skipped": "no Kiwi here" if analyzer == "kiwi" and kiwi() is None
                                  else "no embedding endpoint is configured"}
                continue
            reader = BrainReader(owner_connection, legs=legs, analyzer=analyzer)
            engine = memory_search.MemorySearch(memory_search.Readers(brain=lambda reader=reader: reader,
                                                                      embedder=lambda: embedder))

            def ranked(query: str, engine=engine) -> list[str]:
                answer = json.loads(memory_search.handle_tool_call(
                    engine, memory_search.BRAIN_SEARCH_TOOL, {"query": query, "kinds": ["brain"], "limit": k}))
                if "error" in answer:
                    raise BrainSearchError(answer["error"])
                return [hit["ref"].removeprefix("page:") for hit in answer["results"]]
            try:
                results[label] = evaluate(spec["queries"], ranked, k)
            finally:
                reader.close()
    finally:
        if embedder is not None:
            embedder.close()
    if args.json:
        print(dumps({"k": k, "queries": len(spec["queries"]), "embedding_model": getattr(embedder, "model", None),
                     "results": results}))
        return 0
    print(f"recall@{k} over {len(spec['queries'])} queries"
          + (f" (embedding model {embedder.model})" if embedder is not None else "") + ":")
    for label, result in results.items():
        if "skipped" in result:
            print(f"  {label:30} skipped: {result['skipped']}")
        else:
            misses = ", ".join(result["misses"]) or "none"
            print(f"  {label:30} {result['recall']:.3f}  misses: {misses}")
    return 0
