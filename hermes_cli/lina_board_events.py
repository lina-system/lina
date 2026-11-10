"""One change stream for the app (lina#150): ``GET /api/lina/events`` upgraded to a WebSocket.

Four append-only stores are tailed, each by its own position, and the four positions travel as one opaque cursor:

- ``work``: the work record's ``event`` table (one row per record line and run hand-off), by event id.
- ``worker``: ``lina worker``'s ``receipts.jsonl`` (the hub appends a receipt on every run state change, next to the
  job file it rewrites), by byte offset; every receipt is sent except the few that change nothing on the board
  (``WORKER_NOISE_KINDS``): a dropped change leaves a stale screen, an extra event costs one refetch.
- ``kanban``: the board's ``task_events`` table, by row id, read-only (the plugin's ``/events`` source).
- ``settings``: the work settings' ``work-settings/audit.jsonl`` (lina#256, one line per revision), by line count;
  ``kind`` is ``revised``, ``rolled_back`` or ``outside`` and ``id`` the revision (``r42``).

Each socket polls with one connection per store on its own single-thread executor (the kanban plugin's tailer
pattern), at most every ``BATCH_SECONDS``. No ``since``: the first message is the tail with no events. A ``since``
that does not decode, points past what a store holds, was taken from another store (each position travels with its
store's identity, see the identities in ``Stream``) or has rows missing right after it, gets one ``resync``. Events name
what changed (``source``, ``kind``, ``id``, ``at``) and carry no item bodies; the client refetches the snapshot.

Each source starts on its own: a store that cannot be read when the cursor is taken (the Postgres work store down)
keeps position 0 with the identity ``UNKNOWN`` while the others stream; once it reads, its position is taken at its
tail and the socket sends one ``resync``, since what it wrote meanwhile is not known.
"""

from __future__ import annotations

import asyncio
import base64
import datetime
import hashlib
import json
import logging
import sqlite3
import time
from concurrent.futures import ThreadPoolExecutor
from contextlib import suppress
from pathlib import Path
from typing import Any, NamedTuple

from fastapi import WebSocket, WebSocketDisconnect, status

from hermes_cli.lina_board import _WORK_LOCK, _root, _worker_base

BATCH_SECONDS = 0.3
PAGE = 200
CURSOR_PREFIX = "c3."  # c1 had positions only, c2 no settings source: an old cursor gets one resync
UNKNOWN = "?"  # the identity of a source that could not be read when its position was taken
_FAILS = (OSError, sqlite3.Error, RuntimeError)
# Receipts that change nothing the board shows of a run (``worker/board.py``: state and reason from ``work_state``,
# host, startedAt, endedAt, the now line, the verdict). Every other kind is streamed, kinds added later included.
WORKER_NOISE_KINDS = {
    "card_linked": "a kanban card run's link: board.cards leaves card runs out, the kanban stream covers the card",
    "card_link_failed": "the same link failing: nothing on the board changes",
    "card_noticed": "the end written on the kanban card: the kanban stream carries that comment",
    "card_notice_failed": "the same card note failing: nothing on the board changes",
    "card_notice_waiting": "the card is not ready for the wake yet: nothing on the board changes",
    "card_wake_expired": "the wake given up on the kanban card: the kanban stream carries its comment",
    "award": "the bid winner, recorded just before start_on: the placed receipt that follows carries host and state",
    "publish_failed_resumes": "the job stays placing, as placed already said; the resumed placement ends in run_started",
    "fetch_failed": "the result fetch for the end notice failed: state and delivery come as exec_end and delivery",
    "group_notified": "keyed by a group, not a run; each member's own exec_end was streamed",
    "cancel_requested": "only the request (cancel_why, not on the board); the cancel itself comes as exec_end",
    "steer": "the instruction reached the running run; the job is unchanged and the now line comes from the runner's "
             "live view, not from this receipt",
}
log = logging.getLogger(__name__)


def streamed(receipt: dict) -> bool:
    """Whether one hub receipt goes to the app: every kind except the noise above, kinds unknown here included (a
    dropped change is a stale screen). ``notified`` changes the board only for ``state="human"`` (the run waits for
    a person); any other ``notified`` is an end notice whose change came as its ``exec_end``."""
    kind = receipt.get("kind")
    if kind == "notified":
        return receipt.get("state") == "human"
    return kind not in WORKER_NOISE_KINDS


class _Pruned(Exception):
    """Kanban rows after the position were deleted: the client must resync."""


def _contiguous(since: int, last: int, count: int) -> bool:
    """No ``task_events`` row after *since* up to *last* is gone, given *count* rows kept there. The ids are
    AUTOINCREMENT and never reused, so every id was written once: fewer rows than ids means ``gc_events`` pruned
    some (it removes a done task's old events while an older event of an active task stays, so the lowest id does
    not move). The one rule for a reconnect (``check``, a range count) and a live page (``fetch``, the ids read)."""
    return count == last - since


def _try(name: str, fn, default):
    try:
        return fn()
    except _FAILS as exc:
        log.warning("lina events: %s source failed: %s", name, exc)
        return default


class Cursor(NamedTuple):
    work: int
    worker: int
    kanban: int
    settings: int = 0
    ids: tuple[str, ...] = ("", "", "", "")  # the store identity each position was taken in ("" = empty store)


SOURCES = 4


def encode(cursor: Cursor) -> str:
    raw = ".".join([*(str(p) for p in cursor[:SOURCES]), *(i or "-" for i in cursor.ids)])
    return CURSOR_PREFIX + base64.urlsafe_b64encode(raw.encode()).decode().rstrip("=")


def decode(text: str) -> Cursor | None:
    if not text.startswith(CURSOR_PREFIX):
        return None
    body = text[len(CURSOR_PREFIX):]
    try:
        parts = base64.urlsafe_b64decode(body + "=" * (-len(body) % 4)).decode().split(".")
        values = [int(p) for p in parts[:SOURCES]]
    except ValueError:
        return None
    if len(parts) != 2 * SOURCES or any(v < 0 for v in values):
        return None
    return Cursor(*values, ids=tuple("" if i == "-" else i for i in parts[SOURCES:]))


def _digest(*parts: Any) -> str:
    return hashlib.sha256(repr(parts).encode()).hexdigest()[:16]


def _epoch(value: Any) -> int | None:
    if value in (None, ""):
        return None
    if isinstance(value, (int, float)):
        return int(value)
    try:
        return int(datetime.datetime.fromisoformat(str(value)).timestamp())
    except ValueError:
        return None


class Stream:
    """The four stores of one socket. Every method runs on the socket's own thread (connections are thread-bound)."""

    def __init__(self, root: Path):
        from hermes_cli.worker import settings
        from hermes_cli.worker.hub_store import Store
        self.root = root
        # The receipts are the worker's own state, so they come from the root the worker writes (lina#269): the
        # board's root serves the work record and the read marks, and is not where the worker's hub lives.
        self.receipts = Store(settings.root(_worker_base())).receipts
        from hermes_cli.lina_work_settings_store import Store as SettingsStore
        self.audit = SettingsStore().audit  # the work settings' own root, as their writers use (lina#256)
        self.work_db: Any = None
        self.kanban_db: sqlite3.Connection | None = None
        self.executor = ThreadPoolExecutor(max_workers=1, thread_name_prefix="lina-events")

    # ---------------------------------------------------------------- connections
    def _work(self):
        if self.work_db is None:
            from hermes_cli.work import env, ledger
            with _WORK_LOCK:  # the work package binds its config per process; open under the lock, then let go
                env.bind(env.load(self.root))
                try:
                    self.work_db = ledger.open_db()
                finally:
                    env.reset()
        return self.work_db

    def _kanban(self) -> sqlite3.Connection | None:
        if self.kanban_db is None:
            from hermes_cli.kanban_db import kanban_db_path
            path = Path(kanban_db_path())
            if not path.is_file():
                return None
            self.kanban_db = sqlite3.connect(path.resolve().as_uri() + "?mode=ro", uri=True, timeout=5)
        return self.kanban_db

    # ---------------------------------------------------------------- positions
    def _work_bounds(self) -> int:
        return int(self._work().execute("SELECT COALESCE(max(id), 0) FROM event").fetchone()[0])

    def _worker_size(self) -> int:
        return self.receipts.stat().st_size if self.receipts.is_file() else 0

    def _kanban_top(self) -> int:
        db = self._kanban()
        return 0 if db is None else int(db.execute("SELECT COALESCE(max(id), 0) FROM task_events").fetchone()[0])

    # ---------------------------------------------------------------- identities
    # One identity per store, so a position is never read in another store that grew past it ("" = empty):
    # - work: the first event row (id, at). The table is append-only (nothing deletes from ``event``), so the first
    #   row stays while the store lives, and a new store starts with new rows. The store has no id of its own
    #   (``setting`` holds fixed defaults only).
    # - worker: the receipts file's inode and its first line (a recreated or rotated file differs).
    # - kanban: the database file's inode and the first task inserted (id, created_at; by rowid, since created_at
    #   ties within a second and task ids are random). Not the first ``task_events`` row, which pruning moves.
    #   Deleting that task changes the identity too: one spare resync, never a miss.
    def _work_id(self) -> str:
        first = self._work().execute("SELECT id, at FROM event ORDER BY id LIMIT 1").fetchone()
        return _digest(int(first[0]), str(first[1])) if first else ""

    def _worker_id(self) -> str:
        if not self.receipts.is_file():
            return ""
        with self.receipts.open("rb") as fh:
            line = fh.readline()
        return _digest(self.receipts.stat().st_ino, line) if line else ""

    def _kanban_id(self) -> str:
        db = self._kanban()
        if db is None:
            return ""
        oldest = db.execute("SELECT id, created_at FROM tasks ORDER BY rowid LIMIT 1").fetchone()
        from hermes_cli.kanban_db import kanban_db_path
        return _digest(Path(kanban_db_path()).stat().st_ino, *oldest) if oldest else ""

    def _sources(self) -> tuple:
        """Per source: name, position now, identity, events after a position, and whether a position is kept."""
        return (("work", self._work_bounds, self._work_id, self._work_events, self._work_kept),
                ("worker", self._worker_size, self._worker_id, self._worker_events, self._worker_kept),
                ("kanban", self._kanban_top, self._kanban_id, self._kanban_events, self._kanban_kept),
                ("settings", self._settings_count, self._settings_id, self._settings_events, self._settings_kept))

    # ---------------------------------------------------------------- settings (lina#256)
    def _settings_lines(self) -> list[bytes]:
        if not self.audit.is_file():
            return []
        data = self.audit.read_bytes()
        return data[:data.rfind(b"\n") + 1].splitlines()  # a line still being written waits for the next poll

    def _settings_count(self) -> int:
        return len(self._settings_lines())

    def _settings_id(self) -> str:
        lines = self._settings_lines()
        return _digest(self.audit.stat().st_ino, lines[0]) if lines else ""

    def _settings_kept(self, position: int) -> bool:
        return position <= self._settings_count()

    def _settings_events(self, after: int) -> tuple[int, list[dict]]:
        from hermes_cli.lina_work_settings_store import EVENT_KIND
        lines = self._settings_lines()
        out = []
        for line in lines[after:]:
            try:
                rec = json.loads(line)
            except ValueError:
                continue
            if isinstance(rec, dict) and rec.get("rev"):
                kind = EVENT_KIND.get((rec.get("by") or {}).get("kind"), "revised")
                out.append({"source": "settings", "kind": kind, "id": rec["rev"], "at": _epoch(rec.get("at"))})
        return max(after, len(lines)), out

    def tail(self) -> Cursor:
        """Each source's tail, taken on its own: one that cannot be read gets position 0 and ``UNKNOWN``."""
        taken = [_try(name, lambda at=at, ident=ident: (at(), ident()), (0, UNKNOWN))
                 for name, at, ident, _, _ in self._sources()]
        return Cursor(*(p for p, _ in taken), ids=tuple(i for _, i in taken))

    def check(self, since: Cursor) -> Cursor | None:
        """*since* if the stores still hold everything after it (None: resync). A source that cannot be read now is
        not judged: it becomes ``UNKNOWN`` and is taken again, with one resync, once it reads."""
        ids = list(since.ids)
        for i, (name, _, ident, _, kept) in enumerate(self._sources()):
            if ids[i] == UNKNOWN:
                continue
            try:
                if since[i] and ids[i] != ident():  # a position 0 replays the whole store, whichever it is
                    return None
                if not kept(since[i]):
                    return None
            except _FAILS as exc:
                log.warning("lina events: %s source failed: %s", name, exc)
                ids[i] = UNKNOWN
        return since._replace(ids=tuple(ids))

    def _work_kept(self, position: int) -> bool:
        return position <= self._work_bounds()

    def _worker_kept(self, position: int) -> bool:
        """Not past the file's end, and on a line boundary."""
        if position > self._worker_size():
            return False
        if position:
            with self.receipts.open("rb") as fh:
                fh.seek(position - 1)
                return fh.read(1) == b"\n"
        return True

    def _kanban_kept(self, position: int) -> bool:
        top = self._kanban_top()
        if position >= top:
            return position == top
        kept = self._kanban().execute("SELECT count(*) FROM task_events WHERE id > ? AND id <= ?", (position, top))
        return _contiguous(position, top, int(kept.fetchone()[0]))

    # ---------------------------------------------------------------- reads
    def _work_events(self, after: int) -> tuple[int, list[dict]]:
        rows = self._work().execute("SELECT id, kind, COALESCE(area_id, goal_id, todo_id, run_id), at FROM event "
                                    f"WHERE id > ? ORDER BY id LIMIT {PAGE}", (after,)).fetchall()
        return (int(rows[-1][0]) if rows else after,
                [{"source": "work", "kind": kind, "id": item, "at": _epoch(at)} for _, kind, item, at in rows])

    def _worker_events(self, after: int) -> tuple[int, list[dict]]:
        if not self.receipts.is_file():
            return after, []
        with self.receipts.open("rb") as fh:
            fh.seek(after)
            data = fh.read()
        end = data.rfind(b"\n") + 1  # a line still being written waits for the next poll
        out = []
        for line in data[:end].splitlines():
            try:
                rec = json.loads(line)
            except ValueError:
                continue
            if isinstance(rec, dict) and rec.get("kind") and rec.get("task") and streamed(rec):
                out.append({"source": "worker", "kind": rec["kind"], "id": rec["task"], "at": _epoch(rec.get("ts"))})
        return after + end, out

    def _kanban_events(self, after: int) -> tuple[int, list[dict]]:
        db = self._kanban()
        if db is None:
            return after, []
        rows = db.execute(f"SELECT id, kind, task_id, created_at FROM task_events WHERE id > ? ORDER BY id LIMIT {PAGE}",
                          (after,)).fetchall()
        if rows and not _contiguous(after, int(rows[-1][0]), len(rows)):
            raise _Pruned(after)
        return (int(rows[-1][0]) if rows else after,
                [{"source": "kanban", "kind": kind, "id": task, "at": _epoch(at)} for _, kind, task, at in rows])

    def fetch(self, since: Cursor) -> tuple[Cursor, list[dict], bool]:
        """What each store holds after *since*, and whether the client must resync. A store that fails to read keeps
        its position and is tried again; an ``UNKNOWN`` one that reads now is taken at its tail, and that is a
        resync (the batch's events are dropped: the refetched snapshot has them). So is a kanban page with ids
        missing after the position (``_contiguous``): the source is taken at its tail and nothing past the gap is
        sent as if nothing was lost."""
        positions, ids, events, resync = list(since[:SOURCES]), list(since.ids), [], False
        for i, (name, at, ident, read, _) in enumerate(self._sources()):
            try:
                if ids[i] == UNKNOWN:
                    positions[i], ids[i] = at(), ident()
                    resync = True
                    continue
                after, found = read(positions[i])
            except _Pruned:
                positions[i], ids[i] = _try(name, lambda: (at(), ident()), (positions[i], ids[i]))
                resync = True
                continue
            except _FAILS as exc:
                log.warning("lina events: %s source failed: %s", name, exc)
                continue
            events += found
            moved, positions[i] = after != positions[i], after
            if moved or not ids[i]:  # a store that just got its first rows has its identity now
                ids[i] = _try(name, ident, ids[i])
        return Cursor(*positions, ids=tuple(ids)), [] if resync else events, resync

    def close(self) -> None:
        for db in (self.work_db, self.kanban_db):
            if db is not None:
                with suppress(Exception):
                    db.close()
        self.work_db = self.kanban_db = None

    async def run(self, fn, *args):
        return await asyncio.get_running_loop().run_in_executor(self.executor, fn, *args)

    async def shutdown(self) -> None:
        try:
            await self.run(self.close)
        finally:
            self.executor.shutdown(wait=True, cancel_futures=True)


async def _first(ws: WebSocket, stream: Stream) -> Cursor:
    """The first message: the tail (no ``since``), the missed events (a kept ``since``) or one resync."""
    raw = ws.query_params.get("since")
    if raw is None or not raw.strip():
        cursor = await stream.run(stream.tail)
        await ws.send_json({"cursor": encode(cursor), "events": []})
        return cursor
    since = decode(raw.strip())
    checked = None if since is None else await stream.run(stream.check, since)
    if checked is None:
        cursor = await stream.run(stream.tail)
        await ws.send_json({"cursor": encode(cursor), "resync": True})
        return cursor
    cursor, events, resync = await stream.run(stream.fetch, checked)
    await ws.send_json({"cursor": encode(cursor), "resync": True} if resync else
                       {"cursor": encode(cursor), "events": events})
    return cursor


async def _wait(ws: WebSocket, seconds: float) -> bool:
    """Wait out one batch interval while watching for a disconnect; other client messages are ignored."""
    deadline = time.monotonic() + seconds
    while (left := deadline - time.monotonic()) > 0:
        try:
            msg = await asyncio.wait_for(ws.receive(), timeout=left)
        except TimeoutError:
            return True
        if msg["type"] == "websocket.disconnect":
            return False
    return True


async def serve(ws: WebSocket) -> None:
    from hermes_cli.web_server_chat import _ws_auth_ok
    if not _ws_auth_ok(ws):
        await ws.close(code=status.WS_1008_POLICY_VIOLATION)
        return
    await ws.accept()
    stream = Stream(_root())
    try:
        cursor = await _first(ws, stream)
        while await _wait(ws, BATCH_SECONDS):
            cursor, events, resync = await stream.run(stream.fetch, cursor)
            if resync:
                await ws.send_json({"cursor": encode(cursor), "resync": True})
            elif events:
                await ws.send_json({"cursor": encode(cursor), "events": events})
    except (WebSocketDisconnect, asyncio.CancelledError):
        return
    except Exception:  # one socket's failure must never take the dashboard worker down
        log.exception("lina events stream error")
        with suppress(Exception):
            await ws.close()
    finally:
        await stream.shutdown()
