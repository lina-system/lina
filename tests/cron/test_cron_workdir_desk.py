"""A cron job can work in the Desk wherever it lives (lina-system/lina#37): ``workdir: desk`` or
``desk:<sub path>`` is kept as written and resolved through ``desk.path`` (docs/desk.md) on every run."""

from __future__ import annotations

from pathlib import Path

import pytest

from hermes_cli.desk import init_desk


@pytest.fixture
def root_home(tmp_path, monkeypatch):
    home = tmp_path / ".hermes"
    home.mkdir()
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    monkeypatch.setenv("HOME", str(tmp_path))
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setattr("cron.jobs.CRON_DIR", tmp_path / "cron")
    monkeypatch.setattr("cron.jobs.JOBS_FILE", tmp_path / "cron" / "jobs.json")
    monkeypatch.setattr("cron.jobs.OUTPUT_DIR", tmp_path / "cron" / "output")
    return home


def test_desk_sub_path_resolves_under_the_configured_desk_and_follows_a_move(root_home, tmp_path):
    from cron.jobs import create_job
    from cron.scheduler import _resolve_job_workdir

    first = init_desk(tmp_path / "Synced" / "Desk")
    (first / "Projects" / "launch").mkdir()
    job = create_job(prompt="x", schedule="every 1h", workdir="desk:Projects/launch")

    assert job["workdir"] == "desk:Projects/launch"
    assert _resolve_job_workdir(job, job["id"]) == str((first / "Projects" / "launch").resolve())

    moved = init_desk(tmp_path / "Elsewhere")
    (moved / "Projects" / "launch").mkdir()
    assert _resolve_job_workdir(job, job["id"]) == str((moved / "Projects" / "launch").resolve())


def test_bare_desk_is_the_desk_itself(root_home, tmp_path):
    from cron.jobs import _normalize_workdir
    from cron.scheduler import _resolve_job_workdir

    desk = init_desk(tmp_path / "Desk")
    assert _normalize_workdir("desk") == "desk"
    assert _resolve_job_workdir({"workdir": "desk"}, "j") == str(desk.resolve())


@pytest.mark.parametrize("ref", ["desk:../outside", "desk:Projects/missing"])
def test_a_ref_off_the_desk_or_to_no_folder_is_refused_at_creation(root_home, tmp_path, ref):
    from cron.jobs import _normalize_workdir

    init_desk(tmp_path / "Desk")
    with pytest.raises(ValueError):
        _normalize_workdir(ref)


def test_without_a_desk_creation_refuses_and_a_run_goes_on_without_a_workdir(root_home):
    from cron.jobs import _normalize_workdir
    from cron.scheduler import _resolve_job_workdir

    with pytest.raises(ValueError, match="no Desk"):
        _normalize_workdir("desk:Inbox")
    assert _resolve_job_workdir({"workdir": "desk:Inbox"}, "j") is None
