"""``lina migrate cron`` copies a legacy profile's jobs paused and local into a new agent (lina#639)."""

from __future__ import annotations

import hashlib
import json
from argparse import Namespace

import pytest

from hermes_cli.migrate_cron import run
from tests.hermes_cli.migrate_cron_fixtures import legacy_cron_home, legacy_cron_jobs, new_agent_home


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
    assert rows["e5f6a1b2c3d4"]["script"]["resolves"] is False
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

    assert _report(capsys)["counts"] == {"imported": 0, "already_present": 10, "skipped": 0}
    assert _stored(agent_home) == edited["jobs"]


def test_a_symlinked_legacy_jobs_file_is_refused(homes, tmp_path, capsys):
    legacy_file, agent_home, args = homes
    real = tmp_path / "elsewhere.json"
    legacy_file.rename(real)
    legacy_file.symlink_to(real)

    assert run(args) == 1

    assert "symlinks are forbidden" in _report(capsys)["error"]
    assert not (agent_home / "cron" / "jobs.json").exists()
