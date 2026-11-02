"""The record behind one storage interface: open the configured backend once per command, read every item's latest
record, and append record lines (the caller holds ``common.Lock``).

``work.store.backend``: ``postgres`` (default: schema ``work`` in the LINA store, applied by the numbered migration
on first use, reached with ``psql`` inside the store's container or ``work.store.psql`` argv) or ``sqlite``
(``$LINA_HOME/work/work.db``, for a host without the store; ``lina work import`` moves a record between them).
"""

from __future__ import annotations

from hermes_cli.work import store
from hermes_cli.work.common import iso, now, paths, writer_profile
from hermes_cli.work.env import current
from hermes_cli.work.locale import LABELS

_DB: dict[str, object] = {}
DEFAULT_BACKEND = "postgres"


def backend() -> str:
    conf = current().section("work").get("store")
    value = str(conf.get("backend") or DEFAULT_BACKEND) if isinstance(conf, dict) else DEFAULT_BACKEND
    return value if value in ("sqlite", "postgres") else DEFAULT_BACKEND


def _psql_argv() -> list[str]:
    conf = current().section("work").get("store")
    argv = conf.get("psql") if isinstance(conf, dict) else None
    if isinstance(argv, list) and argv:
        return [str(a) for a in argv]
    from hermes_cli.store import Store, store_dir
    return Store(store_dir()).compose_argv("exec", "-T", "db", "psql", "-U", "lina", "-d", "lina")


def open_db(kind: str | None = None):
    env = current()
    kind = kind or backend()
    if kind == "postgres":
        from hermes_cli.work import store_pg
        db = store_pg.connect(_psql_argv(), env.owner, env.names)
    else:
        from hermes_cli.work import store_sqlite
        db = store_sqlite.connect(paths()["db"], env.owner, env.names, create=True)
    store.seed(db, LABELS[env.locale]["codes"])
    return db


def db():
    if "db" not in _DB:
        _DB["db"] = open_db()
    return _DB["db"]


def close() -> None:
    conn = _DB.pop("db", None)
    if conn is not None:
        conn.close()


def projects_of(r) -> list[str]:
    """A goal's projects; a legacy record's "project" string reads as a one-item list."""
    v = r.get("projects")
    if isinstance(v, list):
        return [x for x in v if x]
    return [r["project"]] if r.get("project") else []


def describe(r) -> dict:
    """What the store's tables derive from a record with the CLI's own rules (link labels, projects, gh owners)."""
    from hermes_cli.work.links import gh_repo, link_label
    labels = []
    for link in r.get("links") or []:
        try:
            labels.append(link_label(link))
        except (ValueError, IndexError):
            labels.append(None)
    return {"labels": labels, "projects": projects_of(r), "gh_repo": gh_repo, "at": store.utc(now())}


def rows(item=None, ops=None):
    """Every parsed record line in order, bookkeeping lines included."""
    return store.rows(db(), item, ops)


def read_all():
    return store.read_all(db())


def stamp(rec, op, actor=None, **extra) -> dict:
    rec = dict(rec)
    rec.pop("_c", None)
    rec.update(extra)
    rec["op"], rec["at"], rec["ts"] = op, iso(), now()
    rec["rev"] = int(rec.get("rev", 0)) + 1
    rec["actor"] = actor or rec.get("by")
    rec["writer"] = writer_profile()
    return rec


def append(rec, op, actor=None, **extra) -> dict:
    """caller holds Lock(). One new line for *rec* (its next revision); returns the line's record."""
    rec = stamp(rec, op, actor, **extra)
    store.append(db(), rec, describe(rec))
    from hermes_cli.work.state import touched
    touched(rec["id"], op)
    return rec


def append_in_txn(rec, op, actor=None, **extra) -> dict:
    """``append`` inside the caller's transaction (a record change and its hand-off)."""
    rec = stamp(rec, op, actor, **extra)
    store.append_in(db(), rec, describe(rec))
    from hermes_cli.work.state import touched
    touched(rec["id"], op)
    return rec


def append_meta(row) -> None:
    """caller holds Lock(). A bookkeeping line ("_meta": true) that is not a record."""
    store.append_meta(db(), row)
