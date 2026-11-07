"""lina#258: planned stops are bounded per card even when waits between them break their streak
(``kanban.planned_stop_total_limit``); a hold for a person starts the count over."""
from __future__ import annotations

import os
import time
from pathlib import Path

import pytest

from hermes_cli import kanban_db as kb
from hermes_cli import kanban_db_connect as kbc
from hermes_cli import kanban_db_dispatch as kbd
from hermes_cli.kanban_db_wait import wake_waiting_tasks


@pytest.fixture
def board(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    home = tmp_path / ".hermes"
    home.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    kb.init_db()
    with kbc.connect_closing() as conn:
        yield conn


def _blocked_reasons(conn, tid):
    return [e.payload.get("reason") for e in kb.list_events(conn, tid) if e.kind == "blocked"]


def _runtime_end(conn, tid):
    assert kb.claim_task(conn, tid) is not None
    kbd._set_worker_pid(conn, tid, os.getpid())
    with kb.write_txn(conn):
        conn.execute("UPDATE task_runs SET started_at = ? WHERE id = "
                     "(SELECT current_run_id FROM tasks WHERE id = ?)", (int(time.time()) - 30, tid))
    assert kbd.enforce_max_runtime(conn, signal_fn=lambda pid, sig: None) == [tid]


def _park_and_wake(conn, tid):
    run_id = kb.claim_task(conn, tid).current_run_id
    assert kb.schedule_task(conn, tid, reason="waiting", wait_for={"kind": "until", "at": 0},
                            expected_run_id=run_id)
    assert wake_waiting_tasks(conn) == [tid]


def test_planned_stops_between_waits_are_bounded_per_card(board, monkeypatch):
    (Path(os.environ["HERMES_HOME"]) / "config.yaml").write_text(
        "kanban:\n  planned_stop_total_limit: 3\n", encoding="utf-8")
    monkeypatch.setattr(kb, "_pid_alive", lambda pid: False)
    tid = kb.create_task(board, title="long job", assignee="worker", max_runtime_seconds=1)

    for _ in range(2):
        _runtime_end(board, tid)
        assert kb.get_task(board, tid).status == "ready"
        _park_and_wake(board, tid)
    _runtime_end(board, tid)

    held = kb.get_task(board, tid)
    assert (held.status, held.block_kind, held.consecutive_failures) == ("blocked", "needs_input", 0)
    assert any("3 planned stops" in r for r in _blocked_reasons(board, tid))
    assert kb.unblock_task(board, tid, by="operator") is True
    _runtime_end(board, tid)
    assert kb.get_task(board, tid).status == "ready"


def test_a_dependency_wait_is_no_hold_for_a_person_and_does_not_start_the_count_over(board, monkeypatch):
    (Path(os.environ["HERMES_HOME"]) / "config.yaml").write_text(
        "kanban:\n  planned_stop_total_limit: 3\n", encoding="utf-8")
    monkeypatch.setattr(kb, "_pid_alive", lambda pid: False)
    tid = kb.create_task(board, title="long job", assignee="worker", max_runtime_seconds=1)
    for _ in range(2):
        _runtime_end(board, tid)
    parent = kb.create_task(board, title="upstream")
    run_id = kb.claim_task(board, tid).current_run_id
    kb.link_tasks(board, parent_id=parent, child_id=tid, expected_child_run_id=run_id)
    assert kb.block_task(board, tid, reason="needs the upstream", kind="dependency", expected_run_id=run_id)
    assert kb.get_task(board, tid).status == "todo"
    assert kb.complete_task(board, parent, result="landed") is True
    assert kb.get_task(board, tid).status == "ready"

    _runtime_end(board, tid)

    held = kb.get_task(board, tid)
    assert (held.status, held.block_kind) == ("blocked", "needs_input")
    assert any("3 planned stops" in r for r in _blocked_reasons(board, tid) if r)
