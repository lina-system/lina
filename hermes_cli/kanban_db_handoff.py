"""Planned stops: a kanban run that ends because a limit ran out, not because it failed.

Three limits end a run as planned (lina#161): the goal loop's turn budget, the dispatcher's
``max_runtime_seconds`` and the agent's iteration budget. Each re-queues the card to its source
phase (ready, or review for a reviewer run), leaves a handoff comment, and never touches
``consecutive_failures``: only a crash or a real error counts toward ``max_retries``.

A card that keeps stopping as planned without ever finishing would loop forever, the risk the
upstream failure accounting guarded against (#29747). That guard is kept separately: once the
latest ``kanban.planned_stop_limit`` (default 5) runs in a row are all planned stops, the card is
blocked ``needs_input`` with no ``wait_for``, for a person to look at.

The streak is read from the run history, no schema of its own. A run is a planned stop when its
outcome is ``timed_out`` (runtime or iteration limit), or ``scheduled`` with a ``scheduled``
event whose ``wait_for`` carries ``handoff`` (the turn-budget re-queue). Any other run end
(completed, crashed, blocked, a WAIT park) breaks the streak.
"""
from __future__ import annotations

import json
import sqlite3
import time
from typing import Optional

DEFAULT_PLANNED_STOP_LIMIT = 5
HANDOFF_KEY = "handoff"


def planned_stop_limit() -> int:
    """``kanban.planned_stop_limit`` from config.yaml; an unusable value falls back to the default."""
    from hermes_cli import kanban_db as kb
    from hermes_cli.config import load_config

    raw = ((load_config() or {}).get("kanban") or {}).get("planned_stop_limit", DEFAULT_PLANNED_STOP_LIMIT)
    try:
        limit = int(raw)
    except (TypeError, ValueError):
        limit = 0
    if limit < 1:
        # The value itself is not logged: a config value read here is untrusted text.
        kb._log.warning("kanban: invalid kanban.planned_stop_limit (%s); using %d", type(raw).__name__,
                        DEFAULT_PLANNED_STOP_LIMIT)
        return DEFAULT_PLANNED_STOP_LIMIT
    return limit


def _run_is_planned_stop(conn: sqlite3.Connection, task_id: str, run_id: int, outcome: Optional[str]) -> bool:
    if outcome == "timed_out":
        return True
    if outcome != "scheduled":
        return False
    row = conn.execute(
        "SELECT payload FROM task_events WHERE task_id = ? AND run_id = ? AND kind = 'scheduled' "
        "ORDER BY id DESC LIMIT 1", (task_id, run_id),
    ).fetchone()
    payload = json.loads(row["payload"]) if row is not None and row["payload"] else {}
    return bool((payload.get("wait_for") or {}).get(HANDOFF_KEY))


def _run_is_park(conn: sqlite3.Connection, task_id: str, run_id: int, outcome: Optional[str]) -> bool:
    """A run that ended parked on a recorded wait (``scheduled``/``blocked`` with ``wait_for``)."""
    if outcome not in ("scheduled", "blocked"):
        return False
    row = conn.execute(
        "SELECT payload FROM task_events WHERE task_id = ? AND run_id = ? AND kind = ? ORDER BY id DESC LIMIT 1",
        (task_id, run_id, outcome),
    ).fetchone()
    payload = json.loads(row["payload"]) if row is not None and row["payload"] else {}
    return isinstance(payload.get("wait_for"), dict)


def latest_attempt_run(conn: sqlite3.Connection, task_id: str) -> Optional[sqlite3.Row]:
    """The latest ended run that was a real attempt, for the respawn guards (``outcome``,
    ``ended_at``, ``metadata``), or None.

    A park on a recorded wait (WAIT verdict, ``--wait-for``, the turn-budget re-queue) and a
    planned stop (``timed_out``: runtime or iteration limit) are no attempts: they say nothing
    about whether the next spawn can succeed. Skipping them keeps every guard reading the run
    that did. For example, a crash whose output merely mentions ``auth`` stays a crash after the
    card was parked and woken, so ``last_failure_error`` (kept by the wake) is not read as an
    auth blocker.
    """
    rows = conn.execute(
        "SELECT id, outcome, ended_at, metadata FROM task_runs WHERE task_id = ? AND ended_at IS NOT NULL "
        "ORDER BY ended_at DESC, id DESC", (task_id,),
    )
    for row in rows:
        if _not_an_attempt(conn, task_id, row):
            continue
        return row
    return None


def _not_an_attempt(conn: sqlite3.Connection, task_id: str, run: sqlite3.Row) -> bool:
    """A park on a recorded wait or a planned stop: the one rule ``latest_attempt_run`` and
    ``worker_started_since`` share."""
    return run["outcome"] == "timed_out" or _run_is_park(conn, task_id, int(run["id"]), run["outcome"])


def worker_started_since(conn: sqlite3.Connection, task_id: str, event_id: int) -> bool:
    """Whether a run claimed after ``event_id`` was a real attempt whose worker started.

    A worker started when the run has a launch event: ``spawned`` (the dispatcher launched it,
    ``_set_worker_pid``), ``worker_registered`` (the worker filled in its own pid,
    ``adopt_worker_pid``) or ``worker_bound`` (a gateway-owned worker bound the run). Those
    events are never rewritten and are pruned only for done/archived cards, whereas
    ``task_runs.worker_pid`` is cleared by the terminal-worker reaper once the process is gone.
    A claim the host refused, a spawn or workspace failure, or a reclaim before the worker came
    up has no launch event, so it is no attempt. Neither is a park or a planned stop
    (``_not_an_attempt``).
    """
    rows = conn.execute(
        "SELECT r.id, r.outcome FROM task_events e JOIN task_runs r ON r.id = e.run_id "
        "WHERE e.task_id = ? AND e.kind = 'claimed' AND e.id > ? AND EXISTS (SELECT 1 FROM task_events s "
        "WHERE s.task_id = e.task_id AND s.run_id = r.id AND s.kind IN "
        "('spawned', 'worker_registered', 'worker_bound'))", (task_id, event_id),
    ).fetchall()
    return any(not _not_an_attempt(conn, task_id, r) for r in rows)


def consecutive_planned_stops(conn: sqlite3.Connection, task_id: str) -> int:
    """How many of the card's latest ended runs in a row were planned stops."""
    rows = conn.execute(
        "SELECT id, outcome FROM task_runs WHERE task_id = ? AND ended_at IS NOT NULL ORDER BY id DESC",
        (task_id,),
    ).fetchall()
    streak = 0
    for row in rows:
        if not _run_is_planned_stop(conn, task_id, int(row["id"]), row["outcome"]):
            break
        streak += 1
    return streak


def _streak_reason(streak: int) -> str:
    return (f"{streak} planned stops in a row (turn budget, runtime or iteration limit) without the "
            "card finishing; a person should look at it before it runs again.")


def hand_off_or_hold(conn: sqlite3.Connection, task_id: str, *, run_id: Optional[int], why: str,
                     author: str) -> bool:
    """After a planned stop ended its run and re-queued the card: write the handoff comment, then
    block the card ``needs_input`` when this stop completes a streak of ``planned_stop_limit``.
    True = the card was held for a person."""
    from hermes_cli import kanban_db as kb

    row = conn.execute("SELECT status FROM tasks WHERE id = ?", (task_id,)).fetchone()
    back_to = row["status"] if row is not None else "ready"
    kb.add_comment(conn, task_id, author, (
        f"Handoff: {why}. The card is back in {back_to}; the next run continues from this card's "
        "comments and prior attempts."), run_id=run_id)
    streak = consecutive_planned_stops(conn, task_id)
    if streak < planned_stop_limit():
        return False
    if back_to == "review":
        return _hold_review_card(conn, task_id, _streak_reason(streak))
    return kb.block_task(conn, task_id, reason=_streak_reason(streak), kind="needs_input")


def _hold_review_card(conn: sqlite3.Connection, task_id: str, reason: str) -> bool:
    """``review`` -> ``blocked`` (``needs_input``) for a reviewer card at the streak.

    ``block_task`` only moves running/ready cards, so a reviewer run that stopped as planned would
    loop forever in review. The ``blocked`` event records ``resume_status: review``, which
    ``unblock_task`` reads (``_resume_status_from_events``), so a person's unblock returns the card
    to review, not to an implementation run. Like ``block_task`` it closes the hold with a
    ``blocked`` run of its own, so the streak starts over after the unblock.
    """
    from hermes_cli import kanban_db as kb

    with kb.write_txn(conn):
        held = conn.execute(
            "UPDATE tasks SET status = 'blocked', block_kind = 'needs_input', block_recurrences = "
            "CASE WHEN block_kind = 'needs_input' THEN block_recurrences + 1 ELSE 1 END "
            "WHERE id = ? AND status = 'review' AND claim_lock IS NULL", (task_id,),
        ).rowcount
        if held != 1:
            return False
        run_id = kb._end_or_synthesize_run(conn, task_id, outcome="blocked", status="blocked",
                                          summary=reason, synthesize=True)
        kb._append_event(conn, task_id, "blocked", {
            "reason": reason, "kind": "needs_input", "source_status": "review", "resume_status": "review",
        }, run_id=run_id)
        task = kb.get_task(conn, task_id)
    kb._fire_task_hook("kanban_task_blocked", task, task_id, run_id, reason=reason)
    kb.record_terminal_outcome(conn, task_id)
    return True


def park_turn_budget_end(conn: sqlite3.Connection, task_id: str, *, run_id: Optional[int], reason: str,
                         handoff: str, author: str) -> bool:
    """The goal loop's turn budget ran out: re-queue the card through ``schedule_task`` with an
    immediate time wait marked ``handoff`` (the next dispatcher tick returns it to its phase), or,
    when this stop completes the streak, block it ``needs_input`` instead. The run's ``handoff``
    text is written as a comment either way. False = this run no longer owns the card."""
    from hermes_cli import kanban_db as kb

    streak = consecutive_planned_stops(conn, task_id) + 1
    if streak >= planned_stop_limit():
        moved = kb.block_task(conn, task_id, reason=_streak_reason(streak), kind="needs_input",
                              expected_run_id=run_id)
    else:
        wait_for = {"kind": "until", "at": int(time.time()), HANDOFF_KEY: "turn_budget"}
        moved = kb.schedule_task(conn, task_id, reason=reason, wait_for=wait_for, expected_run_id=run_id)
    if moved:
        kb.add_comment(conn, task_id, author, handoff, run_id=run_id)
    return moved


def end_iteration_budget_run(conn: sqlite3.Connection, task_id: str, *, used: int, maximum: int) -> bool:
    """The agent's iteration budget ran out on a dispatched worker: end its run ``timed_out`` (the
    outcome a runtime-limit end uses), re-queue the card to its source phase without a failure,
    and hand off. Idempotent: a card no longer ``running`` (another exit path closed the run) is
    left alone. False = nothing to end."""
    from hermes_cli import kanban_db as kb

    with kb.write_txn(conn):
        row = conn.execute("SELECT status, assignee FROM tasks WHERE id = ?", (task_id,)).fetchone()
        if row is None or row["status"] != "running":
            return False
        retry_status = kb._retry_status_for_run(conn, task_id)
        conn.execute(
            "UPDATE tasks SET status = ?, claim_lock = NULL, claim_expires = NULL, worker_pid = NULL, "
            "worker_started_at = NULL, last_heartbeat_at = NULL WHERE id = ? AND status = 'running'",
            (retry_status, task_id),
        )
        payload = {"budget_used": used, "budget_max": maximum, "retry_status": retry_status,
                   "planned_stop": "iteration_limit"}
        run_id = kb._end_run(conn, task_id, outcome="timed_out", status="timed_out",
                             error=f"iteration budget used ({used}/{maximum})", metadata=payload)
        kb._append_event(conn, task_id, "timed_out", payload, run_id=run_id)
    hand_off_or_hold(conn, task_id, run_id=run_id, author=row["assignee"] or "kanban-worker",
                     why=f"the run used its iteration budget ({used}/{maximum}) and stopped as planned")
    return True


__all__ = [
    "DEFAULT_PLANNED_STOP_LIMIT", "HANDOFF_KEY", "consecutive_planned_stops", "end_iteration_budget_run",
    "hand_off_or_hold", "latest_attempt_run", "park_turn_budget_end", "planned_stop_limit",
    "worker_started_since",
]
