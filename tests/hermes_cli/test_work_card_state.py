"""A kanban card is the to-do's runner, so its own state is the to-do's: a card nobody can read is 모름 (never 끝남,
never 하는 중), a blocked card is waited on, and a card that ended leaves the check or asks for a confirmation
(#249). A wait the agent set after reading the evidence outranks the confirm nudge (lina#510)."""

import sqlite3
from contextlib import closing

import pytest

from tests.hermes_cli._work_env import WorkEnv


def card_store(work: WorkEnv, cards: dict[str, tuple[str, int | None]]) -> None:
    """The board's kanban.db under the profile's home, with the given (status, completed_at) cards."""
    from hermes_cli.work import links
    path = work.root / "kanban.db"
    with closing(sqlite3.connect(path)) as db:
        db.execute("CREATE TABLE IF NOT EXISTS tasks (id TEXT PRIMARY KEY, status TEXT, completed_at INTEGER)")
        db.execute("DELETE FROM tasks")
        db.executemany("INSERT INTO tasks VALUES (?, ?, ?)", [(k, s, d) for k, (s, d) in cards.items()])
        db.commit()
    links._KANBAN_CACHE.clear()


def computed(work: WorkEnv, item: str) -> dict:
    return next(row for row in work.json("--all")["items"] if row["id"] == item)["computed"]


def state_of(work: WorkEnv, item: str) -> str:
    return next(row for row in work.json("--all")["items"] if row["id"] == item)["state"]


def goal_and_todo(work: WorkEnv, *, check: str) -> str:
    """A goal and one to-do under it whose own check is *check* (``evidence:<what>`` leaves the card to decide)."""
    work.ok("add", "목표", "--by", "alpha", "--area", "Dev", "--title", "카드 목표", "--basis", "카드를 봐 줘",
            "--done", "카드가 끝난다", "--check", "false")
    return work.ok("add", "할 일", "--by", "alpha", "--goal", "G1", "--title", "카드 할 일", "--why", "w",
                   "--done", "카드가 끝난다", "--check", check, "--omo", "next").split()[0]


def test_a_card_nobody_can_read_is_unknown_for_its_todo_and_its_goal(tmp_path, monkeypatch):
    # Given a to-do and a goal whose cards are not in the store
    work = WorkEnv(tmp_path, monkeypatch)
    todo = goal_and_todo(work, check="false")
    work.ok("link", todo, "kanban:t_11111111")
    work.ok("link", "G1", "kanban:t_22222222")
    # When
    work.ok("check", "--quiet")
    # Then both read 모름, and neither raises a notice: an unread card is never 끝남 and never 하는 중
    assert (computed(work, todo)["status"], computed(work, "G1")["status"]) == ("unknown", "unknown")
    assert "[dry] notice" not in work.ok("check", "--dry")


@pytest.mark.parametrize("status,completed_at", [("blocked", None), ("archived", None)])
def test_a_card_the_todo_waits_on_makes_it_wait_without_a_notice(tmp_path, monkeypatch, status, completed_at):
    # Given a blocked card, or one archived before it finished
    work = WorkEnv(tmp_path, monkeypatch)
    todo = goal_and_todo(work, check="false")
    card_store(work, {"t_11111111": (status, completed_at)})
    work.ok("link", todo, "kanban:t_11111111")
    # When
    work.ok("check", "--quiet")
    # Then the to-do waits on the card, with no alert
    row = computed(work, todo)
    assert (row["status"], row["reasons"]) == ("waiting", [])
    assert "[dry] notice" not in work.ok("check", "--dry")


def test_a_done_card_closes_a_todo_with_no_check_of_its_own_and_reopens_it(tmp_path, monkeypatch):
    # Given a to-do whose check is evidence nobody attached, and whose only card ended
    work = WorkEnv(tmp_path, monkeypatch)
    todo = goal_and_todo(work, check="evidence:보고서")
    card_store(work, {"t_11111111": ("done", work.clock[0] - 60)})
    work.ok("link", todo, "kanban:t_11111111")
    # When
    work.ok("check", "--quiet")
    # Then the card's end is the to-do's check
    assert state_of(work, todo) == "done"
    # When the same card is put back in hand
    card_store(work, {"t_11111111": ("running", None)})
    work.ok("check", "--quiet")
    # Then the to-do it closed opens again, with the reason in its notes
    row = next(row for row in work.json("--all")["items"] if row["id"] == todo)
    assert (row["state"], computed(work, todo)["status"]) == ("open", "working")
    assert row["notes"][-1]["text"].startswith("다시 엶:")


def test_a_done_card_leaves_the_check_of_a_todo_that_has_one(tmp_path, monkeypatch):
    # Given a to-do with its own command check and a card that ended
    work = WorkEnv(tmp_path, monkeypatch)
    todo = goal_and_todo(work, check="false")
    card_store(work, {"t_11111111": ("done", work.clock[0] - 60)})
    work.ok("link", todo, "kanban:t_11111111")
    # When
    work.ok("check", "--quiet")
    # Then the to-do stays open on its own check, which is what is left
    assert computed(work, todo)["status"] == "check_left"
    assert state_of(work, todo) == "open"


def test_a_wait_set_after_the_evidence_outranks_the_confirm_nudge(tmp_path, monkeypatch):
    # Given a to-do whose evidence nobody can verify, and a wait the agent set after reading it
    work = WorkEnv(tmp_path, monkeypatch)
    todo = goal_and_todo(work, check="evidence:보고서")
    work.ok("evidence", todo, "evidence:보고서")
    work.clock[0] += 600
    work.ok("wait", todo, "--on", "자료", "--next", "+1d")
    # When
    work.ok("check", "--quiet")
    # Then the wait is what the agent said it now needs
    assert computed(work, todo)["status"] == "waiting"
    # When evidence arrives after that wait
    work.clock[0] += 600
    work.ok("evidence", todo, "evidence:다른보고서")
    work.ok("check", "--quiet")
    # Then the confirm nudge comes back
    assert computed(work, todo)["status"] == "evidence_wait"
