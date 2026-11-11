"""The work-record side of ``hermes kanban plan`` (lina#163).

A plan's goal may name its record goal (``ledger_goal: G<n>``) and each work card the record to-dos it covers
(``ledger_todos``: an open to-do id ``T<n>`` under that goal, or a new ``{title, why, done, check}``). Order:
:func:`check` reads the record (nothing written) before the board transaction; after it commits, :func:`write`
links the goal card to the goal, creates the new to-dos through ``work add 할 일`` (``--by`` the card's assignee,
``--link kanban:<card>``: the card is the run that carries them), links each card to its to-dos
(``work card link``), then sends every other agent one line naming its card and to-do ids (``notify.tell``).

:func:`write` is idempotent: a card already carries every record to-do with its ``kanban:`` link, open or closed (a
new to-do is matched by its title), and only what it does not carry is created or linked. A notice is owed until
the card holds a ``lina-work`` comment ``[notice] @<agent> …`` written right after it was sent, so a run that
stopped early is finished by the next one and a run after a finished one sends nothing.
"""

from __future__ import annotations

import io
import sqlite3
import sys
from contextlib import contextmanager, redirect_stderr
from dataclasses import dataclass, field
from typing import Iterator

from hermes_cli.kanban_plan import NewTodo, Plan, PlanError, WorkCard

# The card id exists only after the board transaction; the read-only check of a new to-do sees a well-formed card
# link in its place, so the run rule (session, worker run or work card) is checked as the real write will see it.
_PENDING_CARD = "kanban:t_00000000"
_FAILURES = (SystemExit, OSError, ValueError, RuntimeError, sqlite3.Error)


class RecordStepError(RuntimeError):
    """The record step failed after the board was written; ``step`` names where."""

    def __init__(self, step: str, detail: str):
        super().__init__(f"{step}: {detail}")
        self.step = step


@dataclass
class Notice:
    agent: str
    card: str
    line: str


@dataclass
class Record:
    """What the record step did (or, for a dry run, would do) per card key."""
    goal: str
    todos: dict[str, list[str]] = field(default_factory=dict)      # card key -> its to-do ids
    created: dict[str, list[str]] = field(default_factory=dict)    # card key -> to-dos created in this run
    attached: dict[str, list[str]] = field(default_factory=dict)   # card key -> created or linked in this run
    new: dict[str, list[str]] = field(default_factory=dict)        # dry run: card key -> titles to create
    notices: list[Notice] = field(default_factory=list)


@contextmanager
def _work() -> Iterator[None]:
    """The work record bound for this process: fresh config, store closed afterwards."""
    from hermes_cli.work import env, ledger
    env.reset()
    env.current()
    try:
        yield
    finally:
        ledger.close()


@contextmanager
def _refused(where: str) -> Iterator[None]:
    """A ``lina work`` refusal (``work: …`` on stderr, exit 2) as a :class:`PlanError` naming *where*."""
    err = io.StringIO()
    try:
        with redirect_stderr(err):
            yield
    except SystemExit as exc:
        detail = " ".join(line.removeprefix("work: ") for line in err.getvalue().splitlines() if line.strip())
        raise PlanError(f"{where}: {detail or f'refused by the work record (exit {exc.code})'}") from None
    sys.stderr.write(err.getvalue())


@contextmanager
def _step(name: str) -> Iterator[None]:
    err = io.StringIO()
    try:
        with redirect_stderr(err):
            yield
    except _FAILURES as exc:
        said = " ".join(line.removeprefix("work: ") for line in err.getvalue().splitlines() if line.strip())
        detail = said or (f"exit {exc.code}" if isinstance(exc, SystemExit) else f"{type(exc).__name__}: {exc}")
        raise RecordStepError(name, detail) from None
    sys.stderr.write(err.getvalue())


def _identity(title: str) -> str:
    return " ".join((title or "").lower().split())


def _new_todo_args(card: WorkCard, goal: str, todo: NewTodo, link: str):
    from hermes_cli.work.cmd_add import todo_args
    return todo_args(by=card.assignee, goal=goal, title=todo.title, why=todo.why, done=todo.done, check=todo.check,
                     link=link)


def _check_existing(card: WorkCard, todo_id: str, goal: str, recs: dict) -> None:
    from hermes_cli.work.kinds import TODO, kind_now
    r = recs.get(todo_id)
    if not r or kind_now(r.get("kind")) != TODO or r.get("state") != "open" or r.get("parent") != goal:
        raise PlanError(f"card {card.key!r}: ledger_todos {todo_id} is not an open to-do under {goal} "
                        "in the work record")


def _notice_line(card_id: str, title: str, todo_ids: list[str]) -> str:
    from hermes_cli.work.locale import t
    return t("[작업 카드] {card} {title} · 할 일 {ids}", card=card_id, title=title, ids=", ".join(todo_ids))


def _tells(agent: str, runner: str) -> bool:
    """An agent of the record other than the profile running the plan; the owner is never one."""
    from hermes_cli.work.people import agents
    return agent != runner and agent in agents()


def check(plan: Plan, *, runner: str) -> Record:
    """Read-only: the goal is an open record goal, every named to-do an open to-do under it, and every new to-do
    passes ``work add 할 일``'s checks. Returns what :func:`write` would do; raises :class:`PlanError`."""
    from hermes_cli.work import ledger
    from hermes_cli.work.cmd_add import add
    goal = plan.goal.ledger_goal
    out = Record(goal=goal)
    with _work(), _refused("work record"):
        recs, _ = ledger.read_all()
        g = recs.get(goal)
        if not g or g.get("kind") != "목표" or g.get("state") != "open":
            raise PlanError(f"goal: ledger_goal {goal} is not an open goal in the work record")
        for card in plan.cards:
            news = [item for item in card.ledger_todos if isinstance(item, NewTodo)]
            for item in card.ledger_todos:
                if isinstance(item, str):
                    _check_existing(card, item, goal, recs)
            for todo in news:
                with _refused(f"card {card.key!r}: new to-do {todo.title!r}"):
                    add(_new_todo_args(card, goal, todo, _PENDING_CARD), write=False)
            out.todos[card.key] = [item for item in card.ledger_todos if isinstance(item, str)]
            out.new[card.key] = [todo.title for todo in news]
            if card.ledger_todos and _tells(card.assignee, runner):
                out.notices.append(Notice(card.assignee, card.key, ""))
    return out


def _carried(card_ref: str) -> list[str]:
    """Every record to-do carrying the card's ``kanban:`` link, open or closed: what the card already has."""
    from hermes_cli.work import ledger
    from hermes_cli.work.cards import card_link
    from hermes_cli.work.kinds import TODO, kind_now
    link = card_link(card_ref)
    recs, order = ledger.read_all()
    return [i for i in order if kind_now(recs[i]["kind"]) == TODO and link in (recs[i].get("links") or [])]


def _card_todos(card: WorkCard, card_ref: str, goal: str) -> tuple[list[str], list[str]]:
    """Create the card's new to-dos it does not carry yet and link it to every named to-do; returns the ids
    created and the ids attached (created or linked) now."""
    from hermes_cli.work import ledger
    from hermes_cli.work.cards import card_link, link_card
    from hermes_cli.work.cmd_add import add
    have = _carried(card_ref)
    recs, _ = ledger.read_all()
    titles = {_identity(recs[i].get("title", "")) for i in have}
    created = [add(_new_todo_args(card, goal, todo, card_link(card_ref)))["id"]
               for todo in card.ledger_todos if isinstance(todo, NewTodo) and _identity(todo.title) not in titles]
    attach = [i for i in card.ledger_todos if isinstance(i, str) and i not in have] + created
    if attach:
        link_card(card_ref, todos=attach)
    return created, attach


def _marker(agent: str) -> str:
    return f"[notice] @{agent} "


def _board(board: str):
    from hermes_cli import kanban_db as kb
    return None if board in ("", kb.DEFAULT_BOARD) else board


def _told(board: str, card_id: str, agent: str) -> bool:
    """The card carries the ``lina-work`` comment saying its assignee was told (written after the notice)."""
    from hermes_cli import kanban_db as kb
    from hermes_cli.kanban_db_connect import connect_closing
    from hermes_cli.work.cards import AUTHOR
    with connect_closing(board=_board(board)) as conn:
        return any(c.author == AUTHOR and c.body.startswith(_marker(agent)) for c in kb.list_comments(conn, card_id))


def _mark_told(board: str, card_id: str, agent: str, line: str) -> None:
    from hermes_cli import kanban_db as kb
    from hermes_cli.kanban_db_connect import connect_closing
    from hermes_cli.work.cards import AUTHOR
    with connect_closing(board=_board(board)) as conn:
        kb.add_comment(conn, card_id, AUTHOR, _marker(agent) + line)


def write(plan: Plan, ids: dict[str, str], *, board: str, runner: str) -> Record:
    """The record step after the board transaction; raises :class:`RecordStepError` naming the failed step."""
    from hermes_cli import kanban_db as kb
    from hermes_cli.work import ledger, state
    from hermes_cli.work.cards import card_link, link_card
    from hermes_cli.work.notify import tell
    from hermes_cli.work.run import card_todos

    def ref(task_id: str) -> str:
        return task_id if board in ("", kb.DEFAULT_BOARD) else f"{board}/{task_id}"

    goal = plan.goal.ledger_goal
    out = Record(goal=goal)
    with _work():
        with _step(f"goal link ({ids['goal']} -> {goal})"):
            recs, _ = ledger.read_all()
            if card_link(ref(ids["goal"])) not in (recs.get(goal, {}).get("links") or []):
                link_card(ref(ids["goal"]), goal=goal)
        for card in plan.cards:
            with _step(f"to-dos of card {card.key!r} ({ids[card.key]})"):
                out.created[card.key], out.attached[card.key] = (
                    _card_todos(card, ref(ids[card.key]), goal) if card.ledger_todos else ([], []))
                out.todos[card.key] = card_todos(ref(ids[card.key]))
        with _step("state"):
            state.recompute()
        for card in plan.cards:
            # Owed until the card carries the marker: a run that failed before its notices, or between a to-do's
            # add and its card link, is finished by the next run. Told first, marked after (at least once).
            if not (out.todos[card.key] and _tells(card.assignee, runner)):
                continue
            with _step(f"notice to {card.assignee} ({ids[card.key]})"):
                if _told(board, ids[card.key], card.assignee):
                    continue
                line = _notice_line(ids[card.key], card.title, out.todos[card.key])
                tell(card.assignee, line)
                _mark_told(board, ids[card.key], card.assignee, line)
            out.notices.append(Notice(card.assignee, card.key, line))
    return out
