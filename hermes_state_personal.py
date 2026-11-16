"""Personal-document marks of a job, in the store (lina#308, migration 0023, docs/store.md "Personal documents").

A job is the session that started it and every session that continued it after a compaction. A document is marked
personal for a job by the owner (``mark_personal``) or by its folder (the marker file ``.lina-personal``); the
mark is a row of ``memory.personal_marks``, so it outlives the process. ``MessageStore.put_text`` asks
``session_is_marked`` before it writes a row whose text carries an ID or account number, and masks it when the
row's job is marked. The job's run record (work folders, their deletion, refused posts) is
``memory.personal_job_events``. Row-level security keeps every agent to its own rows.
"""

from __future__ import annotations

import json
import socket
from typing import Any, Optional

MARKED_SQL = ("SELECT EXISTS (SELECT 1 FROM memory.personal_job_sessions s JOIN memory.personal_marks m"
              " ON m.agent_id = s.agent_id AND m.job_id = s.job_id WHERE s.session_id = %s)")


def session_is_marked(cur, session_id: str) -> bool:
    """Whether the job *session_id* belongs to carries a personal-document mark (on the caller's cursor)."""
    cur.execute(MARKED_SQL, (session_id,))
    return bool(cur.fetchone()[0])


def device_name() -> str:
    return socket.gethostname() or "unknown"


class PersonalMarks:
    """The marks and run record of one agent's jobs, through its message store's connection."""

    def __init__(self, store) -> None:
        self._store = store

    def job_of(self, session_id: str) -> str:
        """The job *session_id* belongs to: the job it joined, else the session itself."""
        with self._store._cursor() as cur:
            cur.execute("SELECT job_id FROM memory.personal_job_sessions WHERE session_id = %s", (session_id,))
            row = cur.fetchone()
        return row[0] if row else session_id

    def mark(self, session_id: str, path: str, how: str) -> str:
        """Mark *path* personal for the job of *session_id*; returns the job id. Marking twice changes nothing."""
        job = self.job_of(session_id)
        with self._store._cursor() as cur:
            cur.execute("INSERT INTO memory.personal_job_sessions (session_id, job_id) VALUES (%s, %s)"
                        " ON CONFLICT DO NOTHING", (session_id, job))
            cur.execute("INSERT INTO memory.personal_marks (job_id, path, how) VALUES (%s, %s, %s)"
                        " ON CONFLICT DO NOTHING", (job, path, how))
        return job

    def marked_paths(self, session_id: str) -> list[str]:
        with self._store._cursor() as cur:
            cur.execute("SELECT m.path FROM memory.personal_job_sessions s JOIN memory.personal_marks m"
                        " ON m.agent_id = s.agent_id AND m.job_id = s.job_id WHERE s.session_id = %s"
                        " ORDER BY m.marked_at, m.path", (session_id,))
            return [row[0] for row in cur.fetchall()]

    def continue_job(self, parent_session_id: str, session_id: str) -> bool:
        """*session_id* continues *parent_session_id* (a compaction's child): it joins the parent's job when that
        job has a mark. Returns whether it joined."""
        with self._store._cursor() as cur:
            cur.execute("INSERT INTO memory.personal_job_sessions (session_id, job_id)"
                        " SELECT %s, job_id FROM memory.personal_job_sessions WHERE session_id = %s"
                        " ON CONFLICT DO NOTHING", (session_id, parent_session_id))
            return cur.rowcount > 0

    def record(self, job_id: str, kind: str, detail: dict[str, Any], *, device: Optional[str] = None) -> None:
        with self._store._cursor() as cur:
            cur.execute("INSERT INTO memory.personal_job_events (job_id, kind, device, detail)"
                        " VALUES (%s, %s, %s, %s::jsonb)",
                        (job_id, kind, device or device_name(), json.dumps(detail, ensure_ascii=False)))

    def events(self, job_id: str) -> list[dict[str, Any]]:
        with self._store._cursor() as cur:
            cur.execute("SELECT kind, device, detail FROM memory.personal_job_events WHERE job_id = %s"
                        " ORDER BY event_id", (job_id,))
            return [{"kind": kind, "device": device, **detail} for kind, device, detail in cur.fetchall()]
