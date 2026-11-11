"""The board's data served by LINA (lina#39): what today's board snapshot holds beyond lina#112's goals, work cards,
worker runs on cards and inbox.

Sources: the work record (``lina work``: the owner's to-dos, decisions, due groups), ``lina worker`` (runs that are
not kanban work cards, and the devices of ``devices.workers``), LINA's own OCX or ``ocx.external`` (quota). Each read
is separate, so a source that fails shows up in ``errors`` and the counts made from it are ``None``, never 0. Every
count is the length of a list in the same snapshot. Notice lines are in the work record's locale (``work.locale``).
"""

from __future__ import annotations

import logging
import threading
import time
from pathlib import Path
from typing import Any, Callable

API_VERSION = 1
DONE_WINDOW_S = 86400
_WORK_LOCK = threading.Lock()
logger = logging.getLogger(__name__)


def _root() -> Path:
    """The board's own root: the work record, the quota view and the read marks (``lina_ocx.lina_root``). Worker
    state is not read from here but through :func:`_worker_base` (lina#269)."""
    from hermes_cli.lina_ocx import lina_root
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
    """``{machines, person}``: the nodes by role with readiness, load and runners, and the person devices
    (``lina_board_devices``); a metrics reader that fails adds an error and leaves its fields empty. The devices
    and their ``devices.metrics.beszel`` path come from the worker's own config, the same root as
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
    a separate store, device or process (sqlite, psql, ssh, OCX) with its own error types, and one that fails must not
    take the others down, so any error is caught here and reported."""
    from hermes_cli.config_backend import ConfigBackendUnavailable
    try:
        return read()
    except (Exception, ConfigBackendUnavailable) as exc:
        logger.warning("board source %s failed", name, exc_info=True)
        errors.append({"source": name, "message": f"{type(exc).__name__}: {exc}"[:300]})
        return None


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
    """The agent a run waits on for its verdict (``waitingOn: agent:<name>``, lina#295): its to-do, not the owner's."""
    waiting_on = card.get("waitingOn") or ""
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
    return {"version": API_VERSION, "at": now, "errors": errors, "machines": found.get("machines") or [],
            "person": found.get("person") or []}


def quota(now: float | None = None) -> dict:
    """``GET /api/lina/quota``: the quota view and the services OCX holds no account for (``missing``, null when
    OCX could not be read)."""
    from hermes_cli.lina_board_quota import view
    now, root, errors = time.time() if now is None else now, _root(), []
    data = _source("quota", errors, lambda: view(root, now))
    return {"version": API_VERSION, "at": now, "errors": errors, "quota": data["quota"] if data else [],
            "missing": data["missing"] if data else None}


def board(now: float | None = None) -> dict:
    """``GET /api/lina/board``: one snapshot of everything above."""
    now, root, errors = time.time() if now is None else now, _root(), []
    work = own_and_hermes_work(root, errors)
    counted = None if _own_failed(errors) else work
    cards = _source("worker", errors, lambda: worker_read(root, now))
    machines = (_source("devices", errors, lambda: devices_read(root, now, errors)) or {}).get("machines")
    services = _source("quota", errors, lambda: quota_read(root, now))
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
        "version": API_VERSION, "at": now, "errors": errors, "counts": counts,
        "cards": cards or [],
        "inbox": {"decide": (work or {}).get("decide", []), "review": [c["id"] for c in review],
                  "mine": [{k: row[k] for k in ("id", "title", "waiting", "due", "agent", "readOnly") if k in row}
                           for row in (work or {}).get("mine", [])]},
        "due": work["due"] if work else {name: [] for name in ("overdue", "today", "week", "later")},
        "notices": lines or [], "machines": machines or [], "quota": services or [],
        "writer": _writer(now), **_hermes_at(work),
    }


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
