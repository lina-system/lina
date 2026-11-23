"""Taste voicing budgets and task measurements through the agent's Postgres store."""

from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timedelta
from threading import Barrier

import hermes_time


def _promote(sql, agent, label, direction, when):
    sql(
        """
        WITH promoted AS (
            INSERT INTO memory.tastes (agent_id, target_kind, label, valence, strength, status, origin)
            VALUES (%s, 'method', %s, %s, 0.8, 'core', 'outcome')
            RETURNING agent_id, id, valence
        )
        INSERT INTO memory.taste_events
            (agent_id, taste_id, reason, changed_by, changed_at, from_status, to_status, to_valence)
        SELECT agent_id, id, 'status_changed', 'review', %s, 'active', 'core', valence FROM promoted
        """,
        (agent, label, direction, when),
    )


def test_weekly_promotions_win_the_budget_and_reset_at_seoul_monday(memory_store_sql, tmp_path, monkeypatch):
    from agent.taste_voice_store import reserve, settle

    sunday = datetime.fromisoformat("2026-11-08T14:59:00+00:00")
    monday = datetime.fromisoformat("2026-11-01T15:00:00+00:00")
    monkeypatch.setattr(hermes_time, "now", lambda: sunday)
    home = tmp_path / "profiles" / "alpha"
    _promote(memory_store_sql, "alpha", "earlier week", "like", monday - timedelta(seconds=1))
    _promote(memory_store_sql, "beta", "foreign", "like", sunday)
    for offset, label, direction in (
        (0, "oldest", "like"),
        (1, "reviewing", "like"),
        (2, "sorting", "dislike"),
        (3, "grouping", "like"),
    ):
        _promote(memory_store_sql, "alpha", label, direction, monday + timedelta(days=offset))
    _promote(memory_store_sql, "alpha", "after rollover", "like", sunday + timedelta(minutes=1))

    expected = (("like: grouping", "dislike: sorting", "like: reviewing"), False, False)
    assert reserve(home, "first", None, allow_difference=True, dislike=False) == expected
    assert reserve(home, "first", None, allow_difference=True, dislike=False) == expected
    settle(home, "first", completed=True)
    assert reserve(home, "second", None, allow_difference=True, dislike=False) == ((), False, False)

    monkeypatch.setattr(hermes_time, "now", lambda: sunday + timedelta(minutes=1))
    assert reserve(home, "third", None, allow_difference=True, dislike=False) == (
        ("like: after rollover",), False, False,
    )


def test_concurrent_turns_share_budgets_and_failure_releases_them(tmp_path):
    from agent.taste_voice_store import reserve, settle

    home = tmp_path / "profiles" / "alpha"
    other = tmp_path / "profiles" / "beta"
    ready = Barrier(2, timeout=10)

    def claim(turn_id):
        ready.wait()
        return reserve(home, turn_id, "task", allow_difference=True, dislike=True)

    with ThreadPoolExecutor(max_workers=2) as workers:
        first = workers.submit(claim, "first")
        second = workers.submit(claim, "second")
        outcomes = {"first": first.result(timeout=15), "second": second.result(timeout=15)}

    both = ((), True, True)
    neither = ((), False, False)
    assert sorted(outcomes.values()) == [neither, both]
    winner = next(turn_id for turn_id, result in outcomes.items() if result == both)
    assert reserve(other, winner, "task", allow_difference=True, dislike=True) == both
    settle(home, winner, completed=False)
    assert reserve(home, "replacement", "task", allow_difference=True, dislike=True) == both
    settle(home, winner, completed=True)
    settle(home, "replacement", completed=True)
    settle(home, "replacement", completed=False)
    assert reserve(home, "later", "task", allow_difference=True, dislike=True) == neither
    assert reserve(home, "new-task", "other-task", allow_difference=True, dislike=True) == ((), False, True)


def test_measure_uses_only_previous_same_agent_and_task_kind_samples(tmp_path):
    from agent.taste_voice_store import measure

    home = tmp_path / "profiles" / "alpha"
    other = tmp_path / "profiles" / "beta"
    measure(home, "baseline", "editing", 120, ("tests", "lint", "types"), disliked=False)
    measure(home, "baseline", "editing", 1, (), disliked=False)
    measure(home, "other-kind", "reviewing", 1, ("docs",), disliked=False)
    measure(other, "other-agent", "editing", 1, ("format",), disliked=False)

    expected = {"short_output": 1, "skipped_verification": 2}
    assert measure(home, "measured", "editing", 60, ("tests",), disliked=True) == expected
    assert measure(home, "measured", "editing", 1, (), disliked=True) == expected


def test_forgotten_promotion_is_removed_before_delivery(tmp_path, memory_store_sql):
    from agent.taste_voice_store import reserve, settle

    home = tmp_path / "profiles" / "alpha"
    _promote(memory_store_sql, "alpha", "orchard sketches", "like", hermes_time.now())
    assert reserve(home, "pending", None, allow_difference=False, dislike=False)[0] == ("like: orchard sketches",)
    memory_store_sql("UPDATE memory.tastes SET status='forgotten', forgotten_by='owner' WHERE agent_id='alpha'")
    assert settle(home, "pending", completed=True) == ()
    assert reserve(home, "replacement", None, allow_difference=True, dislike=False) == ((), True, False)
