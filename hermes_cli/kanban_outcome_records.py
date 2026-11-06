"""Learning records derived from a card's immutable event history."""

from __future__ import annotations

import json
from typing import Optional

from agent.redact import redact_sensitive_text
from hermes_cli.kanban_gates import DEFAULT_OWNERS

TOOL_NAMES = frozenset({"lina-work", "lina-worker", "quick-issue"})
# A ``scheduled`` park is a wait, not a failure; it keeps its own kind so a fleet pause and a
# worker wait (a schedule whose reason names the worker) stay distinguishable in ``stops``.
STOP_KINDS = frozenset({"blocked", "dependency_wait", "block_loop_detected", "timed_out",
                        "gave_up", "crashed", "reclaimed", "spawn_failed", "scheduled"})
RESUME_KINDS = frozenset({"claimed", "unblocked", "promoted", "completed"})
MAIN_SESSION_SUFFIX = "-main"
# A main session writes its resume note and the reason-less unblock in two calls, one second apart
# on the real board; the note still belongs to that hand when it lands this soon before the event.
UNBLOCK_NOTE_WINDOW_SECONDS = 5


def short_text(value) -> str:
    lines = str(value or "").splitlines()
    return redact_sensitive_text(lines[0] if lines else "", force=True,
                                 file_read=True, secret_file=True)[:200]


def main_session(name, own) -> Optional[str]:
    """The main session a comment author name belongs to, or None for any other name.

    One rule for every profile: ``<profile>-main`` names ``profile``'s main session when
    ``profile`` is one of the card's own names, and an own name is the main session itself.
    """
    name = str(name or "")
    if name in own:
        return name
    base = name[:-len(MAIN_SESSION_SUFFIX)]
    return base if name.endswith(MAIN_SESSION_SUFFIX) and base in own else None


def _same_author(first, second, own) -> bool:
    """Two names that record one hand: the same name, or the same main session."""
    first, second = str(first or ""), str(second or "")
    session = main_session(first, own)
    return bool(first) and bool(second) and (
        first == second or (session is not None and session == main_session(second, own)))


def payload(event) -> dict:
    try:
        value = json.loads(event["payload"] or "{}")
    except (TypeError, ValueError):
        return {}
    return value if isinstance(value, dict) else {}


def comment_events(comments, events) -> dict:
    """New events identify their comment exactly; legacy comments retain window semantics."""
    return {payload(e)["comment_id"]: e for e in events
            if e["kind"] == "commented" and payload(e).get("comment_id") is not None}


def touch_records(task, comments, events, runs, own, now) -> list[dict]:
    indexed = comment_events(comments, events)
    windows = [(r["started_at"], r["ended_at"] or now) for r in runs]
    sessions = set()
    for run in runs:
        try:
            metadata = json.loads(run["metadata"] or "{}")
        except (TypeError, ValueError):
            continue
        if isinstance(metadata, dict) and metadata.get("worker_session_id"):
            sessions.add(metadata["worker_session_id"])
    reviewers = {r["profile"] for r in runs if r["profile"] not in own}
    touches = []
    for comment in comments:
        author, at = comment["author"], comment["created_at"]
        if author in TOOL_NAMES or at < task["created_at"]:
            continue
        event = indexed.get(comment["id"])
        source = payload(event).get("source") if event is not None else None
        if event is not None and event["run_id"] is not None:
            continue
        if source and source.get("session") in sessions:
            continue
        if author in reviewers and author not in DEFAULT_OWNERS:
            continue
        if author in own and event is None and any(lo <= at <= hi for lo, hi in windows):
            continue
        touches.append({"at": at, "by": author, "source": source or {"surface": "legacy"},
                        "kind": "answer" if author in DEFAULT_OWNERS else "comment",
                        "line": short_text(comment["body"])})
    _add_event_touches(touches, events, comments, own)
    return sorted(touches, key=lambda item: item["at"])


def _add_event_touches(touches, events, comments, own) -> None:
    paired = set()
    for event in events:
        data = payload(event)
        kind = {"unblocked": "unblock", "edited": "edit", "linked": "link", "unlinked": "link"}.get(event["kind"])
        actor = unblock_actor(event, comments, own) if event["kind"] == "unblocked" else data.get("by")
        if kind and (actor or event["kind"] == "unblocked") and actor not in TOOL_NAMES:
            line = short_text(data.get("reason"))
            match = (_unblock_match(touches, paired, event, actor, line, comments, own)
                     if kind == "unblock" else None)
            if match is not None:
                touches[match]["kind"] = "answer" if actor in DEFAULT_OWNERS else "unblock"
                paired.add(match)
                continue
            touches.append({"at": event["created_at"], "by": actor,
                            "source": data.get("source") or {"surface": "board"},
                            "kind": kind, "line": line})


def _unblock_match(touches, paired, event, actor, line, comments, own):
    """Index of the comment touch this ``unblocked`` event already accounts for.

    The CLI records one act twice -- a ``UNBLOCK: <reason>`` comment and the event
    carrying ``<reason>`` -- and a legacy event with no reason names its actor in
    that same comment or in the main session's own note within the window. Pair only
    those shapes: a different second, author or text is a second act and keeps its
    own touch.
    """
    lines = set()
    at = event["created_at"]
    if line:
        lines.update((line, short_text(f"UNBLOCK: {line}")))
    else:
        legacy = unblock_comment(event, comments, own)
        if legacy is None:
            return None
        lines.add(short_text(legacy["body"]))
        at = legacy["created_at"]
    return next((i for i, touch in enumerate(touches)
                 if i not in paired and touch["kind"] in ("comment", "answer")
                 and _same_author(touch["by"], actor, own) and touch["at"] == at
                 and touch["line"] in lines), None)


def unblock_comment(event, comments, own=()):
    """The one same-second ``UNBLOCK:`` comment, else the one main-session note, a reason-less
    unblock records its actor in.

    The CLI writes ``UNBLOCK: <reason>``; a main session signs its resume note
    ``<profile>-main`` instead, and that note is the same hand as the unblock it writes
    within ``UNBLOCK_NOTE_WINDOW_SECONDS`` after it. Two notes in that window pair nothing.
    """
    on_card = [c for c in comments if c["task_id"] == event["task_id"]
               and event["created_at"] - UNBLOCK_NOTE_WINDOW_SECONDS <= c["created_at"]
               <= event["created_at"]]
    legacy = [c for c in on_card if c["body"].startswith("UNBLOCK:")
              and c["created_at"] == event["created_at"]]
    if legacy:
        return legacy[0] if len(legacy) == 1 else None
    notes = [c for c in on_card if main_session(c["author"], own)]
    return notes[0] if len(notes) == 1 else None


def unblock_actor(event, comments, own=()):
    actor = payload(event).get("by")
    if actor is not None:
        return actor
    comment = unblock_comment(event, comments, own)
    return comment["author"] if comment is not None else None


def replan_records(comments, events, runs) -> list[dict]:
    from hermes_cli.kanban_outcomes import ADDED_BY_SESSION, ESCALATE_MARK, _todo_lines_anywhere

    first = min((r["started_at"] for r in runs), default=None)
    records = []
    for event in events:
        data = payload(event)
        kind = None
        if event["kind"] == "decomposed":
            kind = "decomposed"
        elif first is not None and event["created_at"] >= first:
            if event["kind"] == "edited" and "body" in data.get("fields", []):
                kind = "edit"
            elif event["kind"] in ("linked", "unlinked"):
                kind = "link"
        if kind:
            records.append({"at": event["created_at"], "by": data.get("by"), "kind": kind,
                            "reason": short_text(data.get("reason"))})
    for comment in comments:
        for marker, kind in ((ADDED_BY_SESSION, "added"), (ESCALATE_MARK, "escalation")):
            for line in comment["body"].splitlines():
                if marker in line.lower():
                    count = max(1, len(_todo_lines_anywhere(comment["body"]))) if kind == "added" else 1
                    records.extend({"at": comment["created_at"], "by": comment["author"],
                                    "kind": kind, "reason": short_text(line)} for _ in range(count))
                    break
    return sorted(records, key=lambda item: item["at"])


def stop_records(events, now, comments=(), own=()) -> tuple[list[dict], int]:
    records, intervals = [], []
    for index, event in enumerate(events):
        if event["kind"] not in STOP_KINDS:
            continue
        resume = next((e for e in events[index + 1:] if e["kind"] in RESUME_KINDS), None)
        until = resume["created_at"] if resume is not None else None
        by = None
        if resume is not None:
            by = unblock_actor(resume, comments, own) if resume["kind"] == "unblocked" else "dispatcher"
        data = payload(event)
        at, end = event["created_at"], until if until is not None else now
        records.append({"at": at, "kind": event["kind"],
                        "reason": short_text(data.get("reason") or data.get("error")),
                        "until": until, "resumed_by": by, "seconds": max(0, end - at)})
        intervals.append((at, max(at, end)))
    # A timeout and its breaker are two explanations of the same stopped interval.
    total, end = 0, 0
    for start, stop in sorted(intervals):
        total += max(0, stop - max(start, end))
        end = max(end, stop)
    return records, total


def review_records(runs, own) -> list[dict]:
    return [{"reviewer": r["profile"], "verdict": r["outcome"], "at": r["started_at"],
             "until": r["ended_at"], "seconds": max(0, (r["ended_at"] or r["started_at"]) - r["started_at"])}
            for r in runs if r["profile"] and r["profile"] not in own]


def learning_records(conn, task, comments, events, runs, now) -> dict:
    from hermes_cli.goals import DEFAULT_MAX_TURNS
    from hermes_cli.kanban_gates import own_names

    own = own_names(conn, task)
    implementer = next((r["profile"] for r in runs if r["profile"] in own), None)
    if implementer is None:
        implementer = task["assignee"] if task["assignee"] in own else next(iter(sorted(own)), None)
    touches = touch_records(task, comments, events, runs, own, now)
    stops, stopped = stop_records(events, now, comments, own)
    waits = [e["created_at"] for e in events if e["kind"] == "goal_gate" and payload(e).get("exit_code") == 75]
    terminal = [e["created_at"] for e in events if e["kind"] in ("completed", "blocked", "gave_up")]
    end = now
    if task["status"] == "done":
        end = task["completed_at"]
    elif task["status"] == "blocked":
        end = max(terminal, default=now)
    return {"assignee": implementer, "touches": touches,
            "owner_answers": sum(t["kind"] == "answer" and t["by"] in DEFAULT_OWNERS for t in touches),
            "main_session_touches": sum(1 for t in touches
                                        if main_session(t["by"], own) and t["by"] not in DEFAULT_OWNERS),
            "replans": replan_records(comments, events, runs), "stops": stops,
            "reviews": review_records(runs, own), "stopped_seconds": stopped,
            "lead_seconds": max(0, (end or now) - task["created_at"]),
            "turn_budget": task["goal_max_turns"] or DEFAULT_MAX_TURNS,
            "waiting_gate_checks": len(waits),
            "waiting_gate_seconds": max(waits) - min(waits) if waits else 0}
