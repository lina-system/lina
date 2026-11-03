"""Confirmation evidence and reopening contracts (lina#36)."""

import pytest

from tests.hermes_cli._work_env import WorkEnv


@pytest.fixture
def work(tmp_path, monkeypatch):
    env = WorkEnv(tmp_path, monkeypatch)
    env.goal_and_todos()
    return env


@pytest.mark.parametrize("actor", ["alpha", "beta"])
def test_seen_confirmation_closes_when_actor_explains_observation(work, actor):
    # Given / When
    work.ok("confirm", "T1", "--by", actor, "--seen", "결과 보고서를 직접 열어 봤다")
    # Then
    row = next(row for row in work.json("--all")["items"] if row["id"] == "T1")
    assert row["state"] == "done"
    assert "결과 보고서를 직접 열어 봤다" in row["done_evidence"]


@pytest.mark.parametrize("args", [[], ["--by", "owner"], ["--by", "owner", "--seen", "봄"]])
def test_confirmation_refuses_when_evidence_is_missing_or_short(work, args):
    # Given / When
    code, _, _ = work.run("confirm", "T1", *args)
    # Then
    assert code == 2


def test_quote_confirmation_closes_when_owner_words_are_provided(work):
    # Given / When
    work.ok("confirm", "T1", "--quote", "결과를 확인했으니 끝내도 된다")
    # Then
    row = next(row for row in work.json("--all")["items"] if row["id"] == "T1")
    assert row["state"] == "done"


def test_unlink_keeps_item_closed_when_confirmation_already_exists(work):
    # Given
    work.ok("link", "T1", "omo:OT-RESULT")
    work.ok("confirm", "T1", "--quote", "결과를 확인했으니 끝내도 된다")
    # When
    work.ok("unlink", "T1", "omo:OT-RESULT")
    # Then
    row = next(row for row in work.json("--all")["items"] if row["id"] == "T1")
    assert row["state"] == "done"
    assert "omo:OT-RESULT" not in row["links"]
