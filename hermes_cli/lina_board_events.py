"""One change stream for the app (lina#150): ``GET /api/lina/events`` upgraded to a WebSocket.

Eight append-only stores are tailed, each by its own position, and the eight positions travel as one opaque cursor:

- ``work``: the work record's ``event`` table (one row per record line and run hand-off), by event id.
- ``worker``: ``lina worker``'s ``receipts.jsonl`` (the hub appends a receipt on every run state change, next to the
  job file it rewrites), by byte offset; every receipt is sent except the few that change nothing on the board
  (``WORKER_NOISE_KINDS``): a dropped change leaves a stale screen, an extra event costs one refetch.
- ``kanban``: the board's ``task_events`` table, by row id, read-only (the plugin's ``/events`` source).
- ``settings``: the work settings' ``work-settings/audit.jsonl`` (lina#256, one line per revision), by line count;
  ``kind`` is ``revised``, ``rolled_back`` or ``outside`` and ``id`` the revision (``r42``).
- ``talk``: ``board/talk.jsonl`` (lina#742), by byte offset: ``kind`` ``message``, ``id`` the agent and
  ``message_id`` the conversation's last message (lina#753, ``lina_talk.message_id``), one line per turn of that
  agent's app conversation, written once the turn reads back through ``GET
  /api/lina/agents/<agent>/talk`` (``lina_talk._relay``, after the gateway's last frame). Only turns the app sends
  through this web server are announced; a turn the gateway writes on its own (a kanban wake posted into the app
  conversation, another API client) shows on the next history read.
- ``notices``: ``board/notices.jsonl`` (lina#742), by byte offset: ``kind`` ``read`` and ``id`` the owner, one line
  per change of the owner's read marks (``lina_board_write.mark_read``); the client refetches the bell.
- ``reports``: ``board/reports.jsonl`` (lina#263), by byte offset: ``kind`` ``created`` or ``updated`` and ``id`` the
  report, one line per version the ``report`` tool writes (``tools/report_tool.py``, from the agent's process); an
  open report viewer refetches ``GET /api/lina/reports/<id>``.
- ``cards``: ``board/cards.jsonl``, by byte offset, sent as ``source`` ``kanban`` with ``id`` ``hermes:<card>``: the
  connected Hermes board's cards (``lina_board_hermes``), which no ``task_events`` row carries. One line per card a
  board pass finds changed since the last good pass (``kind`` ``added``, ``changed`` or ``removed``; the first pass of a
  reader only takes its baseline) and per card line the board lines writer stores changed (``kind`` ``line``,
  ``lina_board_plain.write``). It is the one journal its writer cuts: past ``CUT_BYTES`` it is moved aside to
  ``cards.jsonl.1``, and a socket whose position was taken in the file moved aside resyncs: every read of a
  journal takes the file's identity from the handle it reads, never from a second open (``_Lines``).

Not on the stream, on purpose: device status, quota and usage. They change by themselves all the time and the app
reads them when a screen opens; do not add a poll for them in the app or a source for them here.

Each socket polls with one connection per store on its own single-thread executor (the kanban plugin's tailer
pattern), at most every ``BATCH_SECONDS``; with nothing to send for ``KEEPALIVE_SECONDS`` it sends an empty batch at its
cursor, so no idle limit on the way closes it. No ``since``: the first message is the tail with no events. A ``since``
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
import os
import sqlite3
import threading
import time
from concurrent.futures import ThreadPoolExecutor
from contextlib import suppress
from pathlib import Path
from typing import Any, NamedTuple

from fastapi import WebSocket, WebSocketDisconnect, status

from hermes_cli.lina_board import _WORK_LOCK, _root, _worker_base

BATCH_SECONDS = 0.3
# A stream with nothing to say sends an empty batch at its cursor this often (lina#692): a silent socket is closed by
# an idle limit on the way (a front proxy, the client's own read timeout), and uvicorn sends no ping on a loopback
# bind. Shorter than the 5 s the app's reconnects were seen at, less its 1 s first backoff.
KEEPALIVE_SECONDS = 3.0
PAGE = 200
# c1 had positions only, c2 no settings source, c3 no talk or notices, c4 no reports, c5 no cards: resync
CURSOR_PREFIX = "c6."
JOURNALS = ("talk", "notices", "reports", "cards")  # the sources whose writers append through announce
SENT_AS = {"cards": "kanban"}  # a journal whose events go out under another source; the others under their own
CUT = frozenset({"cards"})  # the journals announce cuts: written on every board pass, they would grow without end
CUT_BYTES = 1 << 20  # about 16,000 lines of a card event
_CUT_LOCK = threading.Lock()
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
    talk: int = 0
    notices: int = 0
    reports: int = 0
    cards: int = 0
    ids: tuple[str, ...] = ("",) * 8  # the store identity each position was taken in ("" = empty store)


SOURCES = 8


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


def journal(root: Path, source: str) -> Path:
    return root / "board" / f"{source}.jsonl"


def announce(root: Path, source: str, kind: str, item: str, *, message_id: str | None = None) -> None:
    """One line on *source*'s journal (one of ``JOURNALS``): what changed, by id only, once the change reads back;
    *message_id* for talk. A failed append is logged and the write it announces stands: other devices see it on
    their next refetch."""
    path = journal(root, source)
    rec = {"kind": kind, "id": item, **({"message_id": message_id} if message_id else {}), "at": int(time.time())}
    line = json.dumps(rec, ensure_ascii=False) + "\n"
    try:
        path.parent.mkdir(parents=True, exist_ok=True)
        if source in CUT:
            _cut(path)
        with path.open("a", encoding="utf-8") as fh:  # one short line: one append, never interleaved
            fh.write(line)
    except OSError as exc:
        log.warning("lina events: the %s journal was not written: %s", source, exc)


def _cut(path: Path) -> None:
    """Moves a journal of ``CUT`` aside once it holds ``CUT_BYTES``; the next line starts a new file (a new identity,
    so a reconnecting cursor resyncs, and a live one too, ``_Lines.events``). Only the last cut file is kept."""
    with _CUT_LOCK:
        if path.is_file() and path.stat().st_size >= CUT_BYTES:
            os.replace(path, path.with_name(path.name + ".1"))


def _worker_event(rec: dict) -> dict | None:
    if rec.get("kind") and rec.get("task") and streamed(rec):
        return {"source": "worker", "kind": rec["kind"], "id": rec["task"], "at": _epoch(rec.get("ts"))}
    return None


class _Lines:
    """An append-only JSON-lines file tailed by byte offset (the worker's receipts, the journals); *event* turns one
    record into the event sent, None for one that is not sent. Identity: the file's inode and its first line (a
    recreated or rotated file differs). The file can be moved aside and replaced at any moment (``_cut``), so each
    method opens it once and takes its size, its identity and its lines from that one handle: a position and the
    identity it travels with always come from the same file."""

    def __init__(self, path: Path, event):
        self.path, self.event = path, event

    def _open(self):
        try:
            return self.path.open("rb")
        except FileNotFoundError:
            return None

    @staticmethod
    def _state(fh) -> tuple[int, str]:
        """The size and identity of the file behind *fh* ("" while it holds no line)."""
        stat = os.fstat(fh.fileno())
        fh.seek(0)
        line = fh.readline()
        return stat.st_size, _digest(stat.st_ino, line) if line else ""

    def snap(self) -> tuple[int, str]:
        """The file's end and identity (``0, ""`` when there is no file)."""
        fh = self._open()
        if fh is None:
            return 0, ""
        with fh:
            return self._state(fh)

    def holds(self, position: int, known: str) -> bool:
        """*position*, taken in the file of identity *known*, is in this file, not past its end, on a line boundary."""
        fh = self._open()
        if fh is None:
            return not position
        with fh:
            size, ident = self._state(fh)
            if (position and ident != known) or position > size:
                return False
            if position:
                fh.seek(position - 1)
                return fh.read(1) == b"\n"
            return True

    def events(self, after: int, known: str) -> tuple[int, list[dict], str]:
        """The events after *after*, the position past them and the file's identity, all from one handle. A position
        taken in another file (*known* differs) is ``_Pruned``: the file it was taken in is gone."""
        fh = self._open()
        if fh is None:
            if after:
                raise _Pruned(after)
            return after, [], ""
        with fh:
            _, ident = self._state(fh)
            if after and ident != known:
                raise _Pruned(after)
            fh.seek(after)
            data = fh.read()
        end = data.rfind(b"\n") + 1  # a line still being written waits for the next poll
        out = []
        for line in data[:end].splitlines():
            try:
                rec = json.loads(line)
            except ValueError:
                continue
            found = self.event(rec) if isinstance(rec, dict) else None
            if found:
                out.append(found)
        return after + end, out, ident


class _Rows:
    """A store read through separate calls (the work record, the kanban board, the work settings): nothing moves it
    aside between them, so its position and identity are read one after the other, in the shape of ``_Lines``. Its
    identity is read again only when the position moved or none was known."""

    def __init__(self, name: str, at, ident, read, kept):
        self.name, self.at, self.ident, self.read, self.kept = name, at, ident, read, kept

    def snap(self) -> tuple[int, str]:
        return self.at(), self.ident()

    def holds(self, position: int, known: str) -> bool:
        return (not position or known == self.ident()) and self.kept(position)

    def events(self, after: int, known: str) -> tuple[int, list[dict], str]:
        moved, found = self.read(after)
        return moved, found, _try(self.name, self.ident, known) if moved != after or not known else known


def _journal_event(source: str):
    def event(rec: dict) -> dict | None:
        if rec.get("kind") and rec.get("id"):
            extra = {"message_id": rec["message_id"]} if rec.get("message_id") else {}
            return {"source": source, "kind": rec["kind"], "id": rec["id"], **extra, "at": _epoch(rec.get("at"))}
        return None
    return event


class Stream:
    """The eight stores of one socket. Every method runs on the socket's own thread (connections are thread-bound)."""

    def __init__(self, root: Path):
        from hermes_cli.worker import settings
        from hermes_cli.worker.hub_store import Store
        self.root = root
        # The receipts are the worker's own state, so they come from the root the worker writes (lina#269): the
        # board's root serves the work record and the read marks, and is not where the worker's hub lives.
        self.receipts = _Lines(Store(settings.root(_worker_base())).receipts, _worker_event)
        self.journals = {source: _Lines(journal(root, source), _journal_event(SENT_AS.get(source, source)))
                         for source in JOURNALS}
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

    def _kanban_top(self) -> int:
        db = self._kanban()
        return 0 if db is None else int(db.execute("SELECT COALESCE(max(id), 0) FROM task_events").fetchone()[0])

    # ---------------------------------------------------------------- identities
    # One identity per store, so a position is never read in another store that grew past it ("" = empty):
    # - work: the first event row (id, at). The table is append-only (nothing deletes from ``event``), so the first
    #   row stays while the store lives, and a new store starts with new rows. The store has no id of its own
    #   (``setting`` holds fixed defaults only).
    # - worker, talk, notices, reports, cards: the file's inode and its first line, read with the position from one
    #   handle (``_Lines``).
    # - kanban: the database file's inode and the first task inserted (id, created_at; by rowid, since created_at
    #   ties within a second and task ids are random). Not the first ``task_events`` row, which pruning moves.
    #   Deleting that task changes the identity too: one spare resync, never a miss.
    def _work_id(self) -> str:
        first = self._work().execute("SELECT id, at FROM event ORDER BY id LIMIT 1").fetchone()
        return _digest(int(first[0]), str(first[1])) if first else ""

    def _kanban_id(self) -> str:
        db = self._kanban()
        if db is None:
            return ""
        oldest = db.execute("SELECT id, created_at FROM tasks ORDER BY rowid LIMIT 1").fetchone()
        from hermes_cli.kanban_db import kanban_db_path
        return _digest(Path(kanban_db_path()).stat().st_ino, *oldest) if oldest else ""

    def _sources(self) -> tuple:
        """Per source: its name and its store, which gives its position with its identity (``snap``), the events after a
        position with the identity they were read in (``events``) and whether a position is still held (``holds``)."""
        return (("work", _Rows("work", self._work_bounds, self._work_id, self._work_events, self._work_kept)),
                ("worker", self.receipts),
                ("kanban", _Rows("kanban", self._kanban_top, self._kanban_id, self._kanban_events, self._kanban_kept)),
                ("settings", _Rows("settings", self._settings_count, self._settings_id, self._settings_events,
                                   self._settings_kept)),
                *self.journals.items())

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
        taken = [_try(name, store.snap, (0, UNKNOWN)) for name, store in self._sources()]
        return Cursor(*(p for p, _ in taken), ids=tuple(i for _, i in taken))

    def check(self, since: Cursor) -> Cursor | None:
        """*since* if the stores still hold everything after it (None: resync). A source that cannot be read now is
        not judged: it becomes ``UNKNOWN`` and is taken again, with one resync, once it reads."""
        ids = list(since.ids)
        for i, (name, store) in enumerate(self._sources()):
            if ids[i] == UNKNOWN:
                continue
            try:
                if not store.holds(since[i], ids[i]):  # a position 0 replays the whole store, whichever it is
                    return None
            except _FAILS as exc:
                log.warning("lina events: %s source failed: %s", name, exc)
                ids[i] = UNKNOWN
        return since._replace(ids=tuple(ids))

    def _work_kept(self, position: int) -> bool:
        return position <= self._work_bounds()

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
        missing after the position (``_contiguous``), or a journal that is no longer the file its position was taken in
        (a cut, ``_Lines.events``): the source is taken at its tail and nothing past the gap is sent as if nothing was
        lost. Each position travels with the identity it was read in, so no cut can pair one file's offset with
        another's identity."""
        positions, ids, events, resync = list(since[:SOURCES]), list(since.ids), [], False
        for i, (name, store) in enumerate(self._sources()):
            try:
                if ids[i] == UNKNOWN:
                    positions[i], ids[i] = store.snap()
                    resync = True
                    continue
                positions[i], found, ids[i] = store.events(positions[i], ids[i])
            except _Pruned:
                positions[i], ids[i] = _try(name, store.snap, (positions[i], ids[i]))
                resync = True
                continue
            except _FAILS as exc:
                log.warning("lina events: %s source failed: %s", name, exc)
                continue
            events += found
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
        sent = time.monotonic()
        while await _wait(ws, BATCH_SECONDS):
            cursor, events, resync = await stream.run(stream.fetch, cursor)
            if resync:
                await ws.send_json({"cursor": encode(cursor), "resync": True})
            elif events or time.monotonic() - sent >= KEEPALIVE_SECONDS:
                await ws.send_json({"cursor": encode(cursor), "events": events})
            else:
                continue
            sent = time.monotonic()
    except (WebSocketDisconnect, asyncio.CancelledError):
        return
    except Exception:  # one socket's failure must never take the dashboard worker down
        log.exception("lina events stream error")
        with suppress(Exception):
            await ws.close()
    finally:
        await stream.shutdown()
