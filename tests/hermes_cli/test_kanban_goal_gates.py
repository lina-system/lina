"""Goal gates on goal-mode kanban cards (lina#108).

The card loop runs the card's own gates in its workspace before the judge; the handoff tools
refuse while a gate fails, judge or no judge; exit 75 waits without spending a turn.
"""

from __future__ import annotations

import json
import os
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


def test_finalize_again_after_gate_repair(kanban_home, workspace, monkeypatch):
    judge_calls = _judge(monkeypatch, "done")
    (workspace / "ok.txt").write_text("ok")
    tid, _ = _card(workspace, ["test -f ok.txt"])

    def on_turn(worker, prompt, number):
        if number == 1:
            (workspace / "ok.txt").unlink()
        elif number == 2:
            (workspace / "ok.txt").write_text("repaired")
        else:
            worker.status = "done"

    worker, blocks = _Worker(on_turn), []

    result = _loop(tid, worker, blocks, max_turns=6)

    assert result["outcome"] == "completed_by_worker" and blocks == []
    assert worker.prompts == [FINALIZE, worker.prompts[1], FINALIZE]
    assert len(judge_calls) == 2
    events = [event.payload for event in _task(tid)[1] if event.kind == "goal_gate"]
    assert [event["passed"] for event in events] == [True, False, True]


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


@pytest.mark.parametrize("command,landed", [("complete", "done"), ("request-review", "review")])
def test_cli_handoff_refused_while_gate_fails_without_judge(kanban_home, workspace, monkeypatch,
                                                            capsys, command, landed):
    import argparse

    from hermes_cli import kanban as kc

    monkeypatch.setattr(kc, "_goal_mode_handoff_rejection", lambda task, ev: ("done", None))
    tid, run_id = _card(workspace, ["echo suite red; test -f ok.txt"], claim=True)
    monkeypatch.setenv("HERMES_KANBAN_TASK", tid)
    monkeypatch.setenv("HERMES_KANBAN_RUN_ID", str(run_id))
    parser = argparse.ArgumentParser(prog="hermes", add_help=False)
    kc.build_parser(parser.add_subparsers(dest="command"))

    def run():
        args = parser.parse_args(["kanban", command, tid, "--summary", "done, trust me"])
        return kc.kanban_command(args)

    assert run() != 0
    err = capsys.readouterr().err
    assert "echo suite red; test -f ok.txt" in err and "suite red" in err
    assert _task(tid)[0].status == "running"

    (workspace / "ok.txt").write_text("ok")
    assert run() == 0
    assert _task(tid)[0].status == landed


def _gate_once(tid):
    from hermes_cli.kanban_goal_gates import run_card_gates

    with kbc.connect_closing() as conn:
        failure = run_card_gates(conn, kb.get_task(conn, tid))
    return failure, [e.payload for e in _task(tid)[1] if e.kind == "goal_gate"]


def test_card_gate_cannot_read_host_provider_credentials(kanban_home, workspace, monkeypatch):
    monkeypatch.setenv("OPENAI_API_KEY", "SENTINEL_NOT_A_KEY_108")
    tid, _ = _card(workspace, ['env | grep -c . ; env | grep -F "sk-gate"; echo "key=${OPENAI_API_KEY:-unset}"; exit 1'])

    failure, events = _gate_once(tid)

    assert failure is not None and "key=unset" in failure.output
    assert "SENTINEL_NOT_A_KEY_108" not in failure.output
    assert "SENTINEL_NOT_A_KEY_108" not in json.dumps(events)


@pytest.mark.parametrize("caller", ["missing", "different"])
@pytest.mark.parametrize("entry", ["handoff", "loop"])
def test_card_gate_context_uses_target_and_connected_board(
        kanban_home, workspace, monkeypatch, caller, entry):
    from hermes_cli.kanban_goal_gates import handoff_gate_failure

    board = workspace / "target board's $data.sqlite"
    kbc.init_db(db_path=board)
    command = (
        'printf "%s\\n" "$HERMES_KANBAN_TASK" "$HERMES_KANBAN_DB"; '
        'test "$HERMES_KANBAN_TASK" = "$(cat expected-card)" && '
        'test "$HERMES_KANBAN_DB" = "$(cat expected-board)" && '
        'test -z "${OPENAI_API_KEY:-}"'
    )
    with kbc.connect_closing(db_path=board) as conn:
        tid = kb.create_task(conn, title="context gate", assignee="test-worker",
                             goal_mode=True, goal_gates=[command], workspace_kind="dir",
                             workspace_path=str(workspace))
    (workspace / "expected-card").write_text(tid)
    (workspace / "expected-board").write_text(str(board))
    monkeypatch.setenv("HERMES_KANBAN_DB", str(workspace / "wrong-board.sqlite"))
    monkeypatch.setenv("OPENAI_API_KEY", "SENTINEL_NOT_A_KEY_CONTEXT")
    if caller == "missing":
        monkeypatch.delenv("HERMES_KANBAN_TASK", raising=False)
    else:
        monkeypatch.setenv("HERMES_KANBAN_TASK", "another-card")
    before = {name: os.environ.get(name) for name in ("HERMES_KANBAN_TASK", "HERMES_KANBAN_DB")}

    if entry == "handoff":
        with kbc.connect_closing(db_path=board) as conn:
            failure = handoff_gate_failure(conn, kb.get_task(conn, tid))
    else:
        failure = loop_gate_check(tid, "context gate", connect=lambda: kbc.connect_closing(db_path=board))()

    assert failure is None, failure
    assert {name: os.environ.get(name) for name in before} == before
    with kbc.connect_closing(db_path=board) as conn:
        stored = json.loads(kb.get_task(conn, tid).goal_gates)[0]
        event = [e for e in kb.list_events(conn, tid) if e.kind == "goal_gate"][-1]
    assert stored["command"] == event.payload["command"] == command
    assert event.payload["passed"] is True
    assert event.payload["output_tail"].splitlines() == [tid, str(board)]


def test_card_gate_runs_in_local_backend_in_card_workspace(kanban_home, workspace):
    (workspace / "marker").write_text("x")
    tid, _ = _card(workspace, ["pwd; test -f marker"])

    failure, events = _gate_once(tid)

    assert failure is None
    assert events[-1]["passed"] is True and str(workspace) in events[-1]["output_tail"]


@pytest.mark.parametrize("persistent", [True, False])
def test_card_gate_docker_workspace(kanban_home, workspace, monkeypatch, persistent):
    from tools import terminal_tool as terminal
    from tools import terminal_tool_lifecycle as lifecycle
    from tools.terminal_scope import reset_terminal_scope, set_terminal_scope

    (workspace / "marker").write_text("worker output")
    recorded = {}

    class MountedEnvironment:
        def execute(self, command, *, cwd, **kwargs):
            recorded["execute_cwd"] = cwd
            visible = (recorded["host_cwd"] == str(workspace) and cwd == "/workspace"
                       and (workspace / "marker").is_file())
            return {"returncode": 0 if visible else 1, "output": "marker" if visible else "missing"}

        def cleanup(self):
            recorded["cleaned"] = True

    def factory(config, env_type, **kwargs):
        recorded.update(kwargs)
        return MountedEnvironment()

    monkeypatch.setattr(lifecycle, "_create_configured_env", factory)
    token = set_terminal_scope({
        "TERMINAL_ENV": "docker", "TERMINAL_CWD": str(kanban_home),
        "TERMINAL_DOCKER_MOUNT_CWD_TO_WORKSPACE": "true",
        "TERMINAL_CONTAINER_PERSISTENT": str(persistent).lower(),
    })
    try:
        tid, _ = _card(workspace, ["test -f marker"])
        failure, events = _gate_once(tid)
        assert terminal.resolve_task_overrides(f"kanban-gate-{tid}") == {}
    finally:
        reset_terminal_scope(token)

    assert failure is None, failure
    assert recorded["host_cwd"] == str(workspace)
    assert recorded["cwd"] == recorded["execute_cwd"] == "/workspace"
    assert recorded["cleaned"] and events[-1]["passed"]


def test_card_gate_fails_closed_when_backend_unreachable(kanban_home, workspace, monkeypatch):
    monkeypatch.setenv("TERMINAL_ENV", "no-such-backend-108")
    (workspace / "marker").write_text("x")
    tid, _ = _card(workspace, ["test -f marker"])

    failure, events = _gate_once(tid)

    assert failure is not None and failure.exit_code == -1
    assert "no-such-backend-108" in failure.output and events[-1]["passed"] is False


def test_card_gate_bounded_output(kanban_home, workspace, monkeypatch):
    from tools.environments import base
    from tools import tool_output_limits

    collectors = []
    original = base._new_output_collector

    def record_collector(process, bounded_capture):
        collector = original(process, bounded_capture)
        collectors.append(collector)
        return collector

    monkeypatch.setattr(tool_output_limits, "get_max_bytes", lambda: 4096)
    monkeypatch.setattr(base, "_new_output_collector", record_collector)
    tid, _ = _card(workspace, ["printf '%200000s' x; printf '\\nEND-OF-GATE\\n'; exit 7"])

    failure, events = _gate_once(tid)

    verbose = [collector for collector in collectors if collector.total_chars >= 200000]
    assert verbose and all(collector.buffered_chars <= 4096 for collector in verbose)
    assert failure.exit_code == 7 and len(failure.output) <= 3000
    assert "END-OF-GATE" in failure.output and events[-1]["exit_code"] == 7


def test_gate_results_of_a_reclaimed_run_are_discarded(kanban_home, workspace, monkeypatch):
    from hermes_cli import kanban_goal_gates as kgg

    judge_calls = _judge(monkeypatch, "done")
    tid, run_a = _card(workspace, ["test -f ok.txt"], claim=True)
    real_gate, successor = kgg.run_card_gate, {}

    def reclaimed_mid_gate(gate, *, cwd, task_id):
        # While run A's gate is still running, the card is reclaimed and run B takes it and
        # records its own (passing) gate result.
        monkeypatch.setattr(kgg, "run_card_gate", real_gate)
        with kbc.connect_closing() as conn:
            assert kb.reclaim_task(conn, tid, signal_fn=lambda *a, **k: None)
            kb.claim_task(conn, tid)
            successor["run"] = kb._current_run_id(conn, tid)
            (workspace / "ok.txt").write_text("ok")
            assert kgg.run_card_gates(conn, kb.get_task(conn, tid), count_attempts=True) is None
        successor["state"] = _task(tid)[0].goal_gates
        return False, 1, "run A saw red"

    monkeypatch.setattr(kgg, "run_card_gate", reclaimed_mid_gate)
    worker, blocks = _Worker(), []

    res = goals.run_kanban_goal_loop(
        task_id=tid, goal_text="ship it", run_turn=worker.turn, task_status_fn=lambda: "running",
        block_fn=blocks.append, first_response="first",
        gate_check=kgg.loop_gate_check(tid, "ship it", run_id=run_a),
        wait_fn=lambda s: pytest.fail("no wait"))

    assert res["outcome"] == "stopped" and blocks == [] and worker.prompts == [] and judge_calls == []
    task, events = _task(tid)
    assert successor["run"] != run_a and task.goal_gates == successor["state"]
    stored = json.loads(task.goal_gates)[0]
    assert (stored["run_id"], stored["last_exit_code"], stored["attempts"]) == (successor["run"], 0, 0)
    gate_events = [e for e in events if e.kind == "goal_gate"]
    assert [e.run_id for e in gate_events] == [successor["run"]]
    assert "run A saw red" not in json.dumps([e.payload for e in gate_events])


def test_gate_state_and_events_name_the_run(kanban_home, workspace, capsys):
    from hermes_cli import kanban as kc

    tid, run_id = _card(workspace, [{"command": "echo red; exit 1", "max_retries": 5}], claim=True)

    failure, _ = _gate_once(tid)

    task, events = _task(tid)
    assert failure is not None and json.loads(task.goal_gates)[0]["run_id"] == run_id
    assert [e.run_id for e in events if e.kind == "goal_gate"] == [run_id]
    kc._print_goal_gates(task)
    assert f"run {run_id}" in capsys.readouterr().out


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
