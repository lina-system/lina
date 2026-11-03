"""Sprint checklist edits and restoration (lina#36)."""

import pytest

from tests.hermes_cli._work_env import WorkEnv


@pytest.fixture
def work(tmp_path, monkeypatch):
    env = WorkEnv(tmp_path, monkeypatch)
    env.goal_and_todos()
    return env


def test_checklist_updates_when_item_state_changes(work):
    # Given
    work.ok("sprint", "set", "T1", "--item", "자료 확인", "--state", "todo")
    # When
    work.ok("sprint", "set", "T1", "--item", "자료 확인", "--state", "done")
    # Then
    output = work.ok("sprint", "show", "T1")
    assert "자료 확인" in output
    assert "✓ 자료 확인" in output


@pytest.mark.parametrize("item", ["G1", "missing"])
def test_sprint_refuses_when_target_is_not_todo(work, item):
    # Given / When
    code, _, _ = work.run("sprint", "set", item, "--item", "자료 확인", "--state", "todo")
    # Then
    assert code == 2


def test_undo_restores_when_sprint_changes(work):
    # Given
    work.ok("sprint", "set", "T1", "--item", "자료 확인", "--state", "todo")
    work.ok("sprint", "set", "T1", "--item", "자료 확인", "--state", "done")
    # When
    work.ok("undo", "T1", "--why", "잘못 표시했다", "--as", "alpha")
    # Then
    output = work.ok("sprint", "show", "T1")
    assert "자료 확인" in output
    assert "✓ 자료 확인" not in output
