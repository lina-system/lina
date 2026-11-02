"""Planned-against-actual outcome of goal-mode work cards (lina#111).

When a goal-mode card reaches ``done`` or ``blocked`` one ``work_card_outcomes`` row is written
(replaced on a later terminal transition, so a card has exactly one row). Every number comes from
the board's own records: the card body, its runs, events (``goal_gate`` included) and comments —
never from what the agent reports. ``hermes kanban outcomes`` prints the rows and one summary line
per agent and kind so the next split of the same kind of work is sized from numbers.
"""

from __future__ import annotations

import json
import logging
import re
import sqlite3
import statistics
import subprocess
import time
from typing import Any, Optional

_log = logging.getLogger(__name__)

WORK_CARD_OUTCOMES_DDL = """
CREATE TABLE IF NOT EXISTS work_card_outcomes (
    task_id              TEXT PRIMARY KEY,
    assignee             TEXT,
    kind                 TEXT NOT NULL,
    status               TEXT NOT NULL,
    planned_todos        INTEGER NOT NULL,
    closed_todos         INTEGER,
    added_todos          INTEGER NOT NULL,
    worker_runs          TEXT NOT NULL,
    worker_run_count     INTEGER NOT NULL,
    escalations          INTEGER NOT NULL,
    turns_used           INTEGER,
    gate_failures        INTEGER NOT NULL,
    elapsed_seconds      INTEGER,
    owner_answers        INTEGER NOT NULL,
    main_session_touches INTEGER NOT NULL,
    dev_red_runs         INTEGER,
    recorded_at          INTEGER NOT NULL,
    todo_states          TEXT
)
"""

# Comment authors that are the owner answering (the dashboard and the CLI default identity).
OWNER_NAMES = frozenset({"user", "dashboard", "default"})
ADDED_BY_SESSION = "added-by: work-session"
ESCALATE_MARK = "escalate:"
GATE_WAIT_EXIT_CODE = 75  # a waiting gate is not a failure (kanban_goal_gates)
CARD_SCOPE = "(card)"     # worker runs whose comment names no to-do

# Worker run IDs recorded in comments, by runner: omo jobs, quick-issue runs, ``lina worker`` runs.
_WORKER_ID_PATTERNS = (
    ("omo", re.compile(r"\b(OT-[A-Z0-9]{6,}(?:-r\d+)?)\b")),
    ("quick-issue", re.compile(r"\bquick-issue(?:\s+job)?[\s:#]+([A-Za-z0-9][\w.-]*\d[\w.-]*)", re.IGNORECASE)),
    ("lina-worker", re.compile(r"\blina[ -]worker(?:\s+run)?[\s:#]+([A-Za-z0-9][\w.-]*\d[\w.-]*)", re.IGNORECASE)),
)
_ISSUE_TODO = re.compile(r"^\s*-\s+([\w.-]+/[\w.-]+)#(\d+)\b")
_CHECK_TODO = re.compile(r"^\s*-\s+\[([ xX])\]\s+(.+?)\s*$")
_TODO_HEADING = re.compile(r"^##\s+To-dos\s*$", re.IGNORECASE)
_DEV_GATE = re.compile(r"\bgate\s+dev\b")
_KIND_WORD = re.compile(r"[A-Za-z][\w-]*")


def ensure_table(conn: sqlite3.Connection) -> None:
    conn.execute(WORK_CARD_OUTCOMES_DDL)
    columns = {row[1] for row in conn.execute("PRAGMA table_info(work_card_outcomes)")}
    if "todo_states" not in columns:
        conn.execute("ALTER TABLE work_card_outcomes ADD COLUMN todo_states TEXT")
    conn.execute("CREATE INDEX IF NOT EXISTS idx_outcomes_agent_kind "
                 "ON work_card_outcomes(assignee, kind)")



def parse_todos(text: Optional[str]) -> list[dict]:
    """To-do lines under ``## To-dos``: ``- owner/repo#n`` or ``- [ ] text`` / ``- [x] text``."""
    todos, inside = [], False
    for line in (text or "").splitlines():
        if line.startswith("## "):
            inside = bool(_TODO_HEADING.match(line))
            continue
        if inside:
            todos.extend(_todo_items(line))
    return todos


def _todo_items(line: str) -> list[dict]:
    issue = _ISSUE_TODO.match(line)
    if issue:
        return [{"ref": f"{issue.group(1)}#{issue.group(2)}", "repo": issue.group(1),
                 "number": int(issue.group(2)), "checked": None}]
    check = _CHECK_TODO.match(line)
    if check:
        return [{"ref": check.group(2), "checked": check.group(1) in "xX"}]
    return []


def _todo_lines_anywhere(text: str) -> list[dict]:
    return [item for line in text.splitlines() for item in _todo_items(line)]


def card_kind(title: str) -> str:
    head, sep, _rest = (title or "").partition(":")
    word = _KIND_WORD.match(head.strip()) if sep else None
    return word.group(0).lower() if word else "general"


def worker_ids(text: str) -> list[tuple[str, str]]:
    found = []
    for runner, pattern in _WORKER_ID_PATTERNS:
        found.extend((runner, m.group(1)) for m in pattern.finditer(text))
    return found


def _todo_for_comment(body: str, todos: list[dict]) -> str:
    """The to-do a comment is about: the first one it names, else the card as a whole."""
    for todo in todos:
        if "number" in todo:
            # A whole reference only: owner/repo#n or a bare #n token, never the #12 inside #123.
            full = rf"(?<![\w./-]){re.escape(todo['repo'])}#{todo['number']}(?![\w])"
            bare = rf"(?<![\w/#.-])#{todo['number']}(?![\w])"
            if re.search(full, body) or re.search(bare, body):
                return todo["ref"]
        elif todo["ref"] in body:
            return todo["ref"]
    return CARD_SCOPE



def gh_issue_closed(repo: str, number: int, *, profile_home: Optional[str] = None) -> Optional[bool]:
    """True/False per ``gh issue view`` run as ``profile_home``'s login (``kanban_pr_acceptance``
    rules); None when gh is missing or the call fails."""
    from hermes_cli.kanban_pr_acceptance import _gh_env

    try:
        proc = subprocess.run(["gh", "issue", "view", str(number), "-R", repo, "--json", "state",
                               "-q", ".state"], stdin=subprocess.DEVNULL, capture_output=True,
                              text=True, encoding="utf-8", errors="replace", timeout=15,
                              check=False, env=_gh_env(profile_home))
    except (OSError, subprocess.SubprocessError):  # FileNotFoundError: gh is not installed
        return None
    if proc.returncode != 0:
        return None
    return proc.stdout.strip().upper() == "CLOSED"


def todo_states(todos: list[dict], known: Optional[dict] = None, *,
                profile_home: Optional[str] = None, ask_github: bool = False) -> dict[str, Optional[bool]]:
    """Per to-do completion evidence: a ticked checklist line, or a GitHub issue closed per gh.
    Without ``ask_github`` (the terminal transition, which runs inside the dispatcher and the
    gateway) an issue not already known stays None for ``outcomes --refresh`` to fill."""
    known = known or {}
    states: dict[str, Optional[bool]] = {}
    for todo in todos:
        if "number" not in todo:
            states[todo["ref"]] = bool(todo["checked"])
        elif known.get(todo["ref"]) is not None:
            states[todo["ref"]] = known[todo["ref"]]
        else:
            states[todo["ref"]] = (gh_issue_closed(todo["repo"], todo["number"],
                                                   profile_home=profile_home)
                                   if ask_github else None)
    return states


def _closed_count(states: dict[str, Optional[bool]]) -> Optional[int]:
    if any(v is None for v in states.values()):
        return None
    return sum(1 for v in states.values() if v)


def _rows(conn: sqlite3.Connection, sql: str, task_id: str) -> list[sqlite3.Row]:
    return conn.execute(sql, (task_id,)).fetchall()


def _payload(row: sqlite3.Row) -> dict:
    try:
        data = json.loads(row["payload"] or "{}")
    except (TypeError, ValueError):
        return {}
    return data if isinstance(data, dict) else {}


def _body_was_edited(events: list[sqlite3.Row]) -> bool:
    return any(e["kind"] == "edited" and "body" in (_payload(e).get("fields") or [])
               for e in events)


def _worker_runs(comments: list[sqlite3.Row], todos: list[dict]) -> dict[str, list[dict]]:
    runs: dict[str, list[dict]] = {}
    seen: set[str] = set()
    for c in comments:
        for runner, run_id in worker_ids(c["body"]):
            if run_id in seen:
                continue
            seen.add(run_id)
            runs.setdefault(_todo_for_comment(c["body"], todos), []).append(
                {"runner": runner, "id": run_id})
    return runs


def _turns_used(runs: list[sqlite3.Row]) -> Optional[int]:
    total, found = 0, False
    for r in runs:
        try:
            meta = json.loads(r["metadata"] or "{}")
        except (TypeError, ValueError):
            continue
        value = meta.get("turns_used") if isinstance(meta, dict) else None
        if isinstance(value, int) and not isinstance(value, bool):
            total, found = total + value, True
    return total if found else None


def _gate_counts(events: list[sqlite3.Row]) -> tuple[int, Optional[int]]:
    """``(gate failures, red dev gate runs or None when no dev gate ran)``."""
    failures, dev_red, dev_seen = 0, 0, False
    for e in events:
        if e["kind"] != "goal_gate":
            continue
        p = _payload(e)
        failed = p.get("passed") is False and p.get("exit_code") != GATE_WAIT_EXIT_CODE
        failures += int(failed)
        if _DEV_GATE.search(str(p.get("command") or "")):
            dev_seen = True
            dev_red += int(failed)
    return failures, (dev_red if dev_seen else None)


def _elapsed(task: sqlite3.Row, runs: list[sqlite3.Row], now: int) -> Optional[int]:
    if not runs:
        return None
    start = min(int(r["started_at"]) for r in runs)
    if task["status"] == "done" and task["completed_at"]:
        end = int(task["completed_at"])
    else:
        end = max(int(r["ended_at"] or now) for r in runs)
    return max(0, end - start)


def _main_session_touches(task: sqlite3.Row, comments: list[sqlite3.Row],
                          runs: list[sqlite3.Row], now: int) -> int:
    """Comments by the card's own agent (assignee or creator) written after registration and
    outside every run of the card: the main session reaching in, not the card's session."""
    authors = {task["assignee"], task["created_by"]} - {None} - OWNER_NAMES
    windows = [(int(r["started_at"]), int(r["ended_at"] or now)) for r in runs]
    return sum(
        1 for c in comments
        if c["author"] in authors and int(c["created_at"]) >= int(task["created_at"])
        and not any(lo <= int(c["created_at"]) <= hi for lo, hi in windows)
    )


def compute_outcome(conn: sqlite3.Connection, task_id: str, *,
                    now: Optional[int] = None) -> Optional[dict[str, Any]]:
    """The outcome row for a terminal goal-mode card, or None for any other card."""
    task = conn.execute("SELECT * FROM tasks WHERE id = ?", (task_id,)).fetchone()
    if task is None or not task["goal_mode"] or task["status"] not in ("done", "blocked"):
        return None
    now = int(now if now is not None else time.time())
    comments = _rows(conn, "SELECT * FROM task_comments WHERE task_id = ? ORDER BY id", task_id)
    events = _rows(conn, "SELECT * FROM task_events WHERE task_id = ? ORDER BY id", task_id)
    runs = _rows(conn, "SELECT * FROM task_runs WHERE task_id = ? ORDER BY id", task_id)
    todos = parse_todos(task["body"])
    added = sum(max(1, len(_todo_lines_anywhere(c["body"]))) for c in comments
                if ADDED_BY_SESSION in c["body"].lower())
    planned = len(todos)
    if _body_was_edited(events):
        # The creation body is gone: count what the session did not add itself.
        planned = max(0, planned - added)
    worker_runs = _worker_runs(comments, todos)
    states = todo_states(todos)
    gate_failures, dev_red = _gate_counts(events)
    return {
        "task_id": task_id, "assignee": task["assignee"], "kind": card_kind(task["title"]),
        "status": task["status"], "planned_todos": planned, "closed_todos": _closed_count(states),
        "added_todos": added, "worker_runs": worker_runs,
        "worker_run_count": sum(len(v) for v in worker_runs.values()),
        "escalations": sum(1 for c in comments if ESCALATE_MARK in c["body"].lower()),
        "turns_used": _turns_used(runs), "gate_failures": gate_failures,
        "elapsed_seconds": _elapsed(task, runs, now),
        "owner_answers": sum(1 for c in comments if c["author"] in OWNER_NAMES),
        "main_session_touches": _main_session_touches(task, comments, runs, now),
        "dev_red_runs": dev_red, "recorded_at": now, "todo_states": states,
    }


_COLUMNS = ("task_id", "assignee", "kind", "status", "planned_todos", "closed_todos",
            "added_todos", "worker_runs", "worker_run_count", "escalations", "turns_used",
            "gate_failures", "elapsed_seconds", "owner_answers", "main_session_touches",
            "dev_red_runs", "recorded_at", "todo_states")


def write_outcome(conn: sqlite3.Connection, task_id: str) -> Optional[dict[str, Any]]:
    from hermes_cli.kanban_db import write_txn

    row = compute_outcome(conn, task_id)
    if row is None:
        return None
    values = [json.dumps(row[c]) if c in ("worker_runs", "todo_states") else row[c]
              for c in _COLUMNS]
    with write_txn(conn):
        conn.execute(
            f"INSERT OR REPLACE INTO work_card_outcomes ({', '.join(_COLUMNS)}) "
            f"VALUES ({', '.join('?' for _ in _COLUMNS)})", values,
        )
    return row


def record_terminal_outcome(conn: sqlite3.Connection, task_id: str) -> None:
    """Post-commit hook of ``complete_task`` / ``block_task``: never breaks the transition."""
    try:
        write_outcome(conn, task_id)
    except Exception:  # health: allow BLE001 -- the transition already committed; log, never raise
        _log.warning("kanban outcomes: could not record the outcome of %s", task_id, exc_info=True)



def _profile_home(assignee: Optional[str]) -> tuple[bool, Optional[str]]:
    """``(resolvable, home)``: gh reads as the assignee's own login, never the ambient one."""
    from hermes_cli.kanban_pr_acceptance import _GateAuthError, _assignee_profile_home

    try:
        return True, _assignee_profile_home(assignee)
    except _GateAuthError:
        return False, None


def refresh_closed(conn: sqlite3.Connection) -> tuple[int, int]:
    """Fill GitHub to-do states left unknown at write time by asking gh; ``(filled, still_null)``."""
    from hermes_cli.kanban_db import write_txn

    filled = pending = 0
    for row in conn.execute("SELECT o.task_id, o.assignee, o.todo_states, t.body "
                            "FROM work_card_outcomes o JOIN tasks t ON t.id = o.task_id "
                            "WHERE o.closed_todos IS NULL").fetchall():
        resolvable, home = _profile_home(row["assignee"])
        known = json.loads(row["todo_states"] or "{}")
        states = todo_states(parse_todos(row["body"]), known, profile_home=home,
                             ask_github=resolvable)
        closed = _closed_count(states)
        with write_txn(conn):
            conn.execute("UPDATE work_card_outcomes SET closed_todos = ?, todo_states = ? "
                         "WHERE task_id = ?", (closed, json.dumps(states), row["task_id"]))
        if closed is None:
            pending += 1
        else:
            filled += 1
    return filled, pending


def list_outcomes(conn: sqlite3.Connection, *, agent: Optional[str] = None,
                  kind: Optional[str] = None, last: int = 20) -> list[dict[str, Any]]:
    where, params = [], []
    for column, value in (("assignee", agent), ("kind", kind)):
        if value:
            where.append(f"{column} = ?")
            params.append(value)
    sql = "SELECT * FROM work_card_outcomes"
    if where:
        sql += " WHERE " + " AND ".join(where)
    sql += " ORDER BY recorded_at DESC, rowid DESC LIMIT ?"
    rows = conn.execute(sql, (*params, max(0, int(last)))).fetchall()
    out = []
    for r in rows:
        item = dict(r)
        item["worker_runs"] = json.loads(item["worker_runs"] or "{}")
        item["todo_states"] = json.loads(item["todo_states"] or "{}")
        out.append(item)
    return out


def _first_try(rows: list[dict]) -> dict[str, list[int]]:
    """Per runner ``[to-dos done in one worker run, to-dos it took first]``. Done needs the
    to-do's own completion evidence (``todo_states``); a single recorded run alone is not."""
    tally: dict[str, list[int]] = {}
    for row in rows:
        for todo, runs in row["worker_runs"].items():
            if todo == CARD_SCOPE or not runs:
                continue
            counts = tally.setdefault(runs[0]["runner"], [0, 0])
            counts[0] += int(len(runs) == 1 and row["todo_states"].get(todo) is True)
            counts[1] += 1
    return tally


def summarize(rows: list[dict]) -> list[dict[str, Any]]:
    groups: dict[tuple, list[dict]] = {}
    for row in rows:
        groups.setdefault((row["assignee"] or "-", row["kind"]), []).append(row)
    summary = []
    for (agent, kind), items in sorted(groups.items()):
        hours = [r["elapsed_seconds"] / 3600 for r in items if r["elapsed_seconds"] is not None]
        summary.append({
            "agent": agent, "kind": kind, "cards": len(items),
            "done": sum(1 for r in items if r["status"] == "done"),
            "first_try": {k: {"first_try": v[0], "todos": v[1]}
                          for k, v in sorted(_first_try(items).items())},
            "median_hours": round(statistics.median(hours), 2) if hours else None,
            "mean_gate_failures": round(statistics.mean(r["gate_failures"] for r in items), 2),
        })
    return summary


def _n(value: Any) -> str:
    return "-" if value is None else str(value)


def format_row(r: dict) -> str:
    hours = "-" if r["elapsed_seconds"] is None else f"{r['elapsed_seconds'] / 3600:.1f}h"
    return (f"{r['task_id']}  {r['assignee'] or '-'}  {r['kind']}  {r['status']}  "
            f"todos {_n(r['closed_todos'])}/{r['planned_todos']} +{r['added_todos']}  "
            f"runs {r['worker_run_count']}  esc {r['escalations']}  turns {_n(r['turns_used'])}  "
            f"gate-fail {r['gate_failures']}  {hours}  owner {r['owner_answers']}  "
            f"touches {r['main_session_touches']}  dev-red {_n(r['dev_red_runs'])}")


def format_summary(s: dict) -> str:
    first = " ".join(f"{k} {v['first_try']}/{v['todos']}" for k, v in s["first_try"].items())
    median = "-" if s["median_hours"] is None else f"{s['median_hours']}h"
    return (f"{s['agent']} / {s['kind']}: {s['cards']} cards, done {s['done']}/{s['cards']}, "
            f"first-try {first or '-'}, median {median} per card, "
            f"mean gate failures {s['mean_gate_failures']}")


def cmd_outcomes(args) -> int:
    from hermes_cli.kanban_db_connect import connect_closing

    with connect_closing() as conn:
        refreshed = refresh_closed(conn) if getattr(args, "refresh", False) else None
        rows = list_outcomes(conn, agent=getattr(args, "agent", None),
                             kind=getattr(args, "kind", None), last=getattr(args, "last", 20))
    summary = summarize(rows)
    if getattr(args, "json", False):
        out = {"rows": rows, "summary": summary}
        if refreshed is not None:
            out["refreshed"] = {"filled": refreshed[0], "still_null": refreshed[1]}
        print(json.dumps(out, indent=2, ensure_ascii=False))
        return 0
    if refreshed is not None:
        print(f"refreshed closed to-dos: {refreshed[0]} filled, {refreshed[1]} still unknown")
    if not rows:
        print("(no work card outcomes recorded)")
        return 0
    for row in rows:
        print(format_row(row))
    print()
    for s in summary:
        print(format_summary(s))
    return 0
