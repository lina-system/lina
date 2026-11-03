"""Board path resolution for the Kanban DB: where a board's files live, and who may reach them.

``default`` keeps the legacy ``<root>/kanban.db`` (plus ``kanban/workspaces`` / ``kanban/logs``)
for back-compat; every other board owns ``<root>/kanban/boards/<slug>/``. Resolution is
``board=`` arg > the ``HERMES_KANBAN_*`` env pins > ``<root>/kanban/current`` > ``default``.

A fenced caller — a dispatched worker or a delegated child — cannot reach another board: when
an explicit board would resolve to a different file than the caller's pin, resolution refuses
with :class:`BoardPinnedError` naming both boards (issue #206) instead of silently answering
with the pinned one. Machine flows that enumerate boards
(``kanban_db.pin_first_board_resolution``) keep the silent pin: their slug is an iteration
variable, not caller intent.

Split out of ``hermes_cli.kanban_db``; origin-resident helpers are reached late-bound via
``_kb`` (import-cycle breaking) so monkeypatching ``kanban_db.<name>`` keeps working.
"""

from __future__ import annotations

import os
from pathlib import Path
from typing import Optional


class BoardPinnedError(ValueError):
    """A fenced caller (dispatched worker / delegated child) asked for a board
    other than the one it is pinned to.

    The fence itself is right — a worker must not reach another board — but
    silently resolving ``board=<other>`` to the pinned file told the caller it
    had written where it asked (issue #206). Resolution refuses instead, before
    anything is written."""


def kanban_home() -> Path:
    """``HERMES_KANBAN_HOME`` else ``get_default_hermes_root()``. Shared across
    profiles BY DESIGN: resolving through the active profile's HERMES_HOME would
    fork the board per profile and break the dispatcher/worker handoff."""
    override = os.environ.get("HERMES_KANBAN_HOME", "").strip()
    if override:
        return Path(override).expanduser()
    from hermes_constants import get_default_hermes_root
    return get_default_hermes_root()


def boards_root() -> Path:
    """``<root>/kanban/boards`` — parent of the *additional* named boards.
    ``default`` is deliberately not here (its DB stays at ``<root>/kanban.db``)."""
    return kanban_home() / "kanban" / "boards"


def current_board_path() -> Path:
    """``<root>/kanban/current`` — one-line slug written by ``boards switch``; absent = ``default``."""
    return kanban_home() / "kanban" / "current"


def board_dir(board: Optional[str] = None) -> Path:
    """``<root>/kanban/boards/<slug>/``. For ``default`` this holds metadata
    only (board.json, workspaces/, logs/) — its DB stays at ``<root>/kanban.db``
    for back-compat (:func:`kanban_db_path`).
    """
    return boards_root() / _kb._slug_or_default(board)


def board_exists(board: Optional[str] = None) -> bool:
    """Board has a ``board.json`` on disk; ``default`` always exists."""
    slug = _kb._slug_or_default(board)
    if slug == _kb.DEFAULT_BOARD:
        return True
    return _kb._dir_holds_board(board_dir(slug))


def own_board_path(slug: str, default_parts: tuple[str, ...], leaf: str) -> Path:
    """Where *slug*'s file lives when no env pin applies (the unfenced path).

    The non-default arm builds the file path from a **resolved, containment-checked**
    board directory: the slug is regex-validated upstream, but a path derived from
    caller data is checked here too, so it cannot address anything outside
    ``boards_root()`` (``../`` in a slug, a symlinked board dir). The default arm is
    built from constants only and needs no check."""
    if slug == _kb.DEFAULT_BOARD:
        return kanban_home().joinpath(*default_parts)
    root = boards_root().resolve()
    candidate = (root / slug).resolve()
    if candidate.parent != root:
        raise ValueError("invalid board slug")
    return candidate / leaf


def _same_file(left: Path, right: Path) -> bool:
    """Same physical file. Both sides arrive resolved — the board path through
    :func:`own_board_path`'s check, the pin through :func:`_physical` — so this
    only compares, and never re-resolves a path built from caller data."""
    return left == right


def _physical(path: Path) -> Path:
    """A path as it exists on disk. Only for paths that are NOT caller-built board
    data: the dispatcher's env pin (resolving it is what lets a worker pinned to a
    symlinked root still recognize its own board) and the constants-only default
    board path."""
    try:
        return path.resolve()
    except (OSError, RuntimeError):
        return path.absolute()


def _board_name_for_pinned_path(pinned: Path) -> str:
    """Slug of the board a pinned path belongs to, for refusal messages.

    The pin is a file path (``HERMES_KANBAN_DB`` & friends), not a slug, so the
    name is recovered from the layout; anything unrecognized falls back to the
    path itself rather than guessing a board."""
    resolved = _physical(pinned)
    try:
        parts = resolved.relative_to(kanban_home().resolve()).parts
    except (ValueError, OSError, RuntimeError):
        return str(pinned)
    if len(parts) >= 3 and parts[0] == "kanban" and parts[1] == "boards":
        return parts[2]
    if parts and parts[0] in ("kanban.db", "kanban"):
        return _kb.DEFAULT_BOARD
    return str(pinned)


def refuse_cross_board_pin(
    slug: str, pinned: Path, default_parts: tuple[str, ...], leaf: str,
) -> None:
    """Raise when the fence would answer ``board=slug`` with a different file
    than *slug*'s own. Same file (e.g. ``board="default"`` from a default-board
    worker) passes."""
    own = own_board_path(slug, default_parts, leaf)
    if slug == _kb.DEFAULT_BOARD:
        # Constants-only path (no slug in it), so resolving it here is safe and keeps
        # a symlinked HERMES_KANBAN_HOME comparable to the pin.
        own = _physical(own)
    if _same_file(own, _physical(pinned)):
        return
    pinned_name = _board_name_for_pinned_path(pinned)
    pinned_label = f"{pinned_name} ({pinned})" if pinned_name != str(pinned) else pinned_name
    raise BoardPinnedError(
        f"board {slug!r} is not available from this pinned session: it is pinned to "
        f"{pinned_label!r}, so nothing was written. Register the card from the main "
        f"session, or leave board unset to use {pinned_name!r}."
    )


def workspaces_root(board: Optional[str] = None) -> Path:
    """Per-board scratch workspace root (``HERMES_KANBAN_WORKSPACES_ROOT`` wins);
    ``default`` keeps the legacy ``<root>/kanban/workspaces/``."""
    return _kb._board_path("HERMES_KANBAN_WORKSPACES_ROOT", board, ("kanban", "workspaces"), "workspaces")


def attachments_root(board: Optional[str] = None) -> Path:
    """Per-board attachments root (``HERMES_KANBAN_ATTACHMENTS_ROOT`` wins). Workers
    read attachments by absolute path, so remote terminal backends must mount it."""
    return _kb._board_path("HERMES_KANBAN_ATTACHMENTS_ROOT", board, ("kanban", "attachments"), "attachments")


def task_attachments_dir(task_id: str, board: Optional[str] = None) -> Path:
    """Return the per-task attachment directory ``<root>/<task_id>/``."""
    return attachments_root(board=board) / task_id


def worker_logs_dir(board: Optional[str] = None) -> Path:
    """Per-board worker log dir (logs follow the board so ``hermes kanban log``
    is unambiguous when two boards share a task id)."""
    return _kb._board_path(None, board, ("kanban", "logs"), "logs")


# Imported at the tail: this module is loaded from kanban_db's own tail, so the
# late binding is what breaks the import cycle (same shape as kanban_db_boards).
from hermes_cli import kanban_db as _kb
