"""A card ends only when every run it started has a verdict (lina#295).

A run a card hands out with ``lina worker`` is written on the card (``kanban_link``): a
``worker_run`` event when it starts, a ``worker_run_ended`` event (and a comment) when it ends
or asks a question (``state: human``). Its verdict lives in the hub's job record, the place
``lina worker accept | reject | cancel`` writes it: ``verdict`` is ``accepted`` or ``rejected``,
or the run's ``state`` is ``cancelled``. Nothing else is a verdict; finishing the card is not one.

* :func:`gate_unjudged_runs` is the one refusal: ``complete_task``, ``request_review`` and the
  goal judge's "done" call it. An operator may close past it (``--allow-unjudged``); that is
  recorded on the card as ``unjudged_runs_allowed``.
* :func:`wake_for_run` lets a run's end or question be judged while the card waits: a card in
  ``todo`` behind an unfinished parent, or ``scheduled``, goes to ``ready`` with a
  ``run_verdict_wake`` event (``claim_task`` honours it past the open parent). After the verdict
  :func:`return_to_wait` puts it back where it waited (``todo``, or the same schedule); the
  worker's ``kanban_complete`` and every dispatcher tick (:func:`verdict_tick`) both call it.
* :func:`verdict_tick` also raises, once each, a run left without a verdict for an hour (on its
  card; on the starting agent's main session when the card is gone) and a pause with no reopen
  condition after a day (on the starting agent's main session). Neither goes to the owner.
"""
from __future__ import annotations

import json
import os
import re
import sqlite3
import time
from pathlib import Path
from typing import Any, Optional

ACTOR = "kanban-dispatcher"
UNJUDGED_RAISE_AFTER_S = 3600
OPEN_PAUSE_RAISE_AFTER_S = 86400
VERDICTS = ("accepted", "rejected")
# Events that end a verdict wake: its own return, or the card leaving ``ready``/``running`` some other way.
_WAKE_CLOSERS = ("verdict_wake_returned", "completed", "scheduled", "blocked", "review_requested", "archived")
_CARD_ID = re.compile(r"(?<![A-Za-z0-9_])t_[0-9a-f]{4,}(?![A-Za-z0-9_])")
_TIME = re.compile(r"\d{4}-\d{2}-\d{2}|(?<!\d)\d{1,2}:\d{2}(?!\d)")


class UnjudgedRunsError(ValueError):
    """The card started runs that have no verdict yet; nothing was changed."""

    def __init__(self, task_id: str, action: str, runs: list[tuple[str, str]]):
        self.task_id, self.action, self.runs = task_id, action, runs
        named = ", ".join(f"{run_id} ({state})" for run_id, state in runs)
        super().__init__(
            f"{action} of {task_id} refused: runs it started have no verdict: {named}. Judge each with "
            "`lina worker accept <id> --note ...`, `lina worker reject <id> --why ...` or "
            "`lina worker cancel <id> --why ...`, then try again")


def _kb():
    from hermes_cli import kanban_db
    return kanban_db


def _worker_root() -> Path:
    from hermes_cli.worker import settings
    return settings.root()


def _job(run_id: str, worker_root: Optional[Path] = None) -> Optional[dict]:
    """The hub's job record of *run_id*, read directly (the kanban side must not need the hub's locks)."""
    from hermes_cli.worker import settings
    path = settings.hub_dir(worker_root or _worker_root()) / "jobs" / f"{run_id}.json"
    try:
        job = json.loads(path.read_text(encoding="utf-8-sig"))
    except (OSError, ValueError):
        return None
    return job if isinstance(job, dict) else None


def _payload(raw: Any) -> dict:
    try:
        value = json.loads(raw) if raw else {}
    except ValueError:
        return {}
    return value if isinstance(value, dict) else {}


def started_runs(conn: sqlite3.Connection, task_id: str) -> list[str]:
    """The run ids the card's ``worker_run`` events name, first start first."""
    rows = conn.execute(
        "SELECT payload FROM task_events WHERE task_id = ? AND kind = 'worker_run' ORDER BY id", (task_id,),
    ).fetchall()
    runs = [str(_payload(row["payload"]).get("run_id") or "") for row in rows]
    return list(dict.fromkeys(run for run in runs if run))


def _card_ends(conn: sqlite3.Connection, task_id: str) -> dict[str, tuple[str, int]]:
    """``{run_id: (state, at)}`` of each run's newest ``worker_run_ended`` event on the card."""
    ends: dict[str, tuple[str, int]] = {}
    for row in conn.execute(
        "SELECT payload, created_at FROM task_events WHERE task_id = ? AND kind = 'worker_run_ended' ORDER BY id",
        (task_id,),
    ):
        payload = _payload(row["payload"])
        if payload.get("run_id"):
            ends[str(payload["run_id"])] = (str(payload.get("state") or "unknown"), int(row["created_at"] or 0))
    return ends


def run_verdicts(conn: sqlite3.Connection, task_id: str, *, worker_root: Optional[Path] = None) -> list[dict]:
    """Each run the card started: ``{run_id, state, verdict, judged, ended_at, agent}``. ``state`` is the
    hub's when it has the run, else the card's last end, else ``unknown``; a run the hub does not know
    has no verdict."""
    return _verdicts(started_runs(conn, task_id), _card_ends(conn, task_id), worker_root)


def verdicts_of_events(events) -> list[dict]:
    """:func:`run_verdicts` from a card's already-read ``kanban_db.Event`` list (oldest first)."""
    started: list[str] = []
    ends: dict[str, tuple[str, int]] = {}
    for event in events:
        payload = event.payload if isinstance(event.payload, dict) else {}
        run_id = str(payload.get("run_id") or "")
        if run_id and event.kind == "worker_run":
            started.append(run_id)
        elif run_id and event.kind == "worker_run_ended":
            ends[run_id] = (str(payload.get("state") or "unknown"), int(event.created_at or 0))
    return _verdicts(list(dict.fromkeys(started)), ends, None)


def _verdicts(started: list[str], ends: dict[str, tuple[str, int]], worker_root: Optional[Path]) -> list[dict]:
    out = []
    for run_id in started:
        job = _job(run_id, worker_root)
        end_state, ended_at = ends.get(run_id, (None, None))
        state = str((job or {}).get("state") or end_state or "unknown")
        verdict = (job or {}).get("verdict") if (job or {}).get("verdict") in VERDICTS else None
        judged = verdict is not None or state == "cancelled" or (job is None and end_state == "cancelled")
        out.append({"run_id": run_id, "state": state, "verdict": verdict, "judged": judged,
                    "ended_at": ended_at if end_state not in (None, "human") else None,
                    "agent": (job or {}).get("bot")})
    return out


def unjudged_runs(conn: sqlite3.Connection, task_id: str, *,
                  worker_root: Optional[Path] = None) -> list[tuple[str, str]]:
    """``(run_id, state)`` of every run the card started that has no verdict."""
    return [(r["run_id"], r["state"]) for r in run_verdicts(conn, task_id, worker_root=worker_root)
            if not r["judged"]]


def gate_unjudged_runs(conn: sqlite3.Connection, task_id: str, action: str, *, allow: bool = False) -> None:
    """Refuse *action* (``complete``, ``request_review``, ``goal done``) while a run the card started
    has no verdict: :class:`UnjudgedRunsError` names the runs and their state. ``allow`` is the
    operator's explicit override; it is recorded as an ``unjudged_runs_allowed`` event (inside the
    caller's transaction, so it lands only with the transition it allowed)."""
    runs = unjudged_runs(conn, task_id)
    if not runs:
        return
    if not allow:
        raise UnjudgedRunsError(task_id, action, runs)
    _kb()._append_event(conn, task_id, "unjudged_runs_allowed",
                        {"action": action, "runs": [{"run_id": r, "state": s} for r, s in runs]})


# --- Judging a run while the card waits -----------------------------------------------------

def _latest_scheduled(conn: sqlite3.Connection, task_id: str) -> tuple[Optional[int], dict, int]:
    row = conn.execute(
        "SELECT id, payload, created_at, run_id FROM task_events WHERE task_id = ? AND kind = 'scheduled' "
        "ORDER BY id DESC LIMIT 1", (task_id,),
    ).fetchone()
    if row is None:
        return None, {}, 0
    return int(row["id"]), _payload(row["payload"]), int(row["created_at"] or 0)


def _names_run(park: dict, run_id: str) -> bool:
    from hermes_cli.worker.kanban_link import RUN_ID
    return run_id in RUN_ID.findall(" ".join([str(park.get("reason") or ""), json.dumps(park.get("wait_for") or {})]))


def verdict_wake_open(conn: sqlite3.Connection, task_id: str) -> Optional[dict]:
    """The card's open verdict wake: ``{"runs": [...], "back": {...}}`` from the ``run_verdict_wake``
    events since the last event that closed one; None when there is none."""
    kinds = ("run_verdict_wake", *_WAKE_CLOSERS)
    rows = conn.execute(
        f"SELECT kind, payload FROM task_events WHERE task_id = ? AND kind IN ({','.join('?' * len(kinds))}) "
        "ORDER BY id DESC", (task_id, *kinds),
    ).fetchall()
    runs: list[str] = []
    back: Optional[dict] = None
    for row in rows:
        if row["kind"] != "run_verdict_wake":
            break
        payload = _payload(row["payload"])
        runs.insert(0, str(payload.get("run_id") or ""))
        back = payload.get("back") if isinstance(payload.get("back"), dict) else back
    if not runs:
        return None
    return {"runs": list(dict.fromkeys(r for r in runs if r)), "back": back}


def _wake_comment(task_id: str, run_id: str, state: str, back: dict, parents: list[tuple[str, str]]) -> str:
    waits = (f"parent card(s) {', '.join(pid for pid, _ in parents)}" if back["status"] == "todo"
             else "its schedule")
    return (f"[kanban · automatic] run {run_id} is {state} and needs this card's verdict. Woken while the card "
            f"waits on {waits}: judge it with `lina worker accept {run_id} --note ...`, `lina worker reject "
            f"{run_id} --why ...` or `lina worker cancel {run_id} --why ...`, then call kanban_complete: the "
            f"card goes back to waiting instead of finishing.")


def wake_for_run(conn: sqlite3.Connection, task_id: str, run_id: str, state: str) -> bool:
    """A run's end or question reached the card: let the card judge it even while it waits.

    ``todo`` behind an unfinished parent, or ``scheduled`` on something else, goes to ``ready`` with
    a ``run_verdict_wake`` event that records where it goes back after the verdict. A schedule that
    names this run is met by its end: an ordinary wake (``unblock_task``), back to ``todo`` only when
    a parent is still open. A card already woken for a verdict adds the run to that wake. Any other
    status is left alone (a running worker reads the comment). True when the card was woken."""
    kb = _kb()
    with kb.write_txn(conn):
        row = conn.execute("SELECT status FROM tasks WHERE id = ?", (task_id,)).fetchone()
        status = row["status"] if row else None
        parents = kb.unsatisfied_parents(conn, task_id)
        open_wake = verdict_wake_open(conn, task_id)
        if status in ("ready", "running") and open_wake:
            kb._append_event(conn, task_id, "run_verdict_wake",
                             {"run_id": run_id, "state": state, "back": open_wake["back"]})
            return False
        if status == "todo" and parents:
            back: dict = {"status": "todo"}
        elif status == "scheduled":
            park_id, park, _ = _latest_scheduled(conn, task_id)
            if _names_run(park, run_id) and not parents:
                return kb.unblock_task(conn, task_id, by=ACTOR, keep_failures=True,
                                       reason=f"wait met: run {run_id} {state}")
            back = {"status": "todo"} if _names_run(park, run_id) else \
                {"status": "scheduled", "park": park, "park_event_id": park_id}
        else:
            return False
        if conn.execute("UPDATE tasks SET status = 'ready', current_run_id = NULL WHERE id = ? AND status = ?",
                        (task_id, status)).rowcount != 1:
            return False
        kb._append_event(conn, task_id, "run_verdict_wake", {"run_id": run_id, "state": state, "back": back})
        kb.add_comment(conn, task_id, ACTOR, _wake_comment(task_id, run_id, state, back, parents))
        return True


def return_to_wait(conn: sqlite3.Connection, task_id: str, *, expected_run_id: Optional[int] = None) -> bool:
    """Put a card woken for a verdict back where it waited, once the runs that woke it are judged.

    From ``ready``, or from ``running`` when ``expected_run_id`` is the current run (the woken
    worker's own ``kanban_complete``; the run ends ``returned_to_wait``). Raises
    :class:`UnjudgedRunsError` while one of those runs has no verdict. False when the card has no
    open verdict wake, or is in neither state."""
    kb = _kb()
    with kb.write_txn(conn):
        wake = verdict_wake_open(conn, task_id)
        if not wake or not wake["back"]:
            return False
        pending = [(r, s) for r, s in unjudged_runs(conn, task_id) if r in wake["runs"]]
        if pending:
            raise UnjudgedRunsError(task_id, "going back to waiting", pending)
        row = conn.execute("SELECT status, current_run_id FROM tasks WHERE id = ?", (task_id,)).fetchone()
        if row is None or row["status"] not in ("ready", "running"):
            return False
        if row["status"] == "running":
            if expected_run_id is None or row["current_run_id"] != int(expected_run_id):
                return False
            kb._end_run(conn, task_id, outcome="returned_to_wait",
                        summary=f"judged {', '.join(wake['runs'])}; back to waiting")
        back = wake["back"]
        conn.execute(
            "UPDATE tasks SET status = ?, current_run_id = NULL, claim_lock = NULL, claim_expires = NULL, "
            "worker_pid = NULL WHERE id = ? AND status = ?", (back["status"], task_id, row["status"]),
        )
        kb._append_event(conn, task_id, "verdict_wake_returned", {"status": back["status"], "runs": wake["runs"]})
        if back["status"] == "scheduled":
            kb._append_event(conn, task_id, "scheduled", back.get("park") or {})
        return True


# --- Raising what nobody judged -------------------------------------------------------------

def _notice_command(worker_root: Path) -> Optional[list]:
    from hermes_cli.worker import settings
    notice = settings.section(worker_root.parent).get("notice")
    return notice.get("command") if isinstance(notice, dict) else None


def _to_main_session(worker_root: Path, agent: str, item_id: str, tag: str, text: str) -> str:
    """The starting agent's main session: the hub's notice path (``worker.notice.command`` with
    ``{agent}``, always kept in ``hub/notices``), the way a run's end reaches an agent outside a card."""
    from hermes_cli.worker import notice, settings
    return notice.deliver({"id": item_id, "bot": agent}, text, tag, settings.hub_dir(worker_root) / "notices",
                          _notice_command(worker_root))


def _once(worker_root: Path, key: str) -> bool:
    """True exactly once per *key* across processes (a marker file created with O_EXCL)."""
    from hermes_cli.worker import settings
    marks = settings.hub_dir(worker_root) / "raised"
    marks.mkdir(parents=True, exist_ok=True)
    try:
        os.close(os.open(marks / key, os.O_CREAT | os.O_EXCL | os.O_WRONLY))
    except FileExistsError:
        return False
    return True


def _unjudged_text(run_id: str, state: str, hours: int, where: str) -> str:
    return (f"[kanban · automatic] run {run_id} ended {hours}h ago ({state}) and has no verdict{where}. Judge it: "
            f"`lina worker accept {run_id} --note ...`, `lina worker reject {run_id} --why ...` or "
            f"`lina worker cancel {run_id} --why ...`.")


def _raised_runs(conn: sqlite3.Connection, task_id: str) -> set[str]:
    rows = conn.execute("SELECT payload FROM task_events WHERE task_id = ? AND kind = 'unjudged_run_raised'",
                        (task_id,)).fetchall()
    return {str(_payload(row["payload"]).get("run_id")) for row in rows}


def _raise_on_card(conn: sqlite3.Connection, task_id: str, run: dict, now: int) -> bool:
    kb = _kb()
    with kb.write_txn(conn):
        if run["run_id"] in _raised_runs(conn, task_id):
            return False
        hours = (now - int(run["ended_at"])) // 3600
        kb.add_comment(conn, task_id, ACTOR,
                       _unjudged_text(run["run_id"], run["state"], hours, "; this card cannot finish until then"))
        kb._append_event(conn, task_id, "unjudged_run_raised",
                         {"run_id": run["run_id"], "state": run["state"], "delivery": "card"})
        return True


def raise_unjudged_runs(conn: sqlite3.Connection, *, now: int, worker_root: Path) -> list[str]:
    """Raise each run that ended over an hour ago without a verdict, once: as a comment on its open
    card, or to the starting agent's main session when its card is done or archived (no session
    owns it any more). Returns the raised run ids."""
    raised: list[str] = []
    cards = conn.execute(
        "SELECT DISTINCT e.task_id, t.status FROM task_events e JOIN tasks t ON t.id = e.task_id "
        "WHERE e.kind = 'worker_run_ended'").fetchall()
    for card in cards:
        stale = [r for r in run_verdicts(conn, card["task_id"], worker_root=worker_root)
                 if not r["judged"] and r["ended_at"] and now - r["ended_at"] > UNJUDGED_RAISE_AFTER_S]
        for run in stale:
            if card["status"] not in ("done", "archived"):
                if _raise_on_card(conn, card["task_id"], run, now):
                    raised.append(run["run_id"])
            elif run["agent"] and _once(worker_root, f"unjudged-{run['run_id']}"):
                _to_main_session(worker_root, run["agent"], run["run_id"], "unjudged", _unjudged_text(
                    run["run_id"], run["state"], (now - run["ended_at"]) // 3600, " and its card is closed"))
                raised.append(run["run_id"])
    return raised


def _board_file(conn: sqlite3.Connection) -> Optional[Path]:
    row = conn.execute("PRAGMA database_list").fetchone()
    return Path(row[2]).resolve() if row and row[2] else None


def raise_runs_of_gone_cards(conn: sqlite3.Connection, *, now: int, worker_root: Path) -> list[str]:
    """Runs whose card was deleted from this board: an hour after the hub last wrote the run (its
    end) without a verdict, once to the starting agent's main session."""
    from hermes_cli.worker import settings
    jobs_dir = settings.hub_dir(worker_root) / "jobs"
    here = _board_file(conn)
    if here is None or not jobs_dir.is_dir():
        return []
    raised: list[str] = []
    for path in jobs_dir.glob("*.json"):
        job = _job(path.stem, worker_root)
        card = (job or {}).get("card")
        if not isinstance(card, dict) or card.get("kind") or job.get("verdict") in VERDICTS \
                or job.get("state") in ("cancelled", "running", "queued", "new", None) \
                or now - path.stat().st_mtime <= UNJUDGED_RAISE_AFTER_S:
            continue
        if _kb().kanban_db_path(card.get("board")).resolve() != here or \
                conn.execute("SELECT 1 FROM tasks WHERE id = ?", (card.get("task"),)).fetchone() is not None:
            continue
        if _once(worker_root, f"unjudged-{job['id']}"):
            _to_main_session(worker_root, job["bot"], job["id"], "unjudged", _unjudged_text(
                job["id"], job["state"], int(now - path.stat().st_mtime) // 3600, " and its card is gone"))
            raised.append(job["id"])
    return raised


def has_reopen_condition(park: dict) -> bool:
    """A pause names what reopens it: a recorded ``wait_for``, or a run id, card id or time in its reason."""
    from hermes_cli.worker.kanban_link import RUN_ID
    if park.get("wait_for"):
        return True
    reason = str(park.get("reason") or "")
    return bool(RUN_ID.search(reason) or _CARD_ID.search(reason) or _TIME.search(reason))


def raise_open_pauses(conn: sqlite3.Connection, *, now: int, worker_root: Path) -> list[str]:
    """Raise each card paused (``scheduled``) for over a day with no reopen condition, once per pause,
    to the main session of the agent whose run paused it (else the assignee)."""
    kb = _kb()
    raised: list[str] = []
    for card in conn.execute("SELECT id, title, assignee FROM tasks WHERE status = 'scheduled'").fetchall():
        park_id, park, parked_at = _latest_scheduled(conn, card["id"])
        if park_id is None or now - parked_at <= OPEN_PAUSE_RAISE_AFTER_S or has_reopen_condition(park):
            continue
        row = conn.execute(
            "SELECT r.profile FROM task_events e JOIN task_runs r ON r.id = e.run_id WHERE e.id = ?", (park_id,),
        ).fetchone()
        agent = (row["profile"] if row else None) or card["assignee"]
        with kb.write_txn(conn):
            done = conn.execute(
                "SELECT 1 FROM task_events WHERE task_id = ? AND kind = 'open_pause_raised' "
                "AND json_extract(payload, '$.park_event_id') = ?", (card["id"], park_id),
            ).fetchone()
            if done or not agent:
                continue
            kb._append_event(conn, card["id"], "open_pause_raised", {"park_event_id": park_id, "agent": agent})
        _to_main_session(worker_root, agent, card["id"], "pause", (
            f"[kanban · automatic] card {card['id']} ({card['title']}) has been paused for "
            f"{(now - parked_at) // 3600}h with nothing that reopens it (reason: {park.get('reason') or '-'}). "
            "Give it a reopen condition (a run id, a card id or a time), unblock it, or close it."))
        raised.append(card["id"])
    return raised


def return_judged_cards(conn: sqlite3.Connection) -> list[str]:
    """Cards woken for a verdict, back in ``ready`` and now judged: back to waiting."""
    returned = []
    for row in conn.execute("SELECT id FROM tasks WHERE status = 'ready'").fetchall():
        try:
            if verdict_wake_open(conn, row["id"]) and return_to_wait(conn, row["id"]):
                returned.append(row["id"])
        except UnjudgedRunsError:
            continue  # still waiting for its verdict: the woken worker judges it
    return returned


def verdict_tick(conn: sqlite3.Connection, *, now: Optional[int] = None,
                 worker_root: Optional[Path] = None) -> dict[str, list[str]]:
    """The dispatcher's pass over verdicts: return judged cards to waiting, then raise what waited too
    long. Each part fails on its own (logged), never the tick."""
    now = int(time.time()) if now is None else int(now)
    root = worker_root or _worker_root()
    out: dict[str, list[str]] = {}
    parts = (("returned", lambda: return_judged_cards(conn)),
             ("unjudged", lambda: raise_unjudged_runs(conn, now=now, worker_root=root)),
             ("gone", lambda: raise_runs_of_gone_cards(conn, now=now, worker_root=root)),
             ("paused", lambda: raise_open_pauses(conn, now=now, worker_root=root)))
    for name, part in parts:
        try:
            out[name] = part()
        except Exception:  # health: allow BLE001 -- one unreadable card or hub record must not stall the dispatcher tick; logged with traceback, retried next tick
            _kb()._log.warning("kanban: verdict pass %s failed", name, exc_info=True)
            out[name] = []
    return out


__all__ = [
    "OPEN_PAUSE_RAISE_AFTER_S", "UNJUDGED_RAISE_AFTER_S", "UnjudgedRunsError", "gate_unjudged_runs",
    "has_reopen_condition", "raise_open_pauses", "raise_runs_of_gone_cards", "raise_unjudged_runs",
    "return_judged_cards", "return_to_wait", "run_verdicts", "started_runs", "unjudged_runs", "verdict_tick",
    "verdict_wake_open", "verdicts_of_events", "wake_for_run",
]
