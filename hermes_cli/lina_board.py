"""The board's data served by LINA (lina#39): what today's board snapshot holds beyond lina#112's goals, work cards,
worker runs on cards and inbox.

Sources: the work record (``lina work``: the owner's to-dos, decisions, due groups), ``lina worker`` (runs that are
not kanban work cards, and the devices of ``devices.workers``), this machine's magpie (quota). Each read
is separate, so a source that fails shows up in ``errors`` and the counts made from it are ``None``, never 0. Every
count is the length of a list in the same snapshot. Notice lines are in the work record's locale (``work.locale``).
"""

from __future__ import annotations

import contextvars
import logging
import threading
import time
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Callable

API_VERSION = 1
DONE_WINDOW_S = 86400
# The app polls /board, /devices and /quota together and each outside read is seconds of ssh and magpie calls, so one read
# answers every request for this long.
READ_TTL_S = 5
# A finished good read older than READ_TTL_S still answers at once for this long while one background read replaces
# it (lina#584); older than this, a request waits for a new read.
STALE_MAX_S = 60
_WORK_LOCK = threading.Lock()
logger = logging.getLogger(__name__)


@dataclass
class _Read:
    at: float
    value: Any = None
    failure: BaseException | None = None
    errors: list[dict] = field(default_factory=list)
    waiting: int = 0
    ended: float | None = None
    done: threading.Event = field(default_factory=threading.Event)

    def result(self):
        if self.failure is not None:
            raise self.failure
        return self.value


class _SharedReads:
    """The outside reads (the running runs' live view, the nodes' ``hello`` and ``probe``, the quota) by key, served
    stale while they revalidate (lina#584). A finished read answers every request for ``ttl`` seconds after it ends,
    a failed one too, so a device that times out is not asked again by every request in that window. A good read
    older than that answers at once for up to ``stale_max`` seconds while one background read (one per key) replaces
    it; it keeps the time it was read (``_Read.at``), never the time it is served. A request waits only when there is
    no read to serve: none yet, a failed one past ``ttl`` (a failure is never served as if it were current), or one
    past ``stale_max``; it then waits on the read already running for the key instead of starting its own. Reads past
    ``stale_max`` are dropped when a new one starts (a key names a set of running runs)."""

    def __init__(self, ttl: float, stale_max: float):
        self.ttl, self.stale_max = ttl, stale_max
        self._lock = threading.Lock()
        self._done: dict[tuple, _Read] = {}
        self._running: dict[tuple, _Read] = {}

    def get(self, key: tuple, read: Callable[[list[dict]], Any]) -> _Read:
        started = None
        with self._lock:
            done, running = self._done.get(key), self._running.get(key)
            if done is not None and self._age(done) < self.ttl:
                return done
            stale = done is not None and done.failure is None and self._age(done) < self.stale_max
            if running is None:
                for old in [k for k, r in self._done.items() if self._age(r) >= self.stale_max]:
                    del self._done[old]
                started = running = self._running[key] = _Read(at=time.time())
            elif not stale:
                running.waiting += 1
        if stale:
            if started is not None:
                threading.Thread(target=contextvars.copy_context().run, args=(self._run, key, started, read),
                                 name=f"lina-board-{key[0]}", daemon=True).start()
            return done
        if started is not None:
            return self._run(key, started, read)
        running.done.wait()
        return running

    def _run(self, key: tuple, got: _Read, read: Callable[[list[dict]], Any]) -> _Read:
        from hermes_cli.config_backend import ConfigBackendUnavailable
        try:
            got.value = read(got.errors)
        except (Exception, ConfigBackendUnavailable) as exc:  # health: allow BLE001 -- kept for every request it answers: _Read.result re-raises it into _source, which logs the traceback
            got.failure = exc
        finally:
            with self._lock:
                got.ended = time.time()
                self._done[key] = got
                if self._running.get(key) is got:
                    del self._running[key]
            got.done.set()
        return got

    @staticmethod
    def _age(read: _Read) -> float:
        age = time.time() - read.ended
        return age if age >= 0 else float("inf")

    def waiting(self, name: str) -> int:
        """How many requests wait on the running *name* read now."""
        with self._lock:
            return sum(read.waiting for key, read in self._running.items() if key[0] == name)

    def running(self, name: str) -> int:
        """How many *name* reads run now, in a request or in the background."""
        with self._lock:
            return sum(1 for key in self._running if key[0] == name)


READS = _SharedReads(READ_TTL_S, STALE_MAX_S)


def warm() -> threading.Thread:
    """One board read in the background when the web server starts (lina#584), so the first sign-in finds the shared
    reads already there. It never holds up the start, and a read that fails only logs a warning."""
    from hermes_cli.config_backend import ConfigBackendUnavailable

    def run():
        try:
            board()
        except (Exception, ConfigBackendUnavailable):
            logger.warning("board: the read at server start failed", exc_info=True)
    thread = threading.Thread(target=contextvars.copy_context().run, args=(run,), name="lina-board-warm", daemon=True)
    thread.start()
    return thread


def _root() -> Path:
    """The board's own root: the work record, the quota view and the read marks (``lina_deps.lina_root``). Worker
    state is not read from here but through :func:`_worker_base` (lina#269)."""
    from hermes_cli.lina_deps import lina_root
    return lina_root()


def _worker_base() -> Path:
    """The root ``lina worker`` writes its state under: ``worker/settings.py``'s ``lina_home`` (lina#227) — ``LINA_HOME``,
    and a ``HERMES_HOME`` of Hermes Agent's (a Hermes card session's) ignored for the platform default. The board's
    worker views and the event stream resolve it here and nowhere else, so both read the runs, devices and run
    receipts the worker itself wrote (lina#269)."""
    from hermes_cli.worker import settings
    return settings.lina_home()


def _item_row(item: dict, recs: dict) -> dict:
    from hermes_cli.work.board import agent_of
    from hermes_cli.work.evaluate import item_name
    return {"id": item["id"], "kind": "goal" if item.get("kind") == "목표" else "todo", "title": item_name(item),
            "due": item.get("due") or None, "agent": agent_of(item, recs) or None}


def work_read(root: Path) -> dict:
    """The work record's part: decisions, the owner's to-dos and the due groups, in one bound read of the record
    (the work package keeps its config and store connection per process, so reads take turns)."""
    from hermes_cli.work import env, ledger
    from hermes_cli.work.board import due_groups, inbox
    from hermes_cli.work.notify import owner_today
    with _WORK_LOCK:
        env.bind(env.load(root))
        try:
            recs, order = ledger.read_all()
            today = owner_today()
            asks, todos = inbox(recs, order, today)
            groups = due_groups(recs, order, today)
            return {
                "agents": dict(env.current().agents), "today": today.isoformat(),
                "decide": [{"target": row["item"]["id"], "title": _item_row(row["item"], recs)["title"],
                            "agent": row["agent"] or None, "kind": row["kind"], "text": row["text"], "at": row["at"],
                            "answered": row["answered"]} for row in asks],
                "mine": [dict(_item_row(item, recs), waiting=waiting, createdAt=int(item.get("created") or 0))
                         for item, waiting in todos],
                "due": {name: [_item_row(item, recs) for item in rows] for name, rows in groups.items()},
            }
        finally:
            ledger.close()
            env.reset()


def worker_read(_board_root: Path, now: float) -> list[dict]:
    """The runs ``lina worker`` holds, read from the root the worker writes (``_worker_base``, lina#269). The board
    root the callers pass is not where they are: the worker's state lives under its own root."""
    from hermes_cli.worker import settings
    from hermes_cli.worker.board import cards
    return cards(settings.root(_worker_base()), now)


def devices_read(_board_root: Path, now: float, errors: list[dict]) -> dict:
    """``{machines, person, monitored}``: the nodes by role with readiness, load and runners, the person devices and
    the machines only Beszel watches (``lina_board_devices``); a metrics reader that fails adds an error and leaves
    its fields empty. The devices and their ``devices.metrics.beszel`` path come from the worker's own config, the same root as
    :func:`worker_read` (lina#269); the board root the callers pass holds none of it."""
    from hermes_cli.lina_board_devices import view
    from hermes_cli.worker.board import beszel_path, beszel_read
    metrics = None
    path = beszel_path(_worker_base())
    if path is not None:
        metrics = _source("metrics", errors, lambda: beszel_read(path, now))
    return view(_worker_base(), metrics, now)


def quota_read(root: Path, now: float) -> list[dict]:
    from hermes_cli.lina_board_quota import read
    return read(root, now)


def _source(name: str, errors: list[dict], read: Callable[[], Any]):
    """One source's read; a failure becomes ``{source, message}`` in *errors* and the read gives None. Each source is
    a separate store, device or process (sqlite, psql, ssh, magpie) with its own error types, and one that fails must not
    take the others down, so any error is caught here and reported."""
    from hermes_cli.config_backend import ConfigBackendUnavailable
    started = time.monotonic()
    try:
        return read()
    except (Exception, ConfigBackendUnavailable) as exc:
        logger.warning("board source %s failed", name, exc_info=True)
        errors.append({"source": name, "message": f"{type(exc).__name__}: {exc}"[:300]})
        return None
    finally:
        logger.debug("board source %s took %.2fs", name, time.monotonic() - started)


def _outside(name: str, root: Path, errors: list[dict], read: Callable[[list[dict]], Any]) -> tuple[Any, float | None]:
    """An outside read as a source, through :data:`READS`: ``(value, the time it was read)``, ``(None, None)`` when it
    failed. The errors the read added and its failure reach every request it answers."""
    got: list[_Read] = []

    def shared():
        got.append(READS.get((name, str(root), str(_worker_base())), read))
        errors.extend(got[0].errors)
        return got[0].result()
    value = _source(name, errors, shared)
    return value, (got[0].at if got and got[0].failure is None else None)


def _parallel(reads: dict[str, Callable[[list[dict]], Any]]) -> tuple[dict[str, Any], list[dict]]:
    """Each read in its own thread with the request's context (they wait on ssh, magpie and the record, not the CPU);
    the errors in the order of *reads*, whatever order the reads end in."""
    errors: dict[str, list[dict]] = {name: [] for name in reads}
    with ThreadPoolExecutor(len(reads), thread_name_prefix="lina-board") as pool:
        futures = {name: pool.submit(contextvars.copy_context().run, read, errors[name])
                   for name, read in reads.items()}
        values = {name: future.result() for name, future in futures.items()}
    return values, [error for name in reads for error in errors[name]]


def notices(work: dict | None, review: list[dict]) -> list[dict]:
    """What came into the inbox, one line per item, newest first. The key carries the moment the item came in, so an
    item that leaves and comes back counts again (the board's bell rules)."""
    from hermes_cli.work.locale import t
    names = (work or {}).get("agents") or {}

    def line(*parts) -> str:
        return " · ".join(str(p) for p in parts if p)
    out = []
    for row in (work or {}).get("decide") or []:
        out.append({"key": f"ask:{row['target']}:{int(row['at'] or 0)}", "target": row["target"], "at": row["at"],
                    "kind": "ask", "line": line(names.get(row["agent"]), row["title"], t("결정 필요: {text}", text=row["text"]))})
    for card in review:
        at = card.get("endedAt") or card.get("startedAt")
        out.append({"key": f"review:{card['id']}:{int(at or 0)}", "target": card["id"], "at": at, "kind": "review",
                    "line": line(names.get(card["agent"], card["agent"]), card["title"], t("검토 필요"))})
    for row in (work or {}).get("mine") or []:
        out.append({"key": f"todo:{row['id']}:{row['createdAt']}", "target": row["id"], "at": row["createdAt"],
                    "kind": "todo", "line": line(names.get(row["agent"]), row["title"], t("내 할 일"))})
    return sorted(out, key=lambda note: -(note["at"] or 0))


def _bound_locale(root: Path):
    """Notice lines need the record's locale bound even when the record itself could not be read."""
    from hermes_cli.work import env
    env.bind(env.load(root))
    return env


def _verdict_agent(card: dict) -> str | None:
    """The agent a run waits on for its verdict (``waitingOn: agent:<name>``, lina#295): its to-do, not the owner's.
    A stopped run's ``waitingOn`` (lina#629) is no verdict and counts for no agent."""
    waiting_on = (card.get("waitingOn") or "") if card.get("state") == "waiting" else ""
    return waiting_on.removeprefix("agent:") if waiting_on.startswith("agent:") else None


def _counts(work: dict | None, cards: list[dict] | None, review: list[dict], now: float) -> dict:
    def n(rows, ok=True):
        return len(rows) if ok and rows is not None else None
    running = [c for c in cards or [] if c["state"] == "in_progress"]
    done = [c for c in cards or [] if c["state"] == "done" and now - (c.get("endedAt") or 0) <= DONE_WINDOW_S]
    agents = dict.fromkeys((work or {}).get("agents") or {}, 0)
    for card in running:
        agents[card["agent"]] = agents.get(card["agent"], 0) + 1
    for agent in filter(None, map(_verdict_agent, cards or [])):
        agents[agent] = agents.get(agent, 0) + 1
    ok = cards is not None
    return {"inbox": n((work or {}).get("decide", []) + review + (work or {}).get("mine", []), work is not None and ok),
            "running": n(running, ok), "stopped": n([c for c in cards or [] if c["state"] == "stopped"], ok),
            "due": sum(len(v) for v in work["due"].values()) if work is not None else None,
            "done": n(done, ok), "agents": agents if ok else None}


def due(now: float | None = None) -> dict:
    """``GET /api/lina/work/due``: the due groups alone."""
    now, root, errors = time.time() if now is None else now, _root(), []
    work = own_and_hermes_work(root, errors)
    groups = work["due"] if work else {name: [] for name in ("overdue", "today", "week", "later")}
    return {"version": API_VERSION, "at": now, "errors": errors, "today": (work or {}).get("today"),
            "count": sum(len(v) for v in groups.values()) if work and not _own_failed(errors) else None,
            "due": groups, **_hermes_at(work)}


def devices(now: float | None = None) -> dict:
    now, root, errors = time.time() if now is None else now, _root(), []
    found = _source("devices", errors, lambda: devices_read(root, now, errors)) or {}
    return {"version": API_VERSION, "at": min(now, found.get("at") or now), "errors": errors,
            "machines": found.get("machines") or [],
            "person": found.get("person") or [], "monitored": found.get("monitored") or []}


def quota(now: float | None = None) -> dict:
    """``GET /api/lina/quota``: the quota view and the services magpie holds no account for (``missing``, null when
    magpie could not be read)."""
    from hermes_cli.lina_board_quota import view
    now, root, errors = time.time() if now is None else now, _root(), []
    data, at = _outside("quota-view", root, errors, lambda _errs: view(root, now))
    return {"version": API_VERSION, "at": min(now, at or now), "errors": errors,
            "quota": data["quota"] if data else [],
            "missing": data["missing"] if data else None}


def board(now: float | None = None) -> dict:
    """``GET /api/lina/board``: one snapshot of everything above. The four reads run at once; their calls to other
    devices and to magpie are shared (:data:`READS`: the quota view, the nodes' ``hello`` and ``probe``, the running
    runs' live view), so ``at`` is the time of the oldest of those served. The record, the hub's runs and the read
    marks are read for every request, so a write (a read mark, ``counts.unread``) shows at once."""
    now, root = time.time() if now is None else now, _root()
    got, errors = _parallel({
        "work": lambda errs: (own_and_hermes_work(root, errs), now),
        "worker": lambda errs: (_source("worker", errs, lambda: worker_read(root, now)), None),
        "devices": lambda errs: _devices_part(root, now, errs),
        "quota": lambda errs: _outside("quota", root, errs, lambda _errs: quota_read(root, now)),
    })
    (work, _), (cards, _), (found, _), (services, _) = got["work"], got["worker"], got["devices"], got["quota"]
    at = min([now] + [read_at for _, read_at in got.values() if read_at is not None])
    counted = None if _own_failed(errors) else work
    machines = (found or {}).get("machines")
    review = [c for c in cards or [] if c["state"] == "waiting" and _verdict_agent(c) is None]
    with _WORK_LOCK:
        lines = _source("work", errors, lambda: _notices_in_locale(root, work, review))
    counts = _counts(counted, cards, review, now)
    if lines is not None:
        from hermes_cli.lina_board_write import read_marks
        read = read_marks(root)
        for note in lines:
            note["read"] = note["key"] in read
    counts["unread"] = (sum(not note["read"] for note in lines)
                        if lines is not None and counted is not None and cards is not None else None)
    return {
        "version": API_VERSION, "at": at, "errors": errors, "counts": counts,
        "cards": cards or [],
        "inbox": {"decide": (work or {}).get("decide", []), "review": [c["id"] for c in review],
                  "mine": [{k: row[k] for k in ("id", "title", "waiting", "due", "agent", "readOnly") if k in row}
                           for row in (work or {}).get("mine", [])]},
        "due": work["due"] if work else {name: [] for name in ("overdue", "today", "week", "later")},
        "notices": lines or [], "machines": machines or [], "quota": services or [],
        "writer": _writer(now), **_hermes_at(work),
    }


def _devices_part(root: Path, now: float, errors: list[dict]) -> tuple[Any, float | None]:
    """The devices view, read for every request: only the nodes' ``hello`` and ``probe`` are shared
    (``lina_board_devices.nodes``); the sign-ins running now and the background reads that landed show at once."""
    found = _source("devices", errors, lambda: devices_read(root, now, errors))
    return found, found.get("at") if isinstance(found, dict) else None


def _hermes_at(work: dict | None) -> dict:
    """``hermesAt``: when the served rows of the ``hermes`` source were read (lina#533), only while it serves them."""
    return {"hermesAt": work["hermesAt"]} if work and "hermesAt" in work else {}


def _own_failed(errors: list[dict]) -> bool:
    """LINA's own record failed: the old record's rows may still be served, but a count would be a guess."""
    return any(e["source"] == "work" for e in errors)


def own_and_hermes_work(root: Path, errors: list[dict]) -> dict | None:
    """The work record's part, with the old record's rows of the ``hermes`` source added while it is set (lina#481);
    when LINA's own record fails, the old record's rows alone."""
    from hermes_cli.lina_board_hermes import merge
    return merge(_source("work", errors, lambda: work_read(root)), errors, root)


def _writer(now: float) -> dict:
    from hermes_cli.lina_board_plain import writer_state
    return writer_state(now=now)


def _notices_in_locale(root: Path, work: dict | None, review: list[dict]) -> list[dict]:
    env = _bound_locale(root)
    try:
        return notices(work, review)
    finally:
        env.reset()
