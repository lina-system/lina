"""The work folders of a job that handles personal documents, and whether a document is personal (lina#308).

A document is personal when the owner marked it for the job (``mark_personal``, stored in the store) or when its
folder, or a folder above it, holds the marker file ``.lina-personal``. Everything derived from such a document
(extracted text, page images, temporary copies) goes under the job's two folders, made by ``open_job_folders``
only: ``<home>/personal-work/<job>/`` and ``<temp dir>/lina-personal-<job>/``. ``end_job`` deletes exactly those
two folders when the job ends, whatever the outcome, records the count deleted in the job's run record and returns
it; it never touches anything beside them.
"""

from __future__ import annotations

import hashlib
import logging
import os
import re
import shutil
import tempfile
from collections.abc import Iterator
from contextlib import contextmanager
from dataclasses import dataclass
from pathlib import Path
from typing import Optional

logger = logging.getLogger(__name__)

MARKER_FILE = ".lina-personal"
WORK_DIR_NAME = "personal-work"
TEMP_PREFIX = "lina-personal-"

_MARKS: dict[str, object] = {}


@dataclass(frozen=True)
class JobFolders:
    job_id: str
    work: Path
    temp: Path

    def holds(self, path: str) -> bool:
        resolved = Path(path).expanduser().resolve()
        return any(resolved == root or root in resolved.parents for root in (self.work, self.temp))


def marker_folder(path: str) -> Optional[Path]:
    """The nearest folder at or above *path*'s folder that holds the marker file, or None."""
    resolved = Path(path).expanduser().resolve()
    for folder in (resolved, *resolved.parents) if resolved.is_dir() else resolved.parents:
        if (folder / MARKER_FILE).is_file():
            return folder
    return None


def _slug(job_id: str) -> str:
    readable = re.sub(r"[^A-Za-z0-9._-]", "_", job_id)[:48]
    return f"{readable}-{hashlib.sha256(job_id.encode('utf-8')).hexdigest()[:10]}"


def folders_for(job_id: str) -> JobFolders:
    from hermes_constants import get_hermes_home
    slug = _slug(job_id)
    return JobFolders(job_id, (get_hermes_home() / WORK_DIR_NAME / slug).resolve(),
                      (Path(tempfile.gettempdir()) / f"{TEMP_PREFIX}{slug}").resolve())


def marks_for_home():
    """The personal marks of the agent whose home is bound now (profile-aware), one per agent and process."""
    from hermes_constants import get_hermes_home
    from hermes_state_personal import PersonalMarks
    from hermes_state_store import MessageStore, agent_id_for
    agent = agent_id_for(get_hermes_home() / "state.db")
    if agent not in _MARKS:
        _MARKS[agent] = PersonalMarks(MessageStore(agent))
    return _MARKS[agent]


def current_session_id() -> str:
    from gateway.session_context import get_session_env
    return get_session_env("HERMES_SESSION_ID", "")


def open_job_folders(marks, job_id: str) -> JobFolders:
    """The job's work and temp folders, made private when missing; their first making is in the run record."""
    folders = folders_for(job_id)
    made = []
    for folder in (folders.work, folders.temp):
        if not folder.is_dir():
            folder.mkdir(parents=True, mode=0o700, exist_ok=True)
            made.append(str(folder))
    if made:
        marks.record(job_id, "work_folder", {"made": made})
    return folders


def _files_under(folder: Path) -> int:
    return sum(len(files) + sum(1 for d in dirs if (Path(root) / d).is_symlink())
               for root, dirs, files in os.walk(folder, followlinks=False))


def end_job(marks, job_id: str, outcome: str) -> int:
    """Delete the job's work and temp folders on this device; returns the number of files deleted. The deletion and
    *outcome* (``success``, ``failure``, ``cancelled``, ``session_end``) go to the run record when a folder was there."""
    folders = folders_for(job_id)
    present = [folder for folder in (folders.work, folders.temp) if folder.is_dir()]
    if not present:
        return 0
    deleted = 0
    for folder in present:
        deleted += _files_under(folder)
        shutil.rmtree(folder)
    marks.record(job_id, "cleaned", {"deleted": deleted, "outcome": outcome, "folders": [str(f) for f in present]})
    logger.info("personal job %s ended (%s): deleted %d derived files", job_id, outcome, deleted)
    return deleted


@contextmanager
def job_folders(marks, job_id: str) -> Iterator[JobFolders]:
    """The job's folders for the length of the block; deleted when it ends, by success, failure or cancel."""
    folders = open_job_folders(marks, job_id)
    outcome = "cancelled"
    try:
        yield folders
        outcome = "success"
    except Exception:
        outcome = "failure"
        raise
    finally:
        end_job(marks, job_id, outcome)


def personal_folders_for(path: str, session_id: Optional[str] = None) -> Optional[JobFolders]:
    """The current job's folders when *path* is a personal document of it (marked, or under a marker folder),
    else None. A store that cannot be reached raises: the caller decides without guessing."""
    session = session_id or current_session_id()
    if not session:
        return None
    marks = marks_for_home()
    if marker_folder(path) is None and not is_marked(marks.marked_paths(session), path):
        return None
    return open_job_folders(marks, marks.job_of(session))


def is_marked(marked_paths: list[str], path: str) -> bool:
    """Whether *path* is one of *marked_paths* or inside a marked folder."""
    resolved = Path(path).expanduser().resolve()
    for marked in map(Path, marked_paths):
        if resolved == marked or marked in resolved.parents:
            return True
    return False
