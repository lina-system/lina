"""Look up work repositories and manage explicit artifact locations."""

from __future__ import annotations

import sqlite3
from collections.abc import Iterator
from contextlib import closing, contextmanager
from typing import Final, NotRequired, TypedDict

import hermes_state_source_visibility as visibility
from hermes_cli.config_backend import ConfigBackendUnavailable
from hermes_cli.work import artifacts, env, ledger, store, store_pg, store_sqlite

_Db = store_pg.PgDb | store_sqlite.SqliteDb
_BRAIN_LIMIT: Final = 50


class CatalogError(RuntimeError):
    """A catalog failure with a status and text safe to send to a client."""

    status: int

    def __init__(self, status: int, message: str) -> None:
        super().__init__(message)
        self.status = status


class _Area(TypedDict):
    id: str
    title: str


class _Areas(TypedDict):
    areas: list[_Area]


class _Repo(TypedDict):
    id: str
    area_id: str
    name: str


class _Repos(TypedDict):
    repos: list[_Repo]


class _Artifact(TypedDict):
    id: int
    run: str
    task: str | None
    goal: str | None
    repo: str | None
    path: str
    commit: str | None
    kind: str
    createdAt: int


class _Artifacts(TypedDict):
    artifacts: list[_Artifact]


class _Binding(TypedDict):
    artifact: _Artifact


class _Citation(TypedDict):
    slug: str
    title: str
    path: str
    line: int
    date: str | None
    grade: str
    commit: str | None


class _Brain(TypedDict):
    available: bool
    pages: list[_Citation]
    truncated: NotRequired[bool]


class _Context(_Artifacts):
    area: str
    repo: str
    path: str
    runs: list[str]
    tasks: list[str]
    goals: list[str]
    brain: _Brain


@contextmanager
def _catalog_db(*, readonly: bool = True) -> Iterator[_Db]:
    """Open the configured store without the ledger's schema or seed writes."""
    try:
        work = env.current()
        if ledger.backend() == "postgres":
            db = store_pg.connect(ledger._psql_argv(), work.owner, work.names, ensure_schema=False)
        elif readonly:
            db = store_sqlite.connect(work.root / "work.db", work.owner, work.names, readonly=True)
        else:
            uri = (work.root / "work.db").resolve().as_uri()
            conn = sqlite3.connect(f"{uri}?mode=rw", uri=True, timeout=60, isolation_level=None)
            db = store_sqlite.SqliteDb(conn, work.owner, work.names)
        with closing(db):
            yield db
    except (ConfigBackendUnavailable, OSError, sqlite3.Error, store_pg.PgError):
        raise CatalogError(503, "The work catalog is unavailable.") from None


def _require_key(value: str) -> None:
    if not value or "\0" in value:
        raise CatalogError(400, "Invalid work catalog lookup.")


def _registered(db: _Db, area: str, repo: str) -> tuple[str, str]:
    """Resolve a slug or canonical ID within the requested area."""
    _require_key(area)
    _require_key(repo)
    repo_id = repo if "/" in repo else f"{area}/{repo}"
    row = db.execute("SELECT id, forge_url FROM repo WHERE area_id = ? AND id = ?", (area, repo_id)).fetchone()
    if row is None:
        raise CatalogError(404, "Work repository not found.")
    return str(row[0]), str(row[1])


def areas() -> _Areas:
    with _catalog_db() as db:
        rows = db.execute("SELECT id, title FROM area ORDER BY id").fetchall()
    return {"areas": [{"id": str(area_id), "title": str(title)} for area_id, title in rows]}


def repos(area: str) -> _Repos:
    _require_key(area)
    with _catalog_db() as db:
        if db.execute("SELECT 1 FROM area WHERE id = ?", (area,)).fetchone() is None:
            raise CatalogError(404, "Work area not found.")
        rows = db.execute("SELECT id, area_id, name FROM repo WHERE area_id = ? ORDER BY name", (area,)).fetchall()
    return {"repos": [{"id": str(repo_id), "area_id": str(area_id), "name": str(name)}
                      for repo_id, area_id, name in rows]}


def remote(area: str, repo: str) -> str:
    """Return the registered Git remote verbatim."""
    with _catalog_db() as db:
        return _registered(db, area, repo)[1]


def inbox() -> _Repos:
    """List registered repositories named ``inbox`` by convention."""
    with _catalog_db() as db:
        rows = db.execute("SELECT id, area_id, name FROM repo WHERE name = ? ORDER BY id", ("inbox",)).fetchall()
    return {"repos": [{"id": str(repo_id), "area_id": str(area_id), "name": str(name)}
                      for repo_id, area_id, name in rows]}


def _brain_citations(db: _Db, key: str) -> _Brain:
    if not isinstance(db, store_pg.PgDb):
        return {"available": False, "pages": []}

    try:
        rows = db.execute(
            "SELECT p.slug, p.title, t.path, t.line, t.entry_date, t.grade, t.source_ref "
            "FROM brain.timeline t JOIN brain.pages p ON p.slug = t.page_slug "
            "WHERE (t.source_ref = ? OR starts_with(t.source_ref, ?)) "
            f"AND {visibility.visible_ref('t.source_ref')} "
            "ORDER BY t.entry_date DESC NULLS LAST, p.slug, t.line LIMIT ?",
            (key, key + "@", _BRAIN_LIMIT + 1),
        ).fetchall()
    except (store_pg.PgError, ConfigBackendUnavailable):
        return {"available": False, "pages": []}

    brain: _Brain = {"available": True, "pages": [
        {"slug": str(slug), "title": str(title), "path": str(path), "line": int(str(line)),
         "date": str(dated) if dated is not None else None, "grade": str(grade),
         "commit": str(ref)[len(key) + 1:] or None}
        for slug, title, path, line, dated, grade, ref in rows[:_BRAIN_LIMIT]
    ]}
    if len(rows) > _BRAIN_LIMIT:
        brain["truncated"] = True
    return brain


def context(area: str, repo: str, path: str) -> _Context:
    """Find recorded work for this exact repository and file path."""
    _require_key(path)
    with _catalog_db() as db:
        repo_id, _ = _registered(db, area, repo)
        rows = db.execute(artifacts._SELECT + " WHERE repo = ? AND path = ? ORDER BY id", (repo_id, path)).fetchall()
        found: list[_Artifact] = [artifacts.served(row) for row in rows]
        brain = _brain_citations(db, f"repo:{repo_id}:{path}")
    return {"area": area, "repo": repo_id, "path": path, "artifacts": found, "brain": brain,
            "runs": list(dict.fromkeys(row["run"] for row in found)),
            "tasks": list(dict.fromkeys(row["task"] for row in found if row["task"] is not None)),
            "goals": list(dict.fromkeys(row["goal"] for row in found if row["goal"] is not None))}


def for_work(item: str) -> _Artifacts:
    """Return the recorded artifacts for a task, goal, or run, including Desk-only rows."""
    _require_key(item)
    with _catalog_db() as db:
        return {"artifacts": artifacts.of_item(db, item)}


def bind_artifact(area: str, repo: str, artifact_id: int, path: str, commit: str) -> _Binding:
    """Associate an existing artifact with a verified repository version at its stored path."""
    _require_key(path)
    _require_key(commit)
    with _catalog_db(readonly=False) as db, store.transaction(db):
        repo_id, _ = _registered(db, area, repo)
        row = db.execute(
            "UPDATE artifact SET repo = ?, commit_sha = ? WHERE id = ? AND path = ? "
            "AND repo IS NULL AND commit_sha IS NULL RETURNING " + ", ".join(artifacts.COLUMNS),
            (repo_id, commit, artifact_id, path),
        ).fetchone()
        if row is None:
            row = db.execute(artifacts._SELECT + " WHERE id = ?", (artifact_id,)).fetchone()
            if row is None:
                raise CatalogError(404, "Work artifact not found.")
            existing = artifacts.served(row)
            if (existing["repo"], existing["path"], existing["commit"]) != (repo_id, path, commit):
                raise CatalogError(409, "The artifact path or repository association conflicts.")
        return {"artifact": artifacts.served(row)}
