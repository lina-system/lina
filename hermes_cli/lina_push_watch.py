"""What pushes, and when (lina#244): the owner's open items, kept in step with every subscribed device.

Exactly three kinds of item push, each taken from the function the app's own screens use:

- ``ask``: an open, unanswered decision: the bell's ``ask`` notices (``lina_board.notices`` over ``work_read``), keyed
  by the notice key, so a read mark from the app (``board/read.json``) takes it out too.
- ``reply``: an agent's latest reply to a comment of the owner's on an open goal or to-do (the record's ``reply``
  events), until the owner comments on that item again or read-marks the key. A reply that asks him to decide (``답변
  필요``) is left to its ``ask``.
- ``blocked`` / ``question``: the kanban cards ``/work/inbox`` lists (``work_api.get_inbox``): blocked waiting for the
  owner, or carrying an open question.

The tag of a notification is the item's key. An item whose text changes is sent again under the same tag (the device
replaces it); one that leaves the set gets a ``close`` for its tag. The last set and each device's undelivered messages
are kept in ``$LINA_HOME/push/state.json``, so a restart neither repeats nor misses; the first sweep with no state
records what is open as sent. A source that cannot be read keeps its items as they were (no close, no resend).

The sender is one thread per LINA root (a lock file in ``push/``), started with the web server. It tails the change
stream's three sources (``lina_board_events.Stream``) and sweeps when one moves, every ``SWEEP_SECONDS`` anyway, and
when a device's backoff runs out. 404 and 410 drop that subscription; other failures back off per device and are
logged with the endpoint's host only.
"""

from __future__ import annotations

import hashlib
import json
import logging
import os
import threading
import time
from pathlib import Path
from typing import Callable

import httpx

from hermes_cli import lina_push
from utils import atomic_json_write, read_json_or_empty

TICK_SECONDS = 1.0
SWEEP_SECONDS = 300.0
BACKOFF_SECONDS = (30, 60, 120, 300, 600, 1800)
TEXT_LIMIT = 300
GONE = (404, 410)
SOURCES = {"ask": "work", "reply": "work", "blocked": "kanban", "question": "kanban"}
log = logging.getLogger(__name__)


def _cut(text) -> str:
    text = " ".join(str(text or "").split())
    return text if len(text) <= TEXT_LIMIT else text[:TEXT_LIMIT - 1] + "…"


def _item(tag: str, kind: str, target: str, at, title: str, body) -> dict:
    return {"tag": tag, "kind": kind, "target": target, "at": int(at or 0), "title": _cut(title), "body": _cut(body)}


# ---------------------------------------------------------------- the sources
def _asks(root: Path, read: set[str]) -> list[dict]:
    from hermes_cli.lina_board import _WORK_LOCK, _notices_in_locale, work_read
    work = work_read(root)
    open_asks = {row["target"]: row for row in work["decide"] if not row["answered"]}
    with _WORK_LOCK:
        notes = _notices_in_locale(root, dict(work, decide=list(open_asks.values()), mine=[]), [])
    return [_item(n["key"], "ask", n["target"], n["at"], n["line"], open_asks[n["target"]]["text"])
            for n in notes if n["kind"] == "ask" and n["key"] not in read]


def _replies(root: Path, read: set[str]) -> list[dict]:
    from hermes_cli.lina_board import _WORK_LOCK
    from hermes_cli.work import env, ledger
    from hermes_cli.work.evaluate import item_name
    out = []
    with _WORK_LOCK:
        env.bind(env.load(root))
        try:
            recs, order = ledger.read_all()
            owner, names = env.current().owner, dict(env.current().agents)
        finally:
            ledger.close()
            env.reset()
    for i in order:
        r = recs[i]
        if r.get("kind") not in ("목표", "할 일") or r.get("state") != "open":
            continue
        comments = r.get("comments") or []
        owner_last = max((int(c.get("at") or 0) for c in comments if c.get("by") == owner), default=0)
        for c in comments:
            last = (c.get("replies") or [None])[-1]
            if c.get("by") != owner or not last or last.get("by") == owner or last.get("result") == "답변 필요":
                continue
            key = f"reply:{r['id']}:{c['cid']}"
            if int(last.get("at") or 0) < owner_last or key in read:
                continue
            out.append(_item(key, "reply", r["id"], last.get("at"),
                             " · ".join(p for p in (names.get(last["by"], last["by"]), item_name(r)) if p),
                             last.get("text")))
    return out


def _cards() -> list[dict]:
    from plugins.kanban.dashboard.work_api import get_inbox
    inbox = get_inbox(board=None)
    out = [_item(f"blocked:{c['cardId']}", "blocked", c["cardId"], 0, " · ".join(filter(None, (c["agent"], c["title"]))),
                 c.get("blockReason") or c.get("blockKind") or c.get("reason")) for c in inbox["blocked"]]
    out += [_item(f"question:{q['cardId']}:{q['commentId']}", "question", q["cardId"], q["askedAt"],
                  " · ".join(filter(None, (q["agent"], q["title"]))), q["body"]) for q in inbox["questions"]]
    return out


def wanted(root: Path) -> tuple[dict[str, dict], set[str]]:
    """The items that push now, by tag, and the sources that could not be read."""
    from hermes_cli.lina_board import _source
    from hermes_cli.lina_board_write import read_marks
    errors: list[dict] = []
    read = _source("work", errors, lambda: read_marks(root))
    work = None if read is None else _source("work", errors, lambda: _asks(root, read) + _replies(root, read))
    cards = _source("kanban", errors, _cards)
    items = {i["tag"]: i for i in (work or []) + (cards or [])}
    return items, {e["source"] for e in errors}


# ---------------------------------------------------------------- state
def _state_path(root: Path) -> Path:
    return lina_push.push_dir(root) / "state.json"


def _save(root: Path, state: dict) -> None:
    folder = lina_push.push_dir(root)
    folder.mkdir(parents=True, exist_ok=True)
    folder.chmod(0o700)
    atomic_json_write(_state_path(root), state, mode=0o600)


def _digest(item: dict) -> str:
    return hashlib.sha256(json.dumps(item, sort_keys=True, ensure_ascii=False).encode()).hexdigest()[:24]


def show(item: dict) -> dict:
    return {"v": 1, "type": "show", **item}


def close(tag: str) -> dict:
    return {"v": 1, "type": "close", "tag": tag}


class Sender:
    """One sweep at a time over one LINA root; *post* is the HTTP seam, *clock* the time."""

    def __init__(self, root: Path, vapid: lina_push.Vapid, post: lina_push.Post = lina_push.http_post,
                 clock: Callable[[], float] = time.time):
        self.root, self.vapid, self.post, self.clock = root, vapid, post, clock
        self.retry_at = 0.0

    def sweep(self) -> None:
        now = self.clock()
        items, failed = wanted(self.root)
        digests = {tag: _digest(item) for tag, item in items.items()}
        state = read_json_or_empty(_state_path(self.root))
        if not state.get("seeded"):
            _save(self.root, {"seeded": True, "items": digests, "devices": {}})
            log.info("push: first start, %d open items recorded as sent", len(digests))
            return
        old = state.get("items") or {}
        stale = {tag: d for tag, d in old.items() if SOURCES.get(tag.split(":")[0]) in failed}
        messages = {tag: show(items[tag]) for tag, d in digests.items() if old.get(tag) != d}
        messages.update({tag: close(tag) for tag in old if tag not in digests and tag not in stale})
        subs = lina_push.subscriptions(self.root, now)
        devices = {sid: dev for sid, dev in (state.get("devices") or {}).items() if sid in subs}
        for sid in subs:
            devices.setdefault(sid, {"pending": {}, "fails": 0, "retryAt": 0})["pending"].update(messages)
        state = {"seeded": True, "items": {**stale, **digests}, "devices": devices}
        _save(self.root, state)
        for sid, sub in subs.items():
            self._deliver(state, sid, sub, now)
        due = [dev["retryAt"] for dev in state["devices"].values() if dev["pending"] and dev["retryAt"]]
        self.retry_at = min(due, default=0.0)

    def _deliver(self, state: dict, sid: str, sub: dict, now: float) -> None:
        dev = state["devices"][sid]
        if dev["retryAt"] > now:
            return
        for tag, message in list(dev["pending"].items()):
            try:
                status = lina_push.send(self.vapid, sub, message, self.post, now)
            except (OSError, ValueError, httpx.HTTPError) as exc:  # a bad stored key or the network: retried
                status = type(exc).__name__
            if isinstance(status, int) and 200 <= status < 300:
                dev["pending"].pop(tag)
                _save(self.root, state)
                continue
            if status in GONE:
                log.info("push: %s answered %s, subscription removed", lina_push.host(sub["endpoint"]), status)
                lina_push.drop(self.root, sid)
                state["devices"].pop(sid)
            else:
                dev["fails"] += 1
                wait = BACKOFF_SECONDS[min(dev["fails"], len(BACKOFF_SECONDS)) - 1]
                dev["retryAt"] = now + wait
                log.warning("push: %s answered %s, retry in %ds", lina_push.host(sub["endpoint"]), status, wait)
            _save(self.root, state)
            return
        dev["fails"], dev["retryAt"] = 0, 0
        _save(self.root, state)

    def run(self, stop: threading.Event) -> None:
        """The thread body: a sweep now, then one on every change of the three stores, every SWEEP_SECONDS and when
        a backoff runs out."""
        from hermes_cli.lina_board_events import Stream
        stream = Stream(self.root)
        try:
            cursor = stream.tail()
            next_sweep = 0.0
            while not stop.is_set():
                cursor, events, resync = stream.fetch(cursor)
                now = time.monotonic()
                if events or resync or now >= next_sweep or (self.retry_at and self.clock() >= self.retry_at):
                    try:
                        self.sweep()
                    except Exception:  # the sender must outlive one bad sweep; the next change tries again
                        log.exception("push: sweep failed")
                    next_sweep = now + SWEEP_SECONDS
                stop.wait(TICK_SECONDS)
        finally:
            stream.close()
            stream.executor.shutdown(wait=False)


def start(root: Path | None = None) -> Callable[[], None]:
    """Start the sender with the web server -> its stop. Without a key, or with another sender already holding this
    root, nothing starts and the stop does nothing."""
    import fcntl

    from hermes_cli.lina_board import _root
    vapid, why = lina_push.vapid_key()
    if vapid is None:
        log.warning("push: off (%s)", why)
        return lambda: None
    root = _root() if root is None else root
    folder = lina_push.push_dir(root)
    folder.mkdir(parents=True, exist_ok=True)
    folder.chmod(0o700)
    holder = os.open(folder / "sender.lock", os.O_RDWR | os.O_CREAT, 0o600)
    try:
        fcntl.flock(holder, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        os.close(holder)
        log.info("push: another process sends for %s", root)
        return lambda: None
    from agent.memory_provider import spawn_context_thread
    stop = threading.Event()
    thread = spawn_context_thread(Sender(root, vapid).run, args=(stop,), name="lina-push")
    thread.start()

    def halt() -> None:
        stop.set()
        thread.join(timeout=5)
        os.close(holder)
    return halt
