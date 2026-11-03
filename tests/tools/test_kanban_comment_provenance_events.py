"""Comments use owned worker provenance, never an inherited marker."""
from __future__ import annotations

import json

import pytest

from hermes_cli import kanban_db as kb
from hermes_cli import kanban_db_connect as kbc
from tools import kanban_tools as kt


@pytest.mark.parametrize("target,owned", [("own", True), ("other", True), ("other", False)])
def test_comment_attributes_only_the_owned_card_run(tmp_path, monkeypatch, target, owned):
    # Given a worker and another card, with ownership resolved independently of env.
    from agent import delegation_context

    monkeypatch.setenv("HERMES_HOME", str(tmp_path))
    kb.init_db()
    with kbc.connect_closing() as conn:
        own = kb.create_task(conn, title="own", initial_status="blocked")
        kb.unblock_task(conn, own)
        claimed = kb.claim_task(conn, own)
        other = kb.create_task(conn, title="other")
        run = claimed.current_run_id
        assert run is not None
    monkeypatch.setenv("HERMES_KANBAN_TASK", own)
    monkeypatch.setenv("HERMES_KANBAN_RUN_ID", str(run))
    monkeypatch.setattr(delegation_context, "owned_kanban_task", lambda: own if owned else "")
    monkeypatch.setattr(kt, "_persisted_identity", lambda: "writer")
    monkeypatch.setattr("gateway.session_context.get_session_env", lambda _: "current-session")
    tid = own if target == "own" else other

    # When the actual registered comment handler writes to the selected card.
    result = json.loads(kt._handle_comment({"task_id": tid, "body": "handoff"}))

    # Then own-run events are grouped, cross-card provenance is task-scoped.
    assert result["ok"], result
    with kbc.connect_closing() as conn:
        event = [e for e in kb.list_events(conn, tid) if e.kind == "commented"][-1]
    assert event.run_id == (run if target == "own" and owned else None)
    expected = {"card": own, "run": run} if owned and target == "other" else {"surface": "tool"}
    assert event.payload["source"] == {**expected, "session": "current-session"}
    assert event.payload["comment_id"] == result["comment_id"]


def test_cross_card_comment_preserves_scoped_session(tmp_path, monkeypatch):
    from gateway.session_context import scoped_current_session_id

    monkeypatch.setenv("HERMES_HOME", str(tmp_path))
    kb.init_db()
    with kbc.connect_closing() as conn:
        own = kb.create_task(conn, title="own")
        run = kb.claim_task(conn, own).current_run_id
        other = kb.create_task(conn, title="other")
    monkeypatch.setenv("HERMES_KANBAN_TASK", own)
    monkeypatch.setenv("HERMES_KANBAN_RUN_ID", str(run))
    monkeypatch.setenv("HERMES_SESSION_ID", "wrong-process-session")
    with scoped_current_session_id("origin-session"):
        result = json.loads(kt._handle_comment({"task_id": other, "body": "cross-card help"}))
    assert result["ok"]
    with kbc.connect_closing() as conn:
        event = [e for e in kb.list_events(conn, other) if e.kind == "commented"][-1]
    assert event.payload["source"] == {"card": own, "run": run, "session": "origin-session"}
