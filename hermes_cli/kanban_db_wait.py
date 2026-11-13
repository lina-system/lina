"""Event waits for parked cards: what a card waits on, and the dispatcher's wake.

A card that waits on something outside itself is parked with the board's
existing transitions, never a new status: ``schedule_task`` (a goal-mode WAIT
verdict, ``kanban schedule --wait-for``) or ``block_task(kind="needs_input")``
(``kanban block --wait-for``). The awaited event rides that transition's event
payload as ``wait_for``:

* ``{"kind": "card", "task_id": ...}``     -- another card is completed (``done``, or archived after it)
* ``{"kind": "review", "task_id": ...}``   -- a review card it asked for has its result (completed the same way)
* ``{"kind": "pr", "repo": ..., "number": ...}`` -- the PR's checks have finished (:func:`read_pr_checks`)
* ``{"kind": "merged", "repo": ..., "number": ...}`` -- the PR is merged (closed unmerged: to a person)
* ``{"kind": "until", "at": <epoch>}``     -- a recorded time passes

Each dispatcher tick calls :func:`wake_waiting_tasks`, which checks those
events without a model turn and wakes a met card with ``unblock_task``. The
unblock's status-guarded update is the exactly-once point: the woken card is
no longer parked, so later ticks never wake it again. A parked card with no
``wait_for`` (a manual schedule, a block on a person's answer) is left alone:
whoever parked it unblocks it.

A wait that can no longer be met by itself is handed to a person once (lina#258): the card
becomes a ``needs_input`` block without ``wait_for`` (:func:`hold_for_person`), its reason naming
why. That is a card or review wait whose upstream card was archived without finishing or
deleted, and a wait that could not be read ``WAIT_READ_FAILURE_LIMIT`` ticks in a row (a PR
wait whose evidence keeps failing, for example a gh login that cannot read the repository).
Each failed read is a ``wait_read_failed`` event carrying the failure and the running count;
the first good read after failures writes ``wait_read_ok``, so only consecutive failures count.
A PR wait still unmet ``PR_WAIT_STALL_S`` after its park writes one ``wait_stalled`` event and goes to
a person the same way (lina#667), so a wait never stands silently.

The same tick runs ``kanban_db_verdicts.verdict_tick``: a card woken to judge a run goes back to
waiting once it is judged, and a run or a pause nobody acts on is raised once (lina#295).
"""
from __future__ import annotations

import json
import re
import sqlite3
import time
from datetime import UTC, datetime
from typing import Any, Callable, Optional

WAIT_KINDS = ("card", "review", "pr", "merged", "until")
DISPATCHER_ACTOR = "kanban-dispatcher"

_PR_URL = re.compile(r"https://github\.com/([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)/pull/([1-9][0-9]*)")
_PR_SHORT = re.compile(r"([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)#([1-9][0-9]*)")
# The last transition into or out of a parked status; ``gave_up`` is the breaker's block.
_PARK_EVENT_KINDS = ("scheduled", "blocked", "unblocked", "gave_up")
_PARKED_EVENT_FOR_STATUS = {"scheduled": "scheduled", "blocked": "blocked"}
# Required-check classifications (kanban_pr_acceptance) that mean "not finished yet".
_UNFINISHED_CHECKS = {"pending", "missing", "stale"}
# Consecutive failed reads of one parked wait before it goes to a person: about half an hour
# at the default kanban.dispatch_interval_seconds (60), long enough to ride out a GitHub blip.
WAIT_READ_FAILURE_LIMIT = 30
_READ_EVENT_KINDS = ("wait_read_failed", "wait_read_ok")
# A PR wait still unmet this long after its park goes to a person: past the longest CI job GitHub runs.
PR_WAIT_STALL_S = 6 * 3600
# Required checks that never post on the head, or no check runs at all, mean nothing is coming once the
# park is this old and no run is going; younger, the checks may just not have registered yet.
PR_EVIDENCE_GRACE_S = 300


class WaitReadFailed(Exception):
    """The evidence for a wait could not be collected; the message names the failure."""


class WaitGone(Exception):
    """The awaited event can no longer happen (a PR closed without merging); the message says why."""


def _parse_until(value: str) -> int:
    if value.isdigit():
        return int(value)
    stamp = datetime.fromisoformat(value)
    if stamp.tzinfo is None:
        stamp = stamp.replace(tzinfo=UTC)
    return int(stamp.timestamp())


def parse_wait_for(spec: str) -> dict[str, Any]:
    """``card:<id>`` / ``review:<id>`` / ``pr:<url|owner/repo#n>`` / ``merged:<url|owner/repo#n>`` /
    ``until:<epoch|ISO-8601>``."""
    kind, sep, value = (spec or "").partition(":")
    kind, value = kind.strip().lower(), value.strip()
    if not sep or kind not in WAIT_KINDS or not value:
        raise ValueError("wait-for must be card:<id>, review:<id>, pr:<url|owner/repo#n>, "
                         "merged:<url|owner/repo#n> or until:<time>")
    if kind in ("card", "review"):
        return {"kind": kind, "task_id": value}
    if kind in ("pr", "merged"):
        if len(re.findall(r"[^\s,]+", value)) > 1:
            raise ValueError(f"a {kind} wait takes one PR; park on one PR, or on each in turn")
        match = _PR_URL.fullmatch(value) or _PR_SHORT.fullmatch(value)
        if not match:
            raise ValueError(f"{kind} wait must be a GitHub PR URL or owner/repo#number")
        return {"kind": kind, "repo": match[1], "number": int(match[2])}
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
    if kind == "merged":
        return f"{wait_for.get('repo')}#{wait_for.get('number')} merged"
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
    implementation run. A manual schedule (no ``wait_for``) from a reviewer run records it too
    (lina#258), so its unblock also returns to review; any other run's schedule records only its
    reason and its ``wait_for``."""
    from hermes_cli import kanban_db as kb

    payload: dict[str, Any] = {"reason": reason}
    if wait_for:
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


def _pr_ref(url: str) -> Optional[tuple[str, int]]:
    match = re.search(r"github\.com/([^/\s]+/[^/\s]+)/pull/(\d+)", url or "", re.IGNORECASE)
    return (match[1].lower(), int(match[2])) if match else None


def _wake_after_pr_comments(conn: sqlite3.Connection, task_id: str,
                            pr_rows: list[tuple[int, str, int]]) -> Optional[sqlite3.Row]:
    """The latest ``unblocked`` event after every PR comment, by event order: each comment's
    ``commented`` event is the anchor, so a same-second tie is ordered too. A comment written
    without one (no event) falls back to its time."""
    ids = [cid for _, _, cid in pr_rows]
    anchor = conn.execute(
        f"SELECT MAX(id) FROM task_events WHERE task_id = ? AND kind = 'commented' AND "
        f"json_extract(payload, '$.comment_id') IN ({','.join('?' for _ in ids)})", (task_id, *ids),
    ).fetchone()[0]
    after, value = ("id > ?", anchor) if anchor is not None else ("created_at >= ?", max(t for t, _, _ in pr_rows))
    return conn.execute(
        f"SELECT id, payload FROM task_events WHERE task_id = ? AND kind = 'unblocked' AND {after} "
        "ORDER BY id DESC LIMIT 1", (task_id, value),
    ).fetchone()


def woken_for_pr_work(conn: sqlite3.Connection, task_id: str, pr_rows: list[tuple[int, str, int]]) -> bool:
    """The respawn guard's ``active_pr`` exception for a card the dispatcher woke after its PR
    comments (``pr_rows``: ``(time, url, comment id)``): the next attempt goes on with that PR.

    The wake's permission is spent by the first real attempt after it: a run whose worker
    started (``kanban_db_handoff.worker_started_since``, the same attempt rule as the respawn
    guards). A refused or failed spawn, a reclaim before the worker came up, a park or a planned
    stop leaves it intact. A ``pr`` wait passes only when the waited PR is one of the commented
    URLs, and a person's later unblock or a newer PR comment ends the exception.
    """
    from hermes_cli.kanban_db_handoff import worker_started_since

    wake = _wake_after_pr_comments(conn, task_id, pr_rows)
    payload = json.loads(wake["payload"]) if wake is not None and wake["payload"] else {}
    if payload.get("by") != DISPATCHER_ACTOR or not str(payload.get("reason") or "").startswith("wait met: "):
        return False
    if worker_started_since(conn, task_id, wake["id"]):
        return False
    park = conn.execute(
        "SELECT payload FROM task_events WHERE task_id = ? AND kind IN ('scheduled', 'blocked') AND id < ? "
        "ORDER BY id DESC LIMIT 1", (task_id, wake["id"]),
    ).fetchone()
    wait_for = (json.loads(park["payload"]) if park is not None and park["payload"] else {}).get("wait_for") or {}
    if wait_for.get("kind") != "pr":
        return True
    waited = (str(wait_for.get("repo") or "").lower(), int(wait_for.get("number") or 0))
    return waited in {_pr_ref(url) for _, url, _ in pr_rows}


def dispatcher_wake(conn: sqlite3.Connection, task_id: str, park_event_id: int, wait_for: dict[str, Any]) -> bool:
    """Wake one parked card for its met event: the dispatcher's own unblock.

    Under one write transaction it first re-checks that the card is still parked by
    ``park_event_id`` (a person may have unblocked and re-parked it while a PR check was being
    read), then unblocks it keeping ``consecutive_failures``: waiting is not a recovery, so only
    a deliberate unblock by a person resets the retry budget.

    A met wait is a block that resolved itself, not a loop a person has to break, so it is not
    counted as a repeat: ``block_recurrences`` goes back to 0 (``block_kind`` stays). The next
    ``--wait-for`` block is therefore a first block again, while a block a person unblocked keeps
    counting toward ``BLOCK_RECURRENCE_LIMIT`` (triage).
    """
    from hermes_cli import kanban_db as kb

    with kb.write_txn(conn):
        if not _still_parked_by(conn, task_id, park_event_id):
            return False
        woken = kb.unblock_task(conn, task_id, by=DISPATCHER_ACTOR, keep_failures=True,
                                reason=f"wait met: {describe_wait(wait_for)}")
        if woken:
            conn.execute("UPDATE tasks SET block_recurrences = 0 WHERE id = ?", (task_id,))
        return woken


def _checks_settled(receipt: dict[str, Any], parked_for: float) -> bool:
    """Whether the checks a PR will get on its head have finished, from a receipt that ran to the end
    (or stopped at "no required checks", which carries the head's runs).

    Required checks that are pending or stale keep waiting. A required check that never posted
    (``missing``), or a repository with no required checks, is judged by the runs on the head: any run
    still going keeps waiting; with none going, the wait is met once the park is ``PR_EVIDENCE_GRACE_S``
    old (younger, the checks may just not have registered). Finished runs on a repository with no
    required checks are proof enough that its checks ran.
    """
    checks = receipt.get("checks") or []
    if any(c.get("classification") in ("pending", "stale") for c in checks):
        return False
    if receipt.get("required") and not any(c.get("classification") == "missing" for c in checks):
        return True
    runs = receipt.get("head_runs") or []
    if any(run.get("status") != "completed" for run in runs):
        return False
    return (not receipt.get("required") and bool(runs)) or parked_for >= PR_EVIDENCE_GRACE_S


def read_pr_checks(wait_for: dict[str, Any], assignee: Optional[str]) -> bool:
    """True once the PR's checks have finished (:func:`_checks_settled`), read from complete evidence
    only. A merged or closed PR is met at once. ``wait_for["parked_for"]`` (seconds since the park, set
    by the tick) ages the evidence grace; without it the grace has not passed.

    ``collect_acceptance`` sets ``detail`` on every path that stops before the closing head/base
    re-read. A head that moved (``stale``) keeps waiting and is re-read next tick. An unreadable PR,
    an auth refusal or a collection error (``infra``, possibly with checks already gathered) raises
    :class:`WaitReadFailed` naming the receipt's classification and detail, which the tick counts.
    The one early stop that is complete is "no required checks" (``required == []``). A receipt
    without ``detail`` ran to the end, so its ``infra`` can only be a check's conclusion.
    """
    from hermes_cli.kanban_pr_acceptance import collect_acceptance

    url = f"https://github.com/{wait_for['repo']}/pull/{int(wait_for['number'])}"
    receipt = collect_acceptance(url, None, assignee)
    if receipt.get("pr_state") in ("MERGED", "CLOSED"):
        return True
    if "detail" in receipt and receipt.get("required") != []:
        if receipt.get("classification") == "stale":
            return False
        raise WaitReadFailed(f"{receipt.get('classification')}: {receipt['detail']}")
    return _checks_settled(receipt, float(wait_for.get("parked_for") or 0))


def read_pr_merged(wait_for: dict[str, Any], assignee: Optional[str]) -> bool:
    """True once the PR is merged, False while it is open. A PR closed without merging raises
    :class:`WaitGone`; an unreadable one :class:`WaitReadFailed`, which the tick counts."""
    import subprocess

    from hermes_cli.kanban_pr_acceptance import _api, _assignee_profile_home, _GateAuthError

    owner, name = str(wait_for["repo"]).split("/")
    query = (f"{{repository(owner:{json.dumps(owner)},name:{json.dumps(name)})"
             f"{{pullRequest(number:{int(wait_for['number']):d}){{state}}}}}}")
    try:
        repository = _api("graphql", query=query, profile_home=_assignee_profile_home(assignee))["data"]["repository"]
    except _GateAuthError as exc:
        raise WaitReadFailed(f"auth: {exc}") from None
    except (subprocess.SubprocessError, OSError, ValueError, KeyError, TypeError) as exc:
        raise WaitReadFailed(f"infra: {type(exc).__name__}") from None
    state = ((repository or {}).get("pullRequest") or {}).get("state")
    if state is None:
        raise WaitReadFailed(f"auth: {wait_for['repo']}#{wait_for['number']} is not readable")
    if state == "CLOSED":
        raise WaitGone(f"{wait_for['repo']}#{wait_for['number']} was closed without merging")
    return state == "MERGED"


def pr_checks_finished(wait_for: dict[str, Any], assignee: Optional[str]) -> bool:
    """:func:`read_pr_checks` with a failed read reported as not finished."""
    try:
        return read_pr_checks(wait_for, assignee)
    except WaitReadFailed:
        return False


def hold_for_person(conn: sqlite3.Connection, task_id: str, park_event_id: int, reason: str) -> bool:
    """End a wait that cannot be met by itself: the card becomes a ``needs_input`` block with no
    ``wait_for``, so the tick leaves it to a person, who reads ``reason`` on the board.

    Only while ``park_event_id`` still parks the card (same re-check as :func:`dispatcher_wake`).
    A ``scheduled`` card moves to ``blocked``; a ``needs_input`` block stays blocked and gets a
    new ``blocked`` event without the wait. A review phase the park recorded is carried as
    ``resume_status: review``, so the person's unblock returns the card to review. Like
    ``kanban_db_handoff._hold_review_card`` it records a ``blocked`` run of its own.
    """
    from hermes_cli import kanban_db as kb

    with kb.write_txn(conn):
        if not _still_parked_by(conn, task_id, park_event_id):
            return False
        status = conn.execute("SELECT status FROM tasks WHERE id = ?", (task_id,)).fetchone()["status"]
        if status == "scheduled":
            resume_status = scheduled_resume_status(conn, task_id)
            conn.execute(
                "UPDATE tasks SET status = 'blocked', block_kind = 'needs_input', block_recurrences = "
                "CASE WHEN block_kind = 'needs_input' THEN block_recurrences + 1 ELSE 1 END "
                "WHERE id = ? AND status = 'scheduled'", (task_id,),
            )
        else:
            resume_status = kb._resume_status_from_events(conn, task_id)
        run_id = kb._end_or_synthesize_run(conn, task_id, outcome="blocked", status="blocked",
                                          summary=reason, synthesize=True)
        kb._append_event(conn, task_id, "blocked", {
            "reason": reason, "kind": "needs_input", "source_status": status,
            **({"resume_status": "review"} if resume_status == "review" else {}),
        }, run_id=run_id)
        task = kb.get_task(conn, task_id)
    kb._fire_task_hook("kanban_task_blocked", task, task_id, run_id, reason=reason)
    kb.record_terminal_outcome(conn, task_id)
    return True


def _upstream_ended_unfinished(conn: sqlite3.Connection, wait_for: dict[str, Any]) -> Optional[str]:
    """``archived``/``deleted`` when a card or review wait's upstream card ended without being
    completed (see :func:`_card_done`), so the awaited event can never happen; else None."""
    if wait_for["kind"] not in ("card", "review"):
        return None
    row = conn.execute("SELECT status, completed_at FROM tasks WHERE id = ?",
                       (str(wait_for.get("task_id")),)).fetchone()
    if row is None:
        return "deleted"
    return "archived" if row["status"] == "archived" and row["completed_at"] is None else None


def _consecutive_read_failures(conn: sqlite3.Connection, task_id: str, park_event_id: int) -> int:
    row = conn.execute(
        f"SELECT kind, payload FROM task_events WHERE task_id = ? AND id > ? AND kind IN "
        f"({','.join('?' for _ in _READ_EVENT_KINDS)}) ORDER BY id DESC LIMIT 1",
        (task_id, park_event_id, *_READ_EVENT_KINDS),
    ).fetchone()
    if row is None or row["kind"] != "wait_read_failed" or not row["payload"]:
        return 0
    return int(json.loads(row["payload"]).get("count") or 0)


def _record_read(conn: sqlite3.Connection, task_id: str, park_event_id: int, wait_for: dict[str, Any],
                 failure: Optional[str]) -> None:
    """Count a failed read of the card's wait (``failure``) or end a run of them (None); hand the
    card to a person once ``WAIT_READ_FAILURE_LIMIT`` reads in a row have failed."""
    from hermes_cli import kanban_db as kb

    if failure is None and not _consecutive_read_failures(conn, task_id, park_event_id):
        return   # the usual good read: nothing to write, no write lock
    with kb.write_txn(conn):
        if not _still_parked_by(conn, task_id, park_event_id):
            return
        count = _consecutive_read_failures(conn, task_id, park_event_id)
        if failure is None:
            if count:
                kb._append_event(conn, task_id, "wait_read_ok", {"after_failures": count})
            return
        count += 1
        kb._append_event(conn, task_id, "wait_read_failed", {"failure": failure, "count": count})
    if count >= WAIT_READ_FAILURE_LIMIT:
        hold_for_person(conn, task_id, park_event_id, (
            f"The wait ({describe_wait(wait_for)}) could not be read {count} times in a row; last "
            f"failure: {failure}. It no longer waits by itself: fix the cause, then unblock the card."))


def _parked_for(conn: sqlite3.Connection, park_event_id: int, now: int) -> int:
    """Seconds since the event that parked the card."""
    row = conn.execute("SELECT created_at FROM task_events WHERE id = ?", (park_event_id,)).fetchone()
    return max(0, now - int(row["created_at"]))


def _hold_stalled_pr_wait(conn: sqlite3.Connection, task_id: str, park_event_id: int,
                          wait_for: dict[str, Any], parked_for: int) -> None:
    """Hand a PR wait that is still unmet ``PR_WAIT_STALL_S`` after its park to a person, once
    (``wait_stalled`` event), the way a wait that cannot be read goes (lina#667)."""
    from hermes_cli import kanban_db as kb

    if parked_for < PR_WAIT_STALL_S:
        return
    with kb.write_txn(conn):
        if not _still_parked_by(conn, task_id, park_event_id):
            return
        kb._append_event(conn, task_id, "wait_stalled", {"wait": describe_wait(wait_for), "parked_for": parked_for})
    hold_for_person(conn, task_id, park_event_id, (
        f"The wait ({describe_wait(wait_for)}) was still unmet {parked_for // 3600} hours after the park: "
        "a check keeps running or queued, or the head keeps moving. It no longer waits by itself: open the "
        "pull request's checks, fix the cause, then unblock the card."))


def _card_done(conn: sqlite3.Connection, task_id: str) -> bool:
    """Done, or archived after it was completed: ``completed_at`` is set only by ``complete_task``
    (a reopen clears it), so an archive of an unfinished card is not completion."""
    row = conn.execute("SELECT status, completed_at FROM tasks WHERE id = ?", (task_id,)).fetchone()
    if row is None:
        return False
    return row["status"] == "done" or (row["status"] == "archived" and row["completed_at"] is not None)


def wait_met(conn: sqlite3.Connection, wait_for: dict[str, Any], *, assignee: Optional[str],
             now: int, pr_checks_fn: Callable[[dict, Optional[str]], bool],
             pr_merged_fn: Callable[[dict, Optional[str]], bool] = read_pr_merged) -> bool:
    kind = wait_for["kind"]
    if kind in ("card", "review"):
        return _card_done(conn, str(wait_for.get("task_id")))
    if kind == "pr":
        return pr_checks_fn(wait_for, assignee)
    if kind == "merged":
        return pr_merged_fn(wait_for, assignee)
    return now >= int(wait_for.get("at") or 0)


def wake_waiting_tasks(conn: sqlite3.Connection, *, now: Optional[int] = None,
                       pr_checks_fn: Optional[Callable[[dict, Optional[str]], bool]] = None,
                       pr_merged_fn: Optional[Callable[[dict, Optional[str]], bool]] = None) -> list[str]:
    """Unblock every parked card whose recorded event happened; returns the woken ids.

    Parked = ``scheduled``, or ``blocked`` with ``block_kind = 'needs_input'``. Network reads
    (PR checks) run outside any write transaction; :func:`dispatcher_wake` re-checks the parking
    event under its own, so a card unblocked or re-parked meanwhile is not woken by the old wait.
    """
    from hermes_cli import kanban_db as kb

    now = int(time.time()) if now is None else int(now)
    pr_checks_fn = pr_checks_fn or read_pr_checks
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
        ended = _upstream_ended_unfinished(conn, wait_for)
        if ended:
            hold_for_person(conn, row["id"], park_event_id, (
                f"The card this one waited for ({wait_for.get('task_id')}) was {ended} without "
                "finishing, so the wait can never be met. Decide what it waits on now, then unblock it."))
            continue
        parked_for = _parked_for(conn, park_event_id, now) if wait_for["kind"] == "pr" else 0
        try:
            met = wait_met(conn, {**wait_for, "parked_for": parked_for}, assignee=row["assignee"], now=now,
                           pr_checks_fn=pr_checks_fn, pr_merged_fn=pr_merged_fn or read_pr_merged)
        except WaitGone as exc:
            hold_for_person(conn, row["id"], park_event_id, (
                f"The wait ({describe_wait(wait_for)}) can never be met: {exc}. "
                "Decide what it waits on now, then unblock it."))
            continue
        except WaitReadFailed as exc:
            _record_read(conn, row["id"], park_event_id, wait_for, str(exc))
            continue
        except Exception as exc:  # health: allow BLE001 -- one unreadable wait must not stall the tick for every other card; logged with traceback, counted, retried next tick
            kb._log.warning("kanban: wait check failed for %s (%s)", row["id"], wait_for, exc_info=True)
            _record_read(conn, row["id"], park_event_id, wait_for, f"error: {type(exc).__name__}")
            continue
        _record_read(conn, row["id"], park_event_id, wait_for, None)
        if met and dispatcher_wake(conn, row["id"], park_event_id, wait_for):
            woken.append(row["id"])
        elif not met and wait_for["kind"] == "pr":
            _hold_stalled_pr_wait(conn, row["id"], park_event_id, wait_for, parked_for)
    from hermes_cli.kanban_db_verdicts import verdict_tick
    verdict_tick(conn, now=now)  # lina#295: judged cards back to waiting; raise what nobody judged
    return woken


__all__ = [
    "DISPATCHER_ACTOR", "PR_EVIDENCE_GRACE_S", "PR_WAIT_STALL_S", "WAIT_KINDS", "WAIT_READ_FAILURE_LIMIT", "WaitGone",
    "WaitReadFailed", "describe_wait", "dispatcher_wake", "hold_for_person", "needs_input_wait", "park_payload",
    "parse_wait_for", "pr_checks_finished", "read_pr_checks", "read_pr_merged", "recorded_wait",
    "scheduled_resume_status", "wait_for_from_directive", "wait_met", "wake_waiting_tasks", "woken_for_pr_work",
]
