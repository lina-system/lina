"""Event waits for parked cards: what a card waits on, and the dispatcher's wake.

A card that waits on something outside itself is parked with the board's
existing transitions, never a new status: ``schedule_task`` (a goal-mode WAIT
verdict, ``kanban schedule --wait-for``) or ``block_task(kind="needs_input")``
(``kanban block --wait-for``). The awaited event rides that transition's event
payload as ``wait_for``:

* ``{"kind": "card", "task_id": ...}``     -- another card reaches ``done``
* ``{"kind": "review", "task_id": ...}``   -- a review card it asked for has its result (``done``)
* ``{"kind": "pr", "repo": ..., "number": ...}`` -- the PR's required checks have all finished
* ``{"kind": "until", "at": <epoch>}``     -- a recorded time passes

Each dispatcher tick calls :func:`wake_waiting_tasks`, which checks those
events without a model turn and wakes a met card with ``unblock_task``. The
unblock's status-guarded update is the exactly-once point: the woken card is
no longer parked, so later ticks never wake it again. A parked card with no
``wait_for`` (a manual schedule, a block on a person's answer) is left alone:
whoever parked it unblocks it.
"""
from __future__ import annotations

import json
import re
import sqlite3
import time
from datetime import UTC, datetime
from typing import Any, Callable, Optional

WAIT_KINDS = ("card", "review", "pr", "until")
DISPATCHER_ACTOR = "kanban-dispatcher"

_PR_URL = re.compile(r"https://github\.com/([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)/pull/([1-9][0-9]*)")
_PR_SHORT = re.compile(r"([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)#([1-9][0-9]*)")
# The last transition into or out of a parked status; ``gave_up`` is the breaker's block.
_PARK_EVENT_KINDS = ("scheduled", "blocked", "unblocked", "gave_up")
_PARKED_EVENT_FOR_STATUS = {"scheduled": "scheduled", "blocked": "blocked"}
# Required-check classifications (kanban_pr_acceptance) that mean "not finished yet".
_UNFINISHED_CHECKS = {"pending", "missing", "stale"}


def _parse_until(value: str) -> int:
    if value.isdigit():
        return int(value)
    stamp = datetime.fromisoformat(value)
    if stamp.tzinfo is None:
        stamp = stamp.replace(tzinfo=UTC)
    return int(stamp.timestamp())


def parse_wait_for(spec: str) -> dict[str, Any]:
    """``card:<id>`` / ``review:<id>`` / ``pr:<url|owner/repo#n>`` / ``until:<epoch|ISO-8601>``."""
    kind, sep, value = (spec or "").partition(":")
    kind, value = kind.strip().lower(), value.strip()
    if not sep or kind not in WAIT_KINDS or not value:
        raise ValueError("wait-for must be card:<id>, review:<id>, pr:<url|owner/repo#n> or until:<time>")
    if kind in ("card", "review"):
        return {"kind": kind, "task_id": value}
    if kind == "pr":
        match = _PR_URL.fullmatch(value) or _PR_SHORT.fullmatch(value)
        if not match:
            raise ValueError("pr wait must be a GitHub PR URL or owner/repo#number")
        return {"kind": "pr", "repo": match[1], "number": int(match[2])}
    try:
        return {"kind": "until", "at": _parse_until(value)}
    except ValueError:
        raise ValueError("until wait must be epoch seconds or an ISO-8601 time") from None


def wait_for_from_directive(directive: Optional[dict[str, Any]], *, now: int,
                            default_seconds: int) -> dict[str, Any]:
    """The card wait for a judge WAIT directive. A process or session wait cannot outlive the
    worker that owns the process, so it (and a bare WAIT) becomes a time wait of ``default_seconds``."""
    directive = directive or {}
    for kind in ("card", "review"):
        if directive.get(kind):
            return {"kind": kind, "task_id": str(directive[kind])}
    if directive.get("pr"):
        try:
            return parse_wait_for(f"pr:{directive['pr']}")
        except ValueError:
            pass
    if directive.get("until"):
        try:
            return {"kind": "until", "at": _parse_until(str(directive["until"]))}
        except ValueError:
            pass
    seconds = int(directive.get("seconds") or 0)
    return {"kind": "until", "at": now + (seconds if seconds > 0 else default_seconds)}


def describe_wait(wait_for: dict[str, Any]) -> str:
    kind = wait_for.get("kind")
    if kind in ("card", "review"):
        return f"{kind} {wait_for.get('task_id')} done"
    if kind == "pr":
        return f"required checks of {wait_for.get('repo')}#{wait_for.get('number')} finished"
    if kind == "until":
        at = datetime.fromtimestamp(int(wait_for.get("at") or 0), tz=UTC)
        return f"time {at.strftime('%Y-%m-%dT%H:%M:%SZ')} passed"
    return f"unknown wait {wait_for!r}"


def needs_input_wait(kind: Optional[str], wait_for: Optional[dict[str, Any]]) -> dict[str, Any]:
    """The ``wait_for`` part of a block event: only a ``needs_input`` block is woken by an event."""
    return {"wait_for": wait_for} if wait_for and kind == "needs_input" else {}


def park_payload(conn: sqlite3.Connection, task_id: str, reason: Optional[str],
                 wait_for: Optional[dict[str, Any]]) -> dict[str, Any]:
    """The ``scheduled`` event payload. A wait parked from a reviewer run (claimed from
    ``review``) records ``resume_status: review`` so the wake returns it to review, not to an
    implementation run. A schedule without ``wait_for`` records only its reason, as before."""
    from hermes_cli import kanban_db as kb

    payload: dict[str, Any] = {"reason": reason}
    if not wait_for:
        return payload
    payload["wait_for"] = wait_for
    row = conn.execute("SELECT status FROM tasks WHERE id = ?", (task_id,)).fetchone()
    if row is not None and row["status"] == "running" and kb._retry_status_for_run(conn, task_id) == "review":
        payload["resume_status"] = "review"
    return payload


def scheduled_resume_status(conn: sqlite3.Connection, task_id: str) -> str:
    """Where ``unblock_task`` returns a ``scheduled`` card: ``review`` when the event that parked
    it recorded a review phase (:func:`park_payload`), else ``ready``."""
    row = conn.execute(
        "SELECT payload FROM task_events WHERE task_id = ? AND kind = 'scheduled' "
        "ORDER BY created_at DESC, id DESC LIMIT 1", (task_id,),
    ).fetchone()
    payload = json.loads(row["payload"]) if row is not None and row["payload"] else {}
    return "review" if payload.get("resume_status") == "review" else "ready"


def _latest_park_event(conn: sqlite3.Connection, task_id: str) -> Optional[sqlite3.Row]:
    placeholders = ",".join("?" for _ in _PARK_EVENT_KINDS)
    return conn.execute(
        f"SELECT id, kind, payload FROM task_events WHERE task_id = ? AND kind IN ({placeholders}) "
        "ORDER BY created_at DESC, id DESC LIMIT 1", (task_id, *_PARK_EVENT_KINDS),
    ).fetchone()


def _parked_wait(conn: sqlite3.Connection, task_id: str, status: str) -> Optional[tuple[int, dict[str, Any]]]:
    """``(event id, wait_for)`` of the transition that parked the card in ``status``, or None."""
    row = _latest_park_event(conn, task_id)
    if row is None or row["kind"] != _PARKED_EVENT_FOR_STATUS.get(status) or not row["payload"]:
        return None
    wait_for = json.loads(row["payload"]).get("wait_for")
    if not (isinstance(wait_for, dict) and wait_for.get("kind") in WAIT_KINDS):
        return None
    return int(row["id"]), wait_for


def recorded_wait(conn: sqlite3.Connection, task_id: str, status: str) -> Optional[dict[str, Any]]:
    """The ``wait_for`` of the transition that parked the card in ``status``, or None."""
    parked = _parked_wait(conn, task_id, status)
    return parked[1] if parked else None


def _still_parked_by(conn: sqlite3.Connection, task_id: str, park_event_id: int) -> bool:
    """The card is still parked by the very event its wait was read from, in a wakeable state."""
    row = conn.execute("SELECT status, block_kind FROM tasks WHERE id = ?", (task_id,)).fetchone()
    if row is None or not (row["status"] == "scheduled"
                           or (row["status"] == "blocked" and row["block_kind"] == "needs_input")):
        return False
    parked = _parked_wait(conn, task_id, row["status"])
    return parked is not None and parked[0] == park_event_id


def dispatcher_wake(conn: sqlite3.Connection, task_id: str, park_event_id: int, wait_for: dict[str, Any]) -> bool:
    """Wake one parked card for its met event: the dispatcher's own unblock.

    Under one write transaction it first re-checks that the card is still parked by
    ``park_event_id`` (a person may have unblocked and re-parked it while a PR check was being
    read), then unblocks it keeping ``consecutive_failures``: waiting is not a recovery, so only
    a deliberate unblock by a person resets the retry budget.
    """
    from hermes_cli import kanban_db as kb

    with kb.write_txn(conn):
        if not _still_parked_by(conn, task_id, park_event_id):
            return False
        return kb.unblock_task(conn, task_id, by=DISPATCHER_ACTOR, keep_failures=True,
                               reason=f"wait met: {describe_wait(wait_for)}")


def pr_checks_finished(wait_for: dict[str, Any], assignee: Optional[str]) -> bool:
    """True once every required check on the PR's head has a conclusion (pass or fail)."""
    from hermes_cli.kanban_pr_acceptance import collect_acceptance

    url = f"https://github.com/{wait_for['repo']}/pull/{int(wait_for['number'])}"
    receipt = collect_acceptance(url, None, assignee)
    if "required" not in receipt or receipt.get("classification") in ("auth", "stale"):
        return False   # the evidence could not be read: keep waiting, re-checked next tick
    if not receipt["required"]:
        return True    # nothing required: there is nothing left to wait for
    checks = receipt.get("checks") or []
    return bool(checks) and all(c.get("classification") not in _UNFINISHED_CHECKS for c in checks)


def _card_done(conn: sqlite3.Connection, task_id: str) -> bool:
    row = conn.execute("SELECT status FROM tasks WHERE id = ?", (task_id,)).fetchone()
    return row is not None and row["status"] == "done"


def wait_met(conn: sqlite3.Connection, wait_for: dict[str, Any], *, assignee: Optional[str],
             now: int, pr_checks_fn: Callable[[dict, Optional[str]], bool]) -> bool:
    kind = wait_for["kind"]
    if kind in ("card", "review"):
        return _card_done(conn, str(wait_for.get("task_id")))
    if kind == "pr":
        return pr_checks_fn(wait_for, assignee)
    return now >= int(wait_for.get("at") or 0)


def wake_waiting_tasks(conn: sqlite3.Connection, *, now: Optional[int] = None,
                       pr_checks_fn: Optional[Callable[[dict, Optional[str]], bool]] = None) -> list[str]:
    """Unblock every parked card whose recorded event happened; returns the woken ids.

    Parked = ``scheduled``, or ``blocked`` with ``block_kind = 'needs_input'``. Network reads
    (PR checks) run outside any write transaction; :func:`dispatcher_wake` re-checks the parking
    event under its own, so a card unblocked or re-parked meanwhile is not woken by the old wait.
    """
    from hermes_cli import kanban_db as kb

    now = int(time.time()) if now is None else int(now)
    pr_checks_fn = pr_checks_fn or pr_checks_finished
    rows = conn.execute(
        "SELECT id, status, assignee FROM tasks WHERE status = 'scheduled' "
        "OR (status = 'blocked' AND block_kind = 'needs_input') ORDER BY created_at ASC"
    ).fetchall()
    woken: list[str] = []
    for row in rows:
        parked = _parked_wait(conn, row["id"], row["status"])
        if parked is None:
            continue
        park_event_id, wait_for = parked
        try:
            met = wait_met(conn, wait_for, assignee=row["assignee"], now=now, pr_checks_fn=pr_checks_fn)
        except Exception:  # health: allow BLE001 -- one unreadable wait must not stall the tick for every other card; logged with traceback, retried next tick
            kb._log.warning("kanban: wait check failed for %s (%s)", row["id"], wait_for, exc_info=True)
            continue
        if met and dispatcher_wake(conn, row["id"], park_event_id, wait_for):
            woken.append(row["id"])
    return woken


__all__ = [
    "DISPATCHER_ACTOR", "WAIT_KINDS", "describe_wait", "dispatcher_wake", "needs_input_wait", "park_payload",
    "parse_wait_for", "pr_checks_finished", "recorded_wait", "scheduled_resume_status", "wait_for_from_directive", "wait_met", "wake_waiting_tasks",
]
