"""Round-two learning-record regressions on real board histories (lina#184)."""

import json
from pathlib import Path
from types import SimpleNamespace

import pytest

from hermes_cli import kanban as cli, kanban_db as kb, kanban_gates as gates, kanban_outcomes as ko
from hermes_cli.kanban_db_connect import connect_closing
from gateway.session_context import scoped_current_session_id
from tools import kanban_tools


@pytest.fixture
def board(tmp_path, monkeypatch):
    monkeypatch.setenv("HERMES_HOME", str(tmp_path))
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    for key in ("HERMES_KANBAN_TASK", "HERMES_KANBAN_RUN_ID", "HERMES_KANBAN_DB"):
        monkeypatch.delenv(key, raising=False)
    monkeypatch.setattr(kb.time, "time", lambda: 1800000000)
    kb.init_db()
    with connect_closing() as conn:
        yield conn


def claimed(conn):
    tid = kb.create_task(conn, title="work", assignee="lina", goal_mode=True)
    task = kb.claim_task(conn, tid)
    conn.execute("UPDATE task_runs SET metadata=? WHERE id=?",
                 (json.dumps({"worker_session_id": "worker-session"}), task.current_run_id))
    conn.commit()
    return tid


def test_chat_tool_source_uses_scoped_session_during_open_run(board, monkeypatch):
    tid = claimed(board)
    monkeypatch.setenv("HERMES_SESSION_ID", "wrong-process-session")
    monkeypatch.setattr(kanban_tools, "_persisted_identity", lambda: "lina")
    with scoped_current_session_id("chat-session"):
        result = json.loads(kanban_tools._handle_comment({"task_id": tid, "body": "outside guidance"}))
    assert result["ok"]
    row = ko.compute_outcome(board, tid, include_active=True)
    assert [(t["line"], t["source"]) for t in row["touches"]] == [
        ("outside guidance", {"surface": "tool", "session": "chat-session"})]


@pytest.mark.parametrize("surface", ["cli", "tool"])
def test_worker_session_without_run_environment_is_not_a_touch(board, monkeypatch, surface):
    tid = claimed(board)
    monkeypatch.setattr(cli, "_profile_author", lambda: "lina")
    monkeypatch.setattr(kanban_tools, "_persisted_identity", lambda: "lina")
    if surface == "cli":
        monkeypatch.setenv("HERMES_SESSION_ID", "worker-session")
        cli.run_slash(f"comment {tid} own-work")
    else:
        # Explicit new source reproduces attribution independently of the tool writer fix.
        kb.add_comment(board, tid, "lina", "own-work", source={"surface": "tool", "session": "worker-session"})
    kb.add_comment(board, tid, "lina", "outside-work", source={"surface": surface, "session": "another-session"})
    row = ko.compute_outcome(board, tid, include_active=True)
    assert [t["line"] for t in row["touches"]] == ["outside-work"]
    assert row["main_session_touches"] == 1


def test_only_comments_without_event_identity_use_legacy_windows(board):
    tid = claimed(board)
    kb.add_comment(board, tid, "lina", "new-without-source")
    board.execute("INSERT INTO task_comments(task_id,author,body,created_at) VALUES(?,?,?,?)",
                  (tid, "lina", "legacy-without-source", 1800000000))
    board.commit()
    row = ko.compute_outcome(board, tid, include_active=True)
    assert [t["line"] for t in row["touches"]] == ["new-without-source"]


def test_goal_omits_only_normal_work_card_dependency_wait(board):
    work = claimed(board)
    goal = kb.create_task(board, title="cycle", parents=[work])
    with kb.write_txn(board):
        kb._append_event(board, goal, "dependency_wait", {"parent": "external", "reason": "external dependency"})
        kb._append_event(board, goal, "blocked", {"reason": "dev red"})
        kb._append_event(board, work, "dependency_wait", {"parent": work, "reason": "work must stay visible"})
    result = ko.goal_outcome(board, goal)
    assert [(s["task_id"], s["reason"]) for s in result["stops"]] == [
        (goal, "external dependency"), (goal, "dev red"), (work, "work must stay visible")]


@pytest.mark.parametrize("authors,expected", [(["sion"], "sion"), (["sion", "rumi"], None), ([], None)])
def test_legacy_unblock_recovers_only_unique_same_second_comment(board, authors, expected):
    tid = claimed(board)
    kb.block_task(board, tid, reason="waiting")
    for author in authors:
        kb.add_comment(board, tid, author, "UNBLOCK: repaired")
    kb.add_comment(board, tid, "wrong-time", "UNBLOCK: old")
    board.execute("UPDATE task_comments SET created_at=created_at-1 WHERE author='wrong-time'")
    other = kb.create_task(board, title="other")
    kb.add_comment(board, other, "wrong-card", "UNBLOCK: unrelated")
    board.commit()
    kb.unblock_task(board, tid)
    row = ko.compute_outcome(board, tid, include_active=True)
    assert row["stops"][0]["resumed_by"] == expected
    assert [t["by"] for t in row["touches"] if t["kind"] == "unblock"] == [expected]


def test_owner_app_approval_passes_review_gate(board):
    tid = claimed(board)
    kb.add_comment(board, tid, "owner", "approve: verified", source={"surface": "app"})
    assert gates.main(["--db", str(kb.kanban_db_path()), "review", "--card", tid]) == gates.PASS


@pytest.mark.parametrize("finish", ["block", "complete"])
def test_worker_session_survives_terminal_run_metadata(board, finish):
    tid = claimed(board)
    kb.add_comment(board, tid, "lina", "own terminal", source={"surface": "cli", "session": "worker-session"})
    if finish == "block":
        kb.block_task(board, tid, reason="wait")
    else:
        kb.complete_task(board, tid, result="done", metadata={"turns_used": 4})
    row = ko.compute_outcome(board, tid)
    assert row["touches"] == []
    run = board.execute("SELECT metadata FROM task_runs WHERE task_id=?", (tid,)).fetchone()
    assert json.loads(run["metadata"])["worker_session_id"] == "worker-session"
    if finish == "complete":
        assert row["turns_used"] == 4


def test_refresh_keeps_known_sizes_but_rebuild_remeasures(board, monkeypatch):
    tid = kb.create_task(board, title="size", assignee="lina", goal_mode=True,
                         body="Size: S\n## To-dos\n- acme/repo#7")
    kb.complete_task(board, tid, result="done")
    monkeypatch.setattr(ko, "_profile_home", lambda _: (True, None))
    calls = []

    def run(command, **kwargs):
        calls.append(command)
        if "pr" in command:
            data = {"additions": 5, "deletions": 2, "changedFiles": 1}
        elif "closedByPullRequestsReferences,labels,body" in command:
            data = {"body": "Size: S", "labels": [], "closedByPullRequestsReferences": [{"url": "https://github.com/acme/repo/pull/8"}]}
        else:
            return SimpleNamespace(returncode=0, stdout="CLOSED")
        return SimpleNamespace(returncode=0, stdout=json.dumps(data))

    monkeypatch.setattr(ko.subprocess, "run", run)
    cli.run_slash("outcomes --refresh --json")
    assert ko.list_outcomes(board)[0]["actual_size"] == "S"
    first_calls = len(calls)
    cli.run_slash("outcomes --refresh --json")
    assert len(calls) == first_calls
    cli.run_slash("outcomes --rebuild --json")
    assert len(calls) > first_calls
