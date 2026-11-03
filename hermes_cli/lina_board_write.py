"""The owner's writes and read marks through the client API (lina#149).

Each write runs the ``lina work`` command the CLI runs (``owner-done``, ``owner-undo``, ``comment``, ``comment
--answer``, ``add 메모 --via``) through ``work.execute``, the CLI's own path (resume hand-offs, run, recompute, close),
with ``via="app"``: the rules, the record line and the notice to the owning agent are the command's own. A command
that refuses (``die``) writes nothing; its one line becomes the 4xx detail. Read marks on the board's notices are kept
per owner in ``$LINA_HOME/board/read.json`` (``{owner id: {notice key: marked at}}``), so every device reads the same.
"""

from __future__ import annotations

import argparse
import collections
import io
import threading
import time
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path
from typing import Callable

from hermes_cli.lina_board import _WORK_LOCK, _root, _source, work_read, worker_read

VIA = "app"
TEXT_LIMIT = 2000
WRITES_PER_MINUTE = 30
KINDS = ("목표", "할 일")
RETRY_TEXT = "다시 해 달라: 앞서 한 것을 이어서 하고 결과를 남겨 줘"
_HITS: dict[str, collections.deque] = {}
_HITS_LOCK = threading.Lock()
_MARKS_LOCK = threading.Lock()


class Refused(Exception):
    def __init__(self, status: int, detail: str):
        super().__init__(detail)
        self.status, self.detail = status, detail


def allow_write(client: str, at: float | None = None) -> bool:
    """A sliding minute of writes per client; the one past the limit is refused before anything runs."""
    at = time.monotonic() if at is None else at
    with _HITS_LOCK:
        hits = _HITS.setdefault(client, collections.deque())
        while hits and at - hits[0] >= 60:
            hits.popleft()
        if len(hits) >= WRITES_PER_MINUTE:
            return False
        hits.append(at)
        return True


# ---------------------------------------------------------------- body checks (the API's own boundary)
def _body(body, allowed: dict[str, type], required: tuple[str, ...] = ()) -> dict:
    body = {} if body is None else body
    if not isinstance(body, dict):
        raise Refused(400, "the body is a JSON object")
    extra = sorted(set(body) - set(allowed))
    if extra:
        raise Refused(400, f"not accepted here: {', '.join(extra)}")
    for key, kind in allowed.items():
        if key in required and key not in body:
            raise Refused(400, f"{key} is required")
        if key in body and body[key] is not None and not isinstance(body[key], kind):
            raise Refused(400, f"{key} must be {kind.__name__}")
    return body


def _text(value: str) -> str:
    text = value.strip()
    if not 1 <= len(text) <= TEXT_LIMIT:
        raise Refused(400, f"text must be 1-{TEXT_LIMIT} characters after trimming")
    return text


# ---------------------------------------------------------------- the adapter
def _args(fn: Callable, **values) -> argparse.Namespace:
    return argparse.Namespace(fn=fn, via=VIA, **values)


def _execute(a: argparse.Namespace, refused: int) -> str:
    """One ``lina work`` command as the CLI runs it -> its stdout; a refusal raises Refused with its message."""
    from hermes_cli.work import execute
    out, err = io.StringIO(), io.StringIO()
    try:
        with redirect_stdout(out), redirect_stderr(err):
            execute(a)
    except SystemExit as exc:
        if exc.code:
            lines = [line.removeprefix("work: ").strip() for line in err.getvalue().splitlines() if line.strip()]
            raise Refused(refused, lines[-1] if lines else "refused") from exc
    return out.getvalue()


def _item(i: str) -> dict:
    from hermes_cli.work import ledger
    recs, _ = ledger.read_all()
    r = recs.get(i)
    if not r or r.get("kind") not in KINDS or r.get("state") == "dropped":
        raise Refused(404, f"no goal or to-do {i}")
    return r


def _result(i: str, cid: str | None = None) -> dict:
    from hermes_cli.lina_board_items import _owner_done
    from hermes_cli.work import ledger
    from hermes_cli.work.common import now
    recs, _ = ledger.read_all()
    r = recs[i]
    if r["kind"] == "목표":
        state, reason = ledger.db().execute("SELECT status, NULL FROM v_goal WHERE id = ?", (i,)).fetchone()
    else:
        state, reason = ledger.db().execute("SELECT status, reason FROM v_todo WHERE id = ?", (i,)).fetchone()
    mark = _owner_done(r)
    undo = mark["undoUntil"] if mark and now() <= mark["undoUntil"] else None
    return {"ok": True, "id": i, "state": state, "reason": reason, "cid": cid, "undoUntil": undo}


def _write(root: Path, item_id: str | None, act: Callable[[str | None], tuple[str, str | None]]) -> dict:
    """Under the work lock with the record bound: check the item, run the command, read its state back."""
    from hermes_cli.work import env, ledger
    with _WORK_LOCK:
        env.bind(env.load(root))
        try:
            if item_id is not None:
                _item(item_id)
            i, cid = act(item_id)
            return _result(i, cid)
        finally:
            ledger.close()
            env.reset()


def mark_done(item_id: str, body) -> dict:
    from hermes_cli.work.cmd_comments import cmd_owner_done
    body = _body(body, {"withOpen": bool})

    def act(i):
        _execute(_args(cmd_owner_done, id=i, with_open=bool(body.get("withOpen"))), 409)
        return i, None
    return _write(_root(), item_id.strip().upper(), act)


def undo_done(item_id: str, body) -> dict:
    from hermes_cli.work.cmd_comments import cmd_owner_undo
    _body(body, {})

    def act(i):
        _execute(_args(cmd_owner_undo, id=i), 409)
        return i, None
    return _write(_root(), item_id.strip().upper(), act)


def _comment(item_id: str, text: str | None, answer: bool) -> dict:
    from hermes_cli.work.cmd_comments import cmd_comment
    from hermes_cli.work.locale import t

    def act(i):
        a = _args(cmd_comment, id=i, text=text or t(RETRY_TEXT), by=None, answer=answer)
        cid = _execute(a, 409).split()
        return i, cid[0] if cid else None
    return _write(_root(), item_id.strip().upper(), act)


def comment(item_id: str, body) -> dict:
    body = _body(body, {"text": str, "answer": bool}, required=("text",))
    return _comment(item_id, _text(body["text"]), bool(body.get("answer")))


def retry(item_id: str, body) -> dict:
    """A comment asking the owning agent to retry, in the record's locale; nothing is re-run here."""
    _body(body, {})
    return _comment(item_id, None, False)


def quick_add(body) -> dict:
    """One line to a chosen agent: ``work add 메모 --by <agent> --via app [--due]`` (the words are its basis)."""
    from hermes_cli.work.cmd_add import cmd_add
    body = _body(body, {"agent": str, "text": str, "due": str}, required=("agent", "text"))
    text = _text(body["text"])

    def act(_):
        from hermes_cli.work.env import current
        if body["agent"] not in current().agents:
            raise Refused(400, f"unknown agent {body['agent']}")
        a = _args(cmd_add, kind="메모", by=body["agent"], title=text.splitlines()[0].strip(), basis=[text],
                  due=body.get("due"), area=None, project=None, from_=None, goal=None, done=None, check=None,
                  parent=None, session=None, omo=None, link=None, promise=False, said=None, every=None, stall=None,
                  note=None, why=None)
        return _execute(a, 400).split()[0], None
    return _write(_root(), None, act)


# ---------------------------------------------------------------- read marks
def _marks_path(root: Path) -> Path:
    return root / "board" / "read.json"


def read_marks(root: Path) -> set[str]:
    from hermes_cli.work import env
    from hermes_cli.work.common import load_json
    marks = load_json(_marks_path(root), {})
    return set((marks.get(env.load(root).owner) or {}) if isinstance(marks, dict) else {})


def current_notices(root: Path, now: float) -> list[dict]:
    """The board's notices now; refused when a source they come from fails (the keys would be a guess)."""
    from hermes_cli.lina_board import _notices_in_locale
    errors: list[dict] = []
    work = _source("work", errors, lambda: work_read(root))
    cards = _source("worker", errors, lambda: worker_read(root, now))
    if errors:
        raise Refused(503, f"{errors[0]['source']}: {errors[0]['message']}")
    with _WORK_LOCK:
        return _notices_in_locale(root, work, [c for c in cards if c["state"] == "waiting"])


def mark_read(body, now: float | None = None) -> dict:
    from hermes_cli.work import env
    from hermes_cli.work.common import load_json, save_json
    body = _body(body, {"keys": list, "all": bool})
    keys, every = body.get("keys"), bool(body.get("all"))
    if (keys is None) == (not every):
        raise Refused(400, "send exactly one of keys or all: true")
    if keys is not None and (not keys or not all(isinstance(k, str) for k in keys)):
        raise Refused(400, "keys is a non-empty list of notice keys")
    now, root = time.time() if now is None else now, _root()
    current = {n["key"] for n in current_notices(root, now)}
    unknown = sorted(set(keys or ()) - current)
    if unknown:
        raise Refused(400, f"not a current notice: {unknown[0]}")
    owner = env.load(root).owner
    with _MARKS_LOCK:
        marks = load_json(_marks_path(root), {})
        marks = marks if isinstance(marks, dict) else {}
        mine = {k: at for k, at in (marks.get(owner) or {}).items() if k in current}
        for key in current if every else keys:
            mine.setdefault(key, int(now))
        marks[owner] = mine
        save_json(_marks_path(root), marks)
    return {"ok": True, "unread": len(current - set(mine))}

