"""Copy a legacy profile's cron jobs into a migrated agent's job store, every job paused (lina#639).

The legacy ``<profile dir>/cron/jobs.json`` is only read, once, into a sha256-receipted snapshot
under ``--work-dir``; the import reads that snapshot. Jobs keep their legacy id, so a rerun finds
them already present and changes nothing. A copy must not reach a live chat: ``deliver`` becomes
``local`` and the legacy value is kept in the job's ``notes``.

The scripts the jobs name (``script``, ``monitor_script``) are snapshotted from the legacy
``scripts/`` beside ``jobs.json`` and copied from there into the agent's ``scripts/`` (lina#641). They
are only read and written, never run; an existing different file in the agent is never overwritten.
"""

from __future__ import annotations

import hashlib
import json
from pathlib import Path
from tempfile import TemporaryDirectory
from typing import Any

from hermes_cli.migrate_memory_copy import SnapshotError, _digest, _safe

PAUSED_REASON = "migrated copy"
_SCRIPT_FIELDS = ("script", "monitor_script")
_NOT_IN_SNAPSHOT = {"status": "not_in_snapshot",
                    "detail": "the snapshot in --work-dir predates script copying; use a new --work-dir"}
# Authored fields a copy must not carry live: each names a legacy chat or a legacy machine path.
_NOT_CARRIED = {
    "deliver": "replaced by local: a copied chat id must not be live in a copy",
    "failure_deliver": "dropped: failure notices follow deliver, now local",
    "origin": "dropped: names the legacy chat the job was created from",
    "interpreter": "dropped: a path on the legacy machine; set a new one with `lina cron edit`",
}


def _snapshot_script(profile_dir: Path, name: str, staging: Path) -> dict[str, Any]:
    """Copy one named script from the legacy ``scripts/`` into the staging snapshot."""
    relative = Path(name)
    if relative.is_absolute() or name.startswith("~") or ".." in relative.parts or not relative.parts:
        return {"status": "refused", "detail": "not a path inside the legacy profile's scripts folder"}
    try:
        source = _safe(profile_dir / "scripts" / relative)
    except SnapshotError as exc:
        return {"status": "refused", "detail": exc.reason}
    if not source.exists():
        return {"status": "missing", "detail": f"{source} does not exist in the legacy profile"}
    if not source.is_file():
        return {"status": "refused", "detail": f"{source} is not a regular file"}
    data = source.read_bytes()
    copy = staging / "scripts" / relative
    copy.parent.mkdir(parents=True, exist_ok=True)
    copy.write_bytes(data)
    return {"status": "snapshotted", "source": str(source), "sha256": hashlib.sha256(data).hexdigest(),
            "size": len(data), "mode": source.stat().st_mode & 0o777}


def snapshot_jobs(source_home: Path, profile: str, work_dir: Path) -> tuple[Path, dict[str, Any]]:
    """Publish ``work_dir/cron-snapshot/`` (``jobs.json`` and the scripts its jobs name) once, or
    verify and reuse it unchanged."""
    source_home, work_dir = _safe(source_home), _safe(work_dir)
    profile_dir = _safe(source_home if profile == "default" else source_home / "profiles" / profile)
    source = _safe(profile_dir / "cron" / "jobs.json")
    root = _safe(work_dir / "cron-snapshot")
    copy, receipt_path = root / "jobs.json", root / "receipt.json"
    if receipt_path.exists():
        receipt = json.loads(receipt_path.read_text(encoding="utf-8"))
        if receipt.get("source") != str(source):
            raise SnapshotError(root, "source request provenance differs; refusing overwrite")
        if _digest(_safe(copy)) != receipt.get("sha256"):
            raise SnapshotError(copy, "snapshot digest differs; refusing overwrite")
        return copy, receipt
    if not source.is_file():
        raise SnapshotError(source, "legacy jobs file is not a regular file")
    data = source.read_bytes()
    source_sha256 = hashlib.sha256(data).hexdigest()
    work_dir.mkdir(parents=True, exist_ok=True)
    with TemporaryDirectory(prefix=".cron-snapshot-", dir=work_dir) as temporary:
        staging = Path(temporary) / "cron-snapshot"
        staging.mkdir()
        (staging / "jobs.json").write_bytes(data)
        if _digest(source) != source_sha256:
            raise SnapshotError(source, "file source changed during snapshot; retry with a stable file")
        names = sorted({str(job[field]) for job in _legacy_jobs(staging / "jobs.json")
                        for field in _SCRIPT_FIELDS if job.get(field)})
        receipt = {"source": str(source), "destination": str(copy), "method": "file",
                   "source_sha256": source_sha256, "sha256": _digest(staging / "jobs.json"),
                   "size": len(data), "consistency": "unchanged-file",
                   "scripts": {name: _snapshot_script(profile_dir, name, staging) for name in names}}
        (staging / "receipt.json").write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")
        for path in staging.rglob("*"):
            if path.is_file():
                path.chmod(0o444)
        staging.rename(root)
    return copy, receipt


def _legacy_jobs(copy: Path) -> list[dict[str, Any]]:
    from cron.jobs import _parse_jobs_file

    data, _ = _parse_jobs_file(copy)
    jobs = data.get("jobs") if isinstance(data, dict) else data
    if not isinstance(jobs, list):
        raise ValueError("legacy jobs file holds no job list")
    return [job for job in jobs if isinstance(job, dict)]


def _authored(job: dict[str, Any]) -> dict[str, Any]:
    from cron.job_definition import JOB_DEFINITION_FIELDS

    authored = {key: job[key] for key in JOB_DEFINITION_FIELDS if key in job and key not in _NOT_CARRIED}
    authored["deliver"] = "local"
    if isinstance(job.get("repeat"), dict):
        authored["repeat"] = {"times": job["repeat"].get("times")}
    return authored


def _notes(job: dict[str, Any], profile: str) -> str:
    lines = [f"Migrated copy of legacy profile {profile!r} job {job['id']!r}, paused on import."]
    lines += [f"Legacy {key}: {json.dumps(job[key], ensure_ascii=False)}"
              for key in _NOT_CARRIED if job.get(key) not in (None, "")]
    lines.append(f"Legacy state: {job.get('state') or 'scheduled'}, enabled={bool(job.get('enabled', True))}")
    return "\n".join(lines)


def _left_out(job: dict[str, Any]) -> list[dict[str, str]]:
    from cron.job_definition import JOB_DEFINITION_FIELDS

    rows = [{"field": key, "reason": reason} for key, reason in _NOT_CARRIED.items()
            if job.get(key) not in (None, "") and not (key == "deliver" and job[key] == "local")]
    rows += [{"field": key, "reason": "scheduler state of the legacy store, not part of the job definition"}
             for key in sorted(job) if key not in JOB_DEFINITION_FIELDS | {"id", "repeat"} | set(_NOT_CARRIED)
             and job[key] not in (None, "", [], {})]
    if isinstance(job.get("repeat"), dict) and job["repeat"].get("completed"):
        rows.append({"field": "repeat.completed", "reason": "the copy counts its own runs from zero"})
    return rows


def _copy_script(name: str, row: dict[str, Any], root: Path, destination: Path) -> dict[str, Any]:
    """Copy one snapshotted script into the agent's ``scripts/``; an existing file is never replaced."""
    if row.get("status") != "snapshotted":
        return {"status": row.get("status"), "detail": row.get("detail")}
    copy = _safe(root / "scripts" / name)
    if _digest(copy) != row["sha256"]:
        return {"status": "refused", "detail": f"{copy} differs from its snapshot receipt"}
    try:
        target = _safe(destination / "scripts" / name)
    except SnapshotError as exc:
        return {"status": "refused", "detail": exc.reason}
    if target.exists():
        if target.is_file() and _digest(target) == row["sha256"]:
            return {"status": "already_present", "sha256": row["sha256"], "path": str(target)}
        return {"status": "kept_existing", "path": str(target),
                "detail": "a different file is already there; it was not overwritten"}
    target.parent.mkdir(parents=True, exist_ok=True)
    with target.open("xb") as stream:
        stream.write(copy.read_bytes())
    target.chmod(row["mode"] & 0o755 | 0o600)
    return {"status": "copied", "sha256": _digest(target), "path": str(target)}


def _resolution(job: dict[str, Any], copies: dict[str, dict[str, Any]]) -> dict[str, Any]:
    """Resolve script and workdir the way a tick would, inside the destination agent's scope."""
    from cron import scheduler as sched
    from cron.scheduler_script import _resolve_script_path

    result: dict[str, Any] = {}
    for field in _SCRIPT_FIELDS:
        if job.get(field):
            path, error = _resolve_script_path(str(job[field]))
            result[field] = {"path": job[field], "resolves": path is not None,
                             "detail": str(path) if path is not None else error,
                             "copy": copies.get(str(job[field]), _NOT_IN_SNAPSHOT)}
    if job.get("workdir"):
        resolved = sched._resolve_job_workdir(job, str(job["id"]))
        result["workdir"] = {"path": job["workdir"], "resolves": resolved is not None,
                             "detail": resolved or "directory does not exist in the new home"}
    return result


def _schedule_text(job: dict[str, Any]) -> str:
    schedule = job.get("schedule")
    if isinstance(schedule, dict):
        return str(job.get("schedule_display") or schedule.get("display") or schedule.get("expr")
                   or schedule.get("run_at") or schedule)
    return str(schedule)


def migrate(source_home: Path, profile: str, agent: str, home: Path, work_dir: Path) -> dict[str, Any]:
    from cron.job_definition import import_job_definitions
    from cron.jobs import load_jobs
    from cron.scheduler_provider import _profile_cron_scope

    destination = _safe(home / "profiles" / agent)
    if not destination.is_dir():
        raise ValueError(f"destination agent home {destination} does not exist; run `lina migrate memory` first")
    copy, receipt = snapshot_jobs(source_home, profile, work_dir)
    legacy = _legacy_jobs(copy)
    copies = {name: _copy_script(name, row, copy.parent, destination)
              for name, row in (receipt.get("scripts") or {}).items()}
    rows: list[dict[str, Any]] = []
    shipped: dict[str, dict[str, Any]] = {}
    notes: dict[str, str] = {}
    with _profile_cron_scope(destination):
        present = {job.get("id") for job in load_jobs()}
        for job in legacy:
            job_id = job.get("id")
            row = {"id": job_id, "name": job.get("name"),
                   "kind": "script" if job.get("no_agent") else "prompt",
                   "schedule": _schedule_text(job),
                   "legacy_state": "enabled" if job.get("enabled", True) and job.get("state") != "paused" else "paused",
                   "legacy_deliver": job.get("deliver") or "local"}
            if not isinstance(job_id, str) or not job_id or job_id in shipped:
                rows.append({**row, "status": "skipped",
                             "reason": "no legacy id" if not job_id else "duplicate legacy id"})
                continue
            row.update(_resolution(job, copies))
            row["left_out"] = _left_out(job)
            if job_id in present:
                row["status"] = "already_present"
            else:
                row["status"] = "imported"
                shipped[job_id] = _authored(job)
                notes[job_id] = _notes(job, profile)
            rows.append(row)
        import_job_definitions(shipped, paused_reason=PAUSED_REASON, notes=notes)
    counts = {status: sum(row["status"] == status for row in rows)
              for status in ("imported", "already_present", "skipped")}
    return {"format": 1, "profile": profile, "agent": agent, "destination_home": str(destination),
            "copy": receipt, "legacy_jobs": len(legacy), "counts": counts, "scripts": copies, "jobs": rows,
            "ok": counts["skipped"] == 0}


def run(args) -> int:
    """Print the JSON report and keep it beside the snapshot; input failures never claim success."""
    from hermes_cli.migrate_memory import _paths

    work = None
    try:
        source, home, work = _paths(args)
        report = migrate(source, args.profile, args.agent, home, work)
    except (OSError, ValueError, RuntimeError) as exc:
        report = {"format": 1, "ok": False, "error": str(exc) if isinstance(exc, ValueError) else type(exc).__name__}
    text = json.dumps(report, ensure_ascii=False, indent=2, default=str)
    if work is not None and work.is_dir():
        _safe(work / "cron-migration-report.json").write_text(text + "\n", encoding="utf-8")
    print(text)
    return 0 if report["ok"] else 1
