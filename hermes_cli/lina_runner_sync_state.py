"""What the daily runner sync (lina#238) keeps under the LINA root, and how it compares versions.

* ``runner-pins.json``: the pins the sync moved, laid over ``pm/lina-deps.json`` by ``lina_runners.pins()``; the repo
  pin is the floor. Every write replaces the file atomically and appends the previous entry to
  ``runner-pins.history.jsonl`` (the way back).
* ``runner-sync/holds.json``: runners (or one runner on one node) a person has to clear, and the major versions a
  person accepted.
* ``runner-sync/lag.json``: since when a node's runner differs from the hub's pin.
* ``runner-sync/last.json``: the last run, by a person or by the serve tick (``started``, ``finished``, ``rc``);
  ``runner-sync/YYYYMMDD.json`` its record.
* ``runner-sync/daily.json``: the serve tick's own daily run (``lina_runner_sync_daily``), which a person's run never
  writes: ``started``, ``finished``, ``rc``, the busy runners (``retry``, ``busy``), the one retry (``retried``,
  ``only``, ``retry_started``, ``retry_finished``, ``retry_rc``), ``error`` when a start or the run failed, and
  ``person_started`` when the day's person-device check started.
"""

from __future__ import annotations

import json
import os
import re
import time
from pathlib import Path

OVERLAY = "runner-pins.json"
HISTORY = "runner-pins.history.jsonl"
SYNC_DIR = "runner-sync"
MOVED_KEYS = ("version", "integrity", "artifacts")
_VERSION = re.compile(r"^(\d+)((?:\.\d+)*)(?:-([0-9A-Za-z.\-]+))?$")
DATE_MAJOR = 1000  # a first number this large is a year (cursor-agent 2026.11.05-...), not a major version


def overlay_path(root: Path) -> Path:
    return root / OVERLAY


def sync_dir(root: Path) -> Path:
    return root / SYNC_DIR


def read_json(path: Path, default):
    try:
        value = json.loads(path.read_text(encoding="utf-8-sig"))
    except (OSError, ValueError):
        return default
    return value if isinstance(value, type(default)) else default


def write_json(path: Path, value) -> None:
    """Replace *path* in one rename, so a reader never sees half of it."""
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(f".{path.name}.{os.getpid()}.tmp")
    tmp.write_text(json.dumps(value, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    os.replace(tmp, path)


def parse(version: str) -> tuple[tuple[int, ...], str] | None:
    found = _VERSION.match(version or "")
    if not found:
        return None
    numbers = (int(found.group(1)), *(int(part) for part in found.group(2).split(".") if part))
    return numbers, found.group(3) or ""


def _pre_key(pre: str) -> tuple:
    return tuple((0, int(part), "") if part.isdigit() else (1, 0, part) for part in pre.split("."))


def compare(new: str, old: str) -> int:
    """-1, 0 or 1 as *new* is below, equal to or above *old*. A pre-release sorts below its release; a date version
    with another suffix on the same day counts as newer (its suffix is a build id, not an order)."""
    a, b = parse(new), parse(old)
    if a is None or b is None:
        return 0 if new == old else 1
    width = max(len(a[0]), len(b[0]))
    na, nb = a[0] + (0,) * (width - len(a[0])), b[0] + (0,) * (width - len(b[0]))
    if na != nb:
        return 1 if na > nb else -1
    if a[1] == b[1]:
        return 0
    if a[0][0] >= DATE_MAJOR:
        return 1
    if not a[1] or not b[1]:
        return 1 if not a[1] else -1
    return 1 if _pre_key(a[1]) > _pre_key(b[1]) else -1


def major_jump(new: str, old: str) -> bool:
    """The first number changes (2.x -> 3.x, 0.x -> 1.x); a year is not a major version."""
    a, b = parse(new), parse(old)
    if a is None or b is None:
        return False
    return a[0][0] != b[0][0] and a[0][0] < DATE_MAJOR and b[0][0] < DATE_MAJOR


def overlay(root: Path) -> dict:
    return read_json(overlay_path(root), {})


def merged(table: dict, root: Path) -> dict:
    """*table* (``pm/lina-deps.json`` ``runners``) with the moved ``version``/``integrity``/``artifacts`` of each runner
    whose move is not below the repo pin."""
    moved = overlay(root)
    out = {}
    for name, spec in table.items():
        entry = moved.get(name)
        if isinstance(entry, dict) and isinstance(entry.get("version"), str) and \
                compare(entry["version"], spec.get("version", "")) >= 0:
            spec = {**spec, **{key: entry[key] for key in MOVED_KEYS if key in entry}}
        out[name] = spec
    return out


def pin_from(root: Path, name: str) -> str:
    """``sync`` when the runner's pin on this root is the daily sync's move, else ``repo``."""
    return "sync" if isinstance(overlay(root).get(name), dict) else "repo"


def write_overlay(root: Path, name: str, entry: dict | None, *, why: str, now: float | None = None) -> None:
    """Set (or with None drop) *name*'s moved pin; the entry it replaces goes to the history first."""
    current = overlay(root)
    stamp = time.time() if now is None else now
    line = {"at": stamp, "runner": name, "before": current.get(name), "after": entry, "why": why}
    root.mkdir(parents=True, exist_ok=True)
    with (root / HISTORY).open("a", encoding="utf-8") as fh:
        fh.write(json.dumps(line, ensure_ascii=False) + "\n")
    if entry is None:
        current.pop(name, None)
    else:
        current[name] = {key: entry[key] for key in MOVED_KEYS if key in entry} | {"at": stamp}
    write_json(overlay_path(root), current)


def _holds_path(root: Path) -> Path:
    return sync_dir(root) / "holds.json"


def holds(root: Path) -> dict:
    data = read_json(_holds_path(root), {})
    return {"holds": [h for h in data.get("holds", []) if isinstance(h, dict)],
            "accepted": {k: v for k, v in (data.get("accepted") or {}).items() if isinstance(v, str)}}


def held(root: Path, runner: str, node: str | None = None) -> dict | None:
    """The hold on *runner* as a whole (``node`` None), or on *runner* on *node*."""
    for hold in holds(root)["holds"]:
        if hold.get("runner") == runner and hold.get("node") == node:
            return hold
    return None


def add_hold(root: Path, runner: str, node: str | None, version: str, error: str, kind: str,
             now: float | None = None) -> None:
    data = holds(root)
    data["holds"] = [h for h in data["holds"] if not (h.get("runner") == runner and h.get("node") == node)]
    data["holds"].append({"runner": runner, "node": node, "version": version, "error": error[:300], "kind": kind,
                          "at": time.time() if now is None else now})
    write_json(_holds_path(root), data)


def clear(root: Path, runner: str, node: str | None = None, accept: str | None = None) -> list[dict]:
    """Drop *runner*'s holds (only *node*'s with a node); ``accept`` lets that major version move. Returns the holds
    dropped."""
    data = holds(root)
    gone = [h for h in data["holds"] if h.get("runner") == runner and (node is None or h.get("node") == node)]
    data["holds"] = [h for h in data["holds"] if h not in gone]
    if accept:
        data["accepted"][runner] = accept
    write_json(_holds_path(root), data)
    return gone


def accepted(root: Path, runner: str) -> str | None:
    return holds(root)["accepted"].get(runner)


def _lag_path(root: Path) -> Path:
    return sync_dir(root) / "lag.json"


def lag(root: Path) -> dict:
    return read_json(_lag_path(root), {})


def update_lag(root: Path, node: str, runner: str, node_version: str | None, hub_version: str,
               now: float) -> None:
    """Start the clock when *node*'s *runner* first differs from the hub's pin; restart it when the node's version
    changes; drop it when they are the same."""
    data = lag(root)
    key = f"{node}/{runner}"
    if node_version == hub_version:
        data.pop(key, None)
    else:
        entry = data.get(key)
        if not isinstance(entry, dict) or entry.get("node_version") != node_version:
            entry = {"since": now, "node_version": node_version}
        data[key] = {**entry, "hub_version": hub_version}
    write_json(_lag_path(root), data)


def last_path(root: Path) -> Path:
    return sync_dir(root) / "last.json"


def last(root: Path) -> dict:
    return read_json(last_path(root), {})


def daily_path(root: Path) -> Path:
    return sync_dir(root) / "daily.json"


_LEGACY_DAILY = ("started", "finished", "rc", "error", "retry", "busy", "retried", "only")


def daily(root: Path) -> dict:
    """The serve tick's daily run. Before ``daily.json`` existed the tick kept it in ``last.json`` marked ``by:
    serve``; such a record becomes ``daily.json`` once (``from: last.json``, ``last.json`` untouched), so a person's
    later run cannot erase it. A ``last.json`` without that mark was a person's run and is not a daily run. Its
    ``error`` was only ever a start that raised, so it keeps that meaning (``could not start: ...``)."""
    path = daily_path(root)
    if path.exists():
        return read_json(path, {})
    legacy = last(root)
    if legacy.get("by") != "serve" or not isinstance(legacy.get("started"), (int, float)):
        return {}
    kept = {key: legacy[key] for key in _LEGACY_DAILY if key in legacy} | {"from": "last.json"}
    if kept.get("error"):
        kept["error"] = f"could not start: {kept['error']}"
    write_json(path, kept)
    return kept


def day(now: float) -> str:
    return time.strftime("%Y%m%d", time.localtime(now))


def record_path(root: Path, now: float) -> Path:
    return sync_dir(root) / f"{day(now)}.json"
