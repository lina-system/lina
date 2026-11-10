"""lina#346: a goal session past its context bound hands off, and the next run's new session can
continue from the card record alone (body, comments, run ids, gate results). Also: the card-session
instructions park a PR-check wait on the PR event instead of a shell wait.

Driven with a scripted judge against a real temporary board; the wake through the real dispatcher
wait tick.
"""
from __future__ import annotations

import json
import re
from pathlib import Path

import pytest

from hermes_cli import goals
from hermes_cli import kanban_db as kb
from hermes_cli import kanban_db_connect as kbc
from hermes_cli.kanban_db_wait import wake_waiting_tasks
from hermes_cli.kanban_goal_gates import loop_gate_check


@pytest.fixture
def board(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    home = tmp_path / ".hermes"
    home.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    kb._INITIALIZED_PATHS.clear()
    kb.init_db()
    monkeypatch.setattr(goals, "judge_goal", lambda goal, response, **kw: ("continue", "tests still red", False, None, False))
    with kbc.connect_closing() as conn:
        yield conn


def _goal_card(conn) -> tuple[str, int]:
    tid = kb.create_task(conn, title="Fix the parser", body="Make tests/test_parser.py pass.", assignee="worker",
                         goal_mode=True)
    run_id = kb.claim_task(conn, tid, claimer="worker").current_run_id
    return tid, run_id


def _loop(conn, tid, run_id, turns, tokens, bound=200_000):
    def turn(prompt):
        turns.append(prompt)
        return {"response": "fixed one case", "failed": False}

    return goals.run_kanban_goal_loop(
        task_id=tid, goal_text="Fix the parser", run_turn=turn,
        task_status_fn=lambda: kb.goal_run_status(conn, tid, run_id),
        block_fn=lambda reason: kb.block_task(conn, tid, reason=reason, expected_run_id=run_id),
        max_turns=10, first_response="started", park_fn=loop_gate_check(tid, "Fix the parser", run_id=run_id).park,
        wait_fn=lambda s: None, context_tokens=lambda: tokens.pop(0) if len(tokens) > 1 else tokens[0],
        context_bound=bound)


def test_session_under_the_bound_keeps_its_turns(board):
    tid, run_id = _goal_card(board)
    turns: list[str] = []
    tokens = [10_000, 50_000, 150_000, 250_000]

    result = _loop(board, tid, run_id, turns, tokens)

    assert result["outcome"] == "handoff_context"
    assert len(turns) == 3  # the three turns under the bound ran; the fourth boundary handed off


def test_new_session_continues_from_the_card_record_alone(board):
    tid, run_id = _goal_card(board)
    kb.add_comment(board, tid, "worker", "Parser fails on nested quotes; see PR o/r#7.")
    gate = {"command": "pytest tests/test_parser.py", "timeout_seconds": 60, "max_retries": 3, "attempts": 1,
            "last_exit_code": 1, "last_output_tail": "FAILED test_nested_quotes", "run_id": run_id}
    board.execute("UPDATE tasks SET goal_gates = ? WHERE id = ?", (json.dumps([gate]), tid))
    board.commit()

    result = _loop(board, tid, run_id, [], [250_000])

    assert result["outcome"] == "handoff_context"
    task = kb.get_task(board, tid)
    assert task.status == "scheduled"
    assert wake_waiting_tasks(board) == [tid]
    new_run = kb.claim_task(board, tid, claimer="worker").current_run_id
    assert new_run != run_id
    record = kb.build_worker_context(board, tid)
    assert "Make tests/test_parser.py pass." in record
    assert "nested quotes" in record
    assert "context reached 250000 tokens" in record  # the handoff comment
    assert f"(run {run_id})" in record
    assert f"`pytest tests/test_parser.py`: failed (exit 1), run {run_id}" in record
    assert "FAILED test_nested_quotes" in record


def test_bound_zero_never_hands_off(board):
    tid, run_id = _goal_card(board)
    turns: list[str] = []

    result = _loop(board, tid, run_id, turns, [900_000], bound=0)

    assert result["outcome"] == "handoff_budget"
    assert len(turns) == 9


def test_session_instructions_park_a_pr_wait_on_the_pr_event(board, monkeypatch):
    """The wait the card-session instructions prescribe, filled with a real PR, is accepted for a
    goal-mode card and woken by the dispatcher when the PR's checks finish."""
    from agent.prompt_builder import KANBAN_GUIDANCE
    from tools import kanban_tools  # registers the kanban handlers
    from tools.registry import registry

    template = re.search(r'wait_for=\\?"(pr:[^"\\]+)\\?"', KANBAN_GUIDANCE)
    assert template, "the card-session instructions name no PR wait"
    spec = template.group(1).replace("<owner/repo>", "lina-system/lina").replace("<n>", "346")
    tid, run_id = _goal_card(board)
    monkeypatch.setenv("HERMES_PROFILE", "worker")
    monkeypatch.setenv("HERMES_KANBAN_TASK", tid)
    monkeypatch.setenv("HERMES_KANBAN_RUN_ID", str(run_id))

    out = json.loads(registry.dispatch("kanban_block", {"reason": "waiting on CI", "kind": "needs_input",
                                                        "wait_for": spec}))

    assert out["ok"] is True and out["wait_for"]["kind"] == "pr"
    assert wake_waiting_tasks(board, pr_checks_fn=lambda wait_for, assignee: False) == []
    assert wake_waiting_tasks(board, pr_checks_fn=lambda wait_for, assignee: True) == [tid]
    assert kb.get_task(board, tid).status == "ready"
