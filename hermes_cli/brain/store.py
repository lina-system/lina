"""Writing a scanned index into schema ``brain`` (services/store/migrations/0014_brain.sql) as the store owner.

One directory is synced in one transaction: only rows that differ from the files are written, so importing an
unchanged directory again changes nothing. A rebuild deletes the directory's file-derived rows first and writes
them all again; merges and atoms are not derived from files and survive it.
"""

from __future__ import annotations

import json
from collections import Counter
from dataclasses import dataclass, field

from hermes_cli.brain.resolver import Mapping, mapping_from
from hermes_cli.brain.scan import Index

TABLES = ("pages", "aliases", "edges", "timeline")
_PAGE_COLUMNS = ("slug", "page_id", "path", "type", "title", "dated", "frontmatter", "compiled_truth",
                 "content_sha256")
_SELECT = {
    "pages": f"SELECT {', '.join(_PAGE_COLUMNS)} FROM brain.pages WHERE root_id = %s",
    "aliases": "SELECT a.alias_key, a.slug, a.alias, a.origin FROM brain.aliases AS a "
               "JOIN brain.pages AS p USING (slug) WHERE p.root_id = %s",
    "edges": "SELECT e.from_slug, e.to_slug, e.type, e.source_ref FROM brain.edges AS e "
             "JOIN brain.pages AS p ON p.slug = e.from_slug WHERE p.root_id = %s",
    "timeline": "SELECT page_slug, path, line, entry_date, grade, source_ref, text FROM brain.timeline "
                "WHERE root_id = %s",
}
_INSERT = {
    "aliases": "INSERT INTO brain.aliases (alias_key, slug, alias, origin) VALUES (%s, %s, %s, %s)",
    "edges": "INSERT INTO brain.edges (from_slug, to_slug, type, source_ref) VALUES (%s, %s, %s, %s)",
    "timeline": "INSERT INTO brain.timeline (page_slug, path, line, entry_date, grade, source_ref, text, root_id) "
                "VALUES (%s, %s, %s, %s, %s, %s, %s, %s)",
}
_DELETE = {
    "aliases": "DELETE FROM brain.aliases WHERE alias_key = %s AND slug = %s",
    "edges": "DELETE FROM brain.edges WHERE from_slug = %s AND to_slug = %s AND type = %s AND source_ref = %s",
    "timeline": "DELETE FROM brain.timeline WHERE root_id = %s AND path = %s AND line = %s",
}


class BrainStoreError(RuntimeError):
    pass


@dataclass
class Changes:
    added: Counter = field(default_factory=Counter)
    changed: Counter = field(default_factory=Counter)
    removed: Counter = field(default_factory=Counter)

    def total(self) -> int:
        return sum(self.added.values()) + sum(self.changed.values()) + sum(self.removed.values())

    def as_json(self) -> dict:
        return {name: {table: counts[table] for table in ("roots", *TABLES) if counts[table]}
                for name, counts in (("added", self.added), ("changed", self.changed), ("removed", self.removed))}


def owner_connection():
    """A connection as the store owner (role ``lina``): the brain's shared tables are written by owner tools only."""
    import psycopg

    import hermes_state_store
    try:
        return psycopg.connect(hermes_state_store.resolve_owner_dsn())
    except psycopg.OperationalError as exc:
        raise hermes_state_store.StoreUnavailableError(f"cannot reach the memory store: {exc}".strip()) from exc


def root_of(conn, path: str) -> tuple[int, Mapping] | None:
    row = conn.execute("SELECT root_id, mapping FROM brain.roots WHERE path = %s", (path,)).fetchone()
    return (row[0], mapping_from(row[1])) if row else None


def merges_of(conn, root_id: int) -> tuple[tuple[str, str], ...]:
    return tuple(conn.execute("SELECT from_slug, into_slug FROM brain.merges WHERE root_id = %s "
                              "ORDER BY merged_at, from_slug", (root_id,)).fetchall())


def snapshot(conn, root_id: int) -> dict:
    """The directory's file-derived rows: ``pages`` by slug, the other tables as sets of rows."""
    found = {table: conn.execute(_SELECT[table], (root_id,)).fetchall() for table in TABLES}
    return {"pages": {row[0]: tuple(row) for row in found["pages"]},
            **{table: {tuple(row) for row in found[table]} for table in TABLES[1:]}}


def _root(conn, index: Index, changes: Changes) -> int:
    from psycopg.types.json import Jsonb
    mapping = index.mapping.as_json()
    row = conn.execute("SELECT root_id, mapping FROM brain.roots WHERE path = %s", (index.root,)).fetchone()
    if row is None:
        return conn.execute("INSERT INTO brain.roots (path, mapping) VALUES (%s, %s) RETURNING root_id",
                            (index.root, Jsonb(mapping))).fetchone()[0]
    if row[1] != mapping:
        conn.execute("UPDATE brain.roots SET mapping = %s, imported_at = now() WHERE root_id = %s",
                     (Jsonb(mapping), row[0]))
        changes.changed["roots"] += 1
    return row[0]


def _drop_foreign_slugs(conn, index: Index, root_id: int) -> None:
    taken = conn.execute("SELECT slug FROM brain.pages WHERE slug = ANY(%s) AND root_id <> %s",
                         (list(index.pages), root_id)).fetchall()
    for (slug,) in taken:
        index.drop(slug, "slug used by another imported directory")


def _desired(index: Index) -> dict:
    return {"pages": {slug: record.row() for slug, record in index.pages.items()},
            "aliases": set(index.aliases), "edges": set(index.edges),
            "timeline": {tuple(row) for row in index.timeline}}


def _sync_pages(conn, old: dict, new: dict, root_id: int, changes: Changes) -> None:
    from psycopg.types.json import Jsonb

    def params(row: tuple) -> tuple:
        return (*row[:6], Jsonb(row[6]), *row[7:])
    added = [new[slug] for slug in sorted(new) if slug not in old]
    changed = [new[slug] for slug in sorted(new) if slug in old and old[slug] != new[slug]]
    with conn.cursor() as cur:
        cur.executemany(f"INSERT INTO brain.pages ({', '.join(_PAGE_COLUMNS)}, root_id) "
                        f"VALUES ({', '.join(['%s'] * len(_PAGE_COLUMNS))}, %s)",
                        [(*params(row), root_id) for row in added])
        sets = ", ".join(f"{column} = %s" for column in _PAGE_COLUMNS[1:])
        cur.executemany(f"UPDATE brain.pages SET {sets} WHERE slug = %s",
                        [(*params(row)[1:], row[0]) for row in changed])
    changes.added["pages"] += len(added)
    changes.changed["pages"] += len(changed)


def _sync_rows(conn, table: str, old: set, new: set, root_id: int, changes: Changes) -> None:
    extra, missing = sorted(old - new, key=repr), sorted(new - old, key=repr)
    with conn.cursor() as cur:
        if table == "timeline":
            cur.executemany(_DELETE[table], [(root_id, row[1], row[2]) for row in extra])
            cur.executemany(_INSERT[table], [(*row, root_id) for row in missing])
        else:
            cur.executemany(_DELETE[table], [row[:2] if table == "aliases" else row for row in extra])
            cur.executemany(_INSERT[table], missing)
    changes.removed[table] += len(extra)
    changes.added[table] += len(missing)


def sync(conn, index: Index, *, rebuild: bool = False) -> Changes:
    """Make the directory's rows equal to *index* in one transaction and say what changed."""
    changes = Changes()
    with conn.transaction():
        root_id = _root(conn, index, changes)
        _drop_foreign_slugs(conn, index, root_id)
        old = snapshot(conn, root_id)
        new = _desired(index)
        gone = sorted(set(old["pages"]) - set(new["pages"]))
        for slug in gone:
            if slug in index.redirects:
                conn.execute("UPDATE brain.atoms SET page_slug = %s WHERE page_slug = %s",
                             (index.redirects[slug], slug))
            else:
                conn.execute("DELETE FROM brain.atoms WHERE page_slug = %s", (slug,))
        if rebuild:
            conn.execute("DELETE FROM brain.pages WHERE root_id = %s", (root_id,))
            old = {"pages": {}, **{table: set() for table in TABLES[1:]}}
        else:
            # Their aliases, edges and timeline rows go with them (ON DELETE CASCADE) and are counted below.
            conn.execute("DELETE FROM brain.pages WHERE slug = ANY(%s)", (gone,))
            changes.removed["pages"] += len(gone)
        _sync_pages(conn, old["pages"], new["pages"], root_id, changes)
        for table in TABLES[1:]:
            _sync_rows(conn, table, old[table], new[table], root_id, changes)
    return changes


def record_merge(conn, from_slug: str, into_slug: str, reason: str | None) -> tuple[str, Mapping]:
    """Record that *from_slug* folds into *into_slug* (both pages of one directory); returns that directory and its
    mapping, which the caller rescans so the merge takes effect."""
    rows = dict(conn.execute("SELECT slug, root_id FROM brain.pages WHERE slug = ANY(%s)",
                             ([from_slug, into_slug],)).fetchall())
    missing = [slug for slug in (from_slug, into_slug) if slug not in rows]
    if missing:
        raise BrainStoreError(f"no brain page {', '.join(missing)}")
    if rows[from_slug] != rows[into_slug]:
        raise BrainStoreError("a merge joins two pages of the same imported directory")
    if from_slug == into_slug:
        raise BrainStoreError("a page cannot merge into itself")
    with conn.transaction():
        conn.execute("INSERT INTO brain.merges (from_slug, into_slug, root_id, reason) VALUES (%s, %s, %s, %s)",
                     (from_slug, into_slug, rows[from_slug], reason))
    path, mapping = conn.execute("SELECT path, mapping FROM brain.roots WHERE root_id = %s",
                                 (rows[from_slug],)).fetchone()
    return path, mapping_from(mapping)


def dumps(value: dict) -> str:
    return json.dumps(value, ensure_ascii=False, indent=2, default=str)
