"""The hub's daily ``lina runners sync`` (lina#238), started by ``lina worker serve`` when
``worker.runner_sync: {at: "HH:MM"}`` is set (off by default).

The daily run keeps its own state in ``runner-sync/daily.json`` (``lina_runner_sync_state.daily``), apart from
``last.json``, which shows the latest run of anyone: a person's ``lina runners sync`` (``--only`` some runners or
all) before ``at`` does not stand in for the day's full run, and one after it does not make it run again.

Once the local time is past ``at`` and ``daily.json`` has no start today, the serve loop writes today's start into
``daily.json`` and starts the sync as its own owned process (``process_identity.spawn_owned``) with ``--daily-run
<that start>``: a run that installs on several nodes over ssh must not hold the 30 s hub tick. The child writes its
end (``finished``, ``rc``) into ``daily.json`` only while that start is still there, so a person's run never ends a
daily one. The start mark keeps a restarted service from running it twice on one day.

While another sync holds the lock (a person's run), the tick starts nothing and writes nothing; the next tick tries
again. A child that still lost the lock to a run started in between takes its start back out of ``daily.json``
(``refused``), so the next tick starts it again. A start that raised is spent for the day and is an issue line.

A run that found the hub busy for some runners leaves ``retry`` and those runners (``busy``) in ``daily.json``.
``RETRY_AFTER_S`` after it finished, on the same day, the tick starts one more run with ``--only`` those runners and
marks it ``retried``; that run never asks for another, so a day has at most two daily runs.

The same tick is the hub's daily health check of the sync (lina#491): it runs ``lina runners sync status --issues``
and writes the lines into ``<LINA root>/status/runner-sync.json``, one of the hub's own health checks (lina#233). A
day with issues is a failing check naming them; a day without is a passing ``no issues`` row. The file is rewritten
when the lines change and once a day, so it goes stale (``HEALTH_STALE_S``) only when the serve loop stops.

When ``devices.workers`` has a person device, the day's first start also starts the read-only person-device check
(``lina_runner_sync_person``, lina#490) as its own owned process; the retry does not. Its start is kept in
``daily.json`` (``person_started``) and survives a refused sync, so a sync started again after losing the lock does
not read the person's devices (ssh) or send their notice a second time that day.
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
    started = state.daily(root).get("started")
    return not isinstance(started, (int, float)) or state.day(started) != state.day(now)


def retry_due(daily: dict, now: float) -> list[str] | None:
    """The busy runners of today's first run once ``RETRY_AFTER_S`` passed since it finished; None otherwise."""
    finished = daily.get("finished")
    if daily.get("retry") is not True or daily.get("retried") or not isinstance(finished, (int, float)):
        return None
    if state.day(finished) != state.day(now) or now < finished + RETRY_AFTER_S:
        return None
    return [str(name) for name in daily.get("busy") or []] or None


def finish(root: Path, run: float, now: float, rc: int, busy: list[str], error: str | None = None) -> None:
    """The end of the daily run started at *run* (the child's ``--daily-run``); the busy runners ask for the retry
    only after the day's first run. A start no longer in ``daily.json`` is left alone."""
    daily = state.daily(root)
    if daily.get("started") == run:
        daily |= {"finished": now, "rc": rc} | ({"retry": True, "busy": busy} if busy else {})
    elif daily.get("retry_started") == run:
        daily |= {"retry_finished": now, "retry_rc": rc}
    else:
        _log.warning("the daily runner sync started at %s is no longer today's; its end is not recorded", run)
        return
    state.write_json(state.daily_path(root), {**daily, **({"error": error} if error else {})})


def refused(root: Path, run: float, now: float) -> None:
    """The daily run started at *run* found another sync holding the lock: take its start back so the next tick
    starts it again."""
    daily = state.daily(root)
    if daily.get("started") == run:
        daily = {"refused": now} | ({"person_started": daily["person_started"]} if "person_started" in daily else {})
    elif daily.get("retry_started") == run:
        daily = {key: value for key, value in daily.items() if key not in _RETRY_KEYS} | {"refused": now}
    else:
        return
    state.write_json(state.daily_path(root), daily)


_RETRY_KEYS = ("retried", "only", "retry_started", "retry_finished", "retry_rc")


def _locked(root: Path) -> bool:
    from hermes_cli.lina_runner_sync import SyncBusy, run_lock
    try:
        with run_lock(root):
            return False
    except SyncBusy:
        return True


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


def _spawn(root: Path, only: list[str] | None, run: float) -> subprocess.Popen:
    return _owned(root, ["hermes_cli.main", "runners", "sync", "--daily-run", repr(run),
                         *(["--only", ",".join(only)] if only else [])], "runner-sync")


def _spawn_person(root: Path) -> subprocess.Popen:
    return _owned(root, ["hermes_cli.lina_runner_sync_person"], "runner-sync-person")


def _start_person(root: Path, spawn: Callable[[Path], subprocess.Popen], now: float) -> dict:
    """``person_started`` for the day's ``daily.json``: today's earlier start, this start, or nothing."""
    from hermes_cli.worker import settings
    global _person
    started = state.daily(root).get("person_started")
    if isinstance(started, (int, float)) and state.day(started) == state.day(now):
        return {"person_started": started}
    if _person is not None and _person.poll() is None:
        return {}
    if not any(dev.role == "person" for dev in settings.devices(root)):
        return {}
    try:
        _person = spawn(root)
    except Exception:  # health: allow BLE001 -- nothing may leave the serve tick; the traceback is logged
        _log.exception("the person-device check could not start")
        return {}
    return {"person_started": now}


def maybe_start(worker_root: Path, *, now: float | None = None,
                spawn: Callable[[Path, list[str] | None, float], subprocess.Popen] | None = None,
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
        only = retry_due(state.daily(root), now)
        if only is None:
            return False
    if _locked(root):
        _log.info("another runner sync holds the lock; the daily one waits for the next tick")
        return False
    if only:
        daily = {**state.daily(root), "retried": True, "only": only, "retry_started": now, "retry_finished": None,
                 "retry_rc": None}
    else:
        person = _start_person(root, person_spawn or _spawn_person, now)
        daily = {"started": now, "finished": None, "rc": None, **person}
    state.write_json(state.daily_path(root), daily)
    try:
        _child = (spawn or _spawn)(root, only, now)
    except Exception as exc:  # health: allow BLE001 -- nothing may leave the serve tick; the traceback is logged
        _log.exception("the daily runner sync could not start")
        _failed_start(root, now, exc, retry=bool(only))
        return False
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


def error_line(exc: BaseException) -> str:
    from hermes_cli.lina_runner_sync import scrub
    return scrub(" ".join(f"{type(exc).__name__}: {exc}".split()))[:300]


def _failed_start(root: Path, now: float, exc: BaseException, *, retry: bool) -> None:
    error = error_line(exc)
    ended = {"retry_finished": now, "retry_rc": 1} if retry else {"finished": now, "rc": 1}
    state.write_json(state.daily_path(root), {**state.daily(root), **ended, "error": f"could not start: {error}"})
    log = state.sync_dir(root) / "sync.log"
    with log.open("a", encoding="utf-8") as fh:
        fh.write(f"{time.strftime('%Y-%m-%d %H:%M:%S', time.localtime(now))} could not start: {error}\n")
