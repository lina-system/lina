"""Reports (lina#263): a long report is a record with versions, not a long chat message.

Schema ``reports`` (``services/store/migrations/0025_report.sql``): one ``report`` row per report and one
``report_version`` row per edit; an update adds a version and the old ones stay readable, and the newest is the
report's body. An agent writes through :func:`agent_connection` (``lina_app`` with ``lina.agent_id`` set, so
row-level security keeps it to its own reports); the owner's client API reads every agent's through
:func:`owner_connection`. A private report is served only to the owner's session (``docs/store.md`` "Reports").
"""

from __future__ import annotations

import re
import secrets
from dataclasses import dataclass, replace
from typing import Any, Optional

REPORT_ID = re.compile(r"rep_[0-9a-f]{12}")
PAGE_PATH = "/reports/{id}/page"  # not under /api: a browser is sent to sign in, not given a JSON 401
TITLE_MAX = 200
SUMMARY_MAX = 280

_COLUMNS = ("r.report_id, r.agent_id, r.title, r.summary, r.card, r.goal, r.private, r.version, v.body, "
            "extract(epoch FROM r.created_at)::bigint, extract(epoch FROM r.updated_at)::bigint, "
            "extract(epoch FROM v.created_at)::bigint")


class ReportError(ValueError):
    """A report that cannot be written as asked (empty title, a summary that is not one line, an unknown id)."""


@dataclass(frozen=True)
class Report:
    report_id: str
    agent: str
    title: str
    summary: str
    card: Optional[str]
    goal: Optional[str]
    private: bool
    version: int
    body: str
    created_at: int
    updated_at: int
    version_at: int

    def page_path(self) -> str:
        return page_path(self.report_id)


def page_path(report_id: str) -> str:
    """The report page's path on the dashboard server (behind the owner's sign-in, which a browser is sent to)."""
    return PAGE_PATH.format(id=report_id)


def page_url(report_id: str, base: str = "") -> str:
    """The page address: *base* (the dashboard's public URL, scheme and host) before the path, or the path alone."""
    return f"{base.rstrip('/')}{page_path(report_id)}"


def one_line(text: Any) -> str:
    return " ".join(str(text or "").split())


def _checked(title: Any, summary: Any, body: Any) -> tuple[str, str, str]:
    title, summary = one_line(title), one_line(summary)
    if not title:
        raise ReportError("a report needs a title")
    if not summary:
        raise ReportError("a report needs a one-line summary: the line the chat shows")
    if len(title) > TITLE_MAX:
        raise ReportError(f"the title is longer than {TITLE_MAX} characters")
    if len(summary) > SUMMARY_MAX:
        raise ReportError(f"the summary is longer than {SUMMARY_MAX} characters: it is one line in a chat")
    body = str(body or "").strip()
    if not body:
        raise ReportError("a report needs a body (markdown)")
    return title, summary, body


def _ref(value: Any) -> Optional[str]:
    text = str(value or "").strip()
    return text or None


def save(conn, *, agent: str, title: Any, summary: Any, body: Any, report_id: Optional[str] = None,
         card: Any = None, goal: Any = None, private: bool = False, session_id: Optional[str] = None) -> Report:
    """Create a report, or with *report_id* add a version to it. One transaction; returns the newest version.

    An update keeps the card and goal it does not name, and a report once private stays private."""
    title, summary, body = _checked(title, summary, body)
    card, goal = _ref(card), _ref(goal)
    with conn.transaction():
        if report_id is None:
            report_id = f"rep_{secrets.token_hex(6)}"
            conn.execute("INSERT INTO reports.report (report_id, agent_id, title, summary, card, goal, private) "
                         "VALUES (%s, %s, %s, %s, %s, %s, %s)", (report_id, agent, title, summary, card, goal, private))
            version = 1
        else:
            if not REPORT_ID.fullmatch(report_id):
                raise ReportError(f"{report_id!r} is not a report id (rep_ and 12 hex digits)")
            row = conn.execute("UPDATE reports.report SET title = %s, summary = %s, card = coalesce(%s, card), "
                               "goal = coalesce(%s, goal), private = private OR %s, version = version + 1, "
                               "updated_at = now() WHERE report_id = %s RETURNING version",
                               (title, summary, card, goal, private, report_id)).fetchone()
            if row is None:
                raise ReportError(f"no report {report_id} of this agent")
            version = row[0]
        conn.execute("INSERT INTO reports.report_version (report_id, version, agent_id, title, summary, body, "
                     "session_id) VALUES (%s, %s, %s, %s, %s, %s, %s)",
                     (report_id, version, agent, title, summary, body, session_id))
    found = get(conn, report_id)
    assert found is not None
    return found


def get(conn, report_id: str, version: Optional[int] = None) -> Optional[Report]:
    """The report with its newest body, or with *version*'s title, summary and body; None when there is none."""
    if not REPORT_ID.fullmatch(report_id):
        return None
    which = "v.version = %s" if version is not None else "v.version = r.version"
    params = (report_id, version) if version is not None else (report_id,)
    row = conn.execute(f"SELECT {_COLUMNS}, v.title, v.summary FROM reports.report AS r "
                       "JOIN reports.report_version AS v USING (report_id) "
                       f"WHERE r.report_id = %s AND {which}", params).fetchone()
    if row is None:
        return None
    report = Report(*row[:12])
    return report if version is None else replace(report, title=row[12], summary=row[13])


def versions(conn, report_id: str) -> list[dict[str, Any]]:
    """Every version of the report, oldest first: number, time, title and summary (no body)."""
    rows = conn.execute("SELECT version, extract(epoch FROM created_at)::bigint, title, summary "
                        "FROM reports.report_version WHERE report_id = %s ORDER BY version", (report_id,)).fetchall()
    return [{"version": v, "at": at, "title": t, "summary": s} for v, at, t, s in rows]


def listing(conn, *, card: Optional[str] = None, goal: Optional[str] = None,
            include_private: bool = False) -> list[Report]:
    """Reports with their newest body, newest change first; only those of *card* and of *goal* when given."""
    where, params = ["v.version = r.version"], []
    for column, value in (("card", card), ("goal", goal)):
        if value:
            where.append(f"r.{column} = %s")
            params.append(value)
    if not include_private:
        where.append("NOT r.private")
    rows = conn.execute(f"SELECT {_COLUMNS} FROM reports.report AS r JOIN reports.report_version AS v "
                        f"USING (report_id) WHERE {' AND '.join(where)} ORDER BY r.updated_at DESC, r.report_id",
                        params).fetchall()
    return [Report(*row) for row in rows]


def agent_connection(agent: str):
    """A connection for *agent* (``lina_app``, ``lina.agent_id`` set): it reads and writes only that agent's reports."""
    import psycopg

    from hermes_state_store import StoreUnavailableError, resolve_dsn
    try:
        conn = psycopg.connect(resolve_dsn(), autocommit=True)
    except psycopg.OperationalError as exc:
        raise StoreUnavailableError(f"cannot reach the memory store: {exc}".strip()) from exc
    conn.execute("SELECT set_config('lina.agent_id', %s, false)", (agent,))
    return conn


def owner_connection():
    """A connection as the store owner, for the owner's client API: every agent's reports."""
    from hermes_state_store import _owner_connection
    conn = _owner_connection()
    conn.autocommit = True
    return conn
