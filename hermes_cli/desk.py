"""The Desk: the one shared folder of the person's files that every agent starts from (lina#44).

Profiles are islands for their own state, but the Desk is shared on purpose: ``desk.path`` is read
from the DEFAULT profile's ``config.yaml`` (the root home), never from the active profile, so one
setting moves every agent. Unset means no Desk and upstream behaviour is unchanged.

The layout is PARA (Inbox, Projects, Areas, Resources, Archive). An adopted folder may already
number them (``1_Projects``); such a folder counts as that PARA folder instead of getting a twin.
A file on the Desk is named ``desk:<relative path>`` wherever records, the app and workers store it.
"""

from __future__ import annotations

import hashlib
import logging
import re
from contextlib import contextmanager
from pathlib import Path
from typing import Any, Iterator

from hermes_constants import get_default_hermes_root

logger = logging.getLogger(__name__)

PARA_FOLDERS = ("Inbox", "Projects", "Areas", "Resources", "Archive")
DEFAULT_DESK_PATH = "~/Desk"
CONFIG_KEY = "desk.path"
REF_PREFIX = "desk:"
_NUMBERED = re.compile(r"^\d+[\s._-]*(.+)$")

AGENTS_MD = """# Desk

This folder is the Desk: the person's files, shared by every agent. Agents start here.

## Where things go

- `{Inbox}/`: anything new that has no place yet. Agents sort it into the folders below.
- `{Projects}/`: work with an end date, one folder per project.
- `{Areas}/`: ongoing responsibilities with no end date.
- `{Resources}/`: reference material and topics of interest.
- `{Archive}/`: finished projects and anything no longer active.

## Rules for agents

- Sort {Inbox}: move each item to the folder where it belongs; ask the person when unsure.
- When a project is finished, move its folder to {Archive}.
- Never delete anything on the Desk without the person's approval. Move it to {Archive} instead.
- One agent edits one file at a time: re-read a file before changing it, and leave a file alone
  while another agent is editing it.
- Keep the person's files here. To-dos, status and deadlines live in the records and point to a
  Desk file as `desk:<path>` instead of copying it.
"""


class DeskError(ValueError):
    """The Desk cannot be created, found, or a path is not on it."""


def desk_config_path() -> Path:
    """The config file that owns ``desk.path`` for every profile: the default profile's."""
    return get_default_hermes_root() / "config.yaml"


def configured_desk_path() -> Path | None:
    """``desk.path`` from the default profile's config, expanded; None when unset or unreadable.

    A bypass worker (safe mode, ``--ignore-user-config``) reads no profile file, and the Desk is user
    config, so it has no Desk."""
    from agent.safe_worker_policy import _current_policy
    from hermes_cli.config_backend import config_exists, read_config_doc_readonly

    policy = _current_policy()
    if policy is not None and policy.ignore_user_config:
        return None
    cfg_path = desk_config_path()
    try:
        doc = read_config_doc_readonly(cfg_path) if config_exists(cfg_path) else None
    except Exception:  # an unreadable root config must not take every agent down
        logger.warning("cannot read the Desk setting from %s", cfg_path, exc_info=True)
        return None
    desk = doc.get("desk") if isinstance(doc, dict) else None
    raw = str((desk.get("path") if isinstance(desk, dict) else None) or "").strip()
    if not raw:
        return None
    path = Path(raw).expanduser()
    if not path.is_absolute():
        logger.warning("desk.path must be absolute, ignoring %r", raw)
        return None
    return path


def get_desk_path() -> Path | None:
    """The Desk when one is configured and exists."""
    path = configured_desk_path()
    return path if path is not None and path.is_dir() else None


def default_workdir() -> str:
    """Where an agent with no explicit working directory starts: the Desk, else the home folder."""
    desk = get_desk_path()
    return str(desk) if desk is not None else str(Path.home())


def _para_match(entry: str, name: str) -> bool:
    numbered = _NUMBERED.match(entry)
    return entry.casefold() == name.casefold() or bool(numbered and numbered.group(1).casefold() == name.casefold())


def desk_layout(root: Path) -> dict[str, Path]:
    """PARA name -> its folder on *root*: an existing (possibly numbered) folder, else ``root/<name>``."""
    entries = sorted(p.name for p in root.iterdir() if p.is_dir()) if root.is_dir() else []
    layout = {}
    for name in PARA_FOLDERS:
        found = next((e for e in entries if e == name), None) or next((e for e in entries if _para_match(e, name)), None)
        layout[name] = root / (found or name)
    return layout


def init_desk(path: str | Path | None = None, *, adopt: bool = False) -> Path:
    """Create (or, with *adopt*, take over) the Desk and point every agent at it.

    Adoption keeps every existing file: it only adds missing PARA folders and an ``AGENTS.md``
    when none exists. Re-running on the configured Desk is a no-op apart from filling gaps."""
    from hermes_cli.config_backend import write_config_key

    root = Path(path or DEFAULT_DESK_PATH).expanduser().absolute()
    if root.exists() and not root.is_dir():
        raise DeskError(f"{root} exists and is not a folder")
    already_desk = configured_desk_path() == root
    if root.is_dir() and any(root.iterdir()) and not (adopt or already_desk):
        raise DeskError(f"{root} is not empty; pass --adopt to use it as the Desk, keeping its files")
    root.mkdir(parents=True, exist_ok=True)
    layout = desk_layout(root)
    for folder in layout.values():
        folder.mkdir(exist_ok=True)
    agents_md = root / "AGENTS.md"
    if not agents_md.exists():
        agents_md.write_text(AGENTS_MD.format(**{k: v.name for k, v in layout.items()}), encoding="utf-8")
    write_config_key(desk_config_path(), CONFIG_KEY, str(root))
    return root


def resolve_desk_ref(ref: str, *, desk: Path | None = None) -> Path:
    """``desk:<relative path>`` (the prefix is optional) -> the absolute path on the Desk.
    Refuses a path that leaves the Desk, through ``..`` or a symlink."""
    desk = desk or get_desk_path()
    if desk is None:
        raise DeskError("no Desk is configured; run `hermes desk init`")
    rel = ref.removeprefix(REF_PREFIX)
    root = desk.resolve()
    target = (root / rel.strip().lstrip("/\\")).resolve()
    if target != root and root not in target.parents:
        raise DeskError(f"{ref!r} is not on the Desk")
    return target


def to_desk_ref(path: str | Path, *, desk: Path | None = None) -> str | None:
    """The ``desk:`` name of *path*, or None when it is not on the Desk."""
    desk = desk or get_desk_path()
    if desk is None:
        return None
    root, target = desk.resolve(), Path(path).expanduser().resolve()
    if target != root and root not in target.parents:
        return None
    rel = target.relative_to(root).as_posix()
    return REF_PREFIX + ("" if rel == "." else rel)


def desk_info() -> dict[str, Any]:
    """The Desk as clients see it (``GET /api/desk``, ``hermes desk show --json``)."""
    path = configured_desk_path()
    exists = path is not None and path.is_dir()
    folders = {name: folder.name for name, folder in desk_layout(path).items()} if exists else {}
    return {
        "path": str(path) if path is not None else None,
        "exists": exists,
        "folders": folders,
        "agents_md": bool(exists and (path / "AGENTS.md").is_file()),
        "config_key": CONFIG_KEY,
        "ref_prefix": REF_PREFIX,
    }


def desk_lock_file(resolved: str | Path) -> Path | None:
    """The lock file guarding writes to *resolved*, or None when it is not on the Desk."""
    desk = get_desk_path()
    target = Path(resolved)
    if desk is None or desk.resolve() not in target.parents:
        return None
    digest = hashlib.sha256(str(target).encode("utf-8")).hexdigest()[:32]
    return get_default_hermes_root() / "desk" / "locks" / f"{digest}.lock"


@contextmanager
def desk_file_lock(resolved: str | Path) -> Iterator[None]:
    """Cross-process lock for one Desk file, held around an agent's write: agents served by
    different processes edit one file at a time. Not on the Desk -> no lock."""
    from tools.skill_usage import skill_file_lock

    lock_file = desk_lock_file(resolved)
    if lock_file is None:
        yield
        return
    with skill_file_lock(lock_file):
        yield
