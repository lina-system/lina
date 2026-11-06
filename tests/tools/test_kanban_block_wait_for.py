"""``kanban_block`` takes ``wait_for``: the event that answers a ``needs_input`` block, parsed
like ``hermes kanban block --wait-for`` (lina#258). The card goes back to ready by itself when
the event happens; a malformed or impossible wait is a tool error that changes nothing."""
from __future__ import annotations

import json
from pathlib import Path

import pytest

from hermes_cli import kanban_db as kb
from hermes_cli import kanban_db_connect as kbc
from hermes_cli.kanban_db_wait import wake_waiting_tasks


@pytest.fixture
def worker(monkeypatch, tmp_path):
    home = tmp_path / ".hermes"
    home.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setenv("HERMES_PROFILE", "test-worker")
    monkeypatch.delenv("HERMES_SESSION_ID", raising=False)
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    kb._INITIALIZED_PATHS.clear()
    kb.init_db()
    with kbc.connect_closing() as conn:
        tid = kb.create_task(conn, title="blocked worker", assignee="test-worker")
        run_id = kb.claim_task(conn, tid).current_run_id
    monkeypatch.setenv("HERMES_KANBAN_TASK", tid)
    monkeypatch.setenv("HERMES_KANBAN_RUN_ID", str(run_id))
    return tid


def _block(**args) -> dict:
    from tools import kanban_tools

    return json.loads(kanban_tools._handle_block({"reason": "waiting on the upstream card", **args}))


def test_a_block_with_wait_for_records_the_event_and_wakes_when_it_happens(worker):
    with kbc.connect_closing() as conn:
        upstream = kb.create_task(conn, title="upstream")

    out = _block(wait_for=f"card:{upstream}")

    assert out["ok"] is True
    assert out["wait_for"] == {"kind": "card", "task_id": upstream}
    with kbc.connect_closing() as conn:
        task = kb.get_task(conn, worker)
        assert (task.status, task.block_kind) == ("blocked", "needs_input")
        assert wake_waiting_tasks(conn) == []
        assert kb.complete_task(conn, upstream, result="landed") is True
        assert wake_waiting_tasks(conn) == [worker]
        assert kb.get_task(conn, worker).status == "ready"


@pytest.mark.parametrize(("args", "message"), [
    ({"wait_for": "card"}, "wait-for must be"),
    ({"wait_for": "soon:1"}, "wait-for must be"),
    ({"wait_for": "pr:not-a-pr"}, "pr wait must be"),
    ({"wait_for": "until:tomorrow"}, "until wait must be"),
    ({"wait_for": 7}, "wait_for must be a string"),
    ({"wait_for": "card:no-such-card"}, "unknown task no-such-card"),
    ({"wait_for": "until:0", "kind": "capability"}, "needs_input blocks only"),
])
def test_an_unusable_wait_for_is_a_tool_error_that_changes_nothing(worker, args, message):
    out = _block(**args)

    assert message in out["error"]
    with kbc.connect_closing() as conn:
        assert kb.get_task(conn, worker).status == "running"
