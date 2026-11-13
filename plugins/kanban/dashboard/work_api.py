"""Work API for the app: goals, work cards, their worker runs and the owner inbox (lina#112).

Mounted by ``plugin_api.py`` under ``/api/plugins/kanban/work`` (behind the dashboard auth gate).
A work card is a goal-mode card, any card whose body has a ``## To-dos`` section (lina#290), or any
blocked or triage card (lina#664); a goal card above it, when there is one, is its link-child. A listed
card carries the title of the work record's goal that links it (``goalTitle``, lina#666), a Hermes board's card too
(lina#690). Reads open the board read-only; the one write (answer) reuses ``add_comment`` and ``unblock_task``.
``work_state`` is the only place the five app states are computed. The schema the app's client generator reads is
``docs/api/work-client.yaml``.
"""

from __future__ import annotations

import re
import sqlite3
import time
from contextlib import closing, contextmanager
from dataclasses import dataclass, field
from typing import Any, Callable, Iterator, Optional

from fastapi import APIRouter, HTTPException, Query
from pydantic import BaseModel

from hermes_cli import kanban_db
from hermes_cli import kanban_db_connect as kbc
from hermes_cli.goals import GATE_WAIT_EXIT_CODE
from hermes_cli.kanban_gates import own_names
from hermes_cli.kanban_goal_gates import GATE_EVENT_KIND
from hermes_cli.lina_board_hermes import READ_ONLY, is_hermes
from plugins.kanban.dashboard import work_api_hermes as hermes_rows

router = APIRouter(prefix="/work")

WORK_STATES = ("planned", "in_progress", "waiting", "stopped", "done")
_PLANNED_STATUSES = {"triage", "todo", "scheduled", "ready"}
# A run is alive while its claim holds or it beat within this window (the claim TTL default).
LIVE_WINDOW_SECONDS = kanban_db.DEFAULT_CLAIM_TTL_SECONDS
NOW_LINE_CHARS = 120
_BLOCK_EVENT_KINDS = ("blocked", "block_loop_detected")
# The transitions that park a blocked card; ``gave_up`` is the failure breaker's block, which ends any earlier wait.
_PARK_EVENT_KINDS = (*_BLOCK_EVENT_KINDS, "gave_up")
_TODO_HEADING = re.compile(r"^##\s+To-?dos\s*$", re.IGNORECASE)
_TODO_LINE = re.compile(r"^\s*[-*]\s+(?:\[(?P<box>[ xX])\]\s+)?(?P<text>\S.*?)\s*$")
_ISSUE_REF = re.compile(r"\b[\w.-]+/[\w.-]+#\d+\b")
_PRS_GATE = re.compile(r"\bgate\s+prs\b")
_QUESTION_PREFIXES = ("질문:", "Q:")
# A stopped card's ``(reason, waitingOn, retry)`` by the kind of its latest block event (lina#629); the table is
# the one the schema's ``Reason`` documents.
# A ``dependency`` block that is still ``blocked`` (a failure-breaker block classified in place) is sticky: only a
# ``todo`` dependency card wakes by itself, so an owner has to act.
_KIND_STOPS = {"needs_input": ("decision", "owner", "none"), "capability": ("feedback", "owner", "none"),
               "transient": ("hold", "owner", "none"), "dependency": ("blocked_by_item", "owner", "none")}
# The kind word of an untyped block written in the owner-inbox format, ``<agent> · <kind> · <summary>``.
_INBOX_LINE = re.compile(r"^[^·\n]+?\s·\s(?P<kind>.+?)\s·\s")
_INBOX_REASONS = {"갈림길": "decision", "질문": "decision", "검토": "review", "돈·바깥": "feedback",
                  "끝 조건": "feedback"}
# The owner's one line on a stopped card (lina#690): what a block reason loses before it is shown, where its first
# sentence ends, and the brackets a lost token leaves empty.
ASK_CHARS = 60
_ASK_DROP = re.compile(r"https?://[^\s()\[\]<>]*[^\s()\[\]<>.,;:!?]"
                       r"|(?:카드\s*)?\b(?:hermes:)?t_[0-9A-Za-z]+\b|\bOT-[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*\b")
_SENTENCE_END = re.compile(r"(?<=[.!?。？！])\s|\n")
_EMPTY_BRACKETS = re.compile(r"[(\[][\s,;·]*[)\]]")
_HANGUL = re.compile(r"[가-힣]")
# Worker run IDs pasted into comments: omo jobs, single-issue runs, ``lina worker`` runs.
_WORKER_ID_PATTERNS = (
    ("omo", re.compile(r"\b(OT-[A-Z0-9]{6,})\b")),
    ("quick-issue", re.compile(r"\bquick-issue[:\s]+(?:run\s+)?([A-Za-z0-9][\w.-]{2,})")),
    ("lina-worker", re.compile(r"\blina[ -]worker[:\s]+(?:run\s+)?([A-Za-z0-9][\w.-]{2,})")),
)


# --- Board access ------------------------------------------------------------

def _board_slug(board: Optional[str]) -> Optional[str]:
    """The slug of an existing board, taken from the board list rather than from the request, so
    the database path is never built from caller input (400 malformed, 404 unknown)."""
    if not board:
        return None
    try:
        wanted = kanban_db._normalize_board_slug(board)
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))
    if not wanted:
        return None
    from hermes_cli.kanban_db_boards import list_boards
    for meta in list_boards(include_archived=False):
        if meta.get("slug") == wanted:
            return str(meta["slug"])
    raise HTTPException(status_code=404, detail=f"board {wanted!r} does not exist")


@contextmanager
def _read_conn(board: Optional[str]) -> Iterator[Optional[sqlite3.Connection]]:
    """Read-only connection to the board, or None when the board has no database yet."""
    path = kanban_db.kanban_db_path(board=_board_slug(board))
    if not path.exists():
        yield None
        return
    conn = sqlite3.connect(path.resolve().as_uri() + "?mode=ro", uri=True)
    conn.row_factory = sqlite3.Row
    conn.text_factory = kanban_db._lossy_text
    with closing(conn):
        yield conn


# --- Card facts ----------------------------------------------------------------

@dataclass
class CardFacts:
    task: kanban_db.Task
    events: list[kanban_db.Event] = field(default_factory=list)
    comments: list[kanban_db.Comment] = field(default_factory=list)
    runs: list[kanban_db.Run] = field(default_factory=list)
    board: Optional[str] = None  # the board the request resolved (``_board_slug``); None = the default board
    own: set[str] = field(default_factory=set)  # ``own_names``: the implementer(s) and the assignee, never the reviewer
    # A source with no claim or heartbeat to read (the Hermes board through its CLI, lina#481): an open run is live.
    assume_live: bool = False


def _facts(conn: sqlite3.Connection, task: kanban_db.Task, board: Optional[str] = None) -> CardFacts:
    """A card's events, comments and runs, and the identities that own it. ``own_names`` reads only
    the card's id and assignee, which is what the gates' card row carries into it."""
    return CardFacts(task, kanban_db.list_events(conn, task.id), kanban_db.list_comments(conn, task.id),
                     kanban_db.list_runs(conn, task.id), board,
                     own_names(conn, {"id": task.id, "assignee": task.assignee}))


def _implementer(facts: CardFacts) -> Optional[str]:
    """The agent that did the card — the ``agent`` the app lists and filters it by (lina#290).

    ``request_review`` moves ``tasks.assignee`` to the reviewer, so the implementer comes from
    ``own_names``: the first run by an identity it counts as the card's own, else an assignee it
    counts, else the first of those identities. The learning record picks its ``assignee`` the
    same way."""
    return (next((r.profile for r in facts.runs if r.profile in facts.own), None)
            or (facts.task.assignee if facts.task.assignee in facts.own else None)
            or next(iter(sorted(facts.own)), None))


def _reviewer(facts: CardFacts) -> Optional[str]:
    """The reviewer, only when a review run exists: the latest run ``own_names`` does not count as
    the card's own (as ``review_records`` reads them)."""
    return next((r.profile for r in reversed(facts.runs) if r.profile and r.profile not in facts.own), None)


def _agent_fields(facts: CardFacts) -> dict[str, Any]:
    """``agent`` (the implementer), plus ``reviewer`` only where a review run exists."""
    reviewer = _reviewer(facts)
    return {"agent": _implementer(facts), **({"reviewer": reviewer} if reviewer is not None else {})}


def _work_cards(conn: sqlite3.Connection) -> list[sqlite3.Row]:
    """Every card the app lists as work (lina#290, lina#664): a goal-mode card, any non-archived card
    whose body has a ``## To-dos`` section, or a blocked or triage card — with or without a goal card above
    it. The checked-line writer selects the same cards (``hermes_cli.lina_board_plain._board_cards``)."""
    from hermes_cli.lina_board_plain import is_work_card
    rows = conn.execute("SELECT id, goal_mode, body, status FROM tasks "
                        "ORDER BY created_at, rowid").fetchall()
    return [r for r in rows if is_work_card(r["goal_mode"], r["body"], r["status"])]


def _work_card_ids(conn: sqlite3.Connection) -> list[str]:
    return [r["id"] for r in _work_cards(conn)]


def _goal_work_cards(conn: sqlite3.Connection) -> dict[str, list[str]]:
    """``{goal_card_id: [work_card_id, ...]}`` for the goal-mode work cards that have a goal card."""
    work = {r["id"] for r in _work_cards(conn) if r["goal_mode"]}
    goals: dict[str, list[str]] = {}
    rows = conn.execute(
        "SELECT l.parent_id, l.child_id FROM task_links l JOIN tasks c ON c.id = l.child_id "
        "JOIN tasks p ON p.id = l.parent_id "
        "WHERE c.status != 'archived' ORDER BY c.created_at, c.rowid, p.created_at, p.rowid").fetchall()
    for r in rows:
        if r["parent_id"] in work:
            goals.setdefault(r["child_id"], []).append(r["parent_id"])
    return goals


def _latest_gate_results(events: list[kanban_db.Event]) -> list[dict[str, Any]]:
    """Latest ``goal_gate`` event per command, in first-run order."""
    latest: dict[str, kanban_db.Event] = {}
    for ev in events:
        if ev.kind == GATE_EVENT_KIND and ev.payload and ev.payload.get("command"):
            latest[ev.payload["command"]] = ev
    return [{
        "command": cmd, "exitCode": ev.payload.get("exit_code"), "passed": bool(ev.payload.get("passed")),
        "waiting": ev.payload.get("exit_code") == GATE_WAIT_EXIT_CODE,
        "outputTail": ev.payload.get("output_tail") or "", "at": ev.created_at,
    } for cmd, ev in latest.items()]


def _latest_gate_event(events: list[kanban_db.Event], run_id: Optional[int]) -> Optional[kanban_db.Event]:
    return next((ev for ev in reversed(events) if ev.kind == GATE_EVENT_KIND and ev.run_id == run_id), None)


def _current_run(facts: CardFacts) -> Optional[kanban_db.Run]:
    run_id = facts.task.current_run_id
    return next((r for r in facts.runs if r.id == run_id and r.ended_at is None), None)


def run_is_live(run: Optional[kanban_db.Run], now: int) -> bool:
    """An open run whose claim still holds or that beat (or started) within the live window."""
    if run is None or run.ended_at is not None:
        return False
    last_beat = run.last_heartbeat_at or run.started_at or 0
    return (run.claim_expires or 0) > now or now - last_beat <= LIVE_WINDOW_SECONDS


def _run_live(facts: CardFacts, run: Optional[kanban_db.Run], now: int) -> bool:
    if facts.assume_live:
        return run is not None and run.ended_at is None
    return run_is_live(run, now)


def _latest_block_payload(events: list[kanban_db.Event]) -> dict[str, Any]:
    ev = next((e for e in reversed(events) if e.kind in _BLOCK_EVENT_KINDS), None)
    return (ev.payload or {}) if ev else {}


def _park_event(events: list[kanban_db.Event]) -> Optional[kanban_db.Event]:
    return next((e for e in reversed(events) if e.kind in _PARK_EVENT_KINDS), None)


def _block_stop(task: kanban_db.Task, events: list[kanban_db.Event]) -> tuple[Optional[str], ...]:
    """``(reason, waitingOn, retry)`` of a blocked card, from its latest block event (its kind, else the card's
    ``block_kind``), never from the title. A recorded wait is woken by the dispatcher; a typed kind reads by
    :data:`_KIND_STOPS`; an untyped block's text is the goal loop's ``gate`` / ``budget``, an owner-inbox line, or a
    plain sentence for the owner. A card with no block event and no kind is all None."""
    event = _park_event(events)
    if event is not None and event.kind == "gave_up":
        return "failed", "owner", "none"
    payload = (event.payload or {}) if event else {}
    kind = payload.get("kind") or task.block_kind
    wait = payload.get("wait_for")
    if isinstance(wait, dict):
        return ("blocked_by_item" if wait.get("kind") in ("card", "review") else "hold"), None, "scheduled"
    if kind in _KIND_STOPS:
        return _KIND_STOPS[kind]
    text = str(payload.get("reason") or "").strip()
    lowered = text.lower()
    if "goal gate" in lowered or "gate still failing" in lowered:
        return "gate", "owner", "none"
    if "turn budget" in lowered or "never called kanban_complete" in lowered:
        return "budget", "owner", "none"
    inbox = _INBOX_LINE.match(text)
    if inbox and inbox["kind"] in _INBOX_REASONS:
        return _INBOX_REASONS[inbox["kind"]], "owner", "none"
    if text:
        return ("decision" if text.endswith(("?", "？")) else "feedback"), "owner", "none"
    return None, None, None


def _ask(text: str) -> Optional[str]:
    """The owner's one line from a block reason (lina#690): an owner-inbox line's summary, else the reason, without
    card ids, run ids and URLs; its first sentence, at most :data:`ASK_CHARS` characters. None when no Korean is left:
    an English-only reason is a worker's note, not a line for the owner."""
    inbox = _INBOX_LINE.match(text)
    text = _ASK_DROP.sub(" ", text[inbox.end():] if inbox else text).strip()
    first = _EMPTY_BRACKETS.sub(" ", _SENTENCE_END.split(text, maxsplit=1)[0])
    first = re.sub(r"\s+([.,!?？])", r"\1", " ".join(re.sub(r"[`*_>#]+", " ", first).split())).strip(" :·,-")
    if not _HANGUL.search(first):
        return None
    return first if len(first) <= ASK_CHARS else first[:ASK_CHARS - 1].rstrip() + "…"


def stop_fields(facts: CardFacts, state: str) -> dict[str, Optional[str]]:
    """``waitingOn``, ``retry`` and ``ask`` of a card (lina#629, lina#690): a blocked card's by :func:`_block_stop`,
    and when the owner has to act, the one line :func:`_ask` makes of its latest block event's reason; a scheduled
    card's ``retry: scheduled``; null on every other card (a running card with no live run included: who acts is not
    known)."""
    if facts.task.status == "scheduled":
        return {"waitingOn": None, "retry": "scheduled", "ask": None}
    if state != "stopped" or facts.task.status != "blocked":
        return {"waitingOn": None, "retry": None, "ask": None}
    _, waiting_on, retry = _block_stop(facts.task, facts.events)
    event = _park_event(facts.events)
    reason = str(((event.payload or {}) if event else {}).get("reason") or "")
    return {"waitingOn": waiting_on, "retry": retry, "ask": _ask(reason) if waiting_on == "owner" else None}


def card_stop(facts: CardFacts, now: int) -> dict[str, Optional[str]]:
    """``state``, ``reason``, ``waitingOn``, ``retry`` and ``ask`` of a card: the one place the list row and the detail
    (the row and more, :func:`card_detail`) take why a card stopped and who acts, on LINA's board and the Hermes board
    alike (lina#690)."""
    state, reason = work_state(facts, now)
    return {"state": state, "reason": reason, **stop_fields(facts, state)}


def work_state(facts: CardFacts, now: Optional[int] = None) -> tuple[str, Optional[str]]:
    """``(state, reason)`` for one card: the single source of the app's five states.

    planned = triage/todo/scheduled/ready; in_progress = running with a live current run;
    waiting = review, or running while the current run's latest gate exited 75; stopped = blocked
    (reason: :func:`_block_stop`) or running with no live run
    (``unresponsive``); done = done. The kanban goal loop turns a judge ``wait`` into
    ``continue`` and records nothing, so a judge wait has no board signal to read.
    """
    now = int(time.time()) if now is None else now
    status = facts.task.status
    if status == "done":
        return "done", None
    if status == "review":
        return "waiting", "review"
    if status == "blocked":
        return "stopped", _block_stop(facts.task, facts.events)[0]
    if status != "running":
        return "planned", status if status in _PLANNED_STATUSES else None
    if not _run_live(facts, _current_run(facts), now):
        return "stopped", "unresponsive"
    gate = _latest_gate_event(facts.events, facts.task.current_run_id)
    if gate is not None and (gate.payload or {}).get("exit_code") == GATE_WAIT_EXIT_CODE:
        return "waiting", "gate"
    return "in_progress", None


# --- To-dos, now line, worker runs ---------------------------------------------------

def _todo_section(body: Optional[str]) -> list[str]:
    lines, inside = [], False
    for line in (body or "").splitlines():
        if _TODO_HEADING.match(line.strip()):
            inside = True
        elif inside and re.match(r"^#{1,2}\s", line):
            break
        elif inside:
            lines.append(line)
    return lines


def _prs_passed_refs(gates: list[dict[str, Any]]) -> tuple[set[str], bool]:
    """Issue refs a passing ``gate prs`` vouches for; ``True`` = it named none, so all of them."""
    refs: set[str] = set()
    covers_all = False
    for gate in gates:
        if gate["passed"] and _PRS_GATE.search(gate["command"]):
            named = set(_ISSUE_REF.findall(gate["command"]))
            refs |= named
            covers_all = covers_all or not named
    return refs, covers_all


def parse_todos(body: Optional[str], gates: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """``## To-dos`` lines: ``- owner/repo#n`` (done when ticked or a passing ``prs`` gate covers
    it) and ``- [ ] text`` / ``- [x] text`` checklist items. Other lines are notes."""
    passed_refs, prs_all = _prs_passed_refs(gates)
    todos = []
    for line in _todo_section(body):
        m = _TODO_LINE.match(line)
        if not m:
            continue
        text, ticked = m["text"], (m["box"] or " ").lower() == "x"
        ref = _ISSUE_REF.match(text)
        if ref:
            done = ticked or prs_all or ref.group(0) in passed_refs
            todos.append({"id": ref.group(0), "title": text, "kind": "issue", "done": done})
        elif m["box"] is not None:
            todos.append({"id": f"check-{len(todos) + 1}", "title": text, "kind": "check", "done": ticked})
    return todos


def _plain_line(text: str) -> str:
    plain = " ".join(re.sub(r"[`*_>#]+", " ", text).split())
    return plain if len(plain) <= NOW_LINE_CHARS else plain[:NOW_LINE_CHARS - 1].rstrip() + "…"


def now_line(facts: CardFacts) -> Optional[str]:
    """Latest comment or heartbeat note, plain and at most 120 chars."""
    notes = [(c.created_at, c.id, c.body) for c in facts.comments]
    notes += [(e.created_at, e.id, e.payload["note"]) for e in facts.events
              if e.kind == "heartbeat" and e.payload and e.payload.get("note")]
    notes = [n for n in notes if str(n[2]).strip()]
    return _plain_line(str(max(notes)[2])) if notes else None


def worker_runs(comments: list[kanban_db.Comment]) -> list[dict[str, Any]]:
    seen: dict[tuple[str, str], dict[str, Any]] = {}
    for c in comments:
        for kind, pattern in _WORKER_ID_PATTERNS:
            for run_id in pattern.findall(c.body or ""):
                entry = seen.setdefault((kind, run_id), {
                    "id": run_id, "kind": kind, "firstSeenAt": c.created_at, "lastSeenAt": c.created_at})
                entry["firstSeenAt"] = min(entry["firstSeenAt"], c.created_at)
                entry["lastSeenAt"] = max(entry["lastSeenAt"], c.created_at)
    return list(seen.values())  # comments come in id order, so this is first-seen order


def runs_awaiting_verdict(facts: CardFacts) -> list[dict[str, Any]]:
    """Runs this card started (``lina worker``) with no verdict yet; each waits on the agent that judges it
    (the run's own agent, else the card's implementer), never on the owner (lina#295)."""
    from hermes_cli.kanban_db_verdicts import verdicts_of_events
    fallback = _implementer(facts)
    return [{"id": r["run_id"], "state": r["state"], "waitingOn": f"agent:{r['agent'] or fallback}"}
            for r in verdicts_of_events(facts.events) if not r["judged"] and (r["agent"] or fallback)]


def open_questions(facts: CardFacts) -> list[kanban_db.Comment]:
    """Assignee comments starting ``질문:`` / ``Q:`` with no later comment by someone else."""
    assignee = facts.task.assignee
    pending: list[kanban_db.Comment] = []
    for c in facts.comments:
        if assignee and c.author == assignee:
            if (c.body or "").lstrip().startswith(_QUESTION_PREFIXES):
                pending.append(c)
        else:
            pending.clear()
    return pending


# --- Serialization ---------------------------------------------------------------

def _elapsed_seconds(task: kanban_db.Task, now: int) -> int:
    start = task.started_at or task.created_at
    if not start:
        return 0
    end = task.completed_at if task.status == "done" and task.completed_at else now
    return max(0, end - start)


def card_times(facts: CardFacts) -> dict[str, Optional[int]]:
    """``endedAt`` (a done card's ``completed_at``) and ``lastHeartbeatAt`` (the latest beat in the card's or a run's
    heartbeat column or its heartbeat events; the Hermes CLI carries only the events) for the list row and the detail
    alike, from facts the list already reads (lina#577)."""
    task = facts.task
    beats = [task.last_heartbeat_at, *(r.last_heartbeat_at for r in facts.runs),
             *(e.created_at for e in facts.events if e.kind == "heartbeat")]
    beats = [b for b in map(kanban_db._to_epoch, beats) if b is not None]
    return {"endedAt": kanban_db._to_epoch(task.completed_at) if task.status == "done" else None,
            "lastHeartbeatAt": max(beats, default=None)}


def card_summary(facts: CardFacts, now: int, goal_title: Optional[str] = None) -> dict[str, Any]:
    from hermes_cli.lina_board_plain import board_key, lookup  # checked model-written lines (lina#151), cache only
    todos = parse_todos(facts.task.body, _latest_gate_results(facts.events))
    return {
        "id": facts.task.id, "title": facts.task.title, "goalTitle": goal_title, **_agent_fields(facts),
        **card_stop(facts, now),
        "elapsedSeconds": _elapsed_seconds(facts.task, now), "todoProgress": {"done": sum(t["done"] for t in todos), "total": len(todos)},
        "now": now_line(facts), **card_times(facts),
        "plain": lookup("card", [facts.task.id], board=board_key(facts.board))[facts.task.id],
    }


def _run_dict(facts: CardFacts, run: kanban_db.Run, now: int) -> dict[str, Any]:
    return {
        "id": run.id, "profile": run.profile, "status": run.status, "outcome": run.outcome,
        "startedAt": run.started_at, "endedAt": run.ended_at, "lastHeartbeatAt": run.last_heartbeat_at,
        "summary": run.summary, "live": _run_live(facts, run, now),
    }


def card_detail(facts: CardFacts, now: int, goal_title: Optional[str] = None) -> dict[str, Any]:
    gates = _latest_gate_results(facts.events)
    return {
        **card_summary(facts, now, goal_title),
        "todos": parse_todos(facts.task.body, gates),
        "workerRuns": worker_runs(facts.comments),
        "runsAwaitingVerdict": runs_awaiting_verdict(facts),
        "gates": gates,
        "comments": [{"id": c.id, "author": c.author, "body": c.body, "createdAt": c.created_at}
                     for c in facts.comments],
        "runs": [_run_dict(facts, r, now) for r in facts.runs],
    }


def _evidence(facts: CardFacts) -> Optional[dict[str, Any]]:
    """What closed a done card: its latest completed run's summary and metadata (or result)."""
    if facts.task.status != "done":
        return None
    run = next((r for r in reversed(facts.runs) if r.outcome == "completed"), None)
    return {
        "cardId": facts.task.id, "title": facts.task.title,
        "summary": (run.summary if run and run.summary else facts.task.result),
        "metadata": run.metadata if run else None, "completedAt": facts.task.completed_at,
    }


def _goal_dict(conn: sqlite3.Connection, goal: kanban_db.Task, work_ids: list[str], now: int,
               board: Optional[str] = None) -> dict[str, Any]:
    goal_facts = _facts(conn, goal, board)
    work = [_facts(conn, t, board) for t in filter(None, (kanban_db.get_task(conn, i) for i in work_ids))]
    return goal_view(goal_facts, work, now)


def goal_view(goal_facts: CardFacts, work: list[CardFacts], now: int,
              goal_title: Callable[[str], Optional[str]] = lambda _card_id: None) -> dict[str, Any]:
    """A goal card with its work cards; *goal_title* gives a work card's ``goalTitle`` by its id (null on LINA's board,
    where the goal row is the goal; the Hermes board's from the record, lina#690)."""
    goal = goal_facts.task
    state, reason = work_state(goal_facts, now)
    return {
        "id": goal.id, "title": goal.title, **_agent_fields(goal_facts), "state": state, "reason": reason,
        "elapsedSeconds": _elapsed_seconds(goal, now),
        "workCards": [card_summary(f, now, goal_title(f.task.id)) for f in work],
        "evidence": [e for e in map(_evidence, [goal_facts, *work]) if e is not None],
    }


def _inbox_blocked(facts: CardFacts, now: int) -> dict[str, Any]:
    state, reason = work_state(facts, now)
    payload = _latest_block_payload(facts.events)
    return {
        "cardId": facts.task.id, "title": facts.task.title, **_agent_fields(facts),
        "state": state, "reason": reason, "blockKind": facts.task.block_kind,
        "blockReason": payload.get("reason"),
    }


# --- Routes ------------------------------------------------------------------

def _require_card(conn: Optional[sqlite3.Connection], card_id: str) -> kanban_db.Task:
    task = kanban_db.get_task(conn, card_id) if conn is not None else None
    if task is None or task.status == "archived":
        raise HTTPException(status_code=404, detail=f"card {card_id} not found")
    return task


def _own_goals(board: Optional[str], now: int) -> dict[str, list]:
    with _read_conn(board) as conn:
        if conn is None:
            return {"goals": []}
        goals = []
        for goal_id, work_ids in _goal_work_cards(conn).items():
            goal = kanban_db.get_task(conn, goal_id)
            if goal is not None:
                goals.append(_goal_dict(conn, goal, work_ids, now, board))
        return {"goals": goals}


def _with_record_errors(reply: dict, titles: GoalTitles) -> dict:
    return dict(reply, errors=[*reply.get("errors", []), *titles.errors]) if titles.errors else reply


@router.get("/goals")
def list_goals(board: Optional[str] = Query(None)):
    now, board, titles = int(time.time()), _board_slug(board), GoalTitles()
    reply = hermes_rows.add(_own_goals(board, now), board,
                            lambda src, src_board: hermes_rows.goals(src, now, titles, src_board))
    return _with_record_errors(reply, titles)


@router.get("/goals/{goal_id}")
def get_goal(goal_id: str, board: Optional[str] = Query(None)):
    if is_hermes(goal_id):
        return hermes_rows.one(goal_id, lambda src, i, src_board: hermes_rows.goal(src, i, int(time.time()),
                                                                                    GoalTitles(), src_board))
    board = _board_slug(board)
    with _read_conn(board) as conn:
        goal = _require_card(conn, goal_id)
        work_ids = _goal_work_cards(conn).get(goal_id)
        if not work_ids:
            raise HTTPException(status_code=404, detail=f"card {goal_id} is not a goal card")
        return _goal_dict(conn, goal, work_ids, int(time.time()), board)


@router.get("/cards")
def list_cards(agent: Optional[str] = Query(None), state: Optional[str] = Query(None),
               board: Optional[str] = Query(None)):
    if state is not None and state not in WORK_STATES:
        raise HTTPException(status_code=400, detail=f"state must be one of {list(WORK_STATES)}")
    now, board, titles = int(time.time()), _board_slug(board), GoalTitles()
    reply = hermes_rows.add(_own_cards(board, now, agent, state, titles), board,
                            lambda src, src_board: hermes_rows.cards(src, now, agent, state, titles, src_board))
    return _with_record_errors(reply, titles)


def _record_goal_titles() -> dict[tuple[str, str], str]:
    """``{(board, card_id): goal title}`` from the work record (lina#666): an item that links ``kanban:<card>`` (a link
    without a board is the default board's) gives the card its goal's title, a goal its own and a to-do its parent
    goal's; the first such item in record order wins (one goal is one to-do, lina#655). Dropped items give none. Read
    the way ``/api/lina/work/items`` reads the record (``lina_board_items._read``)."""
    from hermes_cli.lina_board import _WORK_LOCK, _root
    from hermes_cli.work import env, ledger
    from hermes_cli.work.evaluate import item_name
    from hermes_cli.work.links import kanban_parts
    titles: dict[tuple[str, str], str] = {}
    with _WORK_LOCK:
        env.bind(env.load(_root()))
        try:
            recs, order = ledger.read_all()
            for i in order:
                r = recs[i]
                goal = r if r.get("kind") == "목표" else recs.get(r.get("parent") or "")
                if r.get("state") == "dropped" or not goal or goal.get("kind") != "목표":
                    continue
                for link in r.get("links") or []:
                    link_board, card = kanban_parts(str(link))
                    if card:
                        titles.setdefault((link_board or kanban_db.DEFAULT_BOARD, card.lower()), item_name(goal))
        finally:
            ledger.close()
            env.reset()
    return titles


class GoalTitles:
    """One request's goal titles from the work record (:func:`_record_goal_titles`), read on first use and only once,
    for LINA's and the Hermes board's cards alike (lina#690). A record that cannot be read gives every card a null
    ``goalTitle`` and one ``work`` entry in :attr:`errors`; the cards are still served."""

    def __init__(self) -> None:
        self.errors: list[dict] = []
        self._titles: Optional[dict[tuple[str, str], str]] = None

    def get(self, card_id: str, *boards: Optional[str]) -> Optional[str]:
        """The title of the goal whose item links *card_id* on the first of *boards* that has one (None is the
        default board)."""
        if self._titles is None:
            from hermes_cli.lina_board import _source
            self._titles = _source("work", self.errors, _record_goal_titles) or {}
        found = (self._titles.get((board or kanban_db.DEFAULT_BOARD, card_id.lower())) for board in boards)
        return next((title for title in found if title is not None), None)


def _own_cards(board: Optional[str], now: int, agent: Optional[str], state: Optional[str],
               titles: GoalTitles) -> dict[str, list]:
    with _read_conn(board) as conn:
        if conn is None:
            return {"cards": []}
        cards = []
        for task in filter(None, (kanban_db.get_task(conn, i) for i in _work_card_ids(conn))):
            facts = _facts(conn, task, board)
            if agent is not None and _implementer(facts) != agent:
                continue
            summary = card_summary(facts, now, titles.get(task.id, board))
            if state is None or summary["state"] == state:
                cards.append(summary)
        return {"cards": cards}


@router.get("/cards/{card_id}")
def get_card(card_id: str, board: Optional[str] = Query(None)):
    if is_hermes(card_id):
        return hermes_rows.one(card_id, lambda src, i, src_board: hermes_rows.card(src, i, int(time.time()),
                                                                                    GoalTitles(), src_board))
    board = _board_slug(board)
    with _read_conn(board) as conn:
        task = _require_card(conn, card_id)
        return card_detail(_facts(conn, task, board), int(time.time()), GoalTitles().get(task.id, board))


def question_rows(facts: CardFacts) -> list[dict[str, Any]]:
    return [{"cardId": facts.task.id, "title": facts.task.title, **_agent_fields(facts),
             "commentId": c.id, "body": c.body, "askedAt": c.created_at} for c in open_questions(facts)]


def _own_inbox(board: Optional[str], now: int) -> dict[str, list]:
    with _read_conn(board) as conn:
        if conn is None:
            return {"blocked": [], "questions": []}
        blocked, questions = [], []
        for task in kanban_db.list_tasks(conn):
            facts = _facts(conn, task)
            if task.status == "blocked":
                blocked.append(_inbox_blocked(facts, now))
            questions += question_rows(facts)
        return {"blocked": blocked, "questions": questions}


@router.get("/inbox")
def get_inbox(board: Optional[str] = Query(None)):
    now = int(time.time())
    return hermes_rows.add(_own_inbox(board, now), board, lambda src, _src_board: hermes_rows.inbox(src, now))


class AnswerBody(BaseModel):
    text: str
    author: str = "owner"


@router.post("/inbox/{card_id}/answer")
def answer_inbox_item(card_id: str, payload: AnswerBody, board: Optional[str] = Query(None)):
    """Comment as the owner; unblock the card when it is blocked waiting for input. A card of the Hermes board is read
    only here (lina#481)."""
    if is_hermes(card_id):
        raise HTTPException(status_code=409, detail=READ_ONLY)
    text = payload.text.strip()
    if not text:
        raise HTTPException(status_code=400, detail="text is required")
    with closing(kbc.connect(board=_board_slug(board))) as conn:
        task = _require_card(conn, card_id)
        if payload.author == task.assignee:
            raise HTTPException(status_code=400, detail="the answer must come from someone other than the assignee")
        comment_id = kanban_db.add_comment(conn, card_id, author=payload.author, body=text,
                                          source={"surface": "app"})
        unblocked = (task.status == "blocked" and task.block_kind == "needs_input"
                     and kanban_db.unblock_task(conn, card_id, by=payload.author, reason=text))
        state, reason = work_state(_facts(conn, _require_card(conn, card_id)))
        return {"ok": True, "cardId": card_id, "commentId": comment_id, "unblocked": bool(unblocked),
                "state": state, "reason": reason}
