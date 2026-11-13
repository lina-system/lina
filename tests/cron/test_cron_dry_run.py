"""``hermes cron run <id> --dry-run`` resolves a job like a tick and executes nothing (lina#639).

Real store, real execution ledger and real script resolution under a temp HERMES_HOME.
"""

from __future__ import annotations

import sqlite3
import subprocess
import sys
from argparse import Namespace

import pytest

from cron import jobs
from cron.executions import latest_execution, list_executions, record_dry_run
from hermes_cli.cron import cron_command

# Importing these before the profiler starts keeps import-time module code out of the trace.
import cron.dry_run
import cron.scheduler

_FORBIDDEN = ("scheduler_delivery", "run_agent.py", "send_message")


@pytest.fixture
def home(tmp_path, monkeypatch):
    monkeypatch.setenv("HERMES_HOME", str(tmp_path))
    (tmp_path / "scripts").mkdir()
    (tmp_path / "scripts" / "collect.py").write_text("print('collected')\n")
    with jobs.use_cron_store(tmp_path):
        yield tmp_path


@pytest.fixture
def nothing_runs(monkeypatch):
    """Fail on any subprocess and record every frame from delivery, agent or send-message code."""
    def refuse(*args, **kwargs):
        raise AssertionError(f"a dry run started a subprocess: {args!r}")

    monkeypatch.setattr(subprocess, "Popen", refuse)
    monkeypatch.setattr(subprocess, "run", refuse)
    reached: list[str] = []

    def profile(frame, event, arg):
        if event == "call" and any(name in frame.f_code.co_filename for name in _FORBIDDEN):
            reached.append(f"{frame.f_code.co_filename}:{frame.f_code.co_name}")

    sys.setprofile(profile)
    try:
        yield reached
    finally:
        sys.setprofile(None)


def _dry_run(job_id: str) -> int:
    return cron_command(Namespace(cron_command="run", job_id=job_id, dry_run=True))


def test_a_good_job_exits_zero_and_runs_sends_and_calls_nothing(home, nothing_runs, capsys):
    job = jobs.create_job(prompt="summarize what the script collected", schedule="every 1h",
                          script="collect.py", deliver="local", paused=True)

    assert _dry_run(job["id"]) == 0

    out = capsys.readouterr().out
    assert nothing_runs == []
    assert '"executed": false' in out and "summarize what the script collected" in out
    assert [row["status"] for row in list_executions(job_id=job["id"])] == ["dry_run"]
    assert latest_execution(job["id"]) is None
    assert jobs.get_job(job["id"])["state"] == "paused"


def test_a_job_whose_script_is_missing_fails_and_books_why(home, nothing_runs, capsys):
    job = jobs.create_job(prompt=None, schedule="every 1h", script="collect.py", no_agent=True,
                          deliver="local")
    (home / "scripts" / "collect.py").unlink()

    assert _dry_run(job["id"]) == 1

    assert nothing_runs == []
    assert "Script not found" in capsys.readouterr().out
    [row] = list_executions(job_id=job["id"])
    assert row["status"] == "dry_run" and "Script not found" in row["error"]


def test_an_unknown_skill_and_an_unknown_bot_chat_profile_fail(home, nothing_runs, capsys):
    job = jobs.create_job(prompt="check", schedule="every 1h", skills=["no-such-skill-639"],
                          deliver="bot-chat:no-such-profile-639")

    assert _dry_run(job["id"]) == 1

    out = capsys.readouterr().out
    assert "unknown skill 'no-such-skill-639'" in out
    assert "bot-chat delivery profile 'no-such-profile-639' does not exist" in out
    assert nothing_runs == []


def test_a_skill_with_an_uninstalled_dependency_is_reported_never_installed(home, nothing_runs, monkeypatch, capsys):
    import pm
    import pm.client
    import pm.install

    ensured: list[str] = []
    for module in (pm, pm.client, pm.install):
        monkeypatch.setattr(module, "ensure", lambda name, **kw: ensured.append(name), raising=False)
    skill = home / "skills" / "needs-tool-639"
    skill.mkdir(parents=True)
    (skill / "SKILL.md").write_text(
        "---\nname: needs-tool-639\ndescription: test\ndeps: [lina-639-absent-tool]\n---\nUse the tool.\n")
    job = jobs.create_job(prompt="use it", schedule="every 1h", skills=["needs-tool-639"], deliver="local")

    assert _dry_run(job["id"]) == 0

    assert ensured == [] and nothing_runs == []
    out = capsys.readouterr().out
    assert '"found": true' in out and "lina-639-absent-tool" in out


def test_a_ledger_from_before_dry_runs_is_widened_without_losing_rows(home):
    db = home / "cron" / "executions.db"
    db.parent.mkdir(parents=True, exist_ok=True)
    conn = sqlite3.connect(db)
    conn.execute(
        """CREATE TABLE executions (
             id TEXT PRIMARY KEY, job_id TEXT NOT NULL, source TEXT NOT NULL,
             process_id TEXT NOT NULL, pid INTEGER NOT NULL, process_started_at INTEGER,
             status TEXT NOT NULL CHECK(status IN
               ('claimed','running','completed','failed','unknown','cancelled','missed')),
             handoff_pending INTEGER NOT NULL DEFAULT 0, handoff_started_at REAL,
             claimed_at TEXT NOT NULL, started_at TEXT, finished_at TEXT, error TEXT)""")
    conn.execute(
        "INSERT INTO executions (id, job_id, source, process_id, pid, status, claimed_at) "
        "VALUES ('old', 'j1', 'builtin', 'p', 1, 'cancelled', '2026-01-01T00:00:00+00:00')")
    conn.commit()
    conn.close()

    record_dry_run("j1", error=None)

    assert sorted(r["status"] for r in list_executions(job_id="j1")) == ["cancelled", "dry_run"]
    assert latest_execution("j1")["status"] == "cancelled"
