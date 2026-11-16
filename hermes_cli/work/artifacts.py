"""A run's results tied to the to-do and goal that wanted them (lina#527): the ``artifact`` table of the work store.

A run that ends with files in ``out/`` gets one record per file, a run with none gets none. Each record names the run,
its to-do (the run's first) and goal, and the file's place: ``repo`` and ``commit`` stay empty until the forge of
lina#448, so ``path`` is the file's Desk path. The path carries the goal or to-do id, so a result is found from the id:

    <area folder>/<goal id>/<run id>/<file>      a run for a goal (the goal's area under the Desk's Areas folder)
    Inbox/<to-do id>/<run id>/<file>             a run for a to-do with no goal

The file is copied there when a Desk is configured. Every record written at a run's end is a ``draft``, and a draft
stays the run's latest delivered file; only :func:`finalize` (the owner's explicit action) makes it ``final``, and a
final file is never overwritten.
"""

from __future__ import annotations

import datetime
import shutil
from pathlib import Path

from hermes_cli.work import store

KINDS = ("draft", "final")
COLUMNS = ("id", "run_id", "todo_id", "goal_id", "repo", "path", "commit_sha", "kind", "created_at")
_SELECT = "SELECT " + ", ".join(COLUMNS) + " FROM artifact"


class ArtifactNotFound(LookupError):
    pass


def served(values) -> dict:
    """One record in the apps' shape (``docs/api/work-client.yaml`` ``Artifact``)."""
    r = dict(zip(COLUMNS, values))
    return {"id": int(r["id"]), "run": r["run_id"], "task": r["todo_id"], "goal": r["goal_id"], "repo": r["repo"],
            "path": r["path"], "commit": r["commit_sha"], "kind": r["kind"],
            "createdAt": int(datetime.datetime.fromisoformat(str(r["created_at"])).timestamp())}


def _area_folder(db, goal_id: str, desk: Path | None) -> str:
    title = db.execute("SELECT a.title FROM goal g JOIN area a ON a.id = g.area_id WHERE g.id = ?",
                       (goal_id,)).fetchone()[0]
    areas = "Areas"
    if desk is not None:
        from hermes_cli.desk import desk_layout
        areas = desk_layout(desk)["Areas"].relative_to(desk).as_posix()
    return f"{areas}/{title}"


def placement(db, run_id: str, todo_id: str | None, goal_id: str | None, name: str, desk: Path | None) -> str:
    """The Desk path of *name* (a path under the run's ``out/``)."""
    if goal_id:
        return f"{_area_folder(db, goal_id, desk)}/{goal_id}/{run_id}/{name}"
    return f"Inbox/{todo_id or 'none'}/{run_id}/{name}"


def _owners(db, run_id: str) -> tuple[str | None, str | None, str]:
    todo = db.execute("SELECT todo_id FROM run_todo WHERE run_id = ? ORDER BY added_at, todo_id LIMIT 1",
                      (run_id,)).fetchone()
    todo_id = todo[0] if todo else None
    goal_id, agent = db.execute("SELECT goal_id, agent_id FROM run WHERE id = ?", (run_id,)).fetchone()
    if goal_id is None and todo_id:
        goal_id = db.execute("SELECT goal_id FROM todo WHERE id = ?", (todo_id,)).fetchone()[0]
    return todo_id, goal_id, agent


def record(db, run_id: str, out_dir: Path, desk: Path | None, at: str) -> list[dict]:
    """A finished run's files under *out_dir* -> one draft record each (idempotent: a record already there keeps its
    kind). Returns the run's records for these files."""
    from hermes_cli.worker.runners.base import iter_artifact_files
    out_dir = Path(out_dir)
    if not out_dir.is_dir():
        return []
    todo_id, goal_id, agent = _owners(db, run_id)
    found = []
    for source in iter_artifact_files(out_dir):
        path = placement(db, run_id, todo_id, goal_id, source.relative_to(out_dir).as_posix(), desk)
        db.execute("INSERT INTO artifact (run_id, todo_id, goal_id, path, kind, created_at, agent_id) "
                   "VALUES (?, ?, ?, ?, 'draft', ?, ?) ON CONFLICT (run_id, path) DO NOTHING",
                   (run_id, todo_id, goal_id, path, at, agent))
        row = db.execute(_SELECT + " WHERE run_id = ? AND path = ?", (run_id, path)).fetchone()
        if desk is not None and row[COLUMNS.index("kind")] == "draft":
            target = desk / path
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(source, target)
        found.append(served(row))
    return found


def finalize(db, artifact_id: int, who: str, at: str) -> dict:
    """The owner's acceptance: a draft becomes final (a final stays final). Nothing else changes a record's kind."""
    row = db.execute(_SELECT + " WHERE id = ?", (int(artifact_id),)).fetchone()
    if row is None:
        raise ArtifactNotFound(str(artifact_id))
    if row[COLUMNS.index("kind")] == "draft":
        db.execute("UPDATE artifact SET kind = 'final' WHERE id = ?", (int(artifact_id),))
        art = served(row)
        store.run_event(db, art["run"], "status", who, {"op": "artifact_final", "artifact": art["id"]}, art["path"],
                        at)
        row = db.execute(_SELECT + " WHERE id = ?", (int(artifact_id),)).fetchone()
    return served(row)


def every(db) -> list[dict]:
    return [served(r) for r in db.execute(_SELECT + " ORDER BY id").fetchall()]


def of_item(db, item_id: str) -> list[dict]:
    """The records of a to-do (``T..``), a goal (``G..``) or a run (any other id), oldest first."""
    column = {"T": "todo_id", "G": "goal_id"}.get(item_id[:1], "run_id")
    return [served(r) for r in db.execute(_SELECT + f" WHERE {column} = ? ORDER BY id", (item_id,)).fetchall()]


def at_path(db, path: str) -> dict:
    """The records whose path is *path* or lies under it (a folder), and the runs, to-dos and goals they belong to."""
    path = path.strip().removeprefix("desk:").strip("/")
    rows = db.execute(_SELECT + " WHERE path = ? OR substr(path, 1, ?) = ? ORDER BY id",
                      (path, len(path) + 1, path + "/")).fetchall()
    found = [served(r) for r in rows]
    return {"path": path, "artifacts": found, "runs": _ids(found, "run"), "tasks": _ids(found, "task"),
            "goals": _ids(found, "goal")}


def _ids(found: list[dict], key: str) -> list[str]:
    return list(dict.fromkeys(a[key] for a in found if a[key]))


# ---------------------------------------------------------------- lina work artifact
def _line(a: dict) -> str:
    return f"#{a['id']} {a['kind']} {a['path']} · {a['run']}" + "".join(f" · {a[k]}" for k in ("task", "goal") if a[k])


def _print(found: list[dict], as_json: bool, whole=None) -> None:
    import json
    from hermes_cli.work.locale import t
    if as_json:
        print(json.dumps(whole if whole is not None else found, ensure_ascii=False, indent=1))
        return
    if not found:
        print(t("결과 기록 없음"))
    for a in found:
        print(_line(a))


def cmd_list(a) -> None:
    from hermes_cli.work import ledger
    item = a.id.strip()
    _print(of_item(ledger.db(), item.upper() if item[:1].upper() in "TG" and item[1:].isdigit() else item), a.json)


def cmd_find(a) -> None:
    from hermes_cli.work import ledger
    found = at_path(ledger.db(), a.path)
    _print(found["artifacts"], a.json, found)


def cmd_final(a) -> None:
    from hermes_cli.work import ledger
    from hermes_cli.work.common import Lock, die, now
    from hermes_cli.work.locale import t
    with Lock():
        db = ledger.db()
        try:
            with store.transaction(db):
                art = finalize(db, a.id, db.human, store.utc(now()))
        except ArtifactNotFound:
            die(t("결과 기록이 없다: #{id}", id=a.id))
    print(t("최종으로 정함: {line}", line=_line(art)))
