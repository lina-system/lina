"""``lina cron ops``: the operations jobs that ran as launchd agents (``ai.hermes.learned-card``, the hourly
``learned collect``) and timers (the daily agent review, the daily undo snapshot) as script-only cron jobs of the
current profile (lina#38, lina#484).

Each job is a ``.py`` file under ``<HERMES_HOME>/scripts/`` that runs one ``lina`` command, or one function of this
module, in the cron worker's own interpreter, with ``no_agent`` (no model call) and ``deliver: local`` (the output
is saved, not sent). The learned card reaches a person only through its own ``--send``; the daily review's model
turns run in each agent's own gateway, never in the job. Installing again updates the jobs in place; ``--remove``
deletes them and their scripts. A job script run by hand with ``--dry-run`` shows what it would do.
"""

from __future__ import annotations

import datetime as dt
import sys
import urllib.error
from dataclasses import dataclass
from pathlib import Path

from hermes_constants import get_hermes_home


@dataclass(frozen=True)
class OpsJob:
    name: str
    schedule: str
    argv: tuple[str, ...]
    replaces: str
    entry: str = ""

    @property
    def command(self) -> str:
        return f"cron_ops.{self.entry}()" if self.entry else f"lina {' '.join(self.argv)}"


JOBS = (
    OpsJob("learned-collect", "5 * * * *", ("learned", "collect"), "learned collect, hourly at :05"),
    OpsJob("learned-card", "5 11 * * *", ("learned", "card", "--day", "yesterday", "--send"),
           "launchd ai.hermes.learned-card, daily 11:05"),
    OpsJob("agent-review", "30 23 * * *", (), "the old daily review timer", entry="start_reviews"),
    OpsJob("undo-snapshot", "45 23 * * *", ("undo", "snapshot"), "the old daily undo snapshot timer"),
)

_SCRIPT = '''"""Cron job {name}: `lina {command}` (written by `lina cron ops`)."""
import sys

from hermes_cli.main import main

sys.argv = ["lina", *{argv!r}, *[a for a in sys.argv[1:] if a == "--dry-run"]]
main()
'''

_ENTRY_SCRIPT = '''"""Cron job {name}: `{command}` (written by `lina cron ops`)."""
import sys

from hermes_cli.cron_ops import {entry}

sys.exit({entry}(dry_run="--dry-run" in sys.argv[1:]))
'''

REVIEW_MESSAGE = (
    "Daily heart review for {day}. Run `lina heart review-input --date {day}`, then follow the bot-heart skill's "
    "daily review (templates/heart-review.md): record only supported missed moments, apply at most two checked "
    "self-note changes with `lina heart self-apply`, and record the result, including zero changes.")


def _script_name(job: OpsJob) -> str:
    return f"lina-{job.name}.py"


def _write_script(job: OpsJob) -> str:
    scripts = get_hermes_home() / "scripts"
    scripts.mkdir(parents=True, exist_ok=True)
    name = _script_name(job)
    template = _ENTRY_SCRIPT if job.entry else _SCRIPT
    (scripts / name).write_text(template.format(name=job.name, command=" ".join(job.argv), argv=list(job.argv),
                                                entry=job.entry), encoding="utf-8")
    return name


def review_agents() -> list[tuple[str, Path]]:
    """Agents the default gateway serves that keep a heart journal or a self note."""
    from hermes_cli.profiles import profiles_to_serve
    return [(name, home) for name, home in profiles_to_serve(multiplex=True)
            if (home / "heart").is_dir() or (home / "SOUL.self.md").is_file()]


def _listener_port() -> int:
    """The default profile's api_server port: ``platforms.api_server.extra.port``, then its ``.env``, then 8642."""
    from agent.secret_scope import load_env_file
    from hermes_cli.config import cfg_get, load_config
    from hermes_constants import get_default_hermes_root, reset_hermes_home_override, set_hermes_home_override
    root = get_default_hermes_root()
    token = set_hermes_home_override(str(root))
    try:
        port = cfg_get(load_config(), "platforms", "api_server", "extra", "port", default=None)
    finally:
        reset_hermes_home_override(token)
    return int(port or load_env_file(root / ".env").get("API_SERVER_PORT") or 8642)


def start_reviews(*, dry_run: bool = False) -> int:
    """Start one review turn per agent through its gateway's Runs API; the turn runs in the gateway.

    The idempotency key names the agent and the day, so a second start on the same day replays the first run."""
    from agent.secret_scope import load_env_file
    from hermes_cli.subcommands.peer import _request
    day = dt.date.today().isoformat()
    base = f"http://127.0.0.1:{_listener_port()}"
    agents, failed = review_agents(), 0
    if not agents:
        print("No agent keeps a heart journal; no review started")
    for name, home in agents:
        url = base + ("" if name == "default" else f"/p/{name}") + "/v1/runs"
        key = f"lina-daily-review-{name}-{day}"
        if dry_run:
            print(f"would start {name}: POST {url} (Idempotency-Key {key})")
            continue
        secret = load_env_file(home / ".env").get("API_SERVER_KEY", "")
        if not secret:
            print(f"{name}: no API_SERVER_KEY in its .env; review not started", file=sys.stderr)
            failed += 1
            continue
        try:
            result = _request(url, secret, method="POST", body={"input": REVIEW_MESSAGE.format(day=day)},
                              headers={"Idempotency-Key": key})
        except (urllib.error.URLError, TimeoutError, OSError, RuntimeError) as exc:
            print(f"{name}: the gateway did not take the review ({exc})", file=sys.stderr)
            failed += 1
            continue
        again = " (already started today)" if result.get("replayed") else ""
        print(f"started {name}: run {result.get('run_id')}{again}")
    return 1 if failed else 0


def _by_name() -> dict[str, dict]:
    from cron.jobs import list_jobs
    return {j.get("name"): j for j in list_jobs(include_disabled=True)}


def install() -> list[tuple[str, str]]:
    from cron.jobs import create_job, update_job
    existing, done = _by_name(), []
    for job in JOBS:
        script = _write_script(job)
        fields = {"schedule": job.schedule, "script": script, "no_agent": True, "deliver": "local"}
        if job.name in existing:
            update_job(existing[job.name]["id"], fields)
            done.append((job.name, "updated"))
        else:
            create_job(prompt=None, name=job.name, **fields)
            done.append((job.name, "created"))
    return done


def remove() -> list[str]:
    from cron.jobs import remove_job
    existing, removed = _by_name(), []
    for job in JOBS:
        if job.name in existing and remove_job(existing[job.name]["id"]):
            removed.append(job.name)
        (get_hermes_home() / "scripts" / _script_name(job)).unlink(missing_ok=True)
    return removed


def cmd_ops(args) -> int:
    if getattr(args, "remove", False):
        for name in remove():
            print(f"removed {name}")
        return 0
    for name, what in install():
        job = next(j for j in JOBS if j.name == name)
        print(f"{what} {name}: `{job.command}` at '{job.schedule}' (was {job.replaces})")
    return 0
