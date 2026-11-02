"""``lina worker`` as the board reads it (lina#39): runs that are not kanban work cards as cards in the five work
states, and the devices of ``devices.workers`` as machines with their readiness and load.

A device's readiness is the bidding probe's (``bidding.probe_all``). CPU, memory, disk and temperature come from an
optional metrics reader: Beszel's database, read only, when ``devices.metrics.beszel`` names it. Without one the list
still shows each device's readiness and load.
"""

from __future__ import annotations

import calendar
import json
import re
import sqlite3
import time
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from urllib.parse import quote

from hermes_cli.worker import bidding, runners, settings
from hermes_cli.worker.hub import ENDED, Hub
from hermes_cli.worker.hub_store import Store
from hermes_cli.worker.report import MODES

# lina#112's work states (plugins/kanban/dashboard/work_api.py WORK_STATES): the app knows these five only.
WORK_STATES = ("planned", "in_progress", "waiting", "stopped", "done")
RECENT_S = 2 * 86400
NOW_LINE_CHARS = 120
CPU_HOT_S, QUIET_S = 300, 300
_MODE_PREFIX = re.compile(r"^(?:" + "|".join(sorted(map(re.escape, MODES.values()), key=len, reverse=True)) + r")\s+")


def work_state(job: dict, live: dict | None = None) -> tuple[str, str | None]:
    """``(state, reason)`` of one run in lina#112's vocabulary. A verdict wins over how the run ended; a finished
    run with no verdict waits for review; a running run that asked a person waits for the decision."""
    state, verdict = job.get("state") or "new", job.get("verdict")
    if verdict == "rejected":
        return "stopped", "rejected"
    if state == "cancelled":
        return "stopped", "cancelled"
    if verdict == "accepted":
        return "done", None
    stopped = {"done_empty": "result_missing", "failed": "failed", "stalled": "unresponsive", "unknown": "unresponsive"}
    if state in stopped:
        return "stopped", stopped[state]
    if state in ("done", "review"):
        return "waiting", "review"
    if state in ("new", "queued"):
        return "planned", state
    if (live or {}).get("dash") == "human":
        return "waiting", "decision"
    return "in_progress", None


def _title(job: dict) -> str:
    """The brief's first ``# `` heading (after the mode prefix), else the run's topic."""
    for line in str(job.get("prompt") or "").splitlines():
        line = _MODE_PREFIX.sub("", line.strip())
        if line.startswith("# "):
            return line[2:].strip()
    return str(job.get("topic") or job.get("id"))


def _now_line(live: dict) -> str | None:
    text = " ".join(re.sub(r"[`*_>#]+", " ", str(live.get("last") or live.get("why") or "")).split())
    if not text:
        return None
    return text if len(text) <= NOW_LINE_CHARS else text[:NOW_LINE_CHARS - 1].rstrip() + "…"


def _ends(store: Store) -> dict[str, float]:
    """Each run's latest ``exec_end`` time, from one pass over the hub's receipts."""
    ends: dict[str, float] = {}
    if not store.receipts.is_file():
        return ends
    for line in store.receipts.read_text(encoding="utf-8-sig").splitlines():
        try:
            row = json.loads(line)
        except ValueError:
            continue  # a line the hub is still appending
        if row.get("kind") == "exec_end" and isinstance(row.get("ts"), (int, float)):
            ends[row["task"]] = max(ends.get(row["task"], 0.0), float(row["ts"]))
    return ends


def _recent(job: dict, ended: float | None, now: float) -> bool:
    return job.get("state") not in ENDED or now - max(ended or 0, job.get("created") or 0) <= RECENT_S


def _lives(hub: Hub, jobs: list[dict]) -> dict[str, dict]:
    """The runner's live view of each running run, read from its device; an unreachable device gives none."""
    running = [job for job in jobs if job.get("state") == "running" and job.get("host")]
    if not running:
        return {}
    with ThreadPoolExecutor(min(8, len(running))) as pool:
        remote = dict(zip((job["id"] for job in running), pool.map(lambda job: hub.meta(job, live=True), running)))
    return {tid: dict(meta.get("live") or {}) for tid, meta in remote.items() if not meta.get("unreachable")}


def cards(worker_root: Path, now: float) -> list[dict]:
    """Runs not started from a kanban work card (lina#112 serves those) that run, wait, or moved in the last two
    days, newest first."""
    store = Store(worker_root)
    ends = _ends(store)
    jobs = [job for job in store.all() if not job.get("card") and _recent(job, ends.get(job["id"]), now)]
    lives = _lives(Hub(worker_root, out=lambda *_: None), jobs)
    out = []
    for job in reversed(jobs):
        live = lives.get(job["id"])
        state, reason = work_state(job, live)
        out.append({"id": job["id"], "title": _title(job), "state": state, "reason": reason, "agent": job.get("bot"),
                    "kind": job.get("mode"), "todo": job.get("todo"), "host": job.get("host"),
                    "startedAt": job.get("started"), "endedAt": ends.get(job["id"]) if job.get("state") in ENDED else None,
                    "now": _now_line(live) if state in ("in_progress", "waiting") and live else None})
    return out


def _pb_time(text) -> float | None:
    """PocketBase stores times as UTC text, ``2026-10-31 09:00:00.123Z``."""
    try:
        return float(calendar.timegm(time.strptime(str(text)[:19].replace("T", " "), "%Y-%m-%d %H:%M:%S")))
    except ValueError:
        return None


def _number(value) -> float | None:
    return float(value) if isinstance(value, (int, float)) and not isinstance(value, bool) else None


def beszel_read(path: Path, now: float) -> dict[str, dict]:
    """``{system name: {status, updated, info, series}}`` from Beszel's database, opened read only (the hub keeps
    writing through WAL, so ``immutable`` would read stale pages); series is the last hour of one-minute CPU."""
    db = sqlite3.connect("file:" + quote(str(Path(path).expanduser().resolve())) + "?mode=ro", uri=True, timeout=5)
    try:
        systems = db.execute("SELECT id, name, status, updated, info FROM systems").fetchall()
        since = time.strftime("%Y-%m-%d %H:%M:%S", time.gmtime(now - 3600))
        series: dict[str, list] = {}
        for system, stats, created in db.execute(
                "SELECT system, stats, created FROM system_stats WHERE type = '1m' AND created >= ? ORDER BY created",
                (since,)):
            value, at = json.loads(stats or "{}"), _pb_time(created)
            if isinstance(value, dict) and at:
                series.setdefault(system, []).append((at, _number(value.get("cpu"))))
    finally:
        db.close()
    out = {}
    for key, name, status, updated, info in systems:
        info = json.loads(info or "{}")
        out[str(name)] = {"status": str(status or ""), "updated": _pb_time(updated),
                          "info": info if isinstance(info, dict) else {}, "series": series.get(key, [])}
    return out


def beszel_path(base: Path | None = None) -> Path | None:
    devices = settings._config(base).get("devices")
    metrics = devices.get("metrics") if isinstance(devices, dict) else None
    path = metrics.get("beszel") if isinstance(metrics, dict) else None
    return Path(str(path)).expanduser() if path else None


def cpu_hot(series: list, now: float) -> bool:
    """CPU over 90% for five minutes by the one-minute samples; without samples the current value decides."""
    points = sorted(point for point in series if point[1] is not None)
    if not points:
        return True
    start = None
    for at, value in reversed(points):
        if value <= 90:
            break
        start = at
    return start is not None and now - start >= CPU_HOT_S


def _metric_reasons(metric: dict, now: float) -> tuple[bool, list[str]]:
    """(down, warnings) of one Beszel system: down, or CPU / memory / disk / temperature over its line, or quiet."""
    updated = metric.get("updated")
    if metric.get("status") == "down":
        return True, ["down"]
    info = metric.get("info") or {}
    cpu = _number(info.get("cpu"))
    checks = ((cpu is not None and cpu > 90 and cpu_hot(metric.get("series") or [], now), "cpu"),
              ((_number(info.get("mp")) or 0) > 90, "memory"), ((_number(info.get("dp")) or 0) > 85, "disk"),
              ((_number(info.get("dt")) or 0) > 85, "temp"), (metric.get("status") == "pending", "pending"),
              (bool(updated) and now - updated > QUIET_S, "quiet"))
    return False, [word for failed, word in checks if failed]


def _probe_reasons(dev: settings.Device, probe: dict, min_free_gb: int) -> tuple[bool, list[str]]:
    if "error" in probe:
        return True, ["unreachable"]
    checks = ((not probe.get("ready"), "runner"), (not probe.get("serve"), "service"),
              (int(probe.get("free_gb") or 0) < min_free_gb, "disk"), (int(probe.get("running") or 0) > dev.cap, "over"))
    return False, [word for failed, word in checks if failed]


def machine(dev: settings.Device, probe: dict, metric: dict | None, now: float, min_free_gb: int) -> dict:
    """One device: state ok / warn / down with the first reason as one word, its load and, with metrics, CPU,
    memory, disk and temperature percents."""
    down, why = _probe_reasons(dev, probe, min_free_gb)
    info = (metric or {}).get("info") or {}
    if metric is not None and not down:
        metric_down, more = _metric_reasons(metric, now)
        down, why = metric_down, (more if metric_down else why + [w for w in more if w not in why])
    return {"name": dev.name, "state": "down" if down else "warn" if why else "ok", "why": why[0] if why else None,
            "cpu": None if down else _number(info.get("cpu")), "memory": None if down else _number(info.get("mp")),
            "disk": None if down else _number(info.get("dp")), "temp": None if down else _number(info.get("dt")),
            "load": _number(probe.get("load")), "cores": probe.get("cores"), "running": probe.get("running"),
            "cap": dev.cap, "freeGb": probe.get("free_gb"), "metricsAt": (metric or {}).get("updated")}


def machines(worker_root: Path, metrics: dict[str, dict] | None, now: float) -> list[dict]:
    """``devices.workers`` in config order, down first, then warnings, then the rest."""
    base = worker_root.parent
    worker = settings.section(base)
    devs = settings.devices(base)
    probes = bidding.probe_all(devs, worker_root, repo=None, runner=runners.default_name(worker))
    min_free = int(settings.setting("min_free_gb", worker=worker))
    rows = [machine(dev, probe, (metrics or {}).get(dev.name) or (metrics or {}).get(dev.host), now, min_free)
            for dev, probe in zip(devs, probes)]
    order = ("down", "warn", "ok")
    return sorted(rows, key=lambda row: order.index(row["state"]))
