"""The hub's daily ``lina runners sync`` (lina#238), started by ``lina worker serve`` when
``worker.runner_sync: {at: "HH:MM"}`` is set (off by default).

Once the local time is past ``at`` and ``runner-sync/last.json`` has no start today, the serve loop writes today's
start into ``last.json`` and starts the sync as its own owned process (``process_identity.spawn_owned``): a run that
installs on several nodes over ssh must not hold the 30 s hub tick. The start mark and the sync's own lock keep a
restarted service from running it twice on one day.

A run that found the hub busy for some runners leaves ``retry`` and those runners (``busy``) in ``last.json``
(``lina_runner_sync_hub``). ``RETRY_AFTER_S`` after it finished, on the same day, the tick starts one more run with
``--only`` those runners and marks it ``retried``; that run never asks for another, so a day has at most two.
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
_child: subprocess.Popen | None = None
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


def _spawn(root: Path, only: list[str] | None = None) -> subprocess.Popen:
    from hermes_cli.process_identity import spawn_owned
    from hermes_cli.worker import settings
    from tools.environments.local import build_subprocess_env
    log = state.sync_dir(root) / "sync.log"
    log.parent.mkdir(parents=True, exist_ok=True)
    env = build_subprocess_env(scrub_secrets=False, inherit_profile_home=False,
                               extra={"PYTHONPATH": settings.child_pythonpath()})
    with log.open("a", encoding="utf-8") as fh:
        return spawn_owned([sys.executable, "-P", "-m", "hermes_cli.main", "runners", "sync",
                            *(["--only", ",".join(only)] if only else [])], purpose="runner-sync",
                           env=env, stdin=subprocess.DEVNULL, stdout=fh, stderr=subprocess.STDOUT)


def maybe_start(worker_root: Path, *, now: float | None = None,
                spawn: Callable[[Path, list[str] | None], subprocess.Popen] | None = None) -> bool:
    """Start today's sync when it is due, or today's one retry; True when one was started."""
    from hermes_cli.worker import settings
    global _child
    if _child is not None and _child.poll() is None:
        return False
    _child = None
    root = worker_root.parent
    at = at_of(settings.section(root))
    now = time.time() if now is None else now
    if at is None:
        return False
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
    return True


def _failed_start(root: Path, now: float, exc: BaseException) -> None:
    from hermes_cli.lina_runner_sync import scrub
    error = scrub(" ".join(f"{type(exc).__name__}: {exc}".split()))[:300]
    state.write_json(state.last_path(root), {"started": now, "finished": now, "rc": 1, "by": "serve", "error": error})
    log = state.sync_dir(root) / "sync.log"
    with log.open("a", encoding="utf-8") as fh:
        fh.write(f"{time.strftime('%Y-%m-%d %H:%M:%S', time.localtime(now))} could not start: {error}\n")
