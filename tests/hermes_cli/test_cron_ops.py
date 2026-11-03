"""``lina cron ops``: the ops launchd agents become script-only cron jobs of the profile (lina#38)."""

import runpy
import sys

from hermes_constants import get_hermes_home


def _jobs() -> dict:
    from cron.jobs import list_jobs
    return {j["name"]: j for j in list_jobs(include_disabled=True)}


def test_install_creates_script_jobs_that_run_the_lina_commands(monkeypatch):
    # Given
    from hermes_cli import cron_ops
    seen = []
    import hermes_cli.main as cli_main
    monkeypatch.setattr(cli_main, "main", lambda: seen.append(list(sys.argv)))
    # When
    result = cron_ops.install()
    # Then
    assert result == [("learned-collect", "created"), ("learned-card", "created")]
    jobs = _jobs()
    assert {name: (j["schedule"]["expr"], j["no_agent"], j["deliver"]) for name, j in jobs.items()} == {
        "learned-collect": ("5 * * * *", True, "local"),
        "learned-card": ("5 11 * * *", True, "local")}
    for job in jobs.values():
        runpy.run_path(str(get_hermes_home() / "scripts" / job["script"]), run_name="__main__")
    assert seen == [["lina", "learned", "collect"], ["lina", "learned", "card", "--day", "yesterday", "--send"]]


def test_install_again_updates_in_place_and_remove_deletes_jobs_and_scripts():
    # Given
    from hermes_cli import cron_ops
    cron_ops.install()
    ids = {name: j["id"] for name, j in _jobs().items()}
    # When
    again = cron_ops.install()
    # Then
    assert [what for _, what in again] == ["updated", "updated"]
    assert {name: j["id"] for name, j in _jobs().items()} == ids
    # When
    removed = cron_ops.remove()
    # Then
    assert removed == ["learned-collect", "learned-card"]
    assert _jobs() == {}
    assert not list((get_hermes_home() / "scripts").glob("lina-*.py"))
