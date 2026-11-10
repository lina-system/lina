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
        timeout_seconds: 10
        cache_seconds: 5

Unset, nothing runs. A reply is kept in memory for ``cache_seconds``, a failure too, so a hung command is not run again
on every poll; nothing is written to disk. Items from this source carry ids prefixed ``hermes:`` and ``readOnly: true``;
LINA's write routes refuse such ids with 409 (:data:`READ_ONLY`).
"""

from __future__ import annotations

import dataclasses
import json
import shlex
import subprocess
import threading
import time
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from typing import Any

PREFIX = "hermes:"
SOURCE = "hermes"
DEFAULT_TIMEOUT_S = 10.0
DEFAULT_CACHE_S = 5.0
SHOW_WORKERS = 4
READ_ONLY = "read only: this item is on the Hermes board or in the old work record; change it there"
_CACHE: dict[tuple, tuple[float, bool, Any]] = {}
_CACHE_LOCK = threading.Lock()


class SourceError(RuntimeError):
    """The source is set wrong, or its command failed, timed out or printed something that is not the JSON expected."""


@dataclasses.dataclass(frozen=True)
class Source:
    command: tuple[str, ...] | None
    board: str | None
    work: tuple[str, ...] | None
    home: str | None
    timeout: float
    cache: float


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
                  _seconds(sec, "timeout_seconds", DEFAULT_TIMEOUT_S, positive=True),
                  _seconds(sec, "cache_seconds", DEFAULT_CACHE_S, positive=False))


# ---------------------------------------------------------------- running the commands
def _env(src: Source) -> dict[str, str]:
    """The hub's environment (LINA's ``HERMES_HOME`` and a card session's ids dropped), with the configured home."""
    from hermes_cli.worker.kanban_link import hub_env
    env = hub_env()
    if src.home:
        env["HERMES_HOME"] = src.home
    return env


def _call(src: Source, argv: tuple[str, ...]) -> Any:
    shown = shlex.join(argv)
    try:
        done = subprocess.run(list(argv), capture_output=True, text=True, encoding="utf-8", errors="replace",
                              timeout=src.timeout, env=_env(src), stdin=subprocess.DEVNULL, check=False)
    except subprocess.TimeoutExpired as exc:
        raise SourceError(f"{shown} timed out after {src.timeout:g}s") from exc
    except OSError as exc:
        raise SourceError(f"{shown}: {exc}") from exc
    if done.returncode:
        last = (done.stderr.strip().splitlines() or [""])[-1]
        raise SourceError(f"{shown} exited {done.returncode}: {last}")
    try:
        return json.loads(done.stdout)
    except ValueError as exc:
        raise SourceError(f"{shown} did not print JSON") from exc


def _cached(src: Source, argv: tuple[str, ...]) -> Any:
    """One command's reply, run again only after ``cache_seconds``; a failure is kept as long and raised again."""
    key, now = (argv, src.home), time.monotonic()
    with _CACHE_LOCK:
        hit = _CACHE.get(key)
    if hit is not None and hit[0] > now:
        if not hit[1]:
            raise SourceError(hit[2])
        return hit[2]
    try:
        value, entry = _call(src, argv), None
    except SourceError as exc:
        value, entry = None, (now + src.cache, False, str(exc))
    with _CACHE_LOCK:
        for old in [k for k, v in _CACHE.items() if v[0] <= now]:
            del _CACHE[old]
        _CACHE[key] = entry or (now + src.cache, True, value)
    if entry is not None:
        raise SourceError(entry[2])
    return value


def _kanban(src: Source, verb: str, *args: str) -> Any:
    if src.command is None:
        raise SourceError("board.hermes.command is not set")
    return _cached(src, (*src.command, "kanban", *(("--board", src.board) if src.board else ()), verb, *args))


def card_list(src: Source) -> list[dict]:
    """``kanban list --json``: the board's cards (archived ones are not listed)."""
    rows = _kanban(src, "list", "--json")
    if not isinstance(rows, list) or not all(isinstance(r, dict) and r.get("id") for r in rows):
        raise SourceError("kanban list --json is not a list of cards")
    return [r for r in rows if r.get("status") != "archived"]


def card_show(src: Source, raw: str) -> dict:
    """``kanban show <id> --json``: the card with its comments, events, runs and links."""
    reply = _kanban(src, "show", raw, "--json")
    if not isinstance(reply, dict) or not isinstance(reply.get("task"), dict):
        raise SourceError(f"kanban show {raw} --json is not a card")
    return reply


def cards(src: Source) -> list[dict]:
    """``show`` of every listed card, a few at a time."""
    ids = [str(row["id"]) for row in card_list(src)]
    with ThreadPoolExecutor(SHOW_WORKERS) as pool:
        return list(pool.map(lambda raw: card_show(src, raw), ids))


def record(src: Source) -> dict | None:
    """``<work> json``: the old record's items and agents; None when the record part is not set."""
    if src.work is None:
        return None
    reply = _cached(src, (*src.work, "json"))
    if not isinstance(reply, dict) or not isinstance(reply.get("items"), list):
        raise SourceError("work json has no items list")
    return reply


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
    """LINA's own record part (``lina_board.work_read``) with the old record's rows added after its own. Off, it is
    returned as it is; a failing command adds one ``errors`` entry and LINA's part is served alone."""
    sec = section(root)
    if sec is None:
        return work
    from hermes_cli.lina_board import _source

    def read() -> dict | None:
        reply = record(source(sec))
        return None if reply is None else _record_rows(reply, root)
    extra = _source(SOURCE, errors, read)
    if work is None or extra is None:
        return work
    return {**work, "agents": {**extra["agents"], **work["agents"]},
            "decide": work["decide"] + extra["decide"], "mine": work["mine"] + extra["mine"],
            "due": {name: rows + extra["due"].get(name, []) for name, rows in work["due"].items()}}


def only_hermes_agent(agent: str, root: Path) -> bool:
    """An agent the old record knows and LINA's own record does not: a quick add to it would be a write there."""
    from hermes_cli.work import env
    sec = section(root)
    if sec is None or agent in env.load(root).agents:
        return False
    try:
        reply = record(source(sec))
    except SourceError:
        return False
    return reply is not None and agent in (reply.get("bots") or {})
