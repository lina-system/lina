"""Goal gates on goal-mode kanban cards (lina#108).

The card loop runs the card's own gates in its workspace before the judge; the handoff tools
refuse while a gate fails, judge or no judge; exit 75 waits without spending a turn.
"""

from __future__ import annotations

import json
import sqlite3
from pathlib import Path

import pytest

from hermes_cli import goals
from hermes_cli import kanban_db as kb
from hermes_cli import kanban_db_connect as kbc
from hermes_cli.kanban_goal_gates import loop_gate_check

GATE_FAILED_HEAD = goals.CONTINUATION_PROMPT_GATE_FAILED_TEMPLATE.split("\n", 1)[0]
FINALIZE = goals.KANBAN_GOAL_FINALIZE_TEMPLATE.format(reason="scripted:done")


@pytest.fixture
def kanban_home(tmp_path, monkeypatch):
    home = tmp_path / ".hermes"
    home.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setenv("HERMES_PROFILE", "test-worker")
    monkeypatch.delenv("HERMES_SESSION_ID", raising=False)
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    kb._INITIALIZED_PATHS.clear()
    kb.init_db()
    return home


@pytest.fixture
def workspace(tmp_path):
    ws = tmp_path / "ws"
    ws.mkdir()
    return ws


def _card(workspace, gates, *, claim=False):
    with kbc.connect_closing() as conn:
        tid = kb.create_task(conn, title="ship it", body="tests green", assignee="test-worker",
                             goal_mode=True, goal_gates=gates, workspace_kind="dir",
                             workspace_path=str(workspace))
        run_id = None
        if claim:
            kb.claim_task(conn, tid)
            run_id = kb._current_run_id(conn, tid)
    return tid, run_id


def _task(tid):
    with kbc.connect_closing() as conn:
        return kb.get_task(conn, tid), kb.list_events(conn, tid)


def _judge(monkeypatch, verdict="done"):
    calls = []

    def fake(goal, response, subgoals=None, background_processes=None, **_kw):
        calls.append(response)
        return verdict, f"scripted:{verdict}", False, None, False

    monkeypatch.setattr(goals, "judge_goal", fake)
    return calls


class _Worker:
    def __init__(self, on_turn=None):
        self.prompts, self.status, self.on_turn = [], "running", on_turn

    def turn(self, prompt):
        self.prompts.append(prompt)
        if self.on_turn:
            self.on_turn(self, prompt, len(self.prompts))
        return "worked"


def _loop(tid, worker, blocks, *, waits=None, max_turns=20):
    return goals.run_kanban_goal_loop(
        task_id=tid, goal_text="ship it", run_turn=worker.turn,
        task_status_fn=lambda: worker.status, block_fn=blocks.append, max_turns=max_turns,
        first_response="first", gate_check=loop_gate_check(tid, "ship it"),
        wait_fn=(waits.append if waits is not None else lambda s: pytest.fail("no wait")))


def test_failing_gate_skips_judge_until_it_passes(kanban_home, workspace, monkeypatch):
    judge_calls = _judge(monkeypatch, "done")
    tid, _ = _card(workspace, ["test -f ok.txt || { echo missing ok.txt; exit 3; }"])

    def on_turn(w, prompt, n):
        if n == 1:
            # Judge says done regardless, yet the failing gate kept it from being consulted.
            assert judge_calls == []
            assert prompt.startswith(GATE_FAILED_HEAD) and "missing ok.txt" in prompt
            assert json.loads(_task(tid)[0].goal_gates)[0]["attempts"] == 1
            (workspace / "ok.txt").write_text("ok")
        else:
            assert prompt == FINALIZE  # the judge ran only once the gate passed
            w.status = "done"

    worker, blocks = _Worker(on_turn), []
    res = _loop(tid, worker, blocks)

    assert res["outcome"] == "completed_by_worker" and blocks == []
    assert len(worker.prompts) == 2 and len(judge_calls) == 1
    task, events = _task(tid)
    gate_events = [e.payload for e in events if e.kind == "goal_gate"]
    assert [(e["passed"], e["exit_code"]) for e in gate_events] == [(False, 3), (True, 0)]
    assert "missing ok.txt" in gate_events[0]["output_tail"]
    stored = json.loads(task.goal_gates)[0]
    assert (stored["attempts"], stored["last_exit_code"]) == (0, 0)


def test_gate_retries_exhausted_blocks_card_naming_gate(kanban_home, workspace, monkeypatch):
    judge_calls = _judge(monkeypatch, "done")
    tid, _ = _card(workspace, [{"command": "echo red suite; exit 1", "max_retries": 2}])
    worker, blocks = _Worker(), []

    res = _loop(tid, worker, blocks)

    assert res["outcome"] == "blocked_gate"
    assert judge_calls == [] and len(worker.prompts) == 2
    assert len(blocks) == 1 and "echo red suite; exit 1" in blocks[0] and "red suite" in blocks[0]


def test_waiting_gate_costs_no_turn_or_retry(kanban_home, workspace, monkeypatch):
    judge_calls = _judge(monkeypatch, "done")
    script = ('n=$(cat n 2>/dev/null || echo 0); n=$((n+1)); echo $n > n; '
              'if [ $n -ge 3 ]; then exit 0; fi; echo ci pending; exit 75')
    tid, _ = _card(workspace, [{"command": script, "max_retries": 1}])
    worker, blocks, waits = _Worker(lambda w, p, n: setattr(w, "status", "done")), [], []

    res = _loop(tid, worker, blocks, waits=waits, max_turns=2)

    assert res["outcome"] == "completed_by_worker" and blocks == []
    assert waits == [goals.GATE_WAIT_INITIAL_S, goals.GATE_WAIT_INITIAL_S * 2]
    # Only the finalize nudge after the gate passed: no gate-failed prompt, no turn while waiting.
    assert worker.prompts == [FINALIZE]
    assert len(judge_calls) == 1
    assert json.loads(_task(tid)[0].goal_gates)[0]["attempts"] == 0


def test_card_without_gates_judges_as_before(kanban_home, workspace, monkeypatch):
    judge_calls = _judge(monkeypatch, "continue")
    tid, _ = _card(workspace, None)
    worker, blocks = _Worker(lambda w, p, n: setattr(w, "status", "done")), []

    res = _loop(tid, worker, blocks)

    assert res["outcome"] == "completed_by_worker"
    assert judge_calls == ["first"]
    assert worker.prompts == [goals.KANBAN_GOAL_CONTINUATION_TEMPLATE.format(reason="scripted:continue")]
    task, events = _task(tid)
    assert task.goal_gates is None and not [e for e in events if e.kind == "goal_gate"]


@pytest.mark.parametrize("tool", ["kanban_complete", "kanban_request_review"])
def test_handoff_refused_while_gate_fails_without_judge(kanban_home, workspace, monkeypatch, tool):
    from tools import kanban_tools as kt

    handler = {"kanban_complete": kt._handle_complete,
               "kanban_request_review": kt._handle_request_review}[tool]
    monkeypatch.setattr(kt, "_goal_judge_available", lambda: False)
    monkeypatch.setattr(kt, "judge_goal", lambda *a, **k: pytest.fail("judge must not run"))
    tid, run_id = _card(workspace, ["test -f ok.txt"], claim=True)
    monkeypatch.setenv("HERMES_KANBAN_TASK", tid)
    monkeypatch.setenv("HERMES_KANBAN_RUN_ID", str(run_id))

    out = json.loads(handler({"summary": "done, trust me"}))

    assert "error" in out and "test -f ok.txt" in out["error"]
    assert _task(tid)[0].status == "running"

    (workspace / "ok.txt").write_text("ok")
    out = json.loads(handler({"summary": "done with ok.txt"}))
    assert "error" not in out, out
    assert _task(tid)[0].status == ("done" if tool == "kanban_complete" else "review")


def test_legacy_board_without_goal_gates_column_opens(tmp_path, monkeypatch):
    home = tmp_path / ".hermes"
    home.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    kb._INITIALIZED_PATHS.clear()
    db_path = kb.kanban_db_path()
    db_path.parent.mkdir(parents=True, exist_ok=True)
    legacy = sqlite3.connect(db_path)
    legacy.execute(
        "CREATE TABLE tasks (id TEXT PRIMARY KEY, title TEXT NOT NULL, body TEXT, assignee TEXT, "
        "status TEXT NOT NULL DEFAULT 'ready', priority INTEGER NOT NULL DEFAULT 0, "
        "created_by TEXT, created_at INTEGER NOT NULL, started_at INTEGER, completed_at INTEGER, "
        "workspace_kind TEXT NOT NULL DEFAULT 'scratch', workspace_path TEXT, claim_lock TEXT, "
        "claim_expires INTEGER)")
    legacy.execute("INSERT INTO tasks (id, title, status, priority, created_at, workspace_kind) "
                   "VALUES ('legacy1', 'old', 'ready', 0, 1, 'scratch')")
    legacy.commit()
    legacy.close()

    kb.init_db()

    with kbc.connect_closing() as conn:
        cols = {r["name"] for r in conn.execute("PRAGMA table_info(tasks)")}
        task = kb.get_task(conn, "legacy1")
    assert "goal_gates" in cols
    assert task.title == "old" and task.goal_gates is None


def test_create_rejects_gates_without_goal_mode(kanban_home, workspace):
    with kbc.connect_closing() as conn, pytest.raises(ValueError, match="goal-mode"):
        kb.create_task(conn, title="x", assignee="a", goal_gates=["true"])
