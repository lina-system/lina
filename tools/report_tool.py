"""The ``report`` tool (lina#263): one call writes a report, or a new version of one, and returns its id, its page
address and the reference line to send in chat instead of the report's text.

Reports live in the store (``hermes_state_reports.py``), written as this agent, so row-level security keeps every
agent to its own. A report written from a session that handles the owner's personal documents (lina#308), or marked
private by the call, is served only to the owner's session. Every write is announced on the app's change stream
(source ``reports``), so an open viewer refreshes in place.
"""

import json
from contextlib import closing

from tools.registry import registry, tool_error

REPORT_SCHEMA = {
    "name": "report",
    "description": (
        "Write a report, or a new version of one, when you have more to tell than about three short chat messages: "
        "a long result, findings, a plan, a review. The full text (markdown: headings, tables, lists, links, images "
        "by URL) goes into the report; the chat gets one line. Returns the report's id, its page address and "
        "`reference`: send `reference` as its own line in your message, after at most one short sentence, and never "
        "paste the report's text into the chat. To change a report, call again with its id: the new text becomes "
        "the current version and the earlier versions stay readable."
    ),
    "parameters": {
        "type": "object",
        "properties": {
            "title": {"type": "string", "description": "The report's title."},
            "summary": {"type": "string",
                        "description": "One line (at most 280 characters) that says what the report tells: the line "
                                       "the chat shows."},
            "body": {"type": "string", "description": "The whole report, in markdown."},
            "id": {"type": "string", "description": "A report's id (rep_...) to add a new version to it. Omit to "
                                                    "write a new report."},
            "card": {"type": "string", "description": "The work card the report belongs to, if any."},
            "goal": {"type": "string", "description": "The goal the report belongs to, if any."},
            "private": {"type": "boolean",
                        "description": "Serve it only to the owner's own session. Set when it is built from the "
                                       "owner's private material."},
        },
        "required": ["title", "summary", "body"],
    },
}


def check_store() -> bool:
    """Whether this profile has a store to write reports to (LINA's store installed, or a store DSN given)."""
    from hermes_state_store import StoreUnavailableError, resolve_dsn
    try:
        resolve_dsn()
    except StoreUnavailableError:
        return False
    return True


def _personal_session(session_id: str) -> bool:
    """Whether *session_id*'s job carries a personal-document mark (lina#308's ``agent/personal_work.py``)."""
    if not session_id:
        return False
    try:
        from agent import personal_work
    except ImportError:  # lina#308 has not landed in this build: no session is marked
        return False
    return bool(personal_work.marks_for_home().marked_paths(session_id))


def report_tool(args: dict, session_id: str = "") -> str:
    from gateway.report_reference import line
    from gateway.session_context import get_session_env
    from hermes_cli.dashboard_auth.prefix import resolve_public_url
    from hermes_cli.lina_board_events import announce
    from hermes_cli.lina_deps import lina_root
    from hermes_constants import get_hermes_home
    from hermes_state_reports import ReportError, agent_connection, page_url, save
    from hermes_state_store import StoreUnavailableError, agent_id_for

    session_id = session_id or get_session_env("HERMES_SESSION_ID", "")
    agent = agent_id_for(get_hermes_home() / "state.db")
    try:
        with closing(agent_connection(agent)) as conn:
            report = save(conn, agent=agent, title=args.get("title"), summary=args.get("summary"),
                          body=args.get("body"), report_id=(args.get("id") or None), card=args.get("card"),
                          goal=args.get("goal"),
                          private=bool(args.get("private")) or _personal_session(session_id),
                          session_id=session_id or None)
    except (ReportError, StoreUnavailableError) as exc:
        return tool_error(str(exc))
    announce(lina_root(), "reports", "created" if report.version == 1 else "updated", report.report_id)
    page = page_url(report.report_id, resolve_public_url())
    return json.dumps({"success": True, "id": report.report_id, "version": report.version, "page": page,
                       "private": report.private, "reference": line(report.report_id, page, report.summary)},
                      ensure_ascii=False)


registry.register(
    name="report", toolset="report", schema=REPORT_SCHEMA, check_fn=check_store,
    handler=lambda args, **kw: report_tool(args, session_id=kw.get("session_id") or ""),
    emoji="📄")
