"""A dispatcher's explicit ``--board`` must reach the workers it spawns (#206).

The fence refusal must not turn into a routing change for callers OUTSIDE the
fence. A main session or gateway dispatcher that carries
``HERMES_KANBAN_DB=<default db>`` and dispatches ``board="private"`` connects to
private — so the workers it spawns, their workspace root and its tick lock must
be private's files too. Only a fenced dispatcher (its own worker, a delegated
child) still resolves pin-first, because there the pin IS its isolation.
"""

from __future__ import annotations

import os
import subprocess
from contextlib import closing
from pathlib import Path
from types import SimpleNamespace

import pytest

from hermes_cli import kanban_db as kb
from hermes_cli import kanban_db_connect as kbc
from hermes_cli import kanban_db_dispatch as kbd

_FENCE_VARS = (
    "HERMES_KANBAN_TASK",
    "HERMES_KANBAN_RUN_ID",
    "HERMES_DELEGATED_CHILD_CONTEXT",
)


@pytest.fixture
def boards(tmp_path, monkeypatch):
    """A home with ``default`` + ``private`` boards, no fence, no board pin yet."""
    home = tmp_path / ".hermes"
    home.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    for var in (*_FENCE_VARS, "HERMES_KANBAN_DB", "HERMES_KANBAN_BOARD",
                "HERMES_KANBAN_HOME", "HERMES_KANBAN_WORKSPACES_ROOT"):
        monkeypatch.delenv(var, raising=False)
    kb._INITIALIZED_PATHS.clear()
    kb.init_db()
    kb.create_board("private")
    return SimpleNamespace(
        home=home,
        default_db=kb.kanban_db_path(),
        default_workspaces=kb.workspaces_root(),
        private_db=kb.kanban_db_path(board="private"),
        private_workspaces=kb.workspaces_root(board="private"),
    )


@pytest.fixture
def unfenced_dispatcher(boards, monkeypatch):
    """Main-session / gateway dispatcher: every board pin on the default board, no
    fence — the environment a dispatcher inherits from an agent shell or the
    gateway, which is exactly what an explicit ``--board`` has to outrank."""
    monkeypatch.setenv("HERMES_KANBAN_DB", str(boards.default_db))
    monkeypatch.setenv("HERMES_KANBAN_WORKSPACES_ROOT", str(boards.default_workspaces))
    monkeypatch.setenv("HERMES_KANBAN_BOARD", "default")
    return boards


def _spawn_env(monkeypatch) -> dict:
    """Run ``_default_spawn`` far enough to capture the child env, without spawning."""
    captured = {}

    class FakeProc:
        pid = 4242

    def fake_popen(cmd, *args, **kwargs):
        captured["env"] = kwargs.get("env", {})
        return FakeProc()

    monkeypatch.setattr(subprocess, "Popen", fake_popen)

    task = kb.Task(
        id="t_dispatch_routing", title="routed card", body=None, assignee="worker",
        status="ready", priority=0, created_by="user", created_at=0, started_at=None,
        completed_at=None, workspace_kind="scratch", workspace_path=None, claim_lock=None,
        claim_expires=None, tenant=None,
    )
    kbd._default_spawn(task, "/tmp/unused-by-this-assertion", board="private")
    return captured["env"]


def test_unfenced_spawn_pins_the_board_that_was_asked_for(unfenced_dispatcher, monkeypatch):
    """(a) env: the spawned worker gets private's DB + workspace root and slug."""
    env = _spawn_env(monkeypatch)

    boards = unfenced_dispatcher
    assert env["HERMES_KANBAN_DB"] == str(boards.private_db)
    assert env["HERMES_KANBAN_WORKSPACES_ROOT"] == str(boards.private_workspaces)
    assert env["HERMES_KANBAN_BOARD"] == "private"


def test_unfenced_tick_lock_is_keyed_on_the_asked_for_board(unfenced_dispatcher, monkeypatch):
    """(b) ``dispatch_once`` takes its single-writer lock on private's DB."""
    boards = unfenced_dispatcher
    seen = {}

    import contextlib as _contextlib

    @_contextlib.contextmanager
    def fake_lock(db_path):
        seen["db_path"] = Path(db_path)
        yield False

    monkeypatch.setattr(kbc, "_dispatch_tick_lock", fake_lock)

    with closing(kbc.connect(board="private")) as conn:
        result = kbd.dispatch_once(conn, board="private")

    assert seen["db_path"] == boards.private_db
    assert result.skipped_locked is True


def test_unfenced_lane_workspace_is_on_the_asked_for_board(unfenced_dispatcher, monkeypatch):
    """(b) ``_dispatch_lane_task`` resolves the workspace on private's root."""
    boards = unfenced_dispatcher
    seen = {}
    # The assignee is a stand-in, not an installed profile: trust it (the fallback
    # the dispatcher itself documents for an unimportable profile list).
    monkeypatch.setattr(kbd, "_profile_exists_fn", lambda: None)

    with closing(kbc.connect(board="private")) as conn:
        task_id = kb.create_task(conn, title="lane card", assignee="worker")
        row = conn.execute("SELECT * FROM tasks WHERE id = ?", (task_id,)).fetchone()
        result = kbd.DispatchResult()

        def spawn_fn(task, workspace, board=None):
            # No worker process: the claim is released on the spawn-failure path,
            # which is the only thing this test needs from the lane.
            seen["workspace"] = Path(workspace)

        kbd._dispatch_lane_task(
            conn, row, "worker", result, lane="ready", dry_run=False, ttl_seconds=None,
            board="private", failure_limit=2, spawn_fn=spawn_fn, per_profile_cap=None,
            per_profile_running={},
        )

    workspace = seen["workspace"]
    assert workspace.parent == boards.private_workspaces
    assert workspace.name == task_id


def test_fenced_dispatcher_keeps_resolving_to_the_pin(boards, monkeypatch):
    """(c) A dispatcher that is itself a worker keeps resolving pin-first: never
    refused, and its spawn env / tick lock stay on the pin exactly as before."""
    import contextlib as _contextlib

    monkeypatch.setenv("HERMES_KANBAN_DB", str(boards.default_db))
    monkeypatch.setenv("HERMES_KANBAN_WORKSPACES_ROOT", str(boards.default_workspaces))
    monkeypatch.setenv("HERMES_KANBAN_BOARD", "default")
    monkeypatch.setenv("HERMES_KANBAN_TASK", "t_fenced_dispatcher")

    env = _spawn_env(monkeypatch)
    assert env["HERMES_KANBAN_DB"] == str(boards.default_db)
    assert env["HERMES_KANBAN_WORKSPACES_ROOT"] == str(boards.default_workspaces)
    # The child is fenced to the pinned board, whatever board the tick names.
    assert env["HERMES_KANBAN_TASK"] == "t_dispatch_routing"

    seen = {}

    @_contextlib.contextmanager
    def fake_lock(db_path):
        seen["db_path"] = Path(db_path)
        yield False

    monkeypatch.setattr(kbc, "_dispatch_tick_lock", fake_lock)
    with closing(kbc.connect()) as conn:
        assert kbd.dispatch_once(conn, board="private").skipped_locked is True
    assert seen["db_path"] == boards.default_db
