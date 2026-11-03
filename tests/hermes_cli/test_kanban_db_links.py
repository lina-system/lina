"""SQLite link extraction and facade patch contracts for issue 184."""
from __future__ import annotations

import importlib
import sqlite3
from collections.abc import Iterator
from pathlib import Path

import pytest

from hermes_cli import kanban_db as kb
from hermes_cli import kanban_db_connect as kbc


@pytest.fixture
def board(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Iterator[sqlite3.Connection]:
    monkeypatch.setenv("HERMES_HOME", str(tmp_path))
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    for name in ("HERMES_KANBAN_TASK", "HERMES_KANBAN_RUN_ID", "HERMES_KANBAN_DB",
                 "HERMES_KANBAN_BOARD", "HERMES_KANBAN_HOME"):
        monkeypatch.delenv(name, raising=False)
    kb.init_db()
    with kbc.connect_closing() as conn:
        yield conn


def test_link_unlink_promotes_child_when_last_dependency_removed(board: sqlite3.Connection):
    # Given real tasks and the extracted implementation loaded at test time.
    links = importlib.import_module("hermes_cli.kanban_db_links")
    parent = kb.create_task(board, title="parent")
    child = kb.create_task(board, title="child")

    # When the sibling links and the facade unlinks the same edge.
    gated = links.link_tasks(board, parent, child, by="operator", reason="wait")
    linked_status = kb.get_task(board, child).status
    parents = links.parent_ids(board, child)
    children = links.child_ids(board, parent)
    removed = kb.unlink_tasks(board, parent, child, by="operator", reason="resume")

    # Then the database, readiness, and audit payloads reflect both mutations.
    assert (gated, linked_status, parents, children, removed) == (
        True, "todo", [parent], [child], True,
    )
    assert kb.get_task(board, child).status == "ready"
    assert kb.parent_ids(board, child) == []
    events = {e.kind: e.payload for e in kb.list_events(board, child)}
    assert events["linked"] == {
        "parent": parent, "child": child, "by": "operator", "reason": "wait",
    }
    assert events["unlinked"] == {
        "parent": parent, "child": child, "by": "operator", "reason": "resume",
    }


def test_cycle_rejected_when_path_already_reaches_parent(board: sqlite3.Connection):
    # Given a real two-edge path.
    links = importlib.import_module("hermes_cli.kanban_db_links")
    a, b, c = [kb.create_task(board, title=title) for title in ("a", "b", "c")]
    kb.link_tasks(board, a, b)
    links.link_tasks(board, b, c)

    # When the extracted implementation attempts to close the cycle.
    with pytest.raises(ValueError, match="would create a cycle"):
        links.link_tasks(board, c, a)

    # Then cycle traversal observes the path without recording a new edge.
    assert links._would_cycle(board, c, a) is True
    assert links.parent_ids(board, a) == []


def test_link_rejects_edge_when_facade_cycle_seam_is_patched(
    board: sqlite3.Connection, monkeypatch: pytest.MonkeyPatch,
):
    # Given a patched facade after the sibling has already loaded.
    links = importlib.import_module("hermes_cli.kanban_db_links")
    parent = kb.create_task(board, title="parent")
    child = kb.create_task(board, title="child")
    monkeypatch.setattr(kb, "_would_cycle", lambda conn, parent_id, child_id: True)

    # When a caller uses the sibling directly.
    with pytest.raises(ValueError, match="would create a cycle"):
        links.link_tasks(board, parent, child)

    # Then the patched policy prevents a durable edge.
    assert kb.parent_ids(board, child) == []


@pytest.mark.parametrize("direction", ["parent", "child"])
def test_linked_ids_use_facade_seam_when_patched(
    board: sqlite3.Connection, monkeypatch: pytest.MonkeyPatch, direction: str,
):
    # Given a real edge and a facade lookup override that selects the opposite direction.
    links = importlib.import_module("hermes_cli.kanban_db_links")
    parent = kb.create_task(board, title="parent")
    child = kb.create_task(board, title="child")
    kb.link_tasks(board, parent, child)
    lookup = kb._linked_ids
    monkeypatch.setattr(kb, "_linked_ids", lambda conn, want, where, tid:
                        lookup(conn, where, want, tid))

    # When the sibling queries through its parent/child helpers.
    result = (links.parent_ids(board, parent) if direction == "parent"
              else links.child_ids(board, child))

    # Then the facade override, not a captured sibling helper, determines the result.
    assert result == ([child] if direction == "parent" else [parent])


def test_unlink_uses_facade_recompute_when_patched(
    board: sqlite3.Connection, monkeypatch: pytest.MonkeyPatch,
):
    # Given an edge and a facade readiness override installed after import.
    links = importlib.import_module("hermes_cli.kanban_db_links")
    parent = kb.create_task(board, title="parent")
    child = kb.create_task(board, title="child")
    kb.link_tasks(board, parent, child)
    monkeypatch.setattr(kb, "recompute_ready", lambda conn: 0)

    # When the sibling removes the edge.
    removed = links.unlink_tasks(board, parent, child)

    # Then deletion succeeds while the overridden promotion leaves the child in todo.
    assert removed is True
    assert kb.parent_ids(board, child) == []
    assert kb.get_task(board, child).status == "todo"


@pytest.mark.parametrize("operation", ["link", "unlink"])
def test_mutation_rolls_back_when_facade_event_writer_fails(
    board: sqlite3.Connection, monkeypatch: pytest.MonkeyPatch, operation: str,
):
    # Given a real board and the facade event writer replaced after sibling import.
    links = importlib.import_module("hermes_cli.kanban_db_links")
    parent = kb.create_task(board, title="parent")
    child = kb.create_task(board, title="child")
    if operation == "unlink":
        kb.link_tasks(board, parent, child)
    def fail_event(*args, **kwargs):
        raise sqlite3.OperationalError("event write failed")
    monkeypatch.setattr(kb, "_append_event", fail_event)
    mutation = links.link_tasks if operation == "link" else links.unlink_tasks

    # When the extracted mutation reaches the patched writer.
    with pytest.raises(sqlite3.OperationalError, match="event write failed"):
        mutation(board, parent, child)

    # Then its edge and readiness changes roll back together.
    assert kb.parent_ids(board, child) == ([parent] if operation == "unlink" else [])
    assert kb.get_task(board, child).status == ("todo" if operation == "unlink" else "ready")
