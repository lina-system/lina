"""A legacy profile's cron/jobs.json shaped like a real store, for migration tests and the CLI proof."""

from __future__ import annotations

import json
from datetime import timedelta
from pathlib import Path


def _job(job_id: str, name: str, schedule: str, *, enabled: bool = True, **fields) -> dict:
    from cron.jobs import _hermes_now, parse_schedule

    parsed = parse_schedule(schedule)
    now = _hermes_now()
    job = {
        "id": job_id, "name": name, "prompt": fields.pop("prompt", f"{name} prompt"),
        "skills": [], "skill": None, "model": None, "provider": None, "base_url": None,
        "script": None, "no_agent": False, "context_from": None,
        "schedule": parsed, "schedule_display": parsed.get("display", schedule),
        "repeat": {"times": 1 if parsed["kind"] == "once" else None, "completed": 3},
        "enabled": enabled, "state": "scheduled" if enabled else "paused",
        "paused_at": None if enabled else (now - timedelta(days=2)).isoformat(),
        "paused_reason": None if enabled else "paused by hand",
        "created_at": (now - timedelta(days=30)).isoformat(),
        "next_run_at": (now + timedelta(hours=1)).isoformat() if enabled else None,
        "last_run_at": (now - timedelta(hours=1)).isoformat(), "last_status": "ok",
        "last_error": None, "deliver": "local", "origin": None, "workdir": None,
    }
    job.update(fields)
    return job


def legacy_cron_jobs() -> list[dict]:
    """Ten jobs: scripts and prompts, enabled and disabled, one-shots, deliver telegram/origin/local/bot-chat."""
    origin = {"platform": "telegram", "chat_id": "-1001234567890", "chat_name": "ops"}
    return [
        _job("a1b2c3d4e5f6", "morning brief", "0 9 * * *", deliver="telegram:-1001234567890", origin=origin),
        _job("b2c3d4e5f6a1", "reply where asked", "every 2h", deliver="origin", origin=origin),
        _job("c3d4e5f6a1b2", "paused notes", "every 1d", enabled=False, deliver="local"),
        _job("d4e5f6a1b2c3", "disk report", "30m", script="disk_report.py", no_agent=True, prompt="",
             deliver="bot-chat", monitor_script="disk_watch.sh"),
        _job("e5f6a1b2c3d4", "gone script", "every 6h", script="legacy_only.sh", no_agent=True, prompt="",
             deliver="telegram"),
        _job("f6a1b2c3d4e5", "feed digest", "every 3h", script="collect_feeds.py", deliver="bot-chat:beta",
             interpreter="/home/legacy/venv/bin/python3"),
        _job("0a1b2c3d4e5f", "renew reminder", "2026-01-01T09:00:00+00:00", deliver="telegram:-1001234567890",
             enabled=False, state="completed"),
        _job("1a2b3c4d5e6f", "launch check", "2027-03-01T09:00:00+00:00", skills=["release-check"],
             skill="release-check", deliver="local"),
        _job("2a3b4c5d6e7f", "repo sweep", "every 12h", workdir="/home/legacy/code/repo",
             failure_deliver="telegram:42", deliver="origin,local", origin=origin),
        _job("3a4b5c6d7e8f", "weekly review", "every monday 9am", enabled=False,
             deliver="bot-chat", model="legacy-model", provider="openrouter"),
    ]


# The legacy scripts folder: disk_report.py differs from the new agent's own copy, and the monitor
# script disk_watch.sh is missing.
LEGACY_SCRIPTS = {
    "legacy_only.sh": "#!/bin/sh\necho legacy\n",
    "collect_feeds.py": "print('feeds')\n",
    "disk_report.py": "print('legacy disk')\n",
}


def legacy_cron_home(root: Path, profile: str = "alpha") -> Path:
    """Write the ten jobs and their scripts into ``root``'s ``profile`` (``default`` is the root profile)."""
    profile_dir = root if profile == "default" else root / "profiles" / profile
    (profile_dir / "cron").mkdir(parents=True, exist_ok=True)
    (profile_dir / "scripts").mkdir(exist_ok=True)
    for name, text in LEGACY_SCRIPTS.items():
        (profile_dir / "scripts" / name).write_text(text, encoding="utf-8")
    (profile_dir / "config.yaml").write_text("model: legacy\n", encoding="utf-8")
    (profile_dir / "cron" / "jobs.json").write_text(
        json.dumps({"jobs": legacy_cron_jobs(), "updated_at": "2026-11-01T00:00:00+00:00"}, indent=2) + "\n",
        encoding="utf-8")
    return root


def new_agent_home(root: Path, agent: str) -> Path:
    """A migrated agent home holding one of the legacy scripts, as a person would after the move."""
    home = root / "profiles" / agent
    (home / "scripts").mkdir(parents=True, exist_ok=True)
    (home / "config.yaml").write_text("model: new\n", encoding="utf-8")
    (home / "scripts" / "disk_report.py").write_text("print('disk ok')\n", encoding="utf-8")
    return home
