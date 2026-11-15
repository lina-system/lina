"""The owner's word list (lina#378): a word to avoid, the word to use instead, why, and the correction it came from.

The list is shared by every agent (``memory.word_list``, migration 0016): agents read it through the store login
``lina_app``; only the owner path (``lina words``, role ``lina``) and the refiner (role ``lina_refiner``) write it.
Each correction may keep its flagged and fixed sentences as a pair in ``memory.word_pairs`` for the measurement run
(lina#377). Pairs are the owner's own text: nothing here logs or prints them, and no agent can read them.
"""

from __future__ import annotations

import logging
import unicodedata
from collections.abc import Iterable
from contextlib import contextmanager
from dataclasses import dataclass, field
from datetime import datetime
from typing import Any, Iterator, Optional

logger = logging.getLogger(__name__)

# calque: an English word carried over into Korean; coined: a new name nobody uses; translationese: a phrase built
# like the English sentence; owner_term: the owner's own word for a concept; other: none of these (docs/korean.md).
REASONS = ("calque", "coined", "translationese", "owner_term", "other")

_COLUMNS = "entry_id, avoid_word, use_word, reason, source_uid, created_at, retired_at"


class WordListError(ValueError):
    pass


@dataclass(frozen=True)
class WordEntry:
    entry_id: int
    avoid: str
    use: str
    reason: str
    source_uid: Optional[str]
    created_at: datetime
    retired_at: Optional[datetime] = None

    def as_dict(self) -> dict[str, Any]:
        return {"id": self.entry_id, "avoid": self.avoid, "use": self.use, "reason": self.reason,
                "source": self.source_uid, "created_at": self.created_at.isoformat(),
                "retired_at": self.retired_at.isoformat() if self.retired_at else None}


@dataclass(frozen=True)
class WordPair:
    """A flagged sentence and its fix. The sentences stay out of ``repr``, so a log line or traceback that formats
    a pair never carries the owner's text."""

    flagged: str = field(repr=False)
    fixed: str = field(repr=False)
    source_uid: Optional[str] = None


@dataclass
class ImportResult:
    added: int = 0
    present: int = 0
    problems: list[tuple[int, str]] = field(default_factory=list)

    @property
    def lines(self) -> int:
        return self.added + self.present + len(self.problems)


def normalize(word: str) -> str:
    """NFC (a decomposed 한 and a composed 한 are one word) with surrounding and repeated whitespace removed."""
    return " ".join(unicodedata.normalize("NFC", word).split())


def _entry(row) -> WordEntry:
    return WordEntry(*row)


def _checked(avoid: str, use: str, reason: str) -> tuple[str, str]:
    avoid, use = normalize(avoid), normalize(use)
    if not avoid or not use:
        raise WordListError("the avoid word and the use word must both be given")
    if avoid == use:
        raise WordListError("the avoid word and the use word are the same")
    if reason not in REASONS:
        raise WordListError(f"unknown reason {reason!r}; one of {', '.join(REASONS)}")
    return avoid, use


def add(conn, avoid: str, use: str, reason: str, *, source_uid: Optional[str] = None,
        pair: Optional[WordPair] = None) -> tuple[WordEntry, bool]:
    """Add one correction; returns the entry and whether it is new. The same (avoid, use) again keeps the entry
    already active, and a new pair joins it."""
    avoid, use = _checked(avoid, use, reason)
    if pair is not None and not (normalize(pair.flagged) and normalize(pair.fixed)):
        raise WordListError("a pair needs both the flagged and the fixed sentence")
    row = conn.execute(
        f"INSERT INTO memory.word_list (avoid_word, use_word, reason, source_uid) VALUES (%s, %s, %s, %s) "
        f"ON CONFLICT (avoid_word, use_word) WHERE retired_at IS NULL DO NOTHING RETURNING {_COLUMNS}",
        (avoid, use, reason, source_uid)).fetchone()
    created = row is not None
    if row is None:
        row = conn.execute(f"SELECT {_COLUMNS} FROM memory.word_list "
                           "WHERE avoid_word = %s AND use_word = %s AND retired_at IS NULL", (avoid, use)).fetchone()
    entry = _entry(row)
    if pair is not None:
        conn.execute("INSERT INTO memory.word_pairs (entry_id, flagged, fixed, source_uid) VALUES (%s, %s, %s, %s) "
                     "ON CONFLICT DO NOTHING", (entry.entry_id, pair.flagged, pair.fixed, pair.source_uid))
    logger.info("word list: %s entry %d (reason %s)", "added" if created else "kept", entry.entry_id, entry.reason)
    return entry, created


def retire(conn, avoid: str, *, use: Optional[str] = None, source_uid: Optional[str] = None) -> list[WordEntry]:
    """Retire the active entries for *avoid* (only the one to *use* when given); the next read leaves them out."""
    avoid = normalize(avoid)
    rows = conn.execute(
        f"UPDATE memory.word_list SET retired_at = now(), retired_source_uid = %s "
        f"WHERE avoid_word = %s AND (%s::text IS NULL OR use_word = %s) AND retired_at IS NULL RETURNING {_COLUMNS}",
        (source_uid, avoid, None if use is None else normalize(use), None if use is None else normalize(use))
    ).fetchall()
    for row in rows:
        logger.info("word list: retired entry %d", row[0])
    return [_entry(row) for row in rows]


def import_seed(conn, lines: Iterable[str]) -> ImportResult:
    """Import ``avoid<TAB>use<TAB>reason`` lines. Every line is added, found already present, or reported with its
    line number and why; nothing is dropped silently."""
    result = ImportResult()
    for number, line in enumerate(lines, start=1):
        fields = line.rstrip("\r\n").split("\t")
        if not line.strip():
            result.problems.append((number, "empty line"))
            continue
        if len(fields) != 3:
            result.problems.append((number, f"expected 3 tab-separated columns (avoid, use, reason), found {len(fields)}"))
            continue
        try:
            _, created = add(conn, fields[0], fields[1], fields[2].strip())
        except WordListError as error:
            result.problems.append((number, str(error)))
            continue
        if created:
            result.added += 1
        else:
            result.present += 1
    return result


def entries(conn, *, include_retired: bool = False) -> list[WordEntry]:
    where = "" if include_retired else " WHERE retired_at IS NULL"
    return [_entry(row) for row in conn.execute(
        f"SELECT {_COLUMNS} FROM memory.word_list{where} ORDER BY entry_id").fetchall()]


def pairs(conn) -> dict[int, list[WordPair]]:
    """Every entry's pairs, for the measurement run. Readable by the owner and the refiner only."""
    found: dict[int, list[WordPair]] = {}
    for entry_id, flagged, fixed, source_uid in conn.execute(
            "SELECT entry_id, flagged, fixed, source_uid FROM memory.word_pairs ORDER BY pair_id").fetchall():
        found.setdefault(entry_id, []).append(WordPair(flagged, fixed, source_uid))
    return found


def matches(entry: WordEntry, terms: Iterable[str]) -> bool:
    """Whether the entry's avoid or use word appears in one of *terms* (a word, a phrase or a whole message)."""
    return any(entry.avoid in term or entry.use in term for term in terms)


def word_list_entries(terms: Optional[Iterable[str]] = None) -> list[WordEntry]:
    """The active entries as an agent reads them (login ``lina_app``, any agent or none), for the context builder.
    With *terms*, only the entries whose avoid or use word appears in one of them."""
    import psycopg

    import hermes_state_store
    try:
        conn = psycopg.connect(hermes_state_store.resolve_dsn(), autocommit=True)
    except psycopg.OperationalError as exc:
        raise hermes_state_store.StoreUnavailableError(f"cannot reach the memory store: {exc}".strip()) from exc
    with conn:
        found = entries(conn)
    if terms is None:
        return found
    wanted = [normalize(term) for term in terms]
    return [entry for entry in found if matches(entry, wanted)]


@contextmanager
def owner_connection() -> Iterator[Any]:
    """The owner path's connection (role ``lina``), one transaction committed on success."""
    import psycopg

    import hermes_state_store
    try:
        conn = psycopg.connect(hermes_state_store.resolve_owner_dsn())
    except psycopg.OperationalError as exc:
        raise hermes_state_store.StoreUnavailableError(f"cannot reach the memory store: {exc}".strip()) from exc
    with conn:
        yield conn
