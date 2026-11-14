"""``lina migrate cron`` copies a legacy profile's jobs paused and local into a new agent (lina#639)."""

from __future__ import annotations

import hashlib
import json
import os
import subprocess
from argparse import Namespace

import pytest

from hermes_cli.migrate_cron import run
from tests.hermes_cli.migrate_cron_fixtures import (
    LEGACY_SCRIPTS,
    legacy_cron_home,
    legacy_cron_jobs,
    new_agent_home,
)


@pytest.fixture
def homes(tmp_path, monkeypatch):
    home = tmp_path / "lina"
    monkeypatch.setenv("HERMES_HOME", str(home))
    legacy = legacy_cron_home(tmp_path / "legacy")
    new_agent_home(home, "alpha-copy")
    args = Namespace(from_=str(legacy), profile="alpha", agent="alpha-copy", home=None,
                     work_dir=str(tmp_path / "work"))
    return legacy / "profiles" / "alpha" / "cron" / "jobs.json", home / "profiles" / "alpha-copy", args


def _stored(agent_home):
    return json.loads((agent_home / "cron" / "jobs.json").read_text())["jobs"]


def _report(capsys):
    return json.loads(capsys.readouterr().out)


def test_every_job_lands_paused_local_and_noted_and_the_legacy_file_is_untouched(homes, capsys):
    legacy_file, agent_home, args = homes
    before = hashlib.sha256(legacy_file.read_bytes()).hexdigest()

    assert run(args) == 0
    report = _report(capsys)

    legacy = {job["id"]: job for job in legacy_cron_jobs()}
    stored = {job["id"]: job for job in _stored(agent_home)}
    assert stored.keys() == legacy.keys()
    for job_id, job in stored.items():
        assert (job["enabled"], job["state"], job["paused_reason"], job["next_run_at"]) == (
            False, "paused", "migrated copy", None)
        assert job["deliver"] == "local"
        assert not {"origin", "failure_deliver", "interpreter"} & {k for k, v in job.items() if v}
        assert job["name"] == legacy[job_id]["name"]
        if legacy[job_id]["deliver"] != "local":
            assert json.dumps(legacy[job_id]["deliver"]) in job["notes"]
    assert hashlib.sha256(legacy_file.read_bytes()).hexdigest() == before == report["copy"]["source_sha256"]

    rows = {row["id"]: row for row in report["jobs"]}
    assert report["counts"] == {"imported": 10, "already_present": 0, "skipped": 0}
    assert rows["d4e5f6a1b2c3"]["kind"] == "script" and rows["d4e5f6a1b2c3"]["script"]["resolves"]
    assert rows["e5f6a1b2c3d4"]["script"]["resolves"] is True
    assert rows["1a2b3c4d5e6f"]["kind"] == "prompt"
    assert rows["2a3b4c5d6e7f"]["workdir"]["resolves"] is False
    assert rows["0a1b2c3d4e5f"]["legacy_deliver"] == "telegram:-1001234567890"
    assert {"deliver", "failure_deliver", "origin"} <= {r["field"] for r in rows["2a3b4c5d6e7f"]["left_out"]}
    assert "interpreter" in {r["field"] for r in rows["f6a1b2c3d4e5"]["left_out"]}


def test_a_rerun_changes_nothing_and_keeps_edits_made_to_the_copy(homes, capsys):
    _, agent_home, args = homes
    assert run(args) == 0
    capsys.readouterr()
    jobs_file = agent_home / "cron" / "jobs.json"
    edited = json.loads(jobs_file.read_text())
    edited["jobs"][4]["script"] = "new_path.sh"
    jobs_file.write_text(json.dumps(edited))

    assert run(args) == 0

    report = _report(capsys)
    assert report["counts"] == {"imported": 0, "already_present": 10, "skipped": 0}
    assert report["scripts"]["legacy_only.sh"]["status"] == "already_present"
    assert _stored(agent_home) == edited["jobs"]


def test_named_scripts_are_copied_never_run_and_never_overwrite(homes, monkeypatch, capsys):
    from cron import jobs
    from cron.dry_run import dry_run

    legacy_file, agent_home, args = homes
    legacy_scripts = legacy_file.parent.parent / "scripts"

    def refuse(*a, **kw):
        raise AssertionError(f"the migration started a process: {a!r}")

    for name in ("Popen", "run", "call", "check_call", "check_output"):
        monkeypatch.setattr(subprocess, name, refuse)
    monkeypatch.setattr(os, "system", refuse)

    assert run(args) == 0
    report = _report(capsys)

    rows = {row["id"]: row for row in report["jobs"]}
    copied = rows["e5f6a1b2c3d4"]["script"]["copy"]
    assert copied["status"] == "copied"
    assert copied["sha256"] == hashlib.sha256(LEGACY_SCRIPTS["legacy_only.sh"].encode()).hexdigest()
    assert (agent_home / "scripts" / "legacy_only.sh").read_text() == LEGACY_SCRIPTS["legacy_only.sh"]
    assert rows["f6a1b2c3d4e5"]["script"]["copy"]["status"] == "copied"
    assert rows["d4e5f6a1b2c3"]["script"]["copy"]["status"] == "kept_existing"
    assert (agent_home / "scripts" / "disk_report.py").read_text() == "print('disk ok')\n"
    assert rows["d4e5f6a1b2c3"]["monitor_script"]["copy"]["status"] == "missing"
    assert report["copy"]["scripts"]["legacy_only.sh"]["source"] == str(legacy_scripts / "legacy_only.sh")

    monkeypatch.setenv("HERMES_HOME", str(agent_home))
    with jobs.use_cron_store(agent_home):
        checked = dry_run(jobs.get_job("e5f6a1b2c3d4"))
    assert "error" not in checked["scripts"]["script"] and checked["ok"], checked["problems"]


def test_a_symlinked_legacy_script_is_refused_and_not_copied(homes, tmp_path, capsys):
    legacy_file, agent_home, args = homes
    script = legacy_file.parent.parent / "scripts" / "collect_feeds.py"
    real = tmp_path / "elsewhere.py"
    script.rename(real)
    script.symlink_to(real)

    assert run(args) == 0

    rows = {row["id"]: row for row in _report(capsys)["jobs"]}
    assert rows["f6a1b2c3d4e5"]["script"]["copy"] == {"status": "refused", "detail": "symlinks are forbidden"}
    assert not (agent_home / "scripts" / "collect_feeds.py").exists()
    assert rows["e5f6a1b2c3d4"]["script"]["copy"]["status"] == "copied"


def test_a_symlinked_legacy_jobs_file_is_refused(homes, tmp_path, capsys):
    legacy_file, agent_home, args = homes
    real = tmp_path / "elsewhere.json"
    legacy_file.rename(real)
    legacy_file.symlink_to(real)

    assert run(args) == 1

    assert "symlinks are forbidden" in _report(capsys)["error"]
    assert not (agent_home / "cron" / "jobs.json").exists()
