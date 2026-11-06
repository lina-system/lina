"""Durable continuation accounting for issue 184, through both worker callers."""

import json
import sqlite3
from types import SimpleNamespace

import pytest

from hermes_cli import goals, kanban_db as kb
from hermes_cli.kanban_db_connect import connect_closing


@pytest.mark.parametrize("gate", [{}, {"prompt": "repair", "reason": "failed gate"}])
def test_continuation_records_before_execution_after_wait(monkeypatch, gate):
    # Given a WAIT boundary followed by one executable continuation.
    boundaries = iter([{"wait": "pending"}, gate])
    statuses = iter(["running", "running", "done"])
    observed = []
    monkeypatch.setattr(goals, "judge_goal", lambda *a: ("continue", "next", False, None, False))

    # When the loop advances past the waiting gate.
    result = goals.run_kanban_goal_loop(
        task_id="t", goal_text="g", first_response="first", max_turns=2,
        task_status_fn=lambda: next(statuses), block_fn=lambda _: pytest.fail("blocked"),
        gate_check=lambda: next(boundaries), wait_fn=lambda _: observed.append("wait"),
        record_turn=lambda n: observed.append(n),
        run_turn=lambda _: observed.append("run") or "second",
    )

    # Then only the actual second turn is recorded, before model execution.
    assert observed == ["wait", 2, "run"]
    assert result["turns_used"] == 2


def test_record_failure_logs_and_continues(monkeypatch, caplog):
    # Given unavailable persistence and a continuation that itself fails.
    monkeypatch.setattr(goals, "judge_goal", lambda *a: ("continue", "next", False, None, False))
    turns = []

    def record(_turn):
        raise sqlite3.OperationalError("disk unavailable")

    # When the loop attempts the continuation.
    result = goals.run_kanban_goal_loop(
        task_id="t", goal_text="g", first_response="first", max_turns=2,
        task_status_fn=lambda: "running", block_fn=lambda _: pytest.fail("blocked"),
        record_turn=record,
        run_turn=lambda _: turns.append("ran") or {"failed": True, "failure_reason": "timeout"},
    )

    # Then persistence failure does not prevent the turn or hide its failure.
    assert turns == ["ran"]
    assert result["reason"] == "worker failed: timeout"
    assert any(record.exc_info and isinstance(record.exc_info[1], sqlite3.OperationalError)
               for record in caplog.records)


@pytest.fixture
def claimed_task(tmp_path, monkeypatch):
    monkeypatch.setenv("HERMES_HOME", str(tmp_path))
    kb.init_db()
    path = kb.kanban_db_path()
    with connect_closing(path) as conn:
        task_id = kb.create_task(conn, title="deliver", assignee="default", goal_mode=True, goal_max_turns=3)
        task = kb.claim_task(conn, task_id, claimer="test-worker")
    return path, task


@pytest.mark.parametrize("surface", ["quiet", "chat", "gateway"])
def test_callers_persist_run_scoped_turns_before_execution(claimed_task, monkeypatch, surface):
    # Given a real claimed card and a deterministic judge with no live model calls.
    path, task = claimed_task
    monkeypatch.setenv("HERMES_KANBAN_TASK", task.id)
    monkeypatch.setenv("HERMES_KANBAN_RUN_ID", str(task.current_run_id))
    monkeypatch.setattr(goals, "judge_goal", lambda *a: ("continue", "next", False, None, False))
    monkeypatch.setattr("hermes_cli.kanban_goal_gates.loop_gate_check", lambda *a, **k: dict)
    observed = []

    def turn(*args, **kwargs):
        with connect_closing(path) as conn:
            rows = conn.execute("SELECT run_id, payload FROM task_events WHERE task_id=? AND kind='goal_turn' ORDER BY id",
                                (task.id,)).fetchall()
        observed.append([(row[0], json.loads(row[1])) for row in rows])
        return {"completed": True, "final_response": "progress"}

    agent = SimpleNamespace(run_conversation=turn, session_id="test-session",
                            _session_db=SimpleNamespace(get_messages_as_conversation=lambda _: []))
    cli = SimpleNamespace(agent=agent, session_id="test-session", conversation_history=[],
                          chat=lambda p: turn(p)["final_response"])

    # When each production caller executes its goal loop.
    if surface == "gateway":
        from gateway.session_kanban import _run_task_turns
        monkeypatch.setenv("HERMES_KANBAN_RUN_ID", "999999")
        monkeypatch.setattr("agent.skill_commands.build_preloaded_skills_prompt", lambda *a, **k: ("", [], []))
        context = {"db": str(path), "task_id": task.id, "run_id": task.current_run_id,
                   "skills": [], "context": "", "goal_mode": True, "goal_text": "deliver", "goal_max_turns": 3}
        _run_task_turns(agent, {"text": "first"}, [], context)
    else:
        from hermes_cli.cli_single_query import _run_kanban_goal_loop_chat, _run_kanban_goal_loop_q
        runner = _run_kanban_goal_loop_chat if surface == "chat" else _run_kanban_goal_loop_q
        runner(cli, "first")

    # Then the first turn stays implicit and each continuation is already durable.
    expected = [[(task.current_run_id, {"turn": 2})],
                [(task.current_run_id, {"turn": 2}), (task.current_run_id, {"turn": 3})]]
    assert observed == ([[]] if surface == "gateway" else []) + expected


@pytest.mark.parametrize("run_id", [None, "malformed", "999999"])
def test_cli_never_infers_another_run_for_recording(claimed_task, monkeypatch, run_id):
    # Given a worker without a usable current-attempt identity.
    path, task = claimed_task
    monkeypatch.setenv("HERMES_KANBAN_TASK", task.id)
    monkeypatch.setenv("HERMES_KANBAN_RUN_ID", str(task.current_run_id))
    monkeypatch.setattr(goals, "judge_goal", lambda *a: ("continue", "next", False, None, False))
    monkeypatch.setattr("hermes_cli.kanban_goal_gates.loop_gate_check", lambda *a, **k: dict)
    from hermes_cli.cli_single_query import _run_kanban_goal_loop_q

    _run_kanban_goal_loop_q(SimpleNamespace(), "first", run_turn=lambda _: {"failed": True})
    if run_id is None:
        monkeypatch.delenv("HERMES_KANBAN_RUN_ID", raising=False)
    else:
        monkeypatch.setenv("HERMES_KANBAN_RUN_ID", run_id)

    # When the legacy/unbound caller runs (or a superseded caller stops).
    _run_kanban_goal_loop_q(SimpleNamespace(), "first", run_turn=lambda _: "progress")

    # Then it cannot attribute continuation work to the card's current run.
    with connect_closing(path) as conn:
        rows = conn.execute("SELECT run_id, payload FROM task_events WHERE kind='goal_turn'").fetchall()
        assert [(row[0], json.loads(row[1])) for row in rows] == [(task.current_run_id, {"turn": 2})]
