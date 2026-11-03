"""Append-only revision restoration without an external journal (lina#36)."""

import pytest

from tests.hermes_cli._work_env import WorkEnv


@pytest.fixture
def work(tmp_path, monkeypatch):
    env = WorkEnv(tmp_path, monkeypatch)
    env.goal_and_todos()
    return env


def test_undo_appends_revision_when_authorized_agent_restores(work):
    # Given
    work.ok("edit", "T1", "--title", "보고서 정리")
    # When
    work.ok("undo", "T1", "--to", "1", "--why", "이전 제목 복구", "--as", "alpha")
    # Then
    from hermes_cli.work import ledger
    rows = ledger.rows("T1")
    ledger.close()
    assert [row["rev"] for row in rows] == [1, 2, 3]
    assert (rows[-1]["title"], rows[-1]["op"], rows[-1]["undo_why"]) == (
        rows[0]["title"], "undo", "이전 제목 복구")


def test_undo_refuses_when_other_agent_requests_restore(work):
    # Given
    work.ok("edit", "T1", "--title", "보고서 정리")
    # When
    code, _, _ = work.run("undo", "T1", "--why", "이전 제목 복구", "--as", "beta")
    # Then
    assert code == 2
