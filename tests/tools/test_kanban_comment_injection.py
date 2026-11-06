"""Live operator-note injection into a running kanban worker.

``tools.kanban_tools.inject_new_comments_from_env`` polls the worker's task
for comments added *after* the run started and folds them into the live turn
via the agent's OUT-OF-BAND steer channel — so a user can talk to a running
task without the block→comment→unblock dance or a restart.

Verifies: no-op off a worker, watermark seeding (history isn't re-injected),
new comments steer, and own-authored comments are skipped.
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

import pytest

_WORKTREE = Path(__file__).resolve().parents[2]
if str(_WORKTREE) not in sys.path:
    sys.path.insert(0, str(_WORKTREE))

from hermes_cli import kanban_db as kb
from hermes_cli import kanban_db_connect as kbc
import tools.kanban_tools as kt


class FakeAgent:
    def __init__(self):
        self.steers: list[str] = []

    def steer(self, text: str) -> bool:
        self.steers.append(text)
        return True


@pytest.fixture
def worker_home(tmp_path, monkeypatch):
    home = tmp_path / "hermes_home"
    home.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setenv("HOME", str(tmp_path))
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    for var in ("HERMES_KANBAN_DB", "HERMES_KANBAN_WORKSPACES_ROOT", "HERMES_KANBAN_HOME", "HERMES_KANBAN_BOARD"):
        monkeypatch.delenv(var, raising=False)
    try:
        import hermes_constants
        hermes_constants._cached_default_hermes_root = None  # type: ignore[attr-defined]
    except Exception:
        pass
    kb._INITIALIZED_PATHS.clear()
    # Reset module-level poll state so tests don't leak into each other.
    kt._comment_watermark.clear()
    kt._comment_poll_last_attempt = 0.0
    monkeypatch.delenv("HERMES_KANBAN_RUN_ID", raising=False)
    return home


def _unthrottle():
    """Bypass the inter-poll rate limit for deterministic tests."""
    kt._comment_poll_last_attempt = 0.0


def test_noop_without_worker_env(worker_home, monkeypatch):
    monkeypatch.delenv("HERMES_KANBAN_TASK", raising=False)
    agent = FakeAgent()
    assert kt.inject_new_comments_from_env(agent) is False
    assert agent.steers == []


def _running_worker(monkeypatch, profile="worker-bot"):
    with kbc.connect_closing() as conn:
        tid = kb.create_task(conn, title="comment delivery", assignee=profile)
        run_id = kb.claim_task(conn, tid).current_run_id
    monkeypatch.setenv("HERMES_KANBAN_TASK", tid)
    monkeypatch.setenv("HERMES_KANBAN_RUN_ID", str(run_id))
    monkeypatch.setenv("HERMES_PROFILE", profile)
    agent = FakeAgent()
    assert kt.inject_new_comments_from_env(agent) is False
    return tid, run_id, agent


def test_same_profile_main_session_instructions_arrive_three_times(worker_home, monkeypatch):
    """Regression for #246: all three main-session notes used to be dropped."""
    # Given a running card whose main session uses the same profile name.
    tid, _, agent = _running_worker(monkeypatch)
    delivered = []
    bodies = [f"main-session instruction {i}" for i in range(3)]
    # When each instruction arrives in a separate polling window.
    for body in bodies:
        with monkeypatch.context() as main:
            main.delenv("HERMES_KANBAN_TASK")
            main.delenv("HERMES_KANBAN_RUN_ID")
            result = json.loads(kt._handle_comment({"task_id": tid, "body": body}))
            assert result["ok"], result
        _unthrottle()
        delivered.append(kt.inject_new_comments_from_env(agent))
    # Then every instruction reaches the worker, once and in order.
    assert delivered == [True, True, True]
    assert len(agent.steers) == 3
    assert all(body in note for body, note in zip(bodies, agent.steers))


def test_own_run_comment_is_skipped_in_mixed_batch(worker_home, monkeypatch):
    # Given own progress and main-session guidance with the same author.
    tid, _, agent = _running_worker(monkeypatch)
    own = "own progress must not echo"
    other = "main-session guidance must arrive"
    result = json.loads(kt._handle_comment({"task_id": tid, "body": own}))
    assert result["ok"], result
    with kbc.connect_closing() as conn:
        kb.add_comment(conn, tid, author="worker-bot", body=other)
    # When polling the mixed batch.
    _unthrottle()
    assert kt.inject_new_comments_from_env(agent) is True
    # Then only the foreign note is delivered, with no replay on the next poll.
    assert len(agent.steers) == 1
    assert other in agent.steers[0]
    assert own not in agent.steers[0]
    _unthrottle()
    assert kt.inject_new_comments_from_env(agent) is False


def test_own_run_comment_is_skipped(worker_home, monkeypatch):
    # Given a comment written by this card's own tool session.
    tid, _, agent = _running_worker(monkeypatch)
    result = json.loads(kt._handle_comment({"task_id": tid, "body": "own progress"}))
    assert result["ok"], result
    # When the worker polls.
    _unthrottle()
    assert kt.inject_new_comments_from_env(agent) is False
    # Then the comment never echoes.
    assert agent.steers == []


def test_other_run_comment_with_same_author_arrives(worker_home, monkeypatch):
    # Given a real different run on another card writing on this card.
    tid, _, agent = _running_worker(monkeypatch)
    with kbc.connect_closing() as conn:
        other = kb.create_task(conn, title="other run", assignee="worker-bot")
        other_run = kb.claim_task(conn, other).current_run_id
        kb.add_comment(conn, tid, author="worker-bot", body="other run guidance", run_id=other_run)
    # When the bound worker polls.
    _unthrottle()
    assert kt.inject_new_comments_from_env(agent) is True
    # Then a different run's comment is not mistaken for its own.
    assert "other run guidance" in agent.steers[0]


def test_worker_run_end_comment_arrives(worker_home, monkeypatch):
    from hermes_cli.worker.kanban_link import record_end

    # Given a card whose profile even matches the worker-end author.
    tid, _, agent = _running_worker(monkeypatch, profile="lina-worker")
    record_end({"task": tid}, {"id": "external-run", "runner": "local"},
               "done", "external-run", "worker run finished")
    # When the card's live bridge polls the real worker-end write.
    _unthrottle()
    assert kt.inject_new_comments_from_env(agent) is True
    # Then the completion notice reaches the card without restarting it.
    assert "worker run finished" in agent.steers[0]


def test_receipts_follow_live_delivery_once_without_echo(worker_home, monkeypatch):
    # Given two foreign comments queued but not yet drained into the conversation.
    tid, run_id, agent = _running_worker(monkeypatch)
    agent._session_messages = []
    with kbc.connect_closing() as conn:
        for body in ("first instruction", "second instruction"):
            kb.add_comment(conn, tid, author="operator", body=body)
    _unthrottle()
    assert kt.inject_new_comments_from_env(agent) is True
    _unthrottle()
    assert kt.inject_new_comments_from_env(agent) is False
    with kbc.connect_closing() as conn:
        assert len(kb.list_comments(conn, tid)) == 2
    # When the queued steer lands as a live user row, then two polls run.
    agent._session_messages.append({"role": "user", "content": agent.steers.pop()})
    for _ in range(2):
        _unthrottle()
        assert kt.inject_new_comments_from_env(agent) is False
    # Then each delivered comment has one receipt attributed to this run, with no echo.
    with kbc.connect_closing() as conn:
        receipts = kb.list_comments(conn, tid)[2:]
        events = [e for e in kb.list_events(conn, tid) if e.kind == "commented" and e.run_id == run_id]
    assert len(receipts) == 2
    assert {e.payload["comment_id"] for e in events} == {c.id for c in receipts}
    assert agent.steers == []


def test_seed_then_inject_new_comment(worker_home, monkeypatch):
    conn = kbc.connect()
    try:
        tid = kb.create_task(conn, title="live task")
        kb.add_comment(conn, tid, author="desktop", body="pre-existing note")
    finally:
        conn.close()

    monkeypatch.setenv("HERMES_KANBAN_TASK", tid)
    monkeypatch.setenv("HERMES_PROFILE", "worker-bot")
    agent = FakeAgent()

    # First poll seeds the watermark past the existing thread — no injection.
    _unthrottle()
    assert kt.inject_new_comments_from_env(agent) is False
    assert agent.steers == []

    conn = kbc.connect()
    try:
        kb.add_comment(conn, tid, author="desktop", body="actually use the v2 API")
    finally:
        conn.close()

    _unthrottle()
    assert kt.inject_new_comments_from_env(agent) is True
    assert len(agent.steers) == 1
    assert "v2 API" in agent.steers[0]

    # Watermark advanced — a re-poll with no new comments injects nothing.
    _unthrottle()
    assert kt.inject_new_comments_from_env(agent) is False
    assert len(agent.steers) == 1


def test_skips_own_authored_comments(worker_home, monkeypatch):
    conn = kbc.connect()
    try:
        tid = kb.create_task(conn, title="echo guard")
    finally:
        conn.close()

    monkeypatch.setenv("HERMES_KANBAN_TASK", tid)
    monkeypatch.setenv("HERMES_PROFILE", "worker-bot")
    agent = FakeAgent()

    _unthrottle()
    kt.inject_new_comments_from_env(agent)  # seed

    conn = kbc.connect()
    try:
        kb.add_comment(conn, tid, author="worker-bot", body="i did a thing")
    finally:
        conn.close()

    _unthrottle()
    assert kt.inject_new_comments_from_env(agent) is False
    assert agent.steers == []


def test_delegated_child_in_worker_process_neither_receives_nor_consumes_notes(worker_home, monkeypatch):
    """A delegate_task child inherits the worker's ``HERMES_KANBAN_TASK``; operator notes
    address the worker, so the child must not be steered by them and must not advance the
    shared watermark (which would make the worker miss them) (#112817)."""
    from agent.delegation_context import delegated_child_context

    conn = kbc.connect()
    try:
        tid = kb.create_task(conn, title="live task")
    finally:
        conn.close()
    monkeypatch.setenv("HERMES_KANBAN_TASK", tid)
    monkeypatch.setenv("HERMES_PROFILE", "worker-bot")

    worker = FakeAgent()
    _unthrottle()
    assert kt.inject_new_comments_from_env(worker) is False  # seed

    conn = kbc.connect()
    try:
        kb.add_comment(conn, tid, author="desktop", body="operator note for the worker")
    finally:
        conn.close()

    child = FakeAgent()
    with delegated_child_context():
        _unthrottle()
        assert kt.inject_new_comments_from_env(child) is False
    assert child.steers == []

    _unthrottle()
    assert kt.inject_new_comments_from_env(worker) is True
    assert "operator note" in worker.steers[0]


def test_skips_own_authored_comments_without_env_profile(worker_home, monkeypatch):
    """Own comments are skipped even when ``HERMES_PROFILE`` is absent: the
    injection filter resolves the worker's identity the same way the persisted
    write side does (env → ``HERMES_HOME``-derived active profile), so a worker's
    own notes can never steer its live turn as fake operator messages."""
    conn = kbc.connect()
    try:
        tid = kb.create_task(conn, title="identity echo guard")
    finally:
        conn.close()

    monkeypatch.setenv("HERMES_KANBAN_TASK", tid)
    monkeypatch.delenv("HERMES_PROFILE", raising=False)
    monkeypatch.delenv("HERMES_PROFILE_NAME", raising=False)
    agent = FakeAgent()

    _unthrottle()
    kt.inject_new_comments_from_env(agent)  # seed

    identity = kt._persisted_identity()
    assert identity != "worker"  # resolved from HERMES_HOME, not the generic label

    conn = kbc.connect()
    try:
        kb.add_comment(conn, tid, author=identity, body="note to self")
    finally:
        conn.close()

    _unthrottle()
    assert kt.inject_new_comments_from_env(agent) is False
    assert agent.steers == []
