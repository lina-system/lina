"""``lina cron ops``: the operations jobs that ran as launchd agents (``ai.hermes.learned-card``, the hourly
``learned collect``) as script-only cron jobs of the current profile (lina#38).

Each job is a ``.py`` file under ``<HERMES_HOME>/scripts/`` that runs one ``lina`` command in the cron worker's own
interpreter, with ``no_agent`` (no model call) and ``deliver: local`` (the output is saved, not sent). The learned card
reaches a person only through its own ``--send``. Installing again updates the jobs in place; ``--remove`` deletes
them and their scripts.
"""

from __future__ import annotations

from dataclasses import dataclass

from hermes_constants import get_hermes_home


@dataclass(frozen=True)
class OpsJob:
    name: str
    schedule: str
    argv: tuple[str, ...]
    replaces: str


JOBS = (
    OpsJob("learned-collect", "5 * * * *", ("learned", "collect"), "learned collect, hourly at :05"),
    OpsJob("learned-card", "5 11 * * *", ("learned", "card", "--day", "yesterday", "--send"),
           "launchd ai.hermes.learned-card, daily 11:05"),
)

_SCRIPT = '''"""Cron job {name}: `lina {command}` (written by `lina cron ops`)."""
import sys

from hermes_cli.main import main

sys.argv = ["lina", *{argv!r}]
main()
'''


def _script_name(job: OpsJob) -> str:
    return f"lina-{job.name}.py"


def _write_script(job: OpsJob) -> str:
    scripts = get_hermes_home() / "scripts"
    scripts.mkdir(parents=True, exist_ok=True)
    name = _script_name(job)
    (scripts / name).write_text(_SCRIPT.format(name=job.name, command=" ".join(job.argv), argv=list(job.argv)),
                                encoding="utf-8")
    return name


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
        print(f"{what} {name}: `lina {' '.join(job.argv)}` at '{job.schedule}' (was {job.replaces})")
    return 0
