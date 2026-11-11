"""A read-only ``hermes`` board source (lina#481): the owner's agents' work cards on a Hermes Agent kanban board and
their goals and to-dos in the old work record, shown in the app until lina#185 moves those agents.

LINA's own readers cannot be pointed at those files: the kanban ``connect()`` adds the ``goal_gates`` column and the
``work_card_outcomes`` table on first connect, and the work ledger applies its schema and seed rows when it opens a
store, so either would change a database the agents write. Every read here runs the configured command instead
(``<command> kanban [--board <b>] list --json``, ``... show <id> --json`` and ``<work> json``), the rule
``worker/kanban_link.py`` keeps for writes: no LINA code opens a Hermes file in this mode. ``board.hermes`` of the LINA
root's ``config.yaml``::

    board:
      hermes:
        command: [/path/to/hermes]   # the Hermes Agent CLI; without it the cards part is off
        board: <slug>                # optional; the Hermes CLI's current board when unset
        work: [/path/to/work]        # the old work record's CLI; without it the record part is off
        home: /path/to/hermes-home   # optional; HERMES_HOME for both commands
        timeout_seconds: 10          # one command's limit

Unset, nothing runs. The commands never run in a request (lina#533): the board takes ``list`` plus one ``show`` per
card, minutes on a big board, so a background reader (:class:`_Reader`) runs one pass at a time, when the server starts,
:data:`PASS_EVERY_S` after the last pass, and as soon as ``kanban watch`` prints an event, each pass bounded by
:data:`PASS_LIMIT_S`. A request serves the last good pass with its time (``hermesAt``); before the first one ends, or
while the passes fail, it adds one ``errors`` entry. ``show`` runs only for cards not done and cards done within the
app's done window (``lina_board.DONE_WINDOW_S``); every other done card is its ``list`` row alone, marked
``detail: false``. Nothing is written to disk. Items from this source carry ids prefixed ``hermes:`` and
``readOnly: true``; LINA's write routes refuse such ids with 409 (:data:`READ_ONLY`).
"""

from __future__ import annotations

import dataclasses
import json
import logging
import shlex
import subprocess
import threading
import time
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from typing import Any, Callable

PREFIX = "hermes:"
SOURCE = "hermes"
DEFAULT_TIMEOUT_S = 10.0
SHOW_WORKERS = 4
PASS_EVERY_S = 15.0
PASS_LIMIT_S = 120.0
WATCH_POLL_S = 2.0
FIRST_PASS = "the first read of the Hermes board has not ended yet"
READ_ONLY = "read only: this item is on the Hermes board or in the old work record; change it there"
logger = logging.getLogger(__name__)


class SourceError(RuntimeError):
    """The source is set wrong, or its command failed, timed out or printed something that is not the JSON expected."""


@dataclasses.dataclass(frozen=True)
class Source:
    command: tuple[str, ...] | None
    board: str | None
    work: tuple[str, ...] | None
    home: str | None
    timeout: float


def is_hermes(item_id: object) -> bool:
    """An id this source served (the write routes upper-case ids, so the prefix is matched in any case)."""
    return str(item_id or "").strip().lower().startswith(PREFIX)


def hermes_id(raw: object) -> str:
    return f"{PREFIX}{raw}"


def raw_id(item_id: str) -> str:
    return item_id.strip()[len(PREFIX):]


# ---------------------------------------------------------------- config
def section(root: Path | None = None) -> dict | None:
    """``board.hermes`` of the LINA root's config; None when it is unset or empty (the source is off)."""
    from hermes_cli.config_backend import config_exists, read_config_doc_readonly
    if root is None:
        from hermes_cli.lina_board import _root
        root = _root()
    path = root / "config.yaml"
    doc = read_config_doc_readonly(path) if config_exists(path) else None
    board = doc.get("board") if isinstance(doc, dict) else None
    value = board.get("hermes") if isinstance(board, dict) else None
    return value if isinstance(value, dict) and value else None


def _argv(sec: dict, key: str) -> tuple[str, ...] | None:
    """An argv list from the config; LINA's own ``hermes`` is refused, since its kanban ``connect()`` would migrate
    the Hermes board it was pointed at."""
    value = sec.get(key)
    if value is None:
        return None
    if not isinstance(value, list) or not value or not all(isinstance(part, str) and part for part in value):
        raise SourceError(f"board.hermes.{key} must be an argv list, e.g. [/path/to/{key}]")
    from hermes_cli.worker.kanban_link import _in_lina_tree
    from hermes_platform.resolver import locate_command
    found = next(iter(locate_command(value[0]).command), None)
    if found and _in_lina_tree(found):
        raise SourceError(f"board.hermes.{key}[0] {value[0]!r} is this LINA's own {found}; "
                          "set the Hermes Agent CLI by absolute path")
    return (found or value[0], *value[1:])


def _seconds(sec: dict, key: str, default: float, *, positive: bool) -> float:
    value = sec.get(key, default)
    if isinstance(value, bool) or not isinstance(value, (int, float)) or value < 0 or (positive and value == 0):
        raise SourceError(f"board.hermes.{key} must be a number of seconds")
    return float(value)


def source(sec: dict) -> Source:
    """The checked settings of a set ``board.hermes``; a wrong one raises SourceError (an error of this source)."""
    board, home = sec.get("board"), sec.get("home")
    if board is not None and not (isinstance(board, str) and board.strip()):
        raise SourceError("board.hermes.board must be a board slug")
    if home is not None and not (isinstance(home, str) and home.strip()):
        raise SourceError("board.hermes.home must be a path")
    command, work = _argv(sec, "command"), _argv(sec, "work")
    if command is None and work is None:
        raise SourceError("board.hermes needs command (the Hermes Agent CLI) or work (the old record's CLI)")
    return Source(command, board.strip() if board else None, work, str(Path(home).expanduser()) if home else None,
                  _seconds(sec, "timeout_seconds", DEFAULT_TIMEOUT_S, positive=True))


# ---------------------------------------------------------------- running the commands
def _env(src: Source) -> dict[str, str]:
    """The hub's environment (LINA's ``HERMES_HOME`` and a card session's ids dropped), with the configured home."""
    from hermes_cli.worker.kanban_link import hub_env
    env = hub_env()
    if src.home:
        env["HERMES_HOME"] = src.home
    return env


@dataclasses.dataclass(frozen=True)
class _Budget:
    """One pass's limit: :data:`PASS_LIMIT_S` from its start, and its reader still running."""
    deadline: float
    stopped: threading.Event

    def left(self) -> float:
        if self.stopped.is_set():
            raise SourceError("the Hermes board reader stopped")
        left = self.deadline - time.monotonic()
        if left <= 0:
            raise SourceError(f"the read of the Hermes board passed its {PASS_LIMIT_S:g}s limit")
        return left


def _call(src: Source, argv: tuple[str, ...], budget: _Budget) -> Any:
    shown = shlex.join(argv)
    timeout = min(src.timeout, budget.left())
    try:
        done = subprocess.run(list(argv), capture_output=True, text=True, encoding="utf-8", errors="replace",
                              timeout=timeout, env=_env(src), stdin=subprocess.DEVNULL, check=False)
    except subprocess.TimeoutExpired as exc:
        raise SourceError(f"{shown} timed out after {timeout:g}s") from exc
    except OSError as exc:
        raise SourceError(f"{shown}: {exc}") from exc
    if done.returncode:
        last = (done.stderr.strip().splitlines() or [""])[-1]
        raise SourceError(f"{shown} exited {done.returncode}: {last}")
    try:
        return json.loads(done.stdout)
    except ValueError as exc:
        raise SourceError(f"{shown} did not print JSON") from exc


def _kanban_argv(src: Source, verb: str, *args: str) -> tuple[str, ...]:
    if src.command is None:
        raise SourceError("board.hermes.command is not set")
    return (*src.command, "kanban", *(("--board", src.board) if src.board else ()), verb, *args)


def card_list(src: Source, budget: _Budget) -> list[dict]:
    """``kanban list --json``: the board's cards (archived ones are not listed)."""
    rows = _call(src, _kanban_argv(src, "list", "--json"), budget)
    if not isinstance(rows, list) or not all(isinstance(r, dict) and r.get("id") for r in rows):
        raise SourceError("kanban list --json is not a list of cards")
    return [r for r in rows if r.get("status") != "archived"]


def card_show(src: Source, raw: str, budget: _Budget) -> dict:
    """``kanban show <id> --json``: the card with its comments, events, runs and links."""
    reply = _call(src, _kanban_argv(src, "show", raw, "--json"), budget)
    if not isinstance(reply, dict) or not isinstance(reply.get("task"), dict):
        raise SourceError(f"kanban show {raw} --json is not a card")
    return reply


def _shown(row: dict, now: float) -> bool:
    """A card whose detail the app shows: not done, or done within the app's done window."""
    from hermes_cli.kanban_db import _to_epoch
    from hermes_cli.lina_board import DONE_WINDOW_S
    if row.get("status") != "done":
        return True
    ended = _to_epoch(row.get("completed_at"))
    return ended is not None and now - ended <= DONE_WINDOW_S


def _listed_only(row: dict, links: dict[str, dict[str, list[str]]]) -> dict:
    """A done card outside the window as ``show`` would give it, from its ``list`` row alone (``detail: False``). Its
    links come from the shown cards' (*links*), so a goal and its work cards keep their places; a link between two
    such cards is not known."""
    raw = str(row["id"])
    return {"task": row, "parents": links["parents"].get(raw, []), "children": links["children"].get(raw, []),
            "comments": [], "events": [], "runs": [], "detail": False}


def _links(shows: list[dict]) -> dict[str, dict[str, list[str]]]:
    """The other side of each shown card's links: ``children[x]`` lists the shown cards that name *x* a parent,
    ``parents[x]`` the shown cards that name *x* a child."""
    links: dict[str, dict[str, list[str]]] = {"children": {}, "parents": {}}
    for show in shows:
        raw = str(show["task"]["id"])
        for parent in show.get("parents") or []:
            links["children"].setdefault(str(parent), []).append(raw)
        for child in show.get("children") or []:
            links["parents"].setdefault(str(child), []).append(raw)
    return links


def read_cards(src: Source, budget: _Budget) -> list[dict]:
    """One pass's cards in ``list`` order: ``show`` of every card :func:`_shown`, a few at a time, and the list row
    alone of every other. A failing ``show`` fails the pass and cancels the ones not started."""
    rows, now = card_list(src, budget), time.time()
    wanted = [str(row["id"]) for row in rows if _shown(row, now)]
    pool = ThreadPoolExecutor(SHOW_WORKERS, thread_name_prefix="lina-hermes-show")
    try:
        shows = dict(zip(wanted, pool.map(lambda raw: card_show(src, raw, budget), wanted)))
    finally:
        pool.shutdown(cancel_futures=True)
    links = _links(list(shows.values()))
    return [shows.get(str(row["id"])) or _listed_only(row, links) for row in rows]


def read_record(src: Source, budget: _Budget) -> dict:
    """``<work> json``: the old record's items and agents."""
    if src.work is None:
        raise SourceError("board.hermes.work is not set")
    reply = _call(src, (*src.work, "json"), budget)
    if not isinstance(reply, dict) or not isinstance(reply.get("items"), list):
        raise SourceError("work json has no items list")
    return reply


# ---------------------------------------------------------------- the background reader (lina#533)
@dataclasses.dataclass(frozen=True)
class Part:
    """One part of the reader's state: the last good value (None before one), the wall time its pass ended, and the
    error of the latest pass when it did not end well (:data:`FIRST_PASS` until the first pass ends)."""
    value: Any = None
    at: float | None = None
    error: str | None = FIRST_PASS


def served(part: Part, errors: list[dict]) -> Any:
    """*part*'s value for a reply; a part not read yet, or whose latest pass failed, adds one ``errors`` entry saying
    how old the served value is."""
    if part.error is not None:
        age = "" if part.at is None else f"; serving the read of {max(0, round(time.time() - part.at))}s ago"
        errors.append({"source": SOURCE, "message": f"{part.error}{age}"[:300]})
    return part.value


class _Reader:
    """The passes of one configured source, in a thread of their own: one pass at a time, then a wait of
    :data:`PASS_EVERY_S` cut short when ``kanban watch`` (a second thread) prints an event. It ends when stopped or
    when the config no longer sets this source. Requests only read :meth:`part`."""

    def __init__(self, root: Path, src: Source):
        self.root, self.src = root, src
        self.passes = 0
        self._parts = {"cards": Part(), "record": Part()}
        self._cond = threading.Condition()
        self._wake, self._stopped = threading.Event(), threading.Event()
        self._watch: subprocess.Popen | None = None
        from agent.memory_provider import spawn_context_thread
        spawn_context_thread(self._loop, name="lina-hermes-board").start()
        if src.command is not None:
            spawn_context_thread(self._watch_loop, name="lina-hermes-watch").start()

    @property
    def stopped(self) -> bool:
        return self._stopped.is_set()

    def part(self, name: str) -> Part:
        with self._cond:
            return self._parts[name]

    def wait(self, passes: int, timeout: float) -> bool:
        """True once *passes* passes have ended (tests, and the measurement in lina#533's report)."""
        with self._cond:
            return self._cond.wait_for(lambda: self.passes >= passes, timeout)

    def stop(self) -> None:
        self._stopped.set()
        self._wake.set()
        with self._cond:
            proc = self._watch
        if proc is not None and proc.poll() is None:
            proc.terminate()
            try:
                proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                proc.kill()

    def _still_set(self) -> bool:
        from hermes_cli.config_backend import ConfigBackendUnavailable
        try:
            sec = section(self.root)
            return sec is not None and source(sec) == self.src
        except (SourceError, ConfigBackendUnavailable, OSError, ValueError):
            logger.warning("hermes board: the config could not be read; this reader stops", exc_info=True)
            return False

    def _loop(self) -> None:
        while not self.stopped and self._still_set():
            self._wake.clear()
            self._pass()
            self._wake.wait(PASS_EVERY_S)
        self.stop()

    def _pass(self) -> None:
        budget = _Budget(time.monotonic() + PASS_LIMIT_S, self._stopped)
        reads: dict[str, Callable[[], Any]] = {}
        if self.src.work is not None:
            reads["record"] = lambda: read_record(self.src, budget)
        if self.src.command is not None:
            reads["cards"] = lambda: read_cards(self.src, budget)
        with ThreadPoolExecutor(len(reads), thread_name_prefix="lina-hermes-pass") as pool:
            for name, read in reads.items():
                pool.submit(self._keep, name, read)
        with self._cond:
            self.passes += 1
            self._cond.notify_all()

    def _keep(self, name: str, read: Callable[[], Any]) -> None:
        """One part of a pass. The command's output is outside data, so any error in reading it is kept as this
        part's error (the last good value stays served) and the next pass tries again."""
        try:
            value = read()
        except Exception as exc:
            logger.warning("hermes board: the %s read failed", name, exc_info=not isinstance(exc, SourceError))
            message = str(exc) if isinstance(exc, SourceError) else f"{type(exc).__name__}: {exc}"
            with self._cond:
                old = self._parts[name]
                self._parts[name] = Part(old.value, old.at, message)
            return
        with self._cond:
            self._parts[name] = Part(value, time.time(), None)

    def _watch_loop(self) -> None:
        """``kanban watch``: every event line it prints starts the next pass now. Restarted after
        :data:`PASS_EVERY_S` when it ends; the timer alone keeps the board read meanwhile."""
        argv = list(_kanban_argv(self.src, "watch", "--interval", f"{WATCH_POLL_S:g}"))
        while not self.stopped:
            try:
                # health: allow HX006 -- kanban watch is a stream that runs until the reader stops; stop() ends and reaps it
                proc = subprocess.Popen(argv, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                                        stdin=subprocess.DEVNULL, text=True, encoding="utf-8", errors="replace",
                                        env=_env(self.src))
            except OSError:
                logger.warning("hermes board: %s did not start", shlex.join(argv), exc_info=True)
            else:
                with self._cond:
                    self._watch = proc
                if self.stopped:
                    proc.terminate()
                for line in proc.stdout or ():
                    if line.strip() and not line.startswith("Watching "):
                        self._wake.set()
                try:
                    proc.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait()
            self._stopped.wait(PASS_EVERY_S)


_READER: _Reader | None = None
_READER_LOCK = threading.Lock()


def reader(root: Path | None = None) -> _Reader | None:
    """The running reader of the set source, started on first use (:func:`start` starts it with the server); None
    when the source is off. A reader of another config, or one that ended, is stopped and replaced; a source set
    wrong stops it and raises SourceError."""
    global _READER
    if root is None:
        from hermes_cli.lina_board import _root
        root = _root()
    sec = section(root)
    try:
        src = None if sec is None else source(sec)
    except SourceError:
        stop()
        raise
    with _READER_LOCK:
        if _READER is not None and (_READER.stopped or (_READER.root, _READER.src) != (root, src)):
            _READER.stop()
            _READER = None
        if src is not None and _READER is None:
            _READER = _Reader(root, src)
        return _READER


def start() -> Callable[[], None]:
    """The server's start: the first pass begins now, not on the first request. Returns the stop for shutdown."""
    reader()
    return stop


def stop() -> None:
    global _READER
    with _READER_LOCK:
        running, _READER = _READER, None
    if running is not None:
        running.stop()


# ---------------------------------------------------------------- the old record in the board's shapes
def _record_rows(reply: dict, root: Path) -> dict:
    """``lina_board.work_read``'s shape over the old record, by the board's own rules (``work.board``), with the old
    record's owner and agents bound in place of LINA's for the read; ids are prefixed and rows marked read only."""
    from hermes_cli.lina_board import _WORK_LOCK, _item_row
    from hermes_cli.work import env
    from hermes_cli.work.board import due_groups, inbox
    from hermes_cli.work.notify import owner_today
    items = [x for x in reply["items"] if isinstance(x, dict) and x.get("id")]
    recs = {str(x["id"]): x for x in items}
    agents = {str(k): str(v) for k, v in (reply.get("bots") or {}).items()}
    owner = next((str(x["owner"]) for x in items if x.get("owner")), None)
    with _WORK_LOCK:
        mine = env.load(root)
        env.bind(dataclasses.replace(mine, owner=owner or mine.owner, agents=agents))
        try:
            today = owner_today()
            asks, todos = inbox(recs, list(recs), today)
            groups = due_groups(recs, list(recs), today)

            def row(item: dict) -> dict:
                return dict(_item_row(item, recs), id=hermes_id(item["id"]), readOnly=True)
            return {
                "agents": agents, "today": today.isoformat(),
                "decide": [{"target": hermes_id(a["item"]["id"]), "title": row(a["item"])["title"],
                            "agent": a["agent"] or None, "kind": a["kind"], "text": a["text"], "at": a["at"],
                            "answered": a["answered"], "readOnly": True} for a in asks],
                "mine": [dict(row(item), waiting=waiting, createdAt=int(item.get("created") or 0))
                         for item, waiting in todos],
                "due": {name: [row(item) for item in rows] for name, rows in groups.items()},
            }
        finally:
            env.reset()


def merge(work: dict | None, errors: list[dict], root: Path) -> dict | None:
    """LINA's own record part (``lina_board.work_read``) with the old record's rows of the last good pass added after
    its own, and that pass's time as ``hermesAt``. Off, it is returned as it is; no good pass yet, or a failing one,
    adds one ``errors`` entry (the last good rows stay served), and a failing LINA record leaves the old record's rows
    served alone."""
    from hermes_cli.lina_board import _source
    running = _source(SOURCE, errors, lambda: reader(root))
    if running is None or running.src.work is None:
        return work
    part = running.part("record")
    reply = served(part, errors)
    extra = None if reply is None else _source(SOURCE, errors, lambda: _record_rows(reply, root))
    if work is None or extra is None:
        merged = work if extra is None else extra
    else:
        merged = {**work, "agents": {**extra["agents"], **work["agents"]},
                  "decide": work["decide"] + extra["decide"], "mine": work["mine"] + extra["mine"],
                  "due": {name: rows + extra["due"].get(name, []) for name, rows in work["due"].items()}}
    return None if merged is None else {**merged, "hermesAt": part.at}


def only_hermes_agent(agent: str, root: Path) -> bool:
    """An agent the old record knows and LINA's own record does not: a quick add to it would be a write there. Read
    from the last good pass; before one, or with the source set wrong, no agent is known."""
    from hermes_cli.work import env
    try:
        running = reader(root)
    except SourceError:
        return False
    if running is None or running.src.work is None or agent in env.load(root).agents:
        return False
    reply = running.part("record").value
    return reply is not None and agent in (reply.get("bots") or {})
