"""Comment provenance and actor audit contracts (issue 184)."""
from __future__ import annotations

import pytest

from hermes_cli import kanban as cli
from hermes_cli import kanban_db as kb
from hermes_cli import kanban_db_connect as kbc


@pytest.fixture
def board(tmp_path, monkeypatch):
    monkeypatch.setenv("HERMES_HOME", str(tmp_path))
    for name in ("HERMES_KANBAN_TASK", "HERMES_KANBAN_RUN_ID", "HERMES_KANBAN_DB",
                 "HERMES_KANBAN_BOARD", "HERMES_KANBAN_HOME"):
        monkeypatch.delenv(name, raising=False)
    kb.init_db()
    with kbc.connect_closing() as conn:
        yield conn


def test_comment_event_keeps_source_and_explicit_run_when_written(board):
    # Given a card with a known run and a source owned by the caller.
    tid = kb.create_task(board, title="comment", initial_status="blocked")
    kb.unblock_task(board, tid)
    task = kb.claim_task(board, tid)
    assert task.current_run_id is not None
    source = {"surface": "cli", "session": "conversation"}

    # When a comment is appended to that run.
    cid = kb.add_comment(board, tid, "writer", " body ", source=source,
                         run_id=task.current_run_id)

    # Then legacy event fields and comment identity coexist with provenance.
    event = kb.list_events(board, tid)[-1]
    assert event.payload == {"author": "writer", "len": 6, "source": source, "comment_id": cid}
    assert event.run_id == task.current_run_id
    assert kb.list_comments(board, tid)[-1].body == "body"


@pytest.mark.parametrize("operation", ["edit", "priority", "link", "unlink", "unblock"])
def test_actor_reason_is_redacted_before_first_line_is_capped(board, operation):
    # Given real cards and an audit reason containing a secret and a second line.
    tid = kb.create_task(board, title="child", initial_status="blocked")
    parent = kb.create_task(board, title="parent", initial_status="blocked")
    if operation == "unlink":
        kb.link_tasks(board, parent, tid)
    reason = "token=sk-" + "a" * 50 + " " + "x" * 250 + "\nprivate second line"
    actor = {"by": "operator", "reason": reason}

    # When one audited mutation occurs.
    match operation:
        case "edit":
            kb.edit_task(board, tid, title="updated", **actor)
            kind = "edited"
        case "priority":
            kb.edit_task(board, tid, priority=4, **actor)
            kind = "reprioritized"
        case "link":
            kb.link_tasks(board, parent, tid, **actor)
            kind = "linked"
        case "unlink":
            kb.unlink_tasks(board, parent, tid, **actor)
            kind = "unlinked"
        case "unblock":
            kb.unblock_task(board, tid, **actor)
            kind = "unblocked"

    # Then the actor is durable but secrets and extra lines are absent.
    payload = [e.payload for e in kb.list_events(board, tid) if e.kind == kind][-1]
    assert payload["by"] == "operator"
    assert "a" * 50 not in payload["reason"]
    assert "private second line" not in payload["reason"]
    assert len(payload["reason"]) <= 200
    if operation in ("link", "unlink"):
        assert (payload["parent"], payload["child"]) == (parent, tid)
    if operation == "edit":
        assert payload["fields"] == ["title"]
    if operation == "priority":
        assert payload["priority"] == 4


def test_cli_comment_stamps_surface_when_interactive(board, monkeypatch):
    # Given an interactive CLI session and a real board card.
    tid = kb.create_task(board, title="CLI")
    monkeypatch.setenv("HERMES_SESSION_ID", "cli-session")
    monkeypatch.setattr(cli, "_profile_author", lambda: "operator")

    # When the public slash dispatch appends a comment.
    cli.run_slash(f"comment {tid} hello")

    # Then each event identifies its originating surface or actor.
    events = kb.list_events(board, tid)
    comment = [e for e in events if e.kind == "commented"][-1]
    assert comment.payload["source"] == {"surface": "cli", "session": "cli-session"}
    assert comment.run_id is None


def test_cli_edit_stamps_actor_when_reason_is_given(board, monkeypatch):
    # Given an interactive CLI caller and a card.
    tid = kb.create_task(board, title="CLI")
    monkeypatch.setattr(cli, "_profile_author", lambda: "operator")

    # When the command edits the card with a reason.
    cli.run_slash(f"edit {tid} --title changed --reason clarified")

    # Then the event identifies who requested the edit and why.
    edit = [e for e in kb.list_events(board, tid) if e.kind == "edited"][-1]
    assert (edit.payload["by"], edit.payload["reason"]) == ("operator", "clarified")
