"""A fenced caller asking for another board is refused, not silently rerouted (#206).

The dispatcher pins every worker (``HERMES_KANBAN_TASK``) and delegated child
(``HERMES_DELEGATED_CHILD_CONTEXT``) to its own board through
``HERMES_KANBAN_DB`` & friends. That fence stays: workers must not reach other
boards. But ``kanban_create(board="private", …)`` from a work session used to
return ``{"ok": true}`` while the card landed on the pinned board, so the work
record link (``private/<id>``) read as "state unknown" later.

Contract: when the fence would resolve an explicit board to a different file
than that board's own, resolution raises ``BoardPinnedError`` naming the board
asked for and the pinned one, before anything is written. Same board, unset
board, unfenced callers and board-enumerating machine flows
(``pin_first_board_resolution``) are unchanged.
"""

from __future__ import annotations

import argparse
import json
import sqlite3
from contextlib import closing
from pathlib import Path
from types import SimpleNamespace

import pytest

from hermes_cli import kanban_db as kb

# Every env var that pins a board path or fences a caller. Each test starts from
# a box that has none of them.
_PIN_AND_FENCE_VARS = (
    "HERMES_KANBAN_DB",
    "HERMES_KANBAN_BOARD",
    "HERMES_KANBAN_HOME",
    "HERMES_KANBAN_WORKSPACES_ROOT",
    "HERMES_KANBAN_ATTACHMENTS_ROOT",
    "HERMES_KANBAN_TASK",
    "HERMES_KANBAN_RUN_ID",
    "HERMES_DELEGATED_CHILD_CONTEXT",
)


@pytest.fixture
def boards(tmp_path, monkeypatch):
    """Isolated home holding a ``default`` and a ``private`` board, both created.

    Returns the two physical DB paths captured BEFORE any pin is set, so the
    assertions can read them directly (opening them through the resolver would
    be the very thing under test).
    """
    home = tmp_path / ".hermes"
    home.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    for var in _PIN_AND_FENCE_VARS:
        monkeypatch.delenv(var, raising=False)
    kb._INITIALIZED_PATHS.clear()
    kb.init_db()
    kb.create_board("private")
    return SimpleNamespace(
        default_db=kb.kanban_db_path(),
        private_db=kb.kanban_db_path(board="private"),
    )


@pytest.fixture
def worker(boards, monkeypatch):
    """Pin the process exactly the way the dispatcher pins a worker: on the
    ``default`` board, with ``HERMES_KANBAN_TASK`` set."""
    monkeypatch.setenv("HERMES_KANBAN_DB", str(boards.default_db))
    monkeypatch.setenv("HERMES_KANBAN_BOARD", "default")
    monkeypatch.setenv("HERMES_KANBAN_TASK", "t_pinned_worker")
    return boards


def _task_titles(db_path: Path) -> list[str]:
    """Titles in a board DB, read with sqlite3 directly (never through the resolver)."""
    if not db_path.exists():
        return []
    with closing(sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)) as conn:
        return [row[0] for row in conn.execute("SELECT title FROM tasks").fetchall()]


def _create_via_tool(args: dict) -> dict:
    """Dispatch ``kanban_create`` through the registry (the real tool boundary)."""
    from tools import kanban_tools  # registers the kanban tools on import
    from tools.registry import registry

    return json.loads(registry.dispatch("kanban_create", args))


def test_worker_create_on_another_board_is_refused_and_writes_nothing(worker):
    """The M0 cycle-test shape: a work session registers a ``private`` card.

    It must come back as an error naming both boards (and both remedies) with
    no card written to either DB — not ``ok`` plus a card on the pinned board.
    """
    out = _create_via_tool(
        {"title": "private-source card", "assignee": "worker", "board": "private"},
    )

    assert "error" in out, out
    message = out["error"]
    assert "private" in message, message
    assert "default" in message, message
    assert "main session" in message, message
    assert "leave board unset" in message, message

    assert _task_titles(worker.default_db) == [], "the refusal wrote to the pinned board"
    assert _task_titles(worker.private_db) == [], "the refusal wrote to the requested board"


def test_worker_create_on_its_own_board_still_works(worker):
    """``board="default"`` from a default-board worker is the same board: allowed."""
    out = _create_via_tool(
        {"title": "own-board card", "assignee": "worker", "board": "default"},
    )

    assert out.get("ok") is True, out
    assert _task_titles(worker.default_db) == ["own-board card"]


def test_worker_create_without_a_board_still_works(worker):
    """No explicit board is the documented alternative: it resolves to the pin."""
    out = _create_via_tool({"title": "unset-board card", "assignee": "worker"})

    assert out.get("ok") is True, out
    assert _task_titles(worker.default_db) == ["unset-board card"]


def test_unfenced_caller_still_reaches_the_other_board(boards, monkeypatch):
    """Outside the fence an explicit board opens that board, as today — even on a
    box whose ``HERMES_KANBAN_DB`` pins the default board (main session)."""
    monkeypatch.setenv("HERMES_KANBAN_DB", str(boards.default_db))

    out = _create_via_tool(
        {"title": "main-session card", "assignee": "worker", "board": "private"},
    )

    assert out.get("ok") is True, out
    assert _task_titles(boards.private_db) == ["main-session card"]
    assert _task_titles(boards.default_db) == []


def test_pin_first_board_resolution_stays_silent(worker):
    """Machine flows enumerate every slug; inside ``pin_first_board_resolution``
    each one still resolves to the pin with no refusal."""
    with kb.pin_first_board_resolution():
        assert kb.kanban_db_path(board="private") == worker.default_db
        assert kb.kanban_db_path(board="default") == worker.default_db
        assert kb.kanban_db_path() == worker.default_db


def test_refusal_message_names_both_boards(worker):
    """The refusal carries the asked-for slug, the pinned slug and the remedies."""
    with pytest.raises(kb.BoardPinnedError) as excinfo:
        kb.kanban_db_path(board="private")

    message = str(excinfo.value)
    assert "'private'" in message, message
    assert "'default'" in message, message
    assert "nothing was written" in message, message
    assert "main session" in message and "leave board unset" in message, message


def test_worker_cli_board_flag_is_refused(worker, capsys):
    """``hermes kanban --board private create …`` inside a worker exits non-zero
    with the refusal and writes nothing (the CLI's own ``--board`` surface)."""
    from hermes_cli import kanban as kc

    parser = argparse.ArgumentParser(prog="hermes", add_help=False)
    sub = parser.add_subparsers(dest="command")
    kc.build_parser(sub)
    args = parser.parse_args(["kanban", "--board", "private", "create", "cross-board card"])

    rc = kc.kanban_command(args)

    assert rc != 0
    stderr = capsys.readouterr().err
    assert "'private'" in stderr and "'default'" in stderr, stderr
    assert "main session" in stderr, stderr
    assert _task_titles(worker.default_db) == []
    assert _task_titles(worker.private_db) == []


def test_worker_boards_create_is_refused_and_leaves_no_metadata(worker, capsys):
    """``hermes kanban boards create <slug>`` from a worker is the same cross-board
    write through a different surface: refused, with no board dir left behind."""
    from hermes_cli import kanban as kc

    parser = argparse.ArgumentParser(prog="hermes", add_help=False)
    sub = parser.add_subparsers(dest="command")
    kc.build_parser(sub)
    args = parser.parse_args(["kanban", "boards", "create", "elsewhere"])

    rc = kc.kanban_command(args)

    assert rc != 0
    stderr = capsys.readouterr().err
    assert "'elsewhere'" in stderr and "'default'" in stderr, stderr
    assert not (kb.boards_root() / "elsewhere").exists()


def test_a_valid_slug_can_only_address_boards_root(boards):
    """A slug that passes the regex resolves to ``boards_root()/<slug>/<leaf>`` —
    the containment-checked directory, never a path built from raw caller text."""
    path = kb.kanban_db_path(board="private")

    root = kb.boards_root().resolve()
    assert path.parent.parent == root, path
    assert path.parent.name == "private", path
    assert path == boards.private_db


def test_a_slug_that_escapes_boards_root_is_rejected_before_a_path_is_built(boards):
    """``../`` in a slug never becomes a path: the containment check refuses it, and
    the public resolver refuses it even earlier (slug validation)."""
    from hermes_cli import kanban_db_paths as kbp

    with pytest.raises(ValueError, match="invalid board slug"):
        kbp.own_board_path("../escape", ("kanban.db",), "kanban.db")
    with pytest.raises(ValueError, match="invalid board slug"):
        kbp.own_board_path("private/../../escape", ("kanban.db",), "kanban.db")
    with pytest.raises(ValueError):
        kb.kanban_db_path(board="../escape")

    assert not (kb.kanban_home() / "kanban" / "escape").exists()
    assert not (kb.kanban_home() / "escape").exists()


def test_worker_rm_of_another_board_is_refused_before_the_archive(worker, capsys):
    """``hermes kanban boards rm <other>`` from a worker: the refusal must come
    BEFORE the archive, so the board keeps its folder and ``board.json`` and
    ``_archived/`` stays empty (Codex P2 on #213)."""
    from hermes_cli import kanban as kc

    board_dir = kb.boards_root() / "private"
    before = sorted(p.name for p in board_dir.iterdir())

    parser = argparse.ArgumentParser(prog="hermes", add_help=False)
    sub = parser.add_subparsers(dest="command")
    kc.build_parser(sub)
    args = parser.parse_args(["kanban", "boards", "rm", "private"])

    rc = kc.kanban_command(args)

    assert rc != 0
    stderr = capsys.readouterr().err
    assert "'private'" in stderr and "'default'" in stderr, stderr
    assert board_dir.is_dir(), "the refusal moved the board it refused to touch"
    assert sorted(p.name for p in board_dir.iterdir()) == before
    assert json.loads((board_dir / "board.json").read_text(encoding="utf-8"))["slug"] == "private"
    archived = kb.boards_root() / "_archived"
    assert not archived.exists() or list(archived.iterdir()) == []


def test_worker_hard_delete_of_another_board_is_refused(worker):
    """The ``--delete`` arm moves/removes the folder without writing metadata, so it
    needs the same up-front refusal — nothing is removed at all."""
    with pytest.raises(kb.BoardPinnedError) as excinfo:
        kb.remove_board("private", archive=False)

    assert "'private'" in str(excinfo.value) and "'default'" in str(excinfo.value)
    assert (kb.boards_root() / "private" / "kanban.db").is_file()
    assert (kb.boards_root() / "private" / "board.json").is_file()


def test_worker_import_is_refused_before_anything_lands(boards, tmp_path, monkeypatch):
    """``kanban boards import`` from a worker resolves the new board first: an
    archive's DB must not be moved into a board folder the caller cannot open."""
    from hermes_cli import kanban_transfer as kt

    archive = tmp_path / "private.tar.gz"
    kt.export_board("private", str(archive))

    monkeypatch.setenv("HERMES_KANBAN_DB", str(boards.default_db))
    monkeypatch.setenv("HERMES_KANBAN_BOARD", "default")
    monkeypatch.setenv("HERMES_KANBAN_TASK", "t_pinned_worker")

    with pytest.raises(kb.BoardPinnedError):
        kt.import_board(str(archive), "imported")

    assert not (kb.boards_root() / "imported").exists()


def test_a_board_whose_db_is_a_symlink_to_the_pin_is_its_own_board(boards, monkeypatch):
    """A board's ``kanban.db`` is allowed to be a symlink: naming that board from a
    worker pinned to the file it points at is naming the worker's own board, so it
    passes — while a board holding a different file is still refused (Codex P2)."""
    kb.create_board("other")
    shared = kb.kanban_home() / "shared" / "kanban.db"
    shared.parent.mkdir()
    shared.write_bytes(b"")
    (kb.boards_root() / "private" / "kanban.db").unlink()
    (kb.boards_root() / "private" / "kanban.db").symlink_to(shared)

    monkeypatch.setenv("HERMES_KANBAN_DB", str(shared))
    monkeypatch.setenv("HERMES_KANBAN_BOARD", "default")
    monkeypatch.setenv("HERMES_KANBAN_TASK", "t_pinned_worker")

    assert kb.kanban_db_path(board="private") == shared
    with pytest.raises(kb.BoardPinnedError):
        kb.kanban_db_path(board="other")


def test_worker_rm_of_its_symlinked_own_board_archives_and_leaves_a_tombstone(boards, monkeypatch, capsys):
    """``boards rm`` of a worker's own board whose ``kanban.db`` links to the pin: the
    move takes the symlink with it, so the tombstone write must use the pin check made
    before the move. The board is archived AND an ``archived`` tombstone sits at the
    slug, so a stale caller cannot recreate it (#259)."""
    from hermes_cli import kanban as kc

    shared = kb.kanban_home() / "shared" / "kanban.db"
    shared.parent.mkdir()
    shared.write_bytes(b"")
    board_dir = kb.boards_root() / "private"
    (board_dir / "kanban.db").unlink()
    (board_dir / "kanban.db").symlink_to(shared)

    monkeypatch.setenv("HERMES_KANBAN_DB", str(shared))
    monkeypatch.setenv("HERMES_KANBAN_BOARD", "private")
    monkeypatch.setenv("HERMES_KANBAN_TASK", "t_pinned_worker")

    parser = argparse.ArgumentParser(prog="hermes", add_help=False)
    sub = parser.add_subparsers(dest="command")
    kc.build_parser(sub)
    rc = kc.kanban_command(parser.parse_args(["kanban", "boards", "rm", "private"]))

    assert rc == 0, capsys.readouterr().err
    archived = list((kb.boards_root() / "_archived").iterdir())
    assert [p.name.startswith("private-") for p in archived] == [True]
    assert (archived[0] / "kanban.db").is_symlink()
    tombstone = json.loads((board_dir / "board.json").read_text(encoding="utf-8"))
    assert tombstone["archived"] is True and tombstone["slug"] == "private"
    assert "db_path" not in tombstone
