"""The Hermes board's cards in the work API's shapes (lina#481), read through ``hermes_cli.lina_board_hermes``: every
card comes from ``kanban show <id> --json`` of the configured Hermes CLI, never from its database. The cards are rebuilt
as ``kanban_db`` rows and read by the work API's own functions, so the states, to-dos and inbox rules are the same as
for LINA's board. The CLI carries no claim or heartbeat, so an open run counts as live (``CardFacts.assume_live``);
it carries no ``goal_mode`` either, so a card without the field counts as a goal-mode card unless it is a link-child
of another card (a goal card above work, as ``_goal_work_cards`` reads links). Ids are prefixed
``hermes:`` and every row is marked ``readOnly``.
"""

from __future__ import annotations

import dataclasses
from typing import Any, Callable, Optional

from fastapi import HTTPException

from hermes_cli import kanban_db
from hermes_cli import lina_board_hermes as hermes

_TASK_DEFAULTS = {"title": "", "body": None, "assignee": None, "status": "todo", "priority": 0, "created_by": None,
                  "created_at": None, "started_at": None, "completed_at": None, "workspace_kind": "scratch",
                  "workspace_path": None, "claim_lock": None, "claim_expires": None, "tenant": None}
_TASK_FIELDS = {f.name for f in dataclasses.fields(kanban_db.Task)} - {"goal_gates"}
_RUN_FIELDS = ("profile", "step_key", "status", "worker_pid", "outcome", "summary", "metadata", "error", "ended_at")


def _run(task_id: str, row: dict) -> kanban_db.Run:
    return kanban_db.Run(**{k: row.get(k) for k in _RUN_FIELDS}, id=int(row["id"]), task_id=task_id,
                         claim_lock=None, claim_expires=None, max_runtime_seconds=None, last_heartbeat_at=None,
                         started_at=int(row.get("started_at") or 0))


def facts(show: dict) -> Any:
    """``kanban show --json`` as the work API's ``CardFacts``."""
    from plugins.kanban.dashboard.work_api import CardFacts
    raw = show["task"]
    task_id = hermes.hermes_id(raw["id"])
    runs = [_run(task_id, r) for r in show.get("runs") or [] if isinstance(r, dict) and r.get("id") is not None]
    open_runs = [r.id for r in runs if r.ended_at is None]
    fields = {k: v for k, v in raw.items() if k in _TASK_FIELDS}
    task = kanban_db.Task(**{**_TASK_DEFAULTS, **fields, "id": task_id,
                             "current_run_id": open_runs[-1] if raw.get("status") == "running" and open_runs else None})
    events = [kanban_db.Event(id=n, task_id=task_id, kind=str(e.get("kind")), payload=e.get("payload"),
                              created_at=int(e.get("created_at") or 0), run_id=e.get("run_id"))
              for n, e in enumerate(show.get("events") or [], 1) if isinstance(e, dict)]
    comments = [kanban_db.Comment(id=n, task_id=task_id, author=str(c.get("author") or ""), body=str(c.get("body") or ""),
                                  created_at=int(c.get("created_at") or 0))
                for n, c in enumerate(show.get("comments") or [], 1) if isinstance(c, dict)]
    own = {runs[0].profile} if runs and runs[0].profile else {task.assignee} - {None}
    return CardFacts(task, events, comments, runs, own=own, assume_live=True)


def _read_only(row: dict) -> dict:
    return dict(row, readOnly=True)


class _Board:
    """One read of the Hermes board: every listed card's facts, and which of them are work and goal cards."""

    def __init__(self, src: hermes.Source):
        from hermes_cli.lina_board_plain import is_work_card
        shows = hermes.cards(src)
        self.facts = {hermes.hermes_id(s["task"]["id"]): facts(s) for s in shows}
        children = {str(child) for s in shows for child in s.get("children") or []}

        def goal_mode(task: dict) -> bool:
            return bool(task.get("goal_mode", str(task["id"]) not in children))
        work = [(hermes.hermes_id(s["task"]["id"]), s) for s in shows
                if is_work_card(goal_mode(s["task"]), s["task"].get("body"), s["task"].get("status", ""))]
        self.work = [i for i, _ in work]
        self.goals: dict[str, list[str]] = {}
        for i, show in work:
            if goal_mode(show["task"]):
                for child in show.get("children") or []:
                    if hermes.hermes_id(child) in self.facts:
                        self.goals.setdefault(hermes.hermes_id(child), []).append(i)

    def goal(self, goal_id: str, now: int) -> Optional[dict]:
        from plugins.kanban.dashboard.work_api import goal_view
        if goal_id not in self.goals:
            return None
        view = goal_view(self.facts[goal_id], [self.facts[i] for i in self.goals[goal_id]], now)
        return _read_only(dict(view, workCards=[_read_only(c) for c in view["workCards"]]))


def goals(src: hermes.Source, now: int) -> dict[str, list]:
    board = _Board(src)
    return {"goals": [board.goal(i, now) for i in board.goals]}


def cards(src: hermes.Source, now: int, agent: Optional[str], state: Optional[str]) -> dict[str, list]:
    from plugins.kanban.dashboard.work_api import _implementer, card_summary
    board = _Board(src)
    rows = [card_summary(board.facts[i], now) for i in board.work
            if agent is None or _implementer(board.facts[i]) == agent]
    return {"cards": [_read_only(r) for r in rows if state is None or r["state"] == state]}


def inbox(src: hermes.Source, now: int) -> dict[str, list]:
    from plugins.kanban.dashboard.work_api import _inbox_blocked, question_rows
    board = _Board(src)
    every = list(board.facts.values())
    return {"blocked": [_read_only(_inbox_blocked(f, now)) for f in every if f.task.status == "blocked"],
            "questions": [_read_only(q) for f in every for q in question_rows(f)]}


def card(src: hermes.Source, card_id: str, now: int) -> Optional[dict]:
    from plugins.kanban.dashboard.work_api import card_detail
    raw = hermes.raw_id(card_id)
    if raw not in {str(row["id"]) for row in hermes.card_list(src)}:
        return None
    return _read_only(card_detail(facts(hermes.card_show(src, raw)), now))


def goal(src: hermes.Source, goal_id: str, now: int) -> Optional[dict]:
    return _Board(src).goal(hermes.hermes_id(hermes.raw_id(goal_id)), now)


def add(out: dict, board: Optional[str], read: Callable[[hermes.Source], dict[str, list]]) -> dict:
    """*out* with the Hermes board's rows added after LINA's, and ``errors``. Only the default board's reply gets them,
    and only while the source is set, so every other reply is as before; a failing command is one ``errors`` entry."""
    from hermes_cli.lina_board import _source
    sec = hermes.section() if board is None else None
    if sec is None:
        return out
    errors: list[dict] = []
    extra = _source(hermes.SOURCE, errors, lambda: _cards_part(hermes.source(sec), read)) or {}
    return dict(out, **{key: out[key] + rows for key, rows in extra.items()}, errors=errors)


def _cards_part(src: hermes.Source, read: Callable[[hermes.Source], Any]) -> Any:
    """*read*'s rows, or none when only the old record is set (``board.hermes.work`` without ``command``)."""
    return None if src.command is None else read(src)


def one(item_id: str, read: Callable[[hermes.Source, str], Optional[dict]]) -> dict:
    """A ``hermes:`` card or goal: 404 when the source is off or does not have it, 503 when its command fails."""
    from hermes_cli.lina_board import _source
    sec = hermes.section()
    if sec is None:
        raise HTTPException(status_code=404, detail=f"card {item_id} not found")
    errors: list[dict] = []
    found = _source(hermes.SOURCE, errors, lambda: _cards_part(hermes.source(sec), lambda src: read(src, item_id)))
    if errors:
        raise HTTPException(status_code=503, detail=errors[0]["message"])
    if found is None:
        raise HTTPException(status_code=404, detail=f"card {item_id} not found")
    return found
