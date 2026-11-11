"""``v_todo`` of migration 0010 against 0004's (lina#595): on one varied record the new view returns 0004's rows
exactly, ``v_goal`` over either is the same, and the SQLite edition derives the same states."""

import re
from pathlib import Path

from hermes_cli import store_migrations
from hermes_cli.work import store, store_sqlite
from hermes_cli.work.store_pg import PgDb
from tests.hermes_cli import work_pg_fixture

pg_db = work_pg_fixture.pg_db

OLD = store_migrations.MIGRATIONS_DIR / "0004_work.sql"
NEW = store_migrations.MIGRATIONS_DIR / "0010_work_todo_runs_once.sql"
PAST, FUTURE = "2026-01-01T00:00:00Z", "2999-01-01T00:00:00Z"


def _at(day: int) -> str:
    return f"2026-01-{day:02d}T00:00:00Z"


def _run(run_id: str, created: str, *, started=None, activity=None, ended=None, exit_code=None, result=None,
         cancelled=None, background=None) -> tuple[str, dict]:
    return "run", {"id": run_id, "runner": "omo", "title": run_id, "agent_id": "a", "created_at": created,
                   "started_at": started, "last_activity_at": activity, "ended_at": ended, "exit_code": exit_code,
                   "result_path": result, "cancelled_at": cancelled, "background_since": background}


def _done(run_id: str, day: int):
    return _run(run_id, _at(day), started=_at(day), ended=_at(day), exit_code=0, result="out/report.md")


def _failed(run_id: str, day: int):
    return _run(run_id, _at(day), started=_at(day), ended=_at(day), exit_code=1)


def _todo(todo_id: str, goal: str | None, **closed) -> tuple[str, dict]:
    return "todo", {"id": todo_id, "goal_id": goal, "title": todo_id, "agent_id": "a", "created_by": "o",
                    "created_at": PAST, **closed}


def _links(todo_id: str, *runs: str) -> list[tuple[str, dict]]:
    return [("run_todo", {"run_id": r, "todo_id": todo_id, "added_by": "o"}) for r in runs]


def _wait(wait_id: int, todo_id: str, reason: str, day: int, released=None) -> tuple[str, dict]:
    return "wait", {"id": wait_id, "todo_id": todo_id, "reason": reason, "on_text": reason, "since": _at(day),
                    "released_at": released, "agent_id": "a"}


def _ask(ask_id: int, answered: bool = False, **target) -> tuple[str, dict]:
    return "decision", {"id": ask_id, "kind": "question", "prompt": "?", "asked_by": "a", "asked_at": _at(3),
                        "answer": "yes" if answered else None, "answered_at": _at(4) if answered else None,
                        "agent_id": "a", **target}


ROWS: list[tuple[str, dict]] = [
    ("actor", {"id": "o", "kind": "human", "display_name": "O"}),
    ("actor", {"id": "a", "kind": "agent", "display_name": "A"}),
    ("area", {"id": "A1", "title": "A", "agent_id": "a", "created_by": "o"}),
    *[("goal", {"id": g, "area_id": "A1", "title": g, "agent_id": "a", "created_by": "o", "created_at": PAST})
      for g in ("G1", "G2", "G3")],
    _done("r02a", 2), _failed("r02b", 5),
    _failed("r03a", 6), _done("r03b", 6),
    _run("r04a", _at(7), cancelled=_at(7)), _run("r04b", _at(8), cancelled=_at(8)),
    _done("r05a", 2), _run("r05b", _at(9), started=_at(9), cancelled=_at(9)),
    _done("r06a", 2), _run("r06b", _at(3), started=_at(3), activity=FUTURE),
    _run("r07a", _at(3), started=_at(3), activity=FUTURE),
    _done("r11a", 2),
    _run("r13a", _at(4), started=_at(4), ended=_at(4), exit_code=0),
    _run("r14a", _at(4), started=_at(4), activity=_at(4)),
    _run("r15a", _at(4), started=_at(4), activity=FUTURE, background=_at(4)),
    _run("r16a", _at(4)),
    _todo("T01", "G1"),
    _todo("T02", "G1"), *_links("T02", "r02a", "r02b"),
    _todo("T03", "G1"), *_links("T03", "r03a", "r03b"),
    _todo("T04", "G1"), *_links("T04", "r04a", "r04b"),
    _todo("T05", "G1"), *_links("T05", "r05a", "r05b"),
    _todo("T06", "G2"), *_links("T06", "r06a", "r06b"),
    _todo("T07", "G2"), *_links("T07", "r07a"), _ask(1, run_id="r07a"),
    _todo("T08", "G2"), *_links("T08", "r06a"),
    _wait(1, "T08", "feedback", 3), _wait(2, "T08", "review", 5), _wait(3, "T08", "hold", 9, released=_at(9)),
    _todo("T09", "G2"), _wait(4, "T09", "hold", 5), _wait(5, "T09", "blocked_by_item", 5),
    _todo("T10", "G3"), *_links("T10", "r02b"), _ask(2, todo_id="T10"), _ask(3, answered=True, todo_id="T10"),
    _todo("T11", "G3", closed_at=_at(3), close_kind="done"), *_links("T11", "r11a"),
    _todo("T12", "G3", closed_at=_at(3), close_kind="dropped"), _wait(6, "T12", "hold", 2),
    _todo("T13", None), *_links("T13", "r13a"),
    _todo("T14", None), *_links("T14", "r14a"),
    _todo("T15", None), *_links("T15", "r15a"),
    _todo("T16", None), *_links("T16", "r16a"),
    *[("event", {"actor_id": "o", "kind": "note", "agent_id": "a", "todo_id": t, "at": at})
      for t, at in (("T02", _at(3)), ("T02", _at(8)), ("T16", _at(5)))],
]

# (status, reason, run_count) per to-do, as 0004's v_todo derives them.
STATES = {
    "T01": ("planned", None, 0), "T02": ("stopped", "failed", 2), "T03": ("stopped", "failed", 2),
    "T04": ("planned", None, 0), "T05": ("waiting", "review", 1), "T06": ("in_progress", None, 2),
    "T07": ("waiting", "decision", 1), "T08": ("waiting", "review", 1), "T09": ("waiting", "hold", 0),
    "T10": ("waiting", "decision", 1), "T11": ("done", None, 1), "T12": ("dropped", None, 0),
    "T13": ("stopped", "result_missing", 1), "T14": ("stopped", "no_response", 1),
    "T15": ("stopped", "no_response", 1), "T16": ("planned", None, 1),
}


def _fill(db) -> None:
    store.seed(db, {})
    for table, row in ROWS:
        db.execute(f"INSERT INTO {table} ({', '.join(row)}) VALUES ({', '.join('?' * len(row))})", tuple(row.values()))


def _view(path: Path, name: str, as_name: str) -> str:
    body = re.search(rf"CREATE OR REPLACE VIEW {name} WITH \(security_invoker = true\) AS\n(.*?);\n",
                     path.read_text(encoding="utf-8"), re.S)[1]
    return f"CREATE VIEW {as_name} AS\n{body}"


def test_new_todo_view_returns_the_old_views_rows_on_postgres(pg_db: PgDb):
    # Given the store migrations, a varied record, and v_todo and v_goal as 0004 and 0010 define them.
    store_migrations.apply(pg_db.run_sql, out=lambda _line: None)
    pg_db.begin()
    try:
        _fill(pg_db)
        pg_db.execute(_view(OLD, "v_todo", "v_todo_0004"))
        pg_db.execute(_view(NEW, "v_todo", "v_todo_0010"))
        for todos in ("v_todo_0004", "v_todo_0010"):
            pg_db.execute(_view(OLD, "v_goal", f"v_goal_over_{todos}").replace("v_todo vt", f"{todos} vt"))
        # When every row of each is read.
        old = pg_db.execute("SELECT * FROM v_todo_0004 ORDER BY id").fetchall()
        new = pg_db.execute("SELECT * FROM v_todo_0010 ORDER BY id").fetchall()
        installed = pg_db.execute("SELECT * FROM v_todo ORDER BY id").fetchall()
        goals = [pg_db.execute(f"SELECT * FROM v_goal_over_{t} ORDER BY id").fetchall()
                 for t in ("v_todo_0004", "v_todo_0010")]
        derived = pg_db.execute("SELECT id, status, reason, run_count FROM v_todo_0004 ORDER BY id").fetchall()
        columns = [pg_db.columns(v) for v in ("v_todo_0004", "v_todo_0010", "v_todo")]
    finally:
        pg_db.rollback()
    # Then 0010's rows, the installed view's and the goals over them are 0004's, column for column.
    assert {i: (s, r, n) for i, s, r, n in derived} == STATES
    assert new == old
    assert installed == old
    assert goals[1] == goals[0] and len(goals[0]) == 3
    assert columns[1] == columns[0] == columns[2]


def test_sqlite_todo_view_derives_the_same_states(tmp_path: Path):
    # Given the same record in a new SQLite work store.
    db = store_sqlite.connect(tmp_path / "work.db", "o", {}, create=True)
    try:
        _fill(db)
        # When its v_todo is read.
        rows = db.execute("SELECT id, status, reason, run_count FROM v_todo ORDER BY id").fetchall()
    finally:
        db.close()
    # Then each to-do has the state 0004's Postgres view derives.
    assert {i: (s, r, n) for i, s, r, n in rows} == STATES
