"""Goal health from waiting children and fresh activity (lina#36)."""

import pytest

from tests.hermes_cli._work_env import NOW, WorkEnv


@pytest.mark.parametrize("next_at,health", [(NOW + 3600, "wait"), (NOW - 1, "yellow")])
def test_goal_health_tracks_child_wait_expiry(tmp_path, monkeypatch, next_at, health):
    # Given
    WorkEnv(tmp_path, monkeypatch)
    from hermes_cli.work.view import row_view
    goal = dict(id="G1", kind="목표", state="open", created=NOW - 72000, by="alpha", notes=[], links=[])
    child = dict(id="T1", kind="할 일", parent="G1", state="open", created=NOW - 72000,
                 wait=dict(on="확인 대기", next=next_at), notes=[])
    # When
    view = row_view(goal, dict(status="working", activity=NOW - 72000), {"G1": goal, "T1": child})
    # Then
    assert view["health"] == health


def test_recent_activity_prevents_stale_health(tmp_path, monkeypatch):
    # Given
    WorkEnv(tmp_path, monkeypatch)
    from hermes_cli.work.view import row_view
    goal = dict(id="G1", kind="목표", state="open", created=NOW - 72000, by="alpha", notes=[], links=[])
    # When
    view = row_view(goal, dict(status="working", activity=NOW - 3600, active_now=True), {"G1": goal})
    # Then
    assert view["health"] == "green"
    assert view["activity_at"] == NOW - 3600
