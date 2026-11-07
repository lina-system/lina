"""lina#258: follow-ups to parking a card on an event (lina#161).

A wait that can no longer be met by itself goes to a person once, as a ``needs_input`` block
whose reason says why: its upstream card was archived unfinished or deleted, or the wait could
not be read too many ticks in a row. A reviewer run's manual schedule returns to review on
unblock, while any other run's still returns to ready. Everything runs against a real temporary
board.
"""
from __future__ import annotations

import importlib
from pathlib import Path

import pytest

from hermes_cli import kanban_db as kb
from hermes_cli import kanban_db_connect as kbc
from hermes_cli import kanban_db_dispatch as kbd


def _wait_mod():
    return importlib.import_module("hermes_cli.kanban_db_wait")


@pytest.fixture
def board(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    home = tmp_path / ".hermes"
    home.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    monkeypatch.setattr(kbd, "_profile_exists_fn", lambda: None)
    monkeypatch.setattr(kbd, "_memory_pressure_level", lambda: "unknown")
    kb.init_db()
    with kbc.connect_closing() as conn:
        yield conn


def _running(conn, title="waiting card") -> tuple[str, int]:
    tid = kb.create_task(conn, title=title, assignee="worker")
    claimed = kb.claim_task(conn, tid, claimer="worker")
    assert claimed is not None
    return tid, claimed.current_run_id


def _park(conn, tid, run_id, park, wait_for):
    if park == "schedule":
        assert kb.schedule_task(conn, tid, reason="waiting", wait_for=wait_for, expected_run_id=run_id)
    else:
        assert kb.block_task(conn, tid, reason="waiting", kind="needs_input", wait_for=wait_for,
                             expected_run_id=run_id)


def _tick(conn):
    return kbd.dispatch_once(conn, spawn_fn=lambda task, ws: None)


def _blocked_reasons(conn, tid):
    return [e.payload.get("reason") for e in kb.list_events(conn, tid) if e.kind == "blocked"]


@pytest.mark.parametrize("park", ["schedule", "block"])
@pytest.mark.parametrize("ending", ["archived", "deleted"])
def test_a_wait_on_a_card_that_ended_unfinished_goes_to_a_person_once(board, park, ending):
    upstream = kb.create_task(board, title="upstream")   # unassigned: never spawned
    tid, run_id = _running(board)
    _park(board, tid, run_id, park, {"kind": "card", "task_id": upstream})
    assert (kb.archive_task if ending == "archived" else kb.delete_task)(board, upstream) is True

    _tick(board)
    _tick(board)

    task = kb.get_task(board, tid)
    assert (task.status, task.block_kind) == ("blocked", "needs_input")
    assert _wait_mod().recorded_wait(board, tid, "blocked") is None
    held = [r for r in _blocked_reasons(board, tid) if upstream in r]
    assert len(held) == 1 and f"was {ending} without finishing" in held[0]


def test_an_implementation_runs_manual_schedule_still_unblocks_to_ready(board):
    tid, run_id = _running(board, "worker card")

    assert kb.schedule_task(board, tid, reason="handed to a worker run", expected_run_id=run_id)
    scheduled = [e.payload for e in kb.list_events(board, tid) if e.kind == "scheduled"]
    assert kb.unblock_task(board, tid) is True

    assert scheduled == [{"reason": "handed to a worker run"}]
    assert kb.get_task(board, tid).status == "ready"


_AUTH = {"ok": False, "classification": "auth", "checks": [],
         "detail": "GitHub refused the acceptance read (HTTP 404 on graphql o/r) as the ambient gh login"}
_PENDING = {"ok": False, "classification": "pending", "required": [{"context": "tests"}],
            "checks": [{"name": "tests", "classification": "pending"}]}


def _pr_wait_reading(monkeypatch, board, receipts):
    from hermes_cli import kanban_pr_acceptance

    monkeypatch.setattr(kanban_pr_acceptance, "collect_acceptance", lambda url, pr, assignee: receipts[0])
    tid, run_id = _running(board, "waits on a PR")
    _park(board, tid, run_id, "schedule", {"kind": "pr", "repo": "o/r", "number": 7})
    return tid


def test_a_pr_wait_that_keeps_failing_to_read_goes_to_a_person_naming_the_failure(board, monkeypatch):
    limit = getattr(_wait_mod(), "WAIT_READ_FAILURE_LIMIT", 30)
    tid = _pr_wait_reading(monkeypatch, board, [_AUTH])

    for _ in range(limit - 1):
        assert _wait_mod().wake_waiting_tasks(board) == []
    assert kb.get_task(board, tid).status == "scheduled"
    _wait_mod().wake_waiting_tasks(board)
    _wait_mod().wake_waiting_tasks(board)

    task = kb.get_task(board, tid)
    assert (task.status, task.block_kind) == ("blocked", "needs_input")
    held = [r for r in _blocked_reasons(board, tid) if r and "could not be read" in r]
    assert len(held) == 1 and f"{limit} times in a row" in held[0] and _AUTH["detail"] in held[0]


def test_a_good_read_between_failures_starts_the_count_over(board, monkeypatch):
    limit = getattr(_wait_mod(), "WAIT_READ_FAILURE_LIMIT", 30)
    receipts = [_AUTH]
    tid = _pr_wait_reading(monkeypatch, board, receipts)

    for _ in range(limit - 1):
        _wait_mod().wake_waiting_tasks(board)
    receipts[0] = _PENDING
    _wait_mod().wake_waiting_tasks(board)
    receipts[0] = _AUTH
    for _ in range(limit - 1):
        _wait_mod().wake_waiting_tasks(board)

    assert kb.get_task(board, tid).status == "scheduled"
