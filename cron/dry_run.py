"""``hermes cron run <id> --dry-run``: resolve a job the way a tick would and execute nothing (lina#639).

No script runs, no model is called and nothing is sent. Delivery targets are classified from the
job record alone: no code in ``cron/scheduler_delivery*.py`` runs. Skills are looked up without
their inline-shell preprocessing, and a pre-run script's output is replaced by a placeholder.
"""

from __future__ import annotations

import importlib.util
import json
from typing import Any

_SCRIPT_PLACEHOLDER = "[dry run: the pre-run script was not run; its output is injected here]"


def _schedule(job: dict[str, Any]) -> dict[str, Any]:
    from cron.jobs import compute_next_run

    schedule = job.get("schedule")
    return {"display": job.get("schedule_display"),
            "kind": schedule.get("kind") if isinstance(schedule, dict) else None,
            "next_run_at": job.get("next_run_at"),
            "next_if_resumed": compute_next_run(schedule, job.get("last_run_at"), timezone=job)}


def _skills(job: dict[str, Any], problems: list[str]) -> list[dict[str, Any]]:
    from agent.skill_bundles import resolve_bundle_command_key
    from agent.skill_utils import normalize_skill_lookup_name
    from cron.scheduler_prompt import _job_skill_names
    from tools.skills_tool import skill_view

    rows = []
    for name in _job_skill_names(job):
        if resolve_bundle_command_key(name.lstrip("/")):
            rows.append({"name": name, "found": True, "kind": "bundle"})
            continue
        payload = json.loads(skill_view(normalize_skill_lookup_name(name), preprocess=False))
        found = isinstance(payload, dict) and bool(payload.get("success"))
        rows.append({"name": name, "found": found, "kind": "skill"})
        if not found:
            problems.append(f"unknown skill {name!r}: {payload.get('error') or 'not found'}")
        elif payload.get("setup_needed") or payload.get("readiness_status") == "setup_needed":
            problems.append(f"skill {name!r} is not ready: required setup incomplete")
    return rows


def _scripts(job: dict[str, Any], problems: list[str]) -> dict[str, Any]:
    from cron.scheduler_script import _resolve_script_path, _script_argv

    rows: dict[str, Any] = {}
    for field in ("script", "monitor_script"):
        if not job.get(field):
            continue
        path, error = _resolve_script_path(str(job[field]))
        row: dict[str, Any] = {"path": job[field], "resolved": str(path) if path else None}
        if path is not None:
            try:
                argv, _, error = _script_argv(path, job.get("interpreter"))
            except RuntimeError as exc:
                argv, error = None, str(exc)
            row.update(interpreter=argv[0] if argv else None, argv=argv)
        if error:
            row["error"] = error
            problems.append(f"{field}: {error}")
        rows[field] = row
    return rows


def _prompt(job: dict[str, Any], problems: list[str], warnings: list[str]) -> str | None:
    from cron import scheduler as sched
    from cron.scheduler_prompt import _build_job_prompt, _guard_job_credential_exfil

    try:
        _guard_job_credential_exfil(job)
    except RuntimeError as exc:
        problems.append(str(exc))
    if job.get("no_agent"):
        return None
    sources = job.get("context_from") or []
    sources = [sources] if isinstance(sources, str) else list(sources)
    # Invalid ids are skipped at run time; dropping them here keeps the delivery module's log helper out.
    valid = [s for s in sources if isinstance(s, str) and (s.strip().lower() == "self" or (
        s and all(c in "0123456789abcdef" for c in s)))]
    if len(valid) != len(sources):
        warnings.append("context_from names invalid job ids; a run skips them")
    # Skills are reported by _skills; loading them here would run their inline-shell preprocessing.
    prompt_job = {**job, "skills": [], "skill": None, "context_from": valid or None}
    try:
        return _build_job_prompt(
            prompt_job, prerun_script=(True, _SCRIPT_PLACEHOLDER) if job.get("script") else None)
    except sched.CronPromptInjectionBlocked as exc:
        problems.append(f"the prompt would be blocked by the injection scanner: {exc}")
        return None


def _known_platform(name: str) -> bool:
    from cron.scheduler_delivery import _KNOWN_DELIVERY_PLATFORMS
    from gateway.platform_registry import platform_registry
    from hermes_cli.plugins import discover_plugins

    discover_plugins()
    entry = platform_registry.get(name.lower())
    return name.lower() in _KNOWN_DELIVERY_PLATFORMS or bool(entry and entry.cron_deliver_env_var)


def _target(job: dict[str, Any], part: str, problems: list[str], warnings: list[str]) -> dict[str, Any]:
    lowered = part.lower()
    if lowered == "local":
        return {"deliver": part, "target": "local", "detail": "output is saved, nothing is sent"}
    if lowered == "all":
        return {"deliver": part, "target": "broadcast", "detail": "every platform with a home channel, at run time"}
    if lowered == "bot-chat" or lowered.startswith("bot-chat:"):
        from hermes_cli.profiles import normalize_profile_name, profile_exists

        name = part.split(":", 1)[1].strip() if ":" in part else ""
        if name and not profile_exists(normalize_profile_name(name)):
            problems.append(f"bot-chat delivery profile {name!r} does not exist on this machine")
        return {"deliver": part, "target": "bot-chat", "profile": name or "own profile"}
    if lowered == "origin":
        origin = job.get("origin")
        if isinstance(origin, dict) and origin.get("platform") and origin.get("chat_id"):
            return {"deliver": part, "target": "origin", "platform": origin["platform"], "chat_id": str(origin["chat_id"])}
        warnings.append("deliver=origin without an origin: a run falls back to a home channel, or sends nothing")
        return {"deliver": part, "target": "origin", "detail": "no origin on the job"}
    platform, _, chat = part.partition(":")
    if not _known_platform(platform):
        problems.append(f"delivery platform {platform!r} is not a known cron delivery target")
    return {"deliver": part, "target": "explicit" if chat else "home channel", "platform": platform,
            **({"chat_id": chat} if chat else {})}


def _delivery(job: dict[str, Any], problems: list[str], warnings: list[str]) -> dict[str, Any]:
    raw = job.get("deliver")
    value = ",".join(str(p).strip() for p in raw) if isinstance(raw, (list, tuple)) else str(raw or "")
    parts = [part.strip() for part in value.split(",") if part.strip()] or ["local"]
    targets = [_target(job, part, problems, warnings) for part in parts]
    # HOOK(lina#638): the outbound lock lives in agent/outbound_lock.py, which this base does not
    # have. When it lands, pass each non-local target through its check here and add its refusals
    # to ``problems``, so a dry run reports the target after the lock.
    lock = ("present but not wired into the dry run" if importlib.util.find_spec("agent.outbound_lock")
            else "not available on this build (lina#638)")
    return {"targets": targets, "outbound_lock": lock}


def dry_run(job: dict[str, Any]) -> dict[str, Any]:
    """Resolve *job* like a tick, book a ``dry_run`` ledger row and return the report.

    ``ok`` is False when something would fail at run time: a missing script or interpreter, an
    unknown skill, an unresolvable delivery target or a blocked prompt."""
    from cron import scheduler as sched
    from cron.executions import record_dry_run
    from cron.jobs import effective_job_state

    problems: list[str] = []
    warnings: list[str] = []
    workdir = sched._resolve_job_workdir(job, str(job["id"]))
    if job.get("workdir") and workdir is None:
        warnings.append(f"workdir {job['workdir']!r} does not resolve; a run goes ahead without it")
    report = {
        "id": job["id"], "name": job.get("name"), "state": effective_job_state(job),
        "kind": "script" if job.get("no_agent") else "prompt",
        "schedule": _schedule(job), "skills": _skills(job, problems),
        "workdir": {"configured": job.get("workdir"), "resolved": workdir},
        "scripts": _scripts(job, problems),
        "monitor_url": job.get("monitor_url"),
        "delivery": _delivery(job, problems, warnings),
        "prompt": _prompt(job, problems, warnings),
    }
    record = record_dry_run(job["id"], error="; ".join(problems) or None)
    return {**report, "executed": False, "problems": problems, "warnings": warnings,
            "execution_id": record["id"], "ok": not problems}
