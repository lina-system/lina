"""One change stream for the app (lina#150): ``GET /api/lina/events`` upgraded to a WebSocket.

Three append-only stores are tailed, each by its own position, and the three positions travel as one opaque cursor:

- ``work``: the work record's ``event`` table (one row per record line and run hand-off), by event id.
- ``worker``: ``lina worker``'s ``receipts.jsonl`` (the hub appends a receipt on every run state change, next to the
  job file it rewrites), by byte offset; only the receipts that change a run's state are sent.
- ``kanban``: the board's ``task_events`` table, by row id, read-only (the plugin's ``/events`` source).

Each socket polls with one connection per store on its own single-thread executor (the kanban plugin's tailer
pattern), at most every ``BATCH_SECONDS``. No ``since``: the first message is the tail with no events. A ``since``
that does not decode, or points past what a store holds or before what it kept, gets one ``resync``. Events name
what changed (``source``, ``kind``, ``id``, ``at``) and carry no item bodies; the client refetches the snapshot.
"""

from __future__ import annotations

import asyncio
import base64
import datetime
import json
import logging
import sqlite3
import time
from concurrent.futures import ThreadPoolExecutor
from contextlib import suppress
from pathlib import Path
from typing import Any, NamedTuple

from fastapi import WebSocket, WebSocketDisconnect, status

from hermes_cli.lina_board import _WORK_LOCK, _root

BATCH_SECONDS = 0.3
PAGE = 200
CURSOR_PREFIX = "c1."
WORKER_STATE_KINDS = frozenset({"submitted", "placed", "queued", "run_started", "start_failed_requeued", "follow_up",
                                "exec_end", "delivery", "verdict"})
log = logging.getLogger(__name__)


class Cursor(NamedTuple):
    work: int
    worker: int
    kanban: int


def encode(cursor: Cursor) -> str:
    raw = f"{cursor.work}.{cursor.worker}.{cursor.kanban}".encode()
    return CURSOR_PREFIX + base64.urlsafe_b64encode(raw).decode().rstrip("=")


def decode(text: str) -> Cursor | None:
    if not text.startswith(CURSOR_PREFIX):
        return None
    body = text[len(CURSOR_PREFIX):]
    try:
        parts = base64.urlsafe_b64decode(body + "=" * (-len(body) % 4)).decode().split(".")
        values = [int(p) for p in parts]
    except ValueError:
        return None
    if len(values) != 3 or any(v < 0 for v in values):
        return None
    return Cursor(*values)


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
    """The three stores of one socket. Every method runs on the socket's own thread (connections are thread-bound)."""

    def __init__(self, root: Path):
        from hermes_cli.worker import settings
        from hermes_cli.worker.hub_store import Store
        self.root = root
        self.receipts = Store(settings.root(root)).receipts
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

    def _kanban_bounds(self) -> tuple[int, int]:
        """(max id, lowest id still kept apart from rows the board never prunes) of ``task_events``."""
        db = self._kanban()
        if db is None:
            return 0, 0
        top, low = db.execute("SELECT COALESCE(max(id), 0), COALESCE(min(CASE WHEN kind != 'decomposed' THEN id END), 0) "
                              "FROM task_events").fetchone()
        return int(top), int(low)

    def tail(self) -> Cursor:
        return Cursor(self._work_bounds(), self._worker_size(), self._kanban_bounds()[0])

    def kept(self, since: Cursor) -> bool:
        """The stores still hold everything after *since*: no position past a store's end (another or a reset
        store), the worker offset on a line boundary, and the board has not pruned rows right after its position."""
        if since.work > self._work_bounds() or since.worker > self._worker_size():
            return False
        if since.worker:
            with self.receipts.open("rb") as fh:
                fh.seek(since.worker - 1)
                if fh.read(1) != b"\n":
                    return False
        top, low = self._kanban_bounds()
        return since.kanban <= top and not (low and since.kanban < top and since.kanban + 1 < low)

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
            if isinstance(rec, dict) and rec.get("kind") in WORKER_STATE_KINDS and rec.get("task"):
                out.append({"source": "worker", "kind": rec["kind"], "id": rec["task"], "at": _epoch(rec.get("ts"))})
        return after + end, out

    def _kanban_events(self, after: int) -> tuple[int, list[dict]]:
        db = self._kanban()
        if db is None:
            return after, []
        rows = db.execute(f"SELECT id, kind, task_id, created_at FROM task_events WHERE id > ? ORDER BY id LIMIT {PAGE}",
                          (after,)).fetchall()
        return (int(rows[-1][0]) if rows else after,
                [{"source": "kanban", "kind": kind, "id": task, "at": _epoch(at)} for _, kind, task, at in rows])

    def fetch(self, since: Cursor) -> tuple[Cursor, list[dict]]:
        """What each store holds after *since*. A store that fails to read keeps its position and is tried again."""
        positions, events = [], []
        for name, read, after in (("work", self._work_events, since.work), ("worker", self._worker_events, since.worker),
                                  ("kanban", self._kanban_events, since.kanban)):
            try:
                after, found = read(after)
                events += found
            except (OSError, sqlite3.Error, RuntimeError) as exc:
                log.warning("lina events: %s source failed: %s", name, exc)
            positions.append(after)
        return Cursor(*positions), events

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
    if since is None or not await stream.run(stream.kept, since):
        cursor = await stream.run(stream.tail)
        await ws.send_json({"cursor": encode(cursor), "resync": True})
        return cursor
    cursor, events = await stream.run(stream.fetch, since)
    await ws.send_json({"cursor": encode(cursor), "events": events})
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
            cursor, events = await stream.run(stream.fetch, cursor)
            if events:
                await ws.send_json({"cursor": encode(cursor), "events": events})
    except (WebSocketDisconnect, asyncio.CancelledError):
        return
    except Exception:  # one socket's failure must never take the dashboard worker down
        log.exception("lina events stream error")
        with suppress(Exception):
            await ws.close()
    finally:
        await stream.shutdown()
