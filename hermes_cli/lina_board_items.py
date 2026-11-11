"""Each agent's goals and to-dos for the app (lina#166): ``GET /api/lina/work/items`` and
``/api/lina/work/items/{id}``, in the shapes fixed on the issue (``docs/api/board-client.yaml``).

``state`` and ``reason`` are the store's own state views (``v_todo``, ``v_goal`` over ``v_run`` in
``hermes_cli/work/schema_sqlite.sql`` and ``services/store/migrations/0004_work.sql``), read as they are: nothing here
decides a state. ``v_goal`` has no reason column, so a goal's ``reason`` is null. The list holds what ``lina work json``
holds (``cmd_read.cmd_json``): every open goal and to-do plus those closed in the last three days; a dropped item is
never served. ``stateSince`` is the time of the record row behind the view's answer (the running run's start, the
open decision or wait, the last run's end, the close, else the creation). Text from the record is returned as it is.
"""

from __future__ import annotations

import datetime
import time
from pathlib import Path
from typing import Callable

from hermes_cli.lina_board import _WORK_LOCK, API_VERSION, _root, _source
from hermes_cli.work.kinds import TODO, kind_now

KINDS = {"목표": "goal", TODO: "todo"}
CLOSED_WINDOW_S = 3 * 86400
REPLY_RESULTS = {"반영함": "applied", "답변 필요": "needs_answer", "반영 안 함": "not_applied"}
UNKNOWN_WRITERS = ("", "?", "unknown")


class ItemNotFound(LookupError):
    pass


class RecordUnreadable(RuntimeError):
    pass


def _epoch(value) -> int | None:
    if value in (None, ""):
        return None
    try:
        return int(datetime.datetime.fromisoformat(str(value)).timestamp())
    except ValueError:
        return None


def _int(value) -> int | None:
    return int(value) if value not in (None, "") else None


# The store reads behind the items: (name, columns, query, ORDER BY). ``{cols}`` is the columns.
_STORE_READS: tuple[tuple[str, tuple[str, ...], str, str], ...] = (
    ("todo", ("id", "status", "reason", "run_count", "last_activity_at", "created_by", "created_at", "closed_at"),
     "SELECT {cols} FROM v_todo", ""),
    ("goal", ("id", "status", "todos_done", "todos_total", "created_by", "created_at", "closed_at"),
     "SELECT {cols} FROM v_goal", ""),
    ("runs", ("id", "title", "status", "reason", "goal_id", "created_at", "started_at", "last_activity_at",
              "ended_at"), "SELECT {cols} FROM v_run WHERE status <> 'dropped'", "created_at, id"),
    ("run_todo", ("run_id", "todo_id"), "SELECT {cols} FROM run_todo", ""),
    ("waits", ("todo_id", "since"), "SELECT {cols} FROM wait WHERE released_at IS NULL AND todo_id IS NOT NULL", ""),
    ("asks", ("todo_id", "asked_at"), "SELECT {cols} FROM decision WHERE answered_at IS NULL AND todo_id IS NOT NULL",
     ""),
    ("goal_events", ("goal_id", "at"),
     "SELECT goal_id, max(at) AS at FROM event WHERE goal_id IS NOT NULL GROUP BY goal_id", ""),
)


def _rows_each(db) -> list[list]:
    """Each of :data:`_STORE_READS`' rows as value lists. On Postgres all of them come back in one statement: each
    ``db.execute`` there is a round trip to ``psql`` (lina#584: seven of them were most of the list's 1.5 s on the
    hub). The rows are built as ``PgDb.execute`` builds them, so both ways give the same values."""
    if getattr(db, "dialect", None) != "postgres":
        return [[list(row) for row in db.execute(sql.format(cols=", ".join(cols))
                                                 + (f" ORDER BY {order}" if order else "")).fetchall()]
                for _name, cols, sql, order in _STORE_READS]
    parts = [f"(SELECT coalesce(json_agg((SELECT json_agg(value) FROM json_each(row_to_json(q)))"
             f"{' ORDER BY ' + order if order else ''}), '[]') FROM ({sql.format(cols=', '.join(cols))}) q) AS {name}"
             for name, cols, sql, order in _STORE_READS]
    (row,) = db.execute("SELECT " + ", ".join(parts)).fetchall()
    return [list(value) for value in row]


def _store(db) -> dict:
    got = {name: [dict(zip(cols, row)) for row in rows]
           for (name, cols, _sql, _order), rows in zip(_STORE_READS, _rows_each(db))}
    return {
        "todo": {r["id"]: r for r in got["todo"]}, "goal": {r["id"]: r for r in got["goal"]}, "runs": got["runs"],
        "run_todo": got["run_todo"], "waits": got["waits"], "asks": got["asks"],
        "goal_events": {r["goal_id"]: r["at"] for r in got["goal_events"]},
    }


def _link(spec: str) -> dict | None:
    """A link row as the store projects it (``store.link_of``: kind and target); worker runs and retired links
    (``omo:``, ``dv:``, ``session:``) are not links there and are left out."""
    from hermes_cli.work.links import gh_repo, link_label
    from hermes_cli.work.store import link_of
    kt = link_of(spec, gh_repo)
    if kt is None:
        return None
    kind, target = kt
    try:
        label = link_label(spec)
    except (ValueError, IndexError):
        label = str(spec)[:60]
    url = None
    if kind == "gh_ref" and "#" in target and not target.startswith("/"):
        repo, n = target.split("#", 1)
        url = f"https://github.com/{repo}/issues/{n}" if "/" in repo else None
    elif kind == "gh_milestone" and "/" in target:
        owner, repo = gh_repo(target.rsplit("/", 1)[0])
        url = f"https://github.com/{owner}/{repo}/milestone/{target.rsplit('/', 1)[1]}" if owner else None
    elif kind == "url":
        web = target.partition(":")[2] if target.startswith("notion:") else target
        url = web if web.startswith(("http://", "https://")) else None
    return {"kind": kind, "label": label, "url": url}


def _comment(c: dict) -> dict:
    return {"cid": c.get("cid"), "by": c.get("by"), "at": _int(c.get("at")), "text": c.get("text"),
            "status": c.get("status"), "kind": c.get("kind") or "comment",
            "replies": [{"by": x.get("by"), "at": _int(x.get("at")), "text": x.get("text"),
                         "result": REPLY_RESULTS.get(x.get("result"))} for x in c.get("replies") or []]}


def _decision(r: dict) -> dict | None:
    from hermes_cli.work.board import decision_of
    row = decision_of(r)
    if row is None:
        return None
    return {"text": row["text"], "kind": row["kind"], "choices": list(r.get("decide_choices") or []),
            "recommended": r.get("decide_recommend") or None, "cid": r.get("decide_cid") or None,
            "at": row["at"] or None, "answered": row["answered"]}


def _owner_done(r: dict) -> dict | None:
    """The owner's done mark while it is what closed the item (a later close by check has its own ``done_at``)."""
    from hermes_cli.work.cmd_comments import OWNER_UNDO_LIMIT
    mark = r.get("owner_done") or {}
    if r.get("state") != "done" or not mark.get("at") or int(mark["at"]) != int(r.get("done_at") or 0):
        return None
    return {"at": int(mark["at"]), "undoUntil": int(mark["at"]) + OWNER_UNDO_LIMIT, "with": list(mark.get("with") or [])}


def _cards(r: dict) -> list[str]:
    from hermes_cli.work.links import kanban_parts
    return [card for card in (kanban_parts(str(link))[1] for link in r.get("links") or []) if card]


class _Record:
    """One bound read of the work record: its items and the store's state views."""

    def __init__(self, recs: dict, order: list[str], store: dict, env):
        self.recs, self.order, self.store, self.env = recs, order, store, env
        self.todo_runs: dict[str, list[str]] = {}
        for row in store["run_todo"]:
            self.todo_runs.setdefault(row["todo_id"], []).append(row["run_id"])
        self.kids: dict[str, list[str]] = {}
        for i in order:
            if kind_now(recs[i].get("kind")) == TODO and recs[i].get("state") != "dropped" and recs[i].get("parent"):
                self.kids.setdefault(recs[i]["parent"], []).append(i)

    def served(self, i: str) -> dict | None:
        r = self.recs.get(i)
        return r if r and kind_now(r.get("kind")) in KINDS and r.get("state") != "dropped" else None

    def goal_of(self, r: dict) -> dict | None:
        if r["kind"] == "목표":
            return r
        parent = self.recs.get(r.get("parent") or "")
        return parent if parent and parent.get("kind") == "목표" else None

    def runs(self, i: str) -> list[dict]:
        if kind_now(self.recs[i]["kind"]) == TODO:
            ids = set(self.todo_runs.get(i, []))
        else:
            ids = {run for kid in self.kids.get(i, []) for run in self.todo_runs.get(kid, [])}
        return [run for run in self.store["runs"] if run["id"] in ids or run["goal_id"] == i]

    def _todo_since(self, i: str, view: dict) -> int | None:
        status, reason, runs = view["status"], view["reason"], self.runs(i)
        if status == "done":
            return _epoch(view["closed_at"])
        if status == "in_progress":
            return max((_epoch(run["started_at"]) or 0 for run in runs if run["status"] == "in_progress"), default=None)
        last = runs[-1] if runs else {}
        if status == "stopped" or reason == "review":
            return _epoch(last.get("ended_at") or last.get("last_activity_at") or last.get("started_at"))
        if reason == "decision":
            asked = [_epoch(a["asked_at"]) for a in self.store["asks"] if a["todo_id"] == i]
            asked += [_epoch(run["last_activity_at"] or run["started_at"]) for run in runs if run["status"] == "waiting"]
            return min((at for at in asked if at), default=None)
        if status == "waiting":
            return max((_epoch(w["since"]) or 0 for w in self.store["waits"] if w["todo_id"] == i), default=None)
        return _epoch(view["created_at"])

    def _goal_since(self, i: str, view: dict) -> int | None:
        status = view["status"]
        if status == "done":
            return _epoch(view["closed_at"])
        if status == "planned":
            return _epoch(view["created_at"])
        same = [self._todo_since(k, self.store["todo"][k]) for k in self.kids.get(i, [])
                if self.store["todo"][k]["status"] == status]
        if same:
            return min((at for at in same if at), default=None)
        return max((_epoch(self.store["todo"][k]["closed_at"]) or 0 for k in self.kids.get(i, [])), default=None)

    def _last_activity(self, i: str, view: dict, goal: bool) -> int | None:
        if not goal:
            return _epoch(view["last_activity_at"])
        times = [_epoch(self.store["todo"][k]["last_activity_at"]) for k in self.kids.get(i, [])]
        return max((at for at in times + [_epoch(self.store["goal_events"].get(i))] if at), default=None)

    def summary(self, i: str) -> dict:
        from hermes_cli.work.board import agent_of
        from hermes_cli.work.evaluate import item_name
        from hermes_cli.work.ledger import projects_of
        r = self.recs[i]
        goal = r["kind"] == "목표"
        view = self.store["goal" if goal else "todo"][i]
        top = self.goal_of(r)
        area = self.recs.get((top or {}).get("parent") or "")
        return {
            "id": i, "kind": KINDS[kind_now(r["kind"])], "parent": r.get("parent") or None, "title": item_name(r),
            "agent": agent_of(r, self.recs) or None,
            "addedBy": None if view["created_by"] in UNKNOWN_WRITERS else view["created_by"],
            "mine": r.get("by") == self.env.owner, "state": view["status"], "reason": None if goal else view["reason"],
            "stateSince": self._goal_since(i, view) if goal else self._todo_since(i, view),
            "lastActivityAt": self._last_activity(i, view, goal),
            "createdAt": _epoch(view["created_at"]) or int(r.get("created") or 0),
            "closedAt": _epoch(view["closed_at"]), "due": r.get("due") or None,
            "doneCondition": r.get("done") or None, "area": area.get("title") if area else None,
            "projects": projects_of(top) if top else [],
            "todos": {"done": int(view["todos_done"]), "total": int(view["todos_total"])} if goal else None,
            "runCount": len(self.runs(i)) if goal else int(view["run_count"]),
            "commentCount": len(r.get("comments") or []),
        }

    def people(self) -> dict:
        return {"owner": {"id": self.env.owner, "name": self.env.owner_name},
                "agents": [{"id": k, "name": v} for k, v in self.env.agents.items()]}

    def listing(self, agent: str | None, now: float) -> dict:
        rows = []
        for i in self.order:
            r = self.served(i)
            if r is None or (r.get("state") != "open" and now - int(r.get("done_at") or r.get("ts") or 0) > CLOSED_WINDOW_S):
                continue
            row = self.summary(i)
            if agent is None or row["agent"] == agent:
                rows.append(row)
        return dict(self.people(), items=rows)

    def detail(self, i: str) -> dict | None:
        from hermes_cli.work.sources import basis_of
        r = self.served(i)
        if r is None:
            return None
        kids = self.kids.get(i, []) if r["kind"] == "목표" else []
        return dict(
            self.summary(i), why=r.get("why") or None,
            basis=[{"text": b.get("text") or "", "at": _int(b.get("at")) or None} for b in basis_of(r)],
            children=[self.summary(k) for k in kids],
            comments=[_comment(c) for c in r.get("comments") or []],
            runs=[{"id": run["id"], "title": run["title"], "state": run["status"], "reason": run["reason"],
                   "startedAt": _epoch(run["started_at"]), "endedAt": _epoch(run["ended_at"])} for run in self.runs(i)],
            links=[row for row in (_link(spec) for spec in r.get("links") or []) if row],
            workCards=list(dict.fromkeys(card for x in [r] + [self.recs[k] for k in kids] for card in _cards(x))),
            decision=_decision(r), ownerDone=_owner_done(r),
        )


def _read(root: Path, use: Callable[[_Record], dict | None]) -> dict | None:
    from hermes_cli.work import env, ledger
    with _WORK_LOCK:
        env.bind(env.load(root))
        try:
            recs, order = ledger.read_all()
            return use(_Record(recs, order, _store(ledger.db()), env.current()))
        finally:
            ledger.close()
            env.reset()


def items(agent: str | None = None, now: float | None = None) -> dict:
    """``GET /api/lina/work/items``: a record that fails to read is an error and the list is empty."""
    now, root, errors = time.time() if now is None else now, _root(), []
    data = _source("work", errors, lambda: _read(root, lambda rec: rec.listing(agent, now))) or {}
    return {"version": API_VERSION, "at": now, "errors": errors, "owner": data.get("owner"),
            "agents": data.get("agents", []), "items": data.get("items", [])}


def item(item_id: str) -> dict:
    """``GET /api/lina/work/items/{id}``; raises ItemNotFound or RecordUnreadable."""
    root, errors = _root(), []
    found = _source("work", errors, lambda: _read(root, lambda rec: rec.detail(item_id.strip().upper())))
    if errors:
        raise RecordUnreadable(errors[0]["message"])
    if found is None:
        raise ItemNotFound(item_id)
    return found
