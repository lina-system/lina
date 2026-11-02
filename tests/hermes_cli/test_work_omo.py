"""Worker link normalization and pending runner state (lina#36)."""

import pytest

from tests.hermes_cli._work_env import WorkEnv


def test_worker_link_deduplicates_when_repeated(tmp_path, monkeypatch):
    # Given
    work = WorkEnv(tmp_path, monkeypatch)
    work.goal_and_todos()
    # When
    work.ok("link", "T1", "omo:OT-EXAMPLE", "omo:OT-EXAMPLE")
    # Then
    row = next(row for row in work.json()["items"] if row["id"] == "T1")
    assert row["links"].count("omo:OT-EXAMPLE") == 1


@pytest.mark.parametrize("link", ["omo:", "omo:wrong", "omo:OT-bad_value"])
def test_worker_link_refuses_when_identifier_is_malformed(tmp_path, monkeypatch, link):
    # Given
    work = WorkEnv(tmp_path, monkeypatch)
    work.goal_and_todos()
    # When
    code, _, _ = work.run("link", "T1", link)
    # Then
    assert code == 2
