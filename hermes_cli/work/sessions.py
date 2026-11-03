"""The ``sessions`` list of ``work json``: each queued or running session of the optional spawner queue with its
purpose (the goal it works for), what it does now, its recent tool calls and the last finished run's first line."""

from __future__ import annotations

import json
import re
import sqlite3
from contextlib import closing
from pathlib import Path

from hermes_cli.work.common import now
from hermes_cli.work.locale import t
from hermes_cli.work.people import agent_name
from hermes_cli.work.sources import profile_home, sessions_index

SESSION_OUTPUT_META = re.compile(
    r'^(?:=== \d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} |↻ 세션 재개됨 |'
    r'세션 \S+을\(를\) 찾았지만 메시지가 없어요|⚠ |결과 파일:|session_id:)')
SAFE = re.compile(r"[\w.-]+")


def session_file(path):
    """Read a small queue-referenced file only within the user's home."""
    try:
        path = Path(path).expanduser().resolve()
        if Path.home().resolve() not in path.parents or not path.is_file():
            return None
        with path.open(encoding="utf-8") as f:
            return path.stat().st_mtime, f.read(8192)
    except (OSError, UnicodeError, ValueError, TypeError):
        return None


def _lines(text: str, skip_meta: bool = False) -> list[str]:
    return [line.strip() for line in text.splitlines()
            if line.strip() and not (skip_meta and SESSION_OUTPUT_META.match(line.strip()))]


def _purpose(recs: dict, profile: str, name: str) -> str | None:
    for r in recs.values():
        if r.get("session") == profile + ":" + name or "session:" + profile + ":" + name in (r.get("links") or []):
            goal, seen = r, set()
            while goal.get("kind") != "목표" and goal.get("parent") in recs and goal["parent"] not in seen:
                seen.add(goal["parent"])
                goal = recs[goal["parent"]]
            return (goal.get("id", "") + " " + goal.get("title", "")).strip()
    return None


def _pending_query(x: dict) -> str | None:
    path = x.get("query_snapshot") or x.get("query_file")
    data = session_file(path) if path else None
    lines = _lines(data[1]) if data else []
    return lines[0][:500] if lines else None


def _last_result(last: dict, data) -> str:
    ended = int(last.get("ended") or 0)
    log = session_file(last.get("output_log")) if last.get("output_log") else None
    lines = _lines(log[1] if log is not None else str(last.get("tail") or ""), skip_meta=True)
    if lines:
        return lines[0][:500]
    previous = session_file(last["result_file"]) if last.get("result_file") else data
    if previous and int(last.get("started") or 0) <= previous[0] <= ended:
        lines = _lines(previous[1])
        if lines:
            return lines[0][:500]
    return t("정상 종료") if last.get("rc") == 0 else t("정상 종료하지 못함")


def _running(x: dict, index: dict, profile: str, name: str) -> dict:
    extra, start = {}, float(x.get("started") or 0)
    data = session_file(profile_home(profile) / "cache" / "scratch" / (name + ".md"))
    if data:
        at, contents = data
        lines = _lines(contents)
        if lines and at >= start:
            extra.update(now_text=" ".join(lines[:2])[:500], now_at=int(at))
    last = index.get("last_exited") or {}
    ended = int(last.get("ended") or 0)
    if ended and ended <= start:
        extra["last_result"] = _last_result(last, data)
    extra.update(_state_db(x, profile, name, start, extra))
    return extra


def _session_row(db, x: dict, name: str, columns: set):
    session_id, row = x.get("session_id"), None
    activity = {"last_activity_at", "last_activity_description"} <= columns
    if not session_id:
        fields = "id" + (", last_activity_at, last_activity_description" if activity else "")
        row = db.execute("SELECT " + fields + " FROM sessions WHERE title=? LIMIT 1", (name,)).fetchone()
        session_id = row[0] if row else None
    if not session_id:
        log = session_file(x.get("output_log")) if x.get("output_log") else None
        evidence = str(x.get("tail") or "") + "\n" + (log[1] if log else "")
        match = re.search(r'(?m)^\s*(?:session_id:\s*|↻ 세션 재개됨\s+)([^\s"<>]+)', evidence)
        session_id = match.group(1) if match else None
    if session_id and not row and activity:
        row = db.execute("SELECT id, last_activity_at, last_activity_description FROM sessions WHERE id=? LIMIT 1",
                         (session_id,)).fetchone()
    return session_id, row


def _actions(rows) -> list[dict]:
    actions = []
    for at, calls in rows:
        try:
            parsed = json.loads(calls)
        except (ValueError, TypeError):
            continue
        for call in parsed if isinstance(parsed, list) else []:
            if not isinstance(call, dict):
                continue
            function = call.get("function") or {}
            fname = str(function.get("name") or call.get("name") or "")[:80]
            if not fname:
                continue
            try:
                args = json.loads(function.get("arguments") or "{}")
                keys = ", ".join(str(key)[:40] for key in list(args)[:3]) if isinstance(args, dict) else ""
            except (ValueError, TypeError):
                keys = ""
            actions.append({"text": fname + (f" ({keys})" if keys else ""), "at": at})
    return actions


def _messages(db, session_id: str, start: float):
    columns = {c[1] for c in db.execute("PRAGMA table_info(messages)")}
    newest = db.execute("SELECT content, timestamp FROM messages WHERE session_id=? AND role='assistant' AND "
                        "timestamp>=? AND trim(content)!='' ORDER BY timestamp DESC LIMIT 1",
                        (session_id, start)).fetchone()
    if "tool_calls" not in columns:
        return newest, []
    rows = db.execute("SELECT timestamp, tool_calls FROM messages WHERE session_id=? AND role='assistant' AND "
                      "timestamp>=? AND tool_calls IS NOT NULL ORDER BY timestamp DESC LIMIT 20",
                      (session_id, start)).fetchall()
    return newest, rows


def _state_db(x: dict, profile: str, name: str, start: float, known: dict) -> dict:
    extra = {}
    try:
        uri = (profile_home(profile) / "state.db").resolve().as_uri() + "?mode=ro"
        with closing(sqlite3.connect(uri, uri=True, timeout=0.1)) as db:
            columns = {c[1] for c in db.execute("PRAGMA table_info(sessions)")}
            session_id, row = _session_row(db, x, name, columns)
            if session_id and row and len(row) > 1 and row[1]:
                extra["activity_at"] = row[1]
            newest, rows = _messages(db, session_id, start) if session_id else (None, [])
    except (sqlite3.Error, OSError, ValueError):
        return extra
    actions = _actions(rows)
    if actions:
        extra["actions"] = list(reversed(actions[:5]))
        extra["activity_at"] = max(extra.get("activity_at", 0), known.get("now_at", 0), actions[0]["at"])
    if "now_text" not in known:
        activity = row[1] if row and len(row) > 2 else None
        description = row[2] if row and len(row) > 2 else None
        if activity and description and (not newest or activity - newest[1] > 600):
            extra.update(now_text=description[:500], now_at=activity, now_translate=True)
        elif newest:
            extra.update(now_text=" ".join(newest[0].split())[:500], now_at=newest[1], now_translate=True)
    return extra


def session_details(x: dict, recs: dict, index: dict) -> dict:
    profile, name = x.get("profile") or "", x.get("name") or ""
    if not SAFE.fullmatch(profile) or not SAFE.fullmatch(name) or profile in (".", "..") or name in (".", ".."):
        return {}
    extra = {}
    if x.get("run_budget") is not None:
        extra["run_budget"] = x["run_budget"]
    purpose = _purpose(recs, profile, name)
    if purpose is not None:
        extra["purpose"] = purpose
    if x["_bucket"] == "pending":
        query = _pending_query(x)
        if query:
            extra["query"] = query
    if x["_bucket"] == "running":
        extra.update(_running(x, index, profile, name))
    return extra


def _row(x: dict) -> dict:
    return {"id": x.get("id"), "profile": x.get("profile"), "bot": agent_name(x.get("profile")),
            "name": x.get("name"), "bucket": x["_bucket"], "state": x.get("state"), "rc": x.get("rc"),
            "started": x.get("started"), "ended": x.get("ended"), "created": x.get("created"),
            "wait_reason": x.get("wait_reason")}


def session_list(recs: dict) -> list[dict]:
    sess, sess_id = sessions_index()
    out = []
    for x in sess_id.values():
        if x["_bucket"] == "done" and now() - int(x.get("ended") or 0) > 86400:
            continue
        row = _row(x)
        row.update(session_details(x, recs, sess.get((x.get("profile"), x.get("name")), {})))
        out.append(row)
        if x["_bucket"] != "pending":
            continue
        for instruction in x.get("appends") or []:
            if not isinstance(instruction, dict):
                continue
            appended = dict(x, query_snapshot=instruction.get("query_snapshot"),
                            query_file=instruction.get("query_file"))
            detail = session_details(appended, recs, {})
            out.append(dict(row, id=None, created=instruction.get("added"), query=detail.get("query", "")))
    return out
