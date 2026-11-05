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
import re
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


# One board directory name: no separator, no dot segment, nothing a path
# expression could leave the boards root with.
_BOARD_SLUG_PATTERN = re.compile(r"[a-z0-9][a-z0-9_-]{0,63}")


def _contained_path(root: Path, slug: str, leaf: Optional[str] = None) -> Path:
    """``<root>/<slug>`` (or ``<root>/<slug>/<leaf>``) that provably stays inside *root*.

    The slug comes from a caller (``board=``, ``--board``), so it is filtered by a
    fixed full-match pattern first, then the built string must sit one level below
    the root before anything touches it — and the real directory is checked the same
    way again, because a board directory could be a symlink pointing out of the root.
    All checks stay in this function, on the value it follows.

    *leaf* asks for the board FILE's identity instead of the directory: it is joined
    onto the directory string the checks above approved, so a board whose
    ``kanban.db`` is itself a symlink resolves to what it points at (the fence
    compares files, not spellings) without any caller-built text being followed."""
    if _BOARD_SLUG_PATTERN.fullmatch(slug) is None:
        raise ValueError("invalid board slug")
    root_text = str(root)
    prefix = os.path.join(root_text, "")
    joined = os.path.normpath(os.path.join(root_text, slug))
    if not joined.startswith(prefix) or os.sep in joined[len(prefix):]:
        raise ValueError("invalid board slug")
    real = os.path.realpath(joined)
    if not real.startswith(prefix) or os.sep in real[len(prefix):]:
        raise ValueError("invalid board slug")
    if leaf is None:
        return Path(real)
    return Path(os.path.realpath(os.path.join(real, leaf)))


def own_board_path(slug: str, default_parts: tuple[str, ...], leaf: str) -> Path:
    """Where *slug*'s file lives when no env pin applies (the unfenced path).

    The non-default arm goes through :func:`_contained_path`, so a slug cannot
    address anything outside ``boards_root()`` (``../`` in it, or a board directory
    symlinked elsewhere). The default arm is built from constants only."""
    if slug == _kb.DEFAULT_BOARD:
        return kanban_home().joinpath(*default_parts)
    return _contained_path(boards_root().resolve(), slug) / leaf


def _same_file(left: Path, right: Path) -> bool:
    """Same physical FILE. Both sides arrive as identities — the board's through
    :func:`_contained_path` (leaf included), the pin through :func:`_physical` — so a
    board whose ``kanban.db`` is a symlink to the pinned file compares equal."""
    return left == right


def _physical(path: Path) -> Path:
    """Physical form of a path we own: the dispatcher's board pin, or the
    constants-only default board path.

    A pin is ours, never board data, but it still arrives as an environment string,
    so it is normalized and range-checked here before anything follows it: only a
    path inside the kanban home (or inside its real form, for a symlinked root) is
    followed, and only after the prefix check. Everything else is compared
    lexically — it cannot be one of our board paths, and is never followed."""
    text = os.path.normpath(os.path.abspath(str(path)))
    home = os.path.join(str(kanban_home()), "")
    home_real = os.path.join(os.path.realpath(str(kanban_home())), "")
    if not text.startswith(home) and not text.startswith(home_real):
        return Path(text)
    return Path(os.path.realpath(text))


def _board_name_for_pinned_path(pinned: Path) -> str:
    """Slug of the board a pinned path belongs to, for refusal messages.

    The pin is a file path (``HERMES_KANBAN_DB`` & friends), not a slug, so the
    name is recovered from the layout; anything unrecognized falls back to the
    path itself rather than guessing a board."""
    try:
        parts = _physical(pinned).relative_to(kanban_home().resolve()).parts
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
    than *slug*'s own. Same file passes — ``board="default"`` from a default-board
    worker, or a named board whose ``kanban.db`` links to the pinned file."""
    if slug == _kb.DEFAULT_BOARD:
        # Constants-only path (no slug in it), resolved so a symlinked
        # HERMES_KANBAN_HOME stays comparable to the pin.
        own = _physical(own_board_path(slug, default_parts, leaf))
    else:
        # Resolve the leaf too: naming a board must compare the DB it would open,
        # not the spelling of the directory that holds it (Codex P2 on #213).
        own = _contained_path(boards_root().resolve(), slug, leaf)
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
