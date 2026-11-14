"""``hermes cron run <id> --dry-run``: resolve a job the way a tick would and execute nothing (lina#639).

No script runs, no model is called and nothing is sent. Delivery targets are classified from the
job record; when ``outbound.allow`` is set they are also resolved by the real delivery's resolver
(home channels included, no send) and checked against it (lina#648). Skills are only located and their
frontmatter read: no preprocessing, no dependency install (``skill_view`` would ``pm.ensure`` them),
no usage bump. A pre-run script's output is replaced by a placeholder.
"""

from __future__ import annotations

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


def _find_skill(name: str) -> tuple[Any, str | None]:
    """``(SKILL.md path, None)`` or ``(None, error)``, by the lookup ``skill_view`` uses and nothing more."""
    from agent.skill_utils import parse_qualified_name
    from tools.skills_tool import locate_skill, skill_lookup_path_error, skill_search_dirs

    if error := skill_lookup_path_error(name):
        return None, error
    local_category_name = None
    if ":" in name:
        from hermes_cli.plugins import discover_plugins, get_plugin_manager

        discover_plugins()
        path = get_plugin_manager().find_plugin_skill(name)
        if path is not None and path.exists():
            return path, None
        namespace, bare = parse_qualified_name(name)
        local_category_name = f"{namespace}/{bare}"
        if error := skill_lookup_path_error(local_category_name):
            return None, error
    error, _, skill_md = locate_skill(name, local_category_name, skill_search_dirs()[0])
    if error is not None:
        return None, json.loads(error).get("error") or "not found"
    return skill_md, None


def _missing_deps(frontmatter: dict[str, Any], warnings: list[str], name: str) -> list[str]:
    """Declared ``deps`` not installed now. Checked, never installed: a run installs them when it
    loads the skill."""
    from pm.install import _installed_location, _lockfile, current_target
    from pm.registry import get_package

    declared = frontmatter.get("deps") or []
    declared = [declared] if isinstance(declared, str) else declared if isinstance(declared, list) else []
    missing = []
    for dep in (str(d).strip() for d in declared if str(d).strip()):
        try:
            # heal=False: pm.is_installed would restore a store binary's exec bit in place.
            installed = _installed_location(get_package(dep), _lockfile(), current_target(), heal=False) is not None
        except Exception as exc:
            warnings.append(f"skill {name!r} declares dependency {dep!r}, which cannot be checked: {exc}")
            continue
        if not installed:
            missing.append(dep)
            warnings.append(f"skill {name!r} declares dependency {dep!r}, which is not installed; "
                            "a run would try to install it")
    return missing


def _skills(job: dict[str, Any], problems: list[str], warnings: list[str]) -> list[dict[str, Any]]:
    from agent.skill_bundles import resolve_bundle_command_key
    from agent.skill_utils import normalize_skill_lookup_name, skill_matches_platform
    from cron.scheduler_prompt import _job_skill_names
    from tools.skills_tool import _is_skill_disabled, load_env
    from tools.skills_tool_plugin import _safe_frontmatter
    from tools.skills_tool_setup import _get_required_environment_variables, _is_env_var_persisted

    rows = []
    for name in _job_skill_names(job):
        if resolve_bundle_command_key(name.lstrip("/")):
            rows.append({"name": name, "found": True, "kind": "bundle"})
            continue
        skill_md, error = _find_skill(normalize_skill_lookup_name(name))
        rows.append({"name": name, "found": skill_md is not None, "kind": "skill"})
        if skill_md is None:
            problems.append(f"unknown skill {name!r}: {error}")
            continue
        frontmatter = _safe_frontmatter(skill_md)
        rows[-1].update(path=str(skill_md), missing_deps=_missing_deps(frontmatter, warnings, name))
        if not skill_matches_platform(frontmatter):
            problems.append(f"skill {name!r} is not supported on this platform")
        if _is_skill_disabled(str(frontmatter.get("name") or skill_md.parent.name)):
            problems.append(f"skill {name!r} is disabled")
        required = [e["name"] for e in _get_required_environment_variables(frontmatter) if not e.get("optional")]
        if required:
            env = load_env()
            if unset := [var for var in required if not _is_env_var_persisted(var, env)]:
                problems.append(f"skill {name!r} is not ready: missing env {', '.join(unset)}")
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
    return {"targets": targets, "outbound_allow": _outbound_allow(job, problems)}


def _outbound_allow(job: dict[str, Any], problems: list[str]) -> dict[str, Any]:
    """Each target the delivery would resolve, through the check delivery runs before it sends."""
    from agent.outbound_lock import CONFIG_KEY, allow_entries
    from cron.scheduler_delivery import _resolve_delivery_targets
    from cron.scheduler_delivery_outbound import refused_label, target_refusal

    entries = allow_entries()
    if entries is None:
        return {"allowlist": f"unrestricted: no {CONFIG_KEY} configured", "checked": []}
    checked = []
    for target in _resolve_delivery_targets(job):
        refusal = target_refusal(target, door="cron dry run")
        checked.append({"target": refused_label(target), "allowed": refusal is None})
        if refusal:
            problems.append(f"a run would not deliver: {refusal}")
    return {"allowlist": f"{CONFIG_KEY}: {len(entries)} entries", "checked": checked}


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
        "schedule": _schedule(job), "skills": _skills(job, problems, warnings),
        "workdir": {"configured": job.get("workdir"), "resolved": workdir},
        "scripts": _scripts(job, problems),
        "monitor_url": job.get("monitor_url"),
        "delivery": _delivery(job, problems, warnings),
        "prompt": _prompt(job, problems, warnings),
    }
    record = record_dry_run(job["id"], error="; ".join(problems) or None)
    return {**report, "executed": False, "problems": problems, "warnings": warnings,
            "execution_id": record["id"], "ok": not problems}
