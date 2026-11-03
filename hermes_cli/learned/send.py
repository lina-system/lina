"""Deliver through the canonical messaging tool, only on explicit card --send.

Each attempt appends one line to sent.jsonl. A failed attempt is retried once.
A delivered card leaves cards/<day>.json: {day, sent_at, lines: [{n, id, ids}]},
the item ids on each numbered line, which `learned reply` answers against.
"""
from datetime import datetime, UTC
import json
import os
import subprocess
import time


def records(path):
    try:
        return [json.loads(line) for line in path.read_text(encoding="utf-8-sig").splitlines() if line.strip()]
    except FileNotFoundError:
        return []


def append(path, row):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    with path.open("a", encoding="utf-8") as out:
        os.chmod(path, 0o600)
        out.write(json.dumps(row, ensure_ascii=False) + "\n")


def record(path, value):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    temp = path.with_name(path.name + ".tmp")
    with temp.open("w", encoding="utf-8") as out:
        os.chmod(temp, 0o600)
        out.write(json.dumps(value, ensure_ascii=False) + "\n")
    temp.replace(path)


def attempt(config, text, mask):
    from agent.secret_scope import build_profile_secret_scope, set_secret_scope, reset_secret_scope
    from hermes_cli.learned.upstream import profile_scope
    from hermes_cli.profiles import get_profile_dir
    from tools.send_message_tool import send_message_tool
    home = get_profile_dir(config.send_profile)
    with profile_scope(home):
        token = set_secret_scope(build_profile_secret_scope(home), profile_home=str(home))
        try:
            result = json.loads(send_message_tool({"action": "send", "target": config.send_target, "message": text}))
        finally:
            reset_secret_scope(token)
    if result.get("success") and not result.get("error") and not result.get("skipped"):
        return None
    return mask(str(result.get("error") or "delivery did not succeed"))[:160]


def send(config, view, mask):
    from hermes_cli.learned.storage import State
    with State(config.state):
        return _send_locked(config, view, mask)


def _send_locked(config, view, mask):
    if not config.send_target:
        raise SendNotConfigured()
    path = config.state / "sent.jsonl"
    day = view["day"]
    if any(row.get("day") == day and row.get("delivered") for row in records(path)):
        return 0
    stamp = lambda: datetime.fromtimestamp(time.time(), UTC).astimezone().isoformat(timespec="seconds")
    items = len(view["items"])
    if not items:
        append(path, dict(day=day, at=stamp(), items=0, chars=0, delivered=False, reason="empty"))
        return 0
    text = mask(view["card"])
    for n in range(2):
        if n:
            time.sleep(config.retry_seconds)
        error = attempt(config, text, mask)
        row = dict(day=day, at=stamp(), items=items, chars=len(text), delivered=error is None)
        if error is not None:
            row["error"] = error
        if error is None:
            record(config.state / "cards" / (day + ".json"),
                   dict(day=day, sent_at=row["at"], lines=view.get("card_lines", [])))
        append(path, row)
        if error is None:
            return 0
    return 1


class SendNotConfigured(ValueError):
    pass
