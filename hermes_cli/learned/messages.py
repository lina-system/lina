"""Read-only Hermes conversation access; tool results join by call ID and session."""
import json
import sqlite3
from contextlib import ExitStack, closing
from hermes_state import SessionDB
from hermes_cli.learned.locale import basis_prefixes
from datetime import datetime


def timestamp(value):
    try:
        return float(value)
    except (TypeError, ValueError):
        try:
            return datetime.fromisoformat(str(value)).timestamp()
        except ValueError:
            return 0


def parse(value, default):
    if isinstance(value, (dict, list)):
        return value
    try:
        return json.loads(value)
    except (TypeError, ValueError):
        return default


def basis_kind(text, source):
    if source == "cron" or text.startswith(basis_prefixes("cron")):
        return "cron"
    if text.startswith(basis_prefixes("checker")):
        return "checker"
    if text.startswith(basis_prefixes("bot")):
        return "bot"
    return "owner"


def failed(result):
    return isinstance(result, dict) and (result.get("success") is False or bool(result.get("error"))
        or result.get("is_error") is True or result.get("exit_code", 0) not in (0, None))


def confirmed(result):
    """An empty/unrecognized result is not evidence that a mutation succeeded."""
    return isinstance(result, dict) and not failed(result) and (
        result.get("success") is True or result.get("status") in ("success", "created", "deleted", "ok")
        or result.get("exit_code") == 0 or any(result.get(k) for k in (
            "id", "conclusion_id", "content", "conclusion", "deleted_id", "replaced_entry", "removed_entry",
            "patched_entry_before", "patched_entry_after")))


class Messages:
    def __init__(self, profiles, now):
        self.profiles, self.now = profiles, now
        self.dbs = {}

    def __enter__(self):
        self.stack = ExitStack()
        try:
            for profile, home in self.profiles.items():
                db = self.stack.enter_context(closing(SessionDB(home / "state.db", read_only=True)))
                self.dbs[profile] = db
        except (OSError, ValueError, sqlite3.Error):
            self.stack.close()
            raise
        return self

    def __exit__(self, *args):
        self.stack.close()

    def basis(self, profile, session, at):
        db = self.dbs.get(profile)
        if db is None or not session:
            return []
        return [dict(kind=basis_kind(row["content"] or "", row["source"]), text=row["content"] or "",
                     profile=profile, session=session, at=row["timestamp"])
                for row in db._read_all("""SELECT m.content, m.timestamp, s.source FROM messages m
                  LEFT JOIN sessions s ON s.id=m.session_id
                  WHERE m.session_id=? AND m.role='user' AND m.timestamp<=?
                  ORDER BY m.timestamp DESC, m.id DESC LIMIT 2""", (session, at))]

    def calls(self, since):
        calls = []
        for profile, db in self.dbs.items():
            # Index the small time window once rather than scan messages once per call.
            results = {(row["session_id"], row["tool_call_id"]): row["content"] for row in db._read_all("""
                SELECT session_id, tool_call_id, content FROM messages
                WHERE role='tool' AND timestamp>=? AND timestamp<=?
                ORDER BY timestamp, id""", (since, self.now))}
            for row in db._read_all("""SELECT id, session_id, timestamp, tool_calls FROM messages
                WHERE role='assistant' AND timestamp>=? AND timestamp<=? AND tool_calls IS NOT NULL
                ORDER BY timestamp, id""", (since, self.now)):
                entries = parse(row["tool_calls"], [])
                if not isinstance(entries, list):
                    continue
                for index, entry in enumerate(entries):
                    if not isinstance(entry, dict):
                        continue
                    function = entry.get("function", {})
                    args = parse(function.get("arguments"), {})
                    if not isinstance(args, dict):
                        continue
                    ids = [entry.get("call_id"), entry.get("id")]
                    result = next((results[(row["session_id"], id_)] for id_ in ids
                                   if id_ and (row["session_id"], id_) in results), None)
                    calls.append(dict(profile=profile, session=row["session_id"], at=row["timestamp"],
                                      key=f"{profile}:{row['session_id']}:{row['id']}:{index}",
                                      name=function.get("name", ""), args=args,
                                      result=parse(result, {"text": result}) if result is not None else None))
        # Keep SQL's numeric message order for calls sharing a timestamp.
        return sorted(calls, key=lambda call: call["at"])

    def users(self):
        for profile, db in self.dbs.items():
            for row in db._read_all("""SELECT m.session_id, m.content, m.timestamp, s.source FROM messages m
                LEFT JOIN sessions s ON s.id=m.session_id WHERE m.role='user' AND m.timestamp<=?""", (self.now,)):
                yield dict(profile=profile, session=row["session_id"], text=row["content"] or "",
                           at=row["timestamp"], kind=basis_kind(row["content"] or "", row["source"]))
