"""Parking needs a waker (lina#604): what a ``scheduled`` park may wait on, a park whose waker is gone,
and the one-time read of parks whose time lives only in their reason text.

A park (``kanban_schedule``, ``hermes kanban schedule``) is accepted only when something can wake it.
:func:`resolve_park` is the one validator both use. It returns the ``wait_for`` to record on the
``scheduled`` event, or None when the reason itself names the waker:

* ``wait_for`` ``card:<id>`` / ``review:<id>`` / ``pr:<PR>`` / ``merged:<PR>`` / ``until:<time>``:
  the dispatcher's tick wakes it (``kanban_db_wait``).
* ``wait_for`` ``run:<run id>[,<run id>...]``: the worker hub wakes it after the last named run ends
  (``worker.kanban_link``); so does a run id anywhere in the reason (recorded as before, in the reason).
* ``wait_for`` ``unit:<name>``: the named unit the caller declares unblocks it; nothing else does.
* ``SCHEDULED_UNTIL=<time>`` in the reason: recorded as an ``until`` wait, so the tick wakes it.
* the goal drive's ``WAIT_EVENT=<event> GOAL_ACTION=<action>`` reason: the hub's goal drive owns it.

Anything else (plain prose) is refused with :data:`REFUSAL`, and nothing changes. Only new parks are
checked: a card already resting in ``scheduled`` keeps its state.

:func:`overdue` names a park that should have been woken and was not ("wake overdue"): its recorded
time passed more than :data:`OVERDUE_GRACE_S` ago, a worker run it names has no record or ended that
long ago, or nothing at all wakes it. ``kanban diagnostics`` and the ``kanban list`` line show it.

:func:`migrate_legacy_time_parks` runs in the board's migration pass: a ``scheduled`` card whose
latest park recorded no wait and whose reason carries ``SCHEDULED_UNTIL=<time>`` gets that time as
its recorded wait; a marker that does not parse is recorded once as a ``wait_unparsed`` event and
logged for the operator. Cards that wait on a worker run, or on the goal drive, are not touched.
"""
from __future__ import annotations

import contextlib
import json
import logging
import re
import sqlite3
import time
from datetime import UTC, datetime
from pathlib import Path
from typing import Any, Callable, Optional

log = logging.getLogger(__name__)

ACCEPTED = ("run:<worker run id>", "card:<card id>", "review:<card id>", "until:<epoch seconds or ISO-8601>",
            "pr:<PR URL or owner/repo#n> (its checks finished)", "merged:<PR URL or owner/repo#n>",
            "unit:<name of the unit that unblocks it>")
REFUSAL = ("a park needs something that wakes it. Pass wait_for as one of: " + "; ".join(ACCEPTED)
           + ". A worker run id or SCHEDULED_UNTIL=<time> in the reason is accepted too. Nothing changed.")
# Kinds the worker hub or a declared unit wakes: the dispatcher's tick leaves them alone.
HUB_WAIT_KINDS = ("run", "unit")
# One dispatcher tick is 60 s by default; a wake later than this is overdue, not just pending.
OVERDUE_GRACE_S = 300

_LEGACY_UNTIL = re.compile(r"(?<![A-Za-z0-9_])SCHEDULED_UNTIL=(\S+)")
_DRIVE_EVENT = re.compile(r"\bWAIT_EVENT=(\S+)")
_DRIVE_ACTION = re.compile(r"\bGOAL_ACTION=\S+")
_UNIT_NAME = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.:/-]{0,63}")
_PARK_EVENT_KINDS = ("scheduled", "blocked", "unblocked", "gave_up")


def _run_id_pattern() -> re.Pattern:
    from hermes_cli.worker.kanban_link import RUN_ID
    return RUN_ID


def parse_park_wait(spec: str) -> dict[str, Any]:
    """A park's ``wait_for`` spec: the dispatcher's kinds (``kanban_db_wait.parse_wait_for``) plus
    ``run:`` and ``unit:``. An unknown kind raises :data:`REFUSAL`."""
    from hermes_cli.kanban_db_wait import WAIT_KINDS, parse_wait_for

    kind, sep, value = (spec or "").partition(":")
    kind, value = kind.strip().lower(), value.strip()
    if not sep or not value or kind not in (*WAIT_KINDS, *HUB_WAIT_KINDS):
        raise ValueError(f"wait_for {spec!r} is not a waker: {REFUSAL}")
    if kind == "run":
        runs = [token for token in re.split(r"[\s,]+", value) if token]
        if not runs or not all(_run_id_pattern().fullmatch(run) for run in runs):
            raise ValueError(f"run wait must name worker run ids (OT-/OG-…), got {value!r}")
        return {"kind": "run", "runs": list(dict.fromkeys(runs))}
    if kind == "unit":
        if not _UNIT_NAME.fullmatch(value):
            raise ValueError("unit wait must name the unit in letters, digits and . _ - : / (64 at most)")
        return {"kind": "unit", "name": value}
    return parse_wait_for(f"{kind}:{value}")


def _drive_owned(reason: str) -> bool:
    return bool(_DRIVE_EVENT.search(reason) and _DRIVE_ACTION.search(reason))


def _legacy_until(reason: str) -> Optional[dict[str, Any]]:
    """The ``until`` wait a ``SCHEDULED_UNTIL=<time>`` reason names; None without the marker.
    Raises ValueError when the marker is there but is not a time."""
    from hermes_cli.kanban_db_wait import _parse_until

    match = _LEGACY_UNTIL.search(reason)
    if not match:
        return None
    try:
        return {"kind": "until", "at": _parse_until(match[1])}
    except ValueError:
        raise ValueError(f"SCHEDULED_UNTIL={match[1]} is not a time (epoch seconds or ISO-8601)") from None


def resolve_park(conn: sqlite3.Connection, reason: Optional[str], spec: Any) -> Optional[dict[str, Any]]:
    """The ``wait_for`` to record for a new park, or None when its reason names the waker (a worker run
    id, the goal drive's marker). Raises ValueError naming the accepted forms when nothing wakes it."""
    from hermes_cli import kanban_db as kb

    if spec is not None:
        if not isinstance(spec, str):
            raise ValueError("wait_for must be a string such as 'until:2030-01-01T09:00:00Z'")
        wait_for = parse_park_wait(spec)
        if wait_for.get("task_id") and kb.get_task(conn, wait_for["task_id"]) is None:
            raise ValueError(f"unknown task {wait_for['task_id']} in wait_for")
        return wait_for
    text = reason or ""
    try:
        legacy = _legacy_until(text)
    except ValueError as exc:
        raise ValueError(f"{exc}; {REFUSAL}") from None
    if legacy is not None:
        return legacy
    if _run_id_pattern().search(text) or _drive_owned(text):
        return None
    raise ValueError(REFUSAL)


# --- Overdue parks ---------------------------------------------------------------------------

def _named_runs(park: dict) -> list[str]:
    """Worker runs a park waits on: run ids in its reason, ``goal_park.workers`` and a ``run`` wait."""
    goal_park = park.get("goal_park") if isinstance(park.get("goal_park"), dict) else {}
    wait_for = park.get("wait_for") if isinstance(park.get("wait_for"), dict) else {}
    text = " ".join([str(park.get("reason") or ""), *map(str, goal_park.get("workers") or []),
                     *map(str, wait_for.get("runs") or [])])
    return list(dict.fromkeys(_run_id_pattern().findall(text)))


def hub_run_state(run_id: str, worker_root: Optional[Path] = None) -> Optional[tuple[str, float]]:
    """``("open", 0)`` or ``("ended", <time of its last write>)`` for a worker run as the hub's job
    records hold it (an ``OG-`` batch: its members), None when the hub has no record. A hub with a
    legacy tool configured answers ``("open", 0)`` for a run it does not know: that tool may know it."""
    from hermes_cli.worker import settings
    from hermes_cli.worker.events import END_STATES

    root = worker_root or settings.root()
    jobs = settings.hub_dir(root) / "jobs"
    ended = (*END_STATES, "unknown")
    paths = [jobs / f"{run_id}.json"] if not run_id.startswith("OG-") else sorted(jobs.glob("*.json"))
    found: list[tuple[dict, float]] = []
    for path in paths:
        if not path.is_file():
            continue
        try:
            job = json.loads(path.read_text(encoding="utf-8-sig"))
        except (OSError, ValueError):
            continue
        if path.stem == run_id or job.get("group") == run_id:
            found.append((job, path.stat().st_mtime))
    if not found:
        return ("open", 0.0) if settings.legacy_omo_task(root.parent) else None
    if any(job.get("state") not in ended for job, _ in found):
        return ("open", 0.0)
    return ("ended", max(mtime for _, mtime in found))


def _iso(at: float) -> str:
    return datetime.fromtimestamp(int(at), tz=UTC).strftime("%Y-%m-%dT%H:%M:%SZ")


def _time_overdue(at: int, now: int) -> Optional[str]:
    if now <= at + OVERDUE_GRACE_S:
        return None
    return f"wake overdue: its time {_iso(at)} passed {(now - at) // 60} min ago and it is still parked"


def overdue(park: dict, now: int, run_state: Optional[Callable[[str], Optional[tuple[str, float]]]] = None,
            ) -> Optional[str]:
    """Why a card still ``scheduled`` by the park event payload *park* is overdue, or None.

    Card, review and PR waits are left to the dispatcher, which hands a card whose upstream is gone to
    a person within a tick; a ``unit`` wait to the unit that declared it."""
    from hermes_cli.kanban_db_wait import _parse_until

    wait_for = park.get("wait_for") if isinstance(park.get("wait_for"), dict) else {}
    if wait_for.get("kind") == "until":
        return _time_overdue(int(wait_for.get("at") or 0), now)
    runs = _named_runs(park)
    if runs:
        states = {run: (run_state or hub_run_state)(run) for run in runs}
        missing = [run for run, state in states.items() if state is None]
        if missing:
            return f"wake overdue: worker run {', '.join(missing)} has no record, so nothing will wake it"
        if all(state[0] == "ended" for state in states.values()):
            last = max(state[1] for state in states.values())
            if now > last + OVERDUE_GRACE_S:
                return (f"wake overdue: worker run {', '.join(runs)} ended {int(now - last) // 60} min ago "
                        "and the card is still parked")
        return None
    if wait_for:
        return None
    reason = str(park.get("reason") or "")
    event = _DRIVE_EVENT.search(reason)
    if event and _DRIVE_ACTION.search(reason):
        kind, _, value = event[1].partition(":")
        if kind != "until":
            return None
        try:
            return _time_overdue(_parse_until(value), now)
        except ValueError:
            return None
    try:
        legacy = _legacy_until(reason)
    except ValueError as exc:
        return f"wake overdue: nothing wakes this park ({exc})"
    if legacy is not None:
        return _time_overdue(legacy["at"], now)
    return "wake overdue: nothing wakes this park (no recorded wait, worker run id or time)"


def _latest_park(conn: sqlite3.Connection, task_id: str) -> Optional[sqlite3.Row]:
    placeholders = ",".join("?" for _ in _PARK_EVENT_KINDS)
    return conn.execute(
        f"SELECT id, kind, payload, created_at, run_id FROM task_events WHERE task_id = ? AND kind IN "
        f"({placeholders}) ORDER BY created_at DESC, id DESC LIMIT 1", (task_id, *_PARK_EVENT_KINDS),
    ).fetchone()


def task_overdue(conn: sqlite3.Connection, task_id: str, status: str, *, now: Optional[int] = None,
                 run_state: Optional[Callable[[str], Optional[tuple[str, float]]]] = None) -> Optional[str]:
    """:func:`overdue` for a card on this board (None unless it is ``scheduled``)."""
    if status != "scheduled":
        return None
    row = _latest_park(conn, task_id)
    if row is None or row["kind"] != "scheduled":
        return None
    park = json.loads(row["payload"]) if row["payload"] else {}
    return overdue(park, int(time.time()) if now is None else int(now), run_state)


# --- Parks from before the wait was recorded ------------------------------------------------

def migrate_legacy_time_parks(conn: sqlite3.Connection) -> list[str]:
    """Give each ``scheduled`` card parked with only ``SCHEDULED_UNTIL=<time>`` in its reason that time
    as its recorded wait (the ``scheduled`` event's ``wait_for``, plus a ``wait_recorded`` event).
    Returns the cards whose marker does not parse; each gets one ``wait_unparsed`` event. A card that
    already has a recorded wait, names a worker run, or is the goal drive's park is not touched."""
    from hermes_cli import kanban_db as kb

    found: list[tuple[str, sqlite3.Row, dict, str]] = []
    for card in conn.execute("SELECT id FROM tasks WHERE status = 'scheduled'").fetchall():
        row = _latest_park(conn, card["id"])
        if row is None or row["kind"] != "scheduled":
            continue
        park = json.loads(row["payload"]) if row["payload"] else {}
        reason = str(park.get("reason") or "")
        if not (park.get("wait_for") or _named_runs(park) or _drive_owned(reason)) and _LEGACY_UNTIL.search(reason):
            found.append((card["id"], row, park, reason))
    if not found:
        return []
    unparsed: list[str] = []
    # The board's migration pass may already hold a transaction; its writes then join it.
    with contextlib.nullcontext() if conn.in_transaction else kb.write_txn(conn):
        for task_id, row, park, reason in found:
            try:
                wait_for = _legacy_until(reason)
            except ValueError:
                seen = conn.execute(
                    "SELECT 1 FROM task_events WHERE task_id = ? AND kind = 'wait_unparsed' "
                    "AND json_extract(payload, '$.park_event_id') = ?", (task_id, row["id"]),
                ).fetchone()
                if not seen:
                    kb._append_event(conn, task_id, "wait_unparsed",
                                     {"park_event_id": row["id"], "reason": reason})
                unparsed.append(task_id)
                continue
            conn.execute("UPDATE task_events SET payload = ? WHERE id = ?",
                         (json.dumps({**park, "wait_for": wait_for}), row["id"]))
            kb._append_event(conn, task_id, "wait_recorded",
                             {"park_event_id": row["id"], "wait_for": wait_for, "from": "reason"},
                             run_id=row["run_id"])
    if unparsed:
        log.warning("kanban: %d scheduled card(s) carry a SCHEDULED_UNTIL marker that is not a time and "
                    "nothing wakes them; give each a wait or unblock it: %s", len(unparsed), ", ".join(unparsed))
    return unparsed


__all__ = [
    "ACCEPTED", "HUB_WAIT_KINDS", "OVERDUE_GRACE_S", "REFUSAL", "hub_run_state", "migrate_legacy_time_parks",
    "overdue", "parse_park_wait", "resolve_park", "task_overdue",
]
