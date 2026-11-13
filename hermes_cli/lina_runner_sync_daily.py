"""The hub's daily ``lina runners sync`` (lina#238), started by ``lina worker serve`` when
``worker.runner_sync: {at: "HH:MM"}`` is set (off by default).

Once the local time is past ``at`` and ``runner-sync/last.json`` has no start today, the serve loop writes today's
start into ``last.json`` and starts the sync as its own owned process (``process_identity.spawn_owned``): a run that
installs on several nodes over ssh must not hold the 30 s hub tick. The start mark and the sync's own lock keep a
restarted service from running it twice on one day.

A run that found the hub busy for some runners leaves ``retry`` and those runners (``busy``) in ``last.json``
(``lina_runner_sync_hub``). ``RETRY_AFTER_S`` after it finished, on the same day, the tick starts one more run with
``--only`` those runners and marks it ``retried``; that run never asks for another, so a day has at most two.

The same tick is the hub's daily health check of the sync (lina#491): it runs ``lina runners sync status --issues``
and writes the lines into ``<LINA root>/status/runner-sync.json``, one of the hub's own health checks (lina#233). A
day with issues is a failing check naming them; a day without is a passing ``no issues`` row. The file is rewritten
when the lines change and once a day, so it goes stale (``HEALTH_STALE_S``) only when the serve loop stops.

When ``devices.workers`` has a person device, the day's first start also starts the read-only person-device check
(``lina_runner_sync_person``, lina#490) as its own owned process; the retry does not.
"""

from __future__ import annotations

import logging
import re
import subprocess
import sys
import time
from pathlib import Path
from typing import Callable

from hermes_cli import lina_runner_sync_state as state

_AT = re.compile(r"^([01]?\d|2[0-3]):([0-5]\d)$")
RETRY_AFTER_S = 1800
HEALTH_STALE_S = 2 * 86400
_child: subprocess.Popen | None = None
_person: subprocess.Popen | None = None
_log = logging.getLogger(__name__)


def at_of(worker: dict) -> tuple[int, int] | None:
    sync = worker.get("runner_sync")
    found = _AT.match(str(sync.get("at") or "")) if isinstance(sync, dict) else None
    return (int(found.group(1)), int(found.group(2))) if found else None


def due(root: Path, at: tuple[int, int], now: float) -> bool:
    local = time.localtime(now)
    if (local.tm_hour, local.tm_min) < at:
        return False
    started = state.last(root).get("started")
    return not isinstance(started, (int, float)) or state.day(started) != state.day(now)


def retry_due(last: dict, now: float) -> list[str] | None:
    """The busy runners of today's first run once ``RETRY_AFTER_S`` passed since it finished; None otherwise."""
    finished = last.get("finished")
    if last.get("retry") is not True or last.get("retried") or not isinstance(finished, (int, float)):
        return None
    if state.day(finished) != state.day(now) or now < finished + RETRY_AFTER_S:
        return None
    return [str(name) for name in last.get("busy") or []] or None


def _owned(root: Path, args: list[str], purpose: str) -> subprocess.Popen:
    from hermes_cli.process_identity import spawn_owned
    from hermes_cli.worker import settings
    from tools.environments.local import build_subprocess_env
    log = state.sync_dir(root) / "sync.log"
    log.parent.mkdir(parents=True, exist_ok=True)
    env = build_subprocess_env(scrub_secrets=False, inherit_profile_home=False,
                               extra={"PYTHONPATH": settings.child_pythonpath()})
    with log.open("a", encoding="utf-8") as fh:
        return spawn_owned([sys.executable, "-P", "-m", *args], purpose=purpose,
                           env=env, stdin=subprocess.DEVNULL, stdout=fh, stderr=subprocess.STDOUT)


def _spawn(root: Path, only: list[str] | None = None) -> subprocess.Popen:
    return _owned(root, ["hermes_cli.main", "runners", "sync", *(["--only", ",".join(only)] if only else [])],
                  "runner-sync")


def _spawn_person(root: Path) -> subprocess.Popen:
    return _owned(root, ["hermes_cli.lina_runner_sync_person"], "runner-sync-person")


def _start_person(root: Path, spawn: Callable[[Path], subprocess.Popen]) -> None:
    from hermes_cli.worker import settings
    global _person
    if _person is not None and _person.poll() is None:
        return
    if not any(dev.role == "person" for dev in settings.devices(root)):
        return
    try:
        _person = spawn(root)
    except Exception:  # health: allow BLE001 -- nothing may leave the serve tick; the traceback is logged
        _log.exception("the person-device check could not start")


def maybe_start(worker_root: Path, *, now: float | None = None,
                spawn: Callable[[Path, list[str] | None], subprocess.Popen] | None = None,
                person_spawn: Callable[[Path], subprocess.Popen] | None = None) -> bool:
    """Start today's sync when it is due, or today's one retry; True when one was started."""
    from hermes_cli.worker import settings
    global _child
    root = worker_root.parent
    at = at_of(settings.section(root))
    now = time.time() if now is None else now
    if at is None:
        return False
    check_health(root, now)
    if _child is not None and _child.poll() is None:
        return False
    _child = None
    only = None
    if not due(root, at, now):
        only = retry_due(state.last(root), now)
        if only is None:
            return False
    retried = {"retried": True, "only": only} if only else {}
    state.write_json(state.last_path(root), {"started": now, "finished": None, "rc": None, "by": "serve", **retried})
    try:
        _child = (spawn or _spawn)(root, only)
    except Exception as exc:  # health: allow BLE001 -- nothing may leave the serve tick; the traceback is logged
        _log.exception("the daily runner sync could not start")
        _failed_start(root, now, exc)
        return False
    if only is None:
        _start_person(root, person_spawn or _spawn_person)
    return True


def health_path(root: Path) -> Path:
    return root / "status" / "runner-sync.json"


def check_health(root: Path, now: float) -> None:
    """Write ``sync status --issues`` as the ``runner-sync`` status check when its lines changed or the day did."""
    from hermes_cli.lina_runner_sync import status
    lines: list[str] = []
    try:
        status(root, issues_only=True, out=lines.append)
    except Exception:  # health: allow BLE001 -- nothing may leave the serve tick; the traceback is logged
        _log.exception("the runner sync's health check could not read its issues")
        return
    issues = [line.removeprefix("- ") for line in lines if line != "- none"]
    check = {"name": "runner-sync", "ok": not issues, "line": "; ".join(issues) or "no issues"}
    path = health_path(root)
    old = state.read_json(path, {})
    at = old.get("at")
    if {key: old.get(key) for key in check} == check and isinstance(at, (int, float)) and state.day(at) == state.day(now):
        return
    state.write_json(path, {**check, "at": now, "stale_after": HEALTH_STALE_S})


def _failed_start(root: Path, now: float, exc: BaseException) -> None:
    from hermes_cli.lina_runner_sync import scrub
    error = scrub(" ".join(f"{type(exc).__name__}: {exc}".split()))[:300]
    state.write_json(state.last_path(root), {"started": now, "finished": now, "rc": 1, "by": "serve", "error": error})
    log = state.sync_dir(root) / "sync.log"
    with log.open("a", encoding="utf-8") as fh:
        fh.write(f"{time.strftime('%Y-%m-%d %H:%M:%S', time.localtime(now))} could not start: {error}\n")
