"""Multiple project association and filtering (lina#36)."""

import pytest

from tests.hermes_cli._work_env import WorkEnv


@pytest.fixture
def work(tmp_path, monkeypatch):
    env = WorkEnv(tmp_path, monkeypatch)
    for project in ("First", "Second", "Third"):
        (env.desk / "1_Projects" / project).mkdir(parents=True)
    env.goal_and_todos()
    return env


def test_projects_roundtrip_when_multiple_are_assigned(work):
    # Given / When
    work.ok("edit", "G1", "--project", "First,Second")
    # Then
    goal = next(row for row in work.json()["items"] if row["id"] == "G1")
    assert goal["projects"] == ["1_Projects/First", "1_Projects/Second"]
    assert goal["project"] == "1_Projects/First"


def test_add_remove_project_preserves_order_when_edited(work):
    # Given
    work.ok("edit", "G1", "--project", "First,Second")
    # When
    work.ok("edit", "G1", "--remove-project", "First", "--add-project", "Third")
    # Then
    goal = next(row for row in work.json()["items"] if row["id"] == "G1")
    assert goal["projects"] == ["1_Projects/Second", "1_Projects/Third"]


def test_unknown_project_removal_is_refused(work):
    # Given / When
    code, _, _ = work.run("edit", "G1", "--remove-project", "Unknown")
    # Then
    assert code == 2
